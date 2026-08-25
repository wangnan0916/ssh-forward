package core

import (
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
	servicePort := forward.preferred.RemotePort
	if forward.preferred.Direction == LocalToRemote {
		servicePort = forward.preferred.LocalPort
	}
	return forwardKey{direction: forward.preferred.Direction, servicePort: servicePort}
}

func normalizedForwardingIntent(intent ForwardingIntent) ForwardingIntent {
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
	byRemotePort := make(map[uint16]RememberedForward, len(forwards))
	for _, forward := range forwards {
		if forward.RemotePort == 0 {
			continue
		}
		forward = forward.WithDefaults()
		byRemotePort[forward.RemotePort] = forward
	}
	normalized := make([]RememberedForward, 0, len(byRemotePort))
	for _, forward := range byRemotePort {
		normalized = append(normalized, forward)
	}
	slices.SortFunc(normalized, func(left, right RememberedForward) int {
		return int(left.RemotePort) - int(right.RemotePort)
	})
	return normalized
}

func normalizedManagerPublishedForwards(forwards []PublishedForward) []PublishedForward {
	byLocalPort := make(map[uint16]PublishedForward, len(forwards))
	for _, forward := range forwards {
		forward = forward.WithDefaults()
		if forward.LocalPort != 0 && forward.RemotePort != 0 {
			byLocalPort[forward.LocalPort] = forward
		}
	}
	normalized := make([]PublishedForward, 0, len(byLocalPort))
	for _, forward := range byLocalPort {
		normalized = append(normalized, forward)
	}
	slices.SortFunc(normalized, func(left, right PublishedForward) int {
		return int(left.LocalPort) - int(right.LocalPort)
	})
	usedRemotePorts := make(map[uint16]struct{}, len(normalized))
	unique := normalized[:0]
	for _, forward := range normalized {
		if _, found := usedRemotePorts[forward.RemotePort]; found {
			continue
		}
		usedRemotePorts[forward.RemotePort] = struct{}{}
		unique = append(unique, forward)
	}
	return unique
}

func reservedLocalPorts(forwards []PublishedForward) map[uint16]struct{} {
	ports := make(map[uint16]struct{}, len(forwards))
	for _, forward := range forwards {
		ports[forward.LocalPort] = struct{}{}
	}
	return ports
}

func buildDesiredForwards(
	remembered []RememberedForward,
	published []PublishedForward,
	listeners map[uint16]Listener,
	workingDirectoryRules []string,
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
	for remotePort, listener := range listeners {
		key := forwardKey{direction: RemoteToLocal, servicePort: remotePort}
		if _, selected := desired[key]; selected {
			continue
		}
		if _, published := publishedRemotePorts[remotePort]; published {
			continue
		}
		if matchesWorkingDirectory(workingDirectoryRules, listener.WorkingDirectory) {
			desired[key] = desiredAutomaticForward(remotePort)
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

func forwardStatus(
	desired desiredForward,
	state ForwardState,
	diagnostic string,
	target ForwardTarget,
) ForwardStatus {
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
		preferred: ForwardTarget{
			Direction:  RemoteToLocal,
			RemotePort: forward.RemotePort,
			LocalPort:  forward.LocalPort,
		},
		allowFallback: forward.AllowFallback,
	}
}

func desiredAutomaticForward(port uint16) desiredForward {
	return desiredForward{
		preferred: ForwardTarget{
			Direction:  RemoteToLocal,
			RemotePort: port,
			LocalPort:  port,
		},
		automatic:     true,
		allowFallback: true,
	}
}

func desiredPublishedForward(forward PublishedForward) desiredForward {
	return desiredForward{
		preferred: ForwardTarget{
			Direction:  LocalToRemote,
			RemotePort: forward.RemotePort,
			LocalPort:  forward.LocalPort,
		},
	}
}
