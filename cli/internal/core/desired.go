package core

import (
	"cmp"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
)

type forwardKey struct {
	direction   ForwardDirection
	servicePort uint16
}

type desiredForward struct {
	preferred     ForwardTarget
	automatic     bool
	allowFallback bool
}

func (forward desiredForward) key() forwardKey {
	return keyFor(forward.preferred.Direction, forward.preferred.LocalPort, forward.preferred.RemotePort)
}

var ErrInvalidWorkingDirectoryRule = errors.New("invalid working-directory glob")
var ErrInvalidAppName = errors.New("invalid app name")

// normalizeIntent canonicalizes one host's rules. Zero ports and duplicates are
// rejected. Omitted bind ports still receive the same-port default.
func normalizeIntent(intent ForwardingIntent) (ForwardingIntent, error) {
	var err error
	if intent.AutoForwards, err = NormalizeRememberedForwards(intent.AutoForwards); err != nil {
		return ForwardingIntent{}, err
	}
	if intent.RememberedForwards, err = NormalizeRememberedForwards(intent.RememberedForwards); err != nil {
		return ForwardingIntent{}, err
	}
	if intent.PublishedForwards, err = NormalizePublishedForwards(intent.PublishedForwards); err != nil {
		return ForwardingIntent{}, err
	}
	if intent.WorkingDirectoryRules, err = NormalizeWorkingDirectoryRules(intent.WorkingDirectoryRules); err != nil {
		return ForwardingIntent{}, err
	}
	if intent.IgnoredApps, err = NormalizeIgnoredApps(intent.IgnoredApps); err != nil {
		return ForwardingIntent{}, err
	}
	return intent, nil
}

// validAppName accepts one executable name as shown in the status APP column.
func validAppName(name string) bool {
	return name != "" && len(name) <= 255 && utf8.ValidString(name) &&
		!strings.Contains(name, "/") &&
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

func NormalizeIgnoredApps(apps []string) ([]string, error) {
	return normalizeRuleNames(apps, ValidIgnoredApp)
}

func ValidIgnoredApp(name string) error {
	if !validAppName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidAppName, name)
	}
	return nil
}

func NormalizeWorkingDirectoryRules(patterns []string) ([]string, error) {
	return normalizeRuleNames(patterns, ValidWorkingDirectoryRule)
}

// normalizeRuleNames validates every entry before sorting and deduplicating.
func normalizeRuleNames(values []string, valid func(string) error) ([]string, error) {
	if len(values) == 0 {
		return values, nil
	}
	for _, value := range values {
		if err := valid(value); err != nil {
			return nil, err
		}
	}
	return slices.Compact(slices.Sorted(slices.Values(values))), nil
}

func ValidWorkingDirectoryRule(pattern string) error {
	if !path.IsAbs(pattern) {
		return fmt.Errorf("%w: must be an absolute remote path", ErrInvalidWorkingDirectoryRule)
	}
	if !doublestar.ValidatePattern(pattern) {
		return fmt.Errorf("%w: malformed pattern", ErrInvalidWorkingDirectoryRule)
	}
	return nil
}

func NormalizeRememberedForwards(forwards []RememberedForward) ([]RememberedForward, error) {
	return normalizeForwards(forwards, normalizeRememberedForward,
		func(forward RememberedForward) (uint16, uint16) { return forward.RemotePort, forward.LocalPort }, "remote", "local")
}

func normalizeRememberedForward(forward RememberedForward) (RememberedForward, error) {
	if forward.RemotePort == 0 {
		return RememberedForward{}, errors.New("remote port must be between 1 and 65535")
	}
	return forward.WithDefaults(), nil
}

func NormalizePublishedForwards(forwards []PublishedForward) ([]PublishedForward, error) {
	return normalizeForwards(forwards, normalizePublishedForward,
		func(forward PublishedForward) (uint16, uint16) { return forward.LocalPort, forward.RemotePort }, "published local", "published remote")
}

func normalizePublishedForward(forward PublishedForward) (PublishedForward, error) {
	if forward.LocalPort == 0 {
		return PublishedForward{}, errors.New("local port must be between 1 and 65535")
	}
	return forward.WithDefaults(), nil
}

func normalizeForwards[T any](items []T, defaults func(T) (T, error), ports func(T) (uint16, uint16), service, bind string) ([]T, error) {
	if len(items) == 0 {
		return items, nil
	}
	normalized := make([]T, 0, len(items))
	services, bindings := make(map[uint16]bool), make(map[uint16]uint16)
	for _, item := range items {
		item, err := defaults(item)
		if err != nil {
			return nil, err
		}
		source, target := ports(item)
		if services[source] {
			return nil, fmt.Errorf("duplicate %s port %d", service, source)
		}
		if previous, found := bindings[target]; found {
			return nil, fmt.Errorf("%s port %d is used by %s ports %d and %d", bind, target, service, previous, source)
		}
		services[source], bindings[target] = true, source
		normalized = append(normalized, item)
	}
	slices.SortFunc(normalized, func(a, b T) int {
		left, _ := ports(a)
		right, _ := ports(b)
		return cmp.Compare(left, right)
	})
	return normalized, nil
}

func appIgnored(ignored []string, app string) bool {
	return app != "" && slices.Contains(ignored, app)
}

func reservedLocalPorts(forwards []PublishedForward, additional ...uint16) map[uint16]struct{} {
	ports := make(map[uint16]struct{}, len(forwards))
	for _, forward := range forwards {
		ports[forward.LocalPort] = struct{}{}
	}
	for _, port := range additional {
		ports[port] = struct{}{}
	}
	return ports
}

func buildDesiredForwards(
	remembered []RememberedForward,
	published []PublishedForward,
	listeners map[uint16]Listener,
	workingDirectoryRules []string,
	ignoredApps []string,
	autoForwards ...RememberedForward,
) map[forwardKey]desiredForward {
	desired := make(map[forwardKey]desiredForward, len(remembered)+len(published))
	for _, forward := range remembered {
		item := desiredRememberedForward(forward)
		desired[item.key()] = item
	}
	publishedRemotePorts := make(map[uint16]struct{}, len(published))
	for _, forward := range published {
		item := desiredPublishedForward(forward)
		desired[item.key()] = item
		publishedRemotePorts[forward.RemotePort] = struct{}{}
	}
	// Build candidates in precedence order; only observed listeners can select
	// a global port rule or a working-directory rule.
	automatic := make(map[uint16]desiredForward, len(autoForwards))
	for _, forward := range autoForwards {
		if _, exists := automatic[forward.RemotePort]; !exists {
			automatic[forward.RemotePort] = desiredRememberedForward(forward)
		}
	}
	for port, listener := range listeners {
		if appIgnored(ignoredApps, listener.App) {
			continue
		}
		candidate, matched := automatic[port]
		if !matched {
			if !matchesWorkingDirectory(workingDirectoryRules, listener.WorkingDirectory) {
				continue
			}
			candidate = desiredAutomaticForward(port)
		}
		key := candidate.key()
		_, selected := desired[key]
		_, published := publishedRemotePorts[port]
		if !selected && !published {
			candidate.automatic = true
			desired[key] = candidate
		}
	}
	return desired
}

func matchesWorkingDirectory(patterns []string, directory string) bool {
	if directory == "" || !path.IsAbs(directory) {
		return false
	}
	for _, pattern := range patterns {
		matched, err := doublestar.Match(pattern, directory)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func forwardStatus(desired desiredForward, state ForwardState, diagnostic string, target ForwardTarget) ForwardStatus {
	status := ForwardStatus{
		Direction:     desired.preferred.Direction,
		State:         state,
		Diagnostic:    diagnostic,
		Automatic:     desired.automatic,
		AllowFallback: desired.allowFallback,
	}
	if desired.preferred.Direction == LocalToRemote {
		status.LocalPort = desired.preferred.LocalPort
		status.PreferredRemotePort = desired.preferred.RemotePort
		status.RemotePort = target.RemotePort
		return status
	}
	status.RemotePort = desired.preferred.RemotePort
	status.PreferredLocalPort = desired.preferred.LocalPort
	status.LocalPort = target.LocalPort
	return status
}

func desiredRememberedForward(forward RememberedForward) desiredForward {
	return desiredForward{
		preferred:     ForwardTarget{Direction: RemoteToLocal, RemotePort: forward.RemotePort, LocalPort: forward.LocalPort},
		allowFallback: forward.AllowFallback,
	}
}

func desiredAutomaticForward(port uint16) desiredForward {
	return desiredForward{preferred: ForwardTarget{Direction: RemoteToLocal, RemotePort: port, LocalPort: port}, automatic: true, allowFallback: true}
}

func desiredPublishedForward(forward PublishedForward) desiredForward {
	return desiredForward{preferred: ForwardTarget{Direction: LocalToRemote, RemotePort: forward.RemotePort, LocalPort: forward.LocalPort}}
}

func keyFor(direction ForwardDirection, local, remote uint16) forwardKey {
	if direction == LocalToRemote {
		return forwardKey{direction: direction, servicePort: local}
	}
	return forwardKey{direction: direction, servicePort: remote}
}
