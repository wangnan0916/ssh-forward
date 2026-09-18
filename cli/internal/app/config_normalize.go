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

func normalizeConfig(file configFile) (configFile, error) {
	if file.SchemaVersion < 1 || file.SchemaVersion > configSchemaVersion {
		return configFile{}, fmt.Errorf("config.jsonc: unsupported schema_version %d (want 1..%d)", file.SchemaVersion, configSchemaVersion)
	}
	if len(file.LegacyForwards) > 0 && file.RememberedForwards == nil {
		file.RememberedForwards = make(map[string][]core.RememberedForward)
	}
	for host, ports := range file.LegacyForwards {
		if host == "" {
			return configFile{}, errors.New("config.jsonc: empty host alias")
		}
		for _, port := range slices.Compact(slices.Sorted(slices.Values(ports))) {
			file.RememberedForwards[host] = append(file.RememberedForwards[host], core.RememberedForward{RemotePort: port}.WithDefaults())
		}
	}
	for _, forwards := range file.RememberedForwards {
		if file.SchemaVersion < 4 {
			for i := range forwards {
				forwards[i].AllowFallback = forwards[i].LocalPort == 0 || forwards[i].LocalPort == forwards[i].RemotePort
			}
		}
	}
	if file.SchemaVersion < 5 {
		file.PublishedForwards = nil
	}
	if file.SchemaVersion < 6 {
		file.Hosts = nil
		file.IgnoredHosts = nil
		file.GlobalForwards = nil
		file.GlobalWorkingDirectoryRules = nil
	}
	// Empty keys in historical per-host maps must not become global rules.
	_, imports := file.RememberedForwards[""]
	_, publications := file.PublishedForwards[""]
	_, directories := file.WorkingDirectoryRules[""]
	if imports || publications || directories {
		return configFile{}, errors.New("config.jsonc: empty host alias")
	}
	config := file.model()
	if config.Hosts == nil {
		config.Hosts = make(map[string]HostTarget)
	}
	remember := func(host string) {
		if _, exists := config.Hosts[host]; host != "" && !exists {
			config.Hosts[host] = HostTarget{Target: host}
		}
	}
	remember(file.DefaultHost)
	for host, rules := range config.Rules {
		remember(host)
		if err := rules.normalize(); err != nil {
			return configFile{}, err
		}
	}
	for name, target := range config.Hosts {
		if err := validateTarget(name, target); err != nil {
			return configFile{}, err
		}
	}
	normalized, err := config.file()
	normalized.SchemaVersion = file.SchemaVersion
	return normalized, err
}

func (rules *scopeRules) normalize() error {
	if err := errors.Join(
		normalizeInto(&rules.Forwards, normalizedRememberedForwards),
		normalizeInto(&rules.Published, normalizedPublishedForwards),
		normalizeInto(&rules.Directories, normalizedWorkingDirectoryRules),
	); err != nil {
		return err
	}
	return validateLocalPortReservations(rules.Forwards, rules.Published)
}

func normalizeInto[T any](items *[]T, normalize func([]T) ([]T, error)) error {
	if len(*items) == 0 {
		return nil
	}
	values, err := normalize(*items)
	if err == nil {
		*items = values
	}
	return err
}

func normalizedRememberedForwards(forwards []core.RememberedForward) ([]core.RememberedForward, error) {
	return normalizeForwards(forwards, normalizedRememberedForward,
		func(f core.RememberedForward) (uint16, uint16) { return f.RemotePort, f.LocalPort }, "remote", "local")
}

func normalizedRememberedForward(forward core.RememberedForward) (core.RememberedForward, error) {
	if forward.RemotePort == 0 {
		return core.RememberedForward{}, errors.New("config.jsonc: remote port must be between 1 and 65535")
	}
	return forward.WithDefaults(), nil
}

func normalizedPublishedForwards(forwards []core.PublishedForward) ([]core.PublishedForward, error) {
	return normalizeForwards(forwards, normalizedPublishedForward,
		func(f core.PublishedForward) (uint16, uint16) { return f.LocalPort, f.RemotePort }, "published local", "published remote")
}

// Both forwarding directions have a service port (identity) and a bind port
// (exclusive reservation). Their validation differs only in those roles.
func normalizeForwards[T any](items []T, defaults func(T) (T, error), ports func(T) (uint16, uint16), service, bind string) ([]T, error) {
	normalized := make([]T, 0, len(items))
	services, bindings := make(map[uint16]bool), make(map[uint16]uint16)
	for _, item := range items {
		item, err := defaults(item)
		if err != nil {
			return nil, err
		}
		source, target := ports(item)
		if services[source] {
			return nil, fmt.Errorf("config.jsonc: duplicate %s port %d", service, source)
		}
		if previous, found := bindings[target]; found {
			return nil, fmt.Errorf("config.jsonc: %s port %d is used by %s ports %d and %d", bind, target, service, previous, source)
		}
		services[source], bindings[target] = true, source
		normalized = append(normalized, item)
	}
	slices.SortFunc(normalized, func(a, b T) int {
		left, _ := ports(a)
		right, _ := ports(b)
		return int(left) - int(right)
	})
	return normalized, nil
}

func normalizedPublishedForward(forward core.PublishedForward) (core.PublishedForward, error) {
	if forward.LocalPort == 0 {
		return core.PublishedForward{}, errors.New("config.jsonc: local port must be between 1 and 65535")
	}
	return forward.WithDefaults(), nil
}

func validateLocalPortReservations(remembered []core.RememberedForward, published []core.PublishedForward) error {
	reserved := make(map[uint16]struct{}, len(published))
	for _, forward := range published {
		reserved[forward.LocalPort] = struct{}{}
	}
	for _, forward := range remembered {
		if _, found := reserved[forward.LocalPort]; found && !forward.AllowFallback {
			return fmt.Errorf("config.jsonc: local port %d is reserved by a published forward", forward.LocalPort)
		}
	}
	return nil
}
