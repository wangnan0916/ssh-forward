package core

import (
	"maps"
	"path"
	"slices"

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

func normalizedForwardingIntent(intent ForwardingIntent) ForwardingIntent {
	intent.AutoForwards = normalizedManagerRememberedForwards(intent.AutoForwards)
	intent.RememberedForwards = normalizedManagerRememberedForwards(intent.RememberedForwards)
	intent.PublishedForwards = normalizedManagerPublishedForwards(intent.PublishedForwards)
	patterns := make([]string, 0, len(intent.WorkingDirectoryRules))
	for _, pattern := range intent.WorkingDirectoryRules {
		if path.IsAbs(pattern) && doublestar.ValidatePattern(pattern) {
			patterns = append(patterns, pattern)
		}
	}
	slices.Sort(patterns)
	intent.WorkingDirectoryRules = slices.Compact(patterns)
	return intent
}

func normalizedManagerRememberedForwards(forwards []RememberedForward) []RememberedForward {
	return normalizeByPort(forwards, RememberedForward.WithDefaults, func(f RememberedForward) uint16 { return f.RemotePort })
}

func normalizedManagerPublishedForwards(forwards []PublishedForward) []PublishedForward {
	normalized := normalizeByPort(forwards, PublishedForward.WithDefaults, func(f PublishedForward) uint16 { return f.LocalPort })
	used := make(map[uint16]bool, len(normalized))
	return slices.DeleteFunc(normalized, func(f PublishedForward) bool {
		duplicate := used[f.RemotePort]
		used[f.RemotePort] = true
		return duplicate
	})
}

// Manager input is permissive: discard zero ports, keep the last service-port
// mapping, and return deterministic order. Disk validation is deliberately strict.
func normalizeByPort[T any](forwards []T, defaults func(T) T, port func(T) uint16) []T {
	indexed := make(map[uint16]T, len(forwards))
	for _, forward := range forwards {
		if key := port(forward); key != 0 {
			indexed[key] = defaults(forward)
		}
	}
	return slices.SortedFunc(maps.Values(indexed), func(a, b T) int { return int(port(a)) - int(port(b)) })
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
