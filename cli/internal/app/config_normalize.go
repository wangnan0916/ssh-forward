package app

import (
	"errors"
	"fmt"
	"slices"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func parseConfig(content []byte) (configFile, error) {
	var config configFile
	if err := decodeJSONC(content, "config.jsonc", &config); err != nil {
		return configFile{}, err
	}
	return normalizeConfig(config)
}

func normalizeConfig(config configFile) (configFile, error) {
	if config.SchemaVersion < 1 || config.SchemaVersion > configSchemaVersion {
		return configFile{}, fmt.Errorf(
			"config.jsonc: unsupported schema_version %d (want 1..%d)",
			config.SchemaVersion,
			configSchemaVersion,
		)
	}
	if len(config.LegacyForwards) != 0 && config.RememberedForwards == nil {
		config.RememberedForwards = make(map[string][]core.RememberedForward)
	}
	for host, ports := range config.LegacyForwards {
		if host == "" {
			return configFile{}, errors.New("config.jsonc: empty host alias")
		}
		if slices.Contains(ports, uint16(0)) {
			return configFile{}, errors.New("config.jsonc: port must be between 1 and 65535")
		}
		for _, port := range normalizedLegacyPorts(ports) {
			config.RememberedForwards[host] = append(
				config.RememberedForwards[host],
				core.RememberedForward{RemotePort: port}.WithDefaults(),
			)
		}
	}
	config.LegacyForwards = nil
	for host, forwards := range config.RememberedForwards {
		if host == "" {
			return configFile{}, errors.New("config.jsonc: empty host alias")
		}
		if config.SchemaVersion < 4 {
			for index := range forwards {
				forward := &forwards[index]
				forward.AllowFallback = forward.LocalPort == 0 || forward.LocalPort == forward.RemotePort
			}
		}
		normalized, err := normalizedRememberedForwards(forwards)
		if err != nil {
			return configFile{}, err
		}
		config.RememberedForwards[host] = normalized
	}
	if config.SchemaVersion < configSchemaVersion {
		config.PublishedForwards = nil
	}
	for host, forwards := range config.PublishedForwards {
		if host == "" {
			return configFile{}, errors.New("config.jsonc: empty host alias")
		}
		normalized, err := normalizedPublishedForwards(forwards)
		if err != nil {
			return configFile{}, err
		}
		config.PublishedForwards[host] = normalized
	}
	for host, forwards := range config.RememberedForwards {
		if err := validateLocalPortReservations(forwards, config.PublishedForwards[host]); err != nil {
			return configFile{}, err
		}
	}
	for host, patterns := range config.WorkingDirectoryRules {
		if host == "" {
			return configFile{}, errors.New("config.jsonc: empty host alias")
		}
		normalized, err := normalizedWorkingDirectoryRules(patterns)
		if err != nil {
			return configFile{}, err
		}
		config.WorkingDirectoryRules[host] = normalized
	}
	return config, nil
}

func normalizedLegacyPorts(ports []uint16) []uint16 {
	ports = slices.Clone(ports)
	slices.Sort(ports)
	return slices.Compact(ports)
}

func normalizedRememberedForwards(forwards []core.RememberedForward) ([]core.RememberedForward, error) {
	normalized := make([]core.RememberedForward, 0, len(forwards))
	remotePorts := make(map[uint16]bool, len(forwards))
	localPorts := make(map[uint16]uint16, len(forwards))
	for _, forward := range forwards {
		forward, err := normalizedRememberedForward(forward)
		if err != nil {
			return nil, err
		}
		if remotePorts[forward.RemotePort] {
			return nil, fmt.Errorf("config.jsonc: duplicate remote port %d", forward.RemotePort)
		}
		if remotePort, found := localPorts[forward.LocalPort]; found {
			return nil, fmt.Errorf(
				"config.jsonc: local port %d is used by remote ports %d and %d",
				forward.LocalPort, remotePort, forward.RemotePort,
			)
		}
		remotePorts[forward.RemotePort] = true
		localPorts[forward.LocalPort] = forward.RemotePort
		normalized = append(normalized, forward)
	}
	slices.SortFunc(normalized, func(left, right core.RememberedForward) int {
		return int(left.RemotePort) - int(right.RemotePort)
	})
	return normalized, nil
}

func normalizedRememberedForward(forward core.RememberedForward) (core.RememberedForward, error) {
	if forward.RemotePort == 0 {
		return core.RememberedForward{}, errors.New("config.jsonc: remote port must be between 1 and 65535")
	}
	return forward.WithDefaults(), nil
}

func normalizedPublishedForwards(forwards []core.PublishedForward) ([]core.PublishedForward, error) {
	normalized := make([]core.PublishedForward, 0, len(forwards))
	localPorts := make(map[uint16]bool, len(forwards))
	remotePorts := make(map[uint16]uint16, len(forwards))
	for _, forward := range forwards {
		forward, err := normalizedPublishedForward(forward)
		if err != nil {
			return nil, err
		}
		if localPorts[forward.LocalPort] {
			return nil, fmt.Errorf("config.jsonc: duplicate published local port %d", forward.LocalPort)
		}
		if localPort, found := remotePorts[forward.RemotePort]; found {
			return nil, fmt.Errorf(
				"config.jsonc: published remote port %d is used by local ports %d and %d",
				forward.RemotePort, localPort, forward.LocalPort,
			)
		}
		localPorts[forward.LocalPort] = true
		remotePorts[forward.RemotePort] = forward.LocalPort
		normalized = append(normalized, forward)
	}
	slices.SortFunc(normalized, func(left, right core.PublishedForward) int {
		return int(left.LocalPort) - int(right.LocalPort)
	})
	return normalized, nil
}

func normalizedPublishedForward(forward core.PublishedForward) (core.PublishedForward, error) {
	if forward.LocalPort == 0 {
		return core.PublishedForward{}, errors.New("config.jsonc: local port must be between 1 and 65535")
	}
	return forward.WithDefaults(), nil
}

func validateLocalPortReservations(
	remembered []core.RememberedForward,
	published []core.PublishedForward,
) error {
	reserved := make(map[uint16]struct{}, len(published))
	for _, forward := range published {
		reserved[forward.LocalPort] = struct{}{}
	}
	for _, forward := range remembered {
		if _, found := reserved[forward.LocalPort]; found && !forward.AllowFallback {
			return fmt.Errorf(
				"config.jsonc: local port %d is reserved by a published forward",
				forward.LocalPort,
			)
		}
	}
	return nil
}
