package app

import (
	"fmt"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func parseConfig(content []byte) (configuration, error) {
	var config configuration
	if err := decodeJSONC(content, "config.jsonc", &config); err != nil {
		return configuration{}, err
	}
	return normalizeConfig(config)
}

func normalizeConfig(config configuration) (configuration, error) {
	if config.SchemaVersion != configSchemaVersion {
		return configuration{}, fmt.Errorf("config.jsonc: unsupported schema_version %d (want %d)", config.SchemaVersion, configSchemaVersion)
	}
	if config.Hosts == nil {
		config.Hosts = map[string]HostTarget{}
	}
	if config.Rules == nil {
		config.Rules = map[string]*scopeRules{}
	}
	remember := func(host string) {
		if _, exists := config.Hosts[host]; host != "" && !exists {
			config.Hosts[host] = HostTarget{Target: host}
		}
	}
	for host, rules := range config.Rules {
		remember(host)
		if rules == nil {
			continue
		}
		if err := rules.normalize(); err != nil {
			return configuration{}, err
		}
	}
	for name, target := range config.Hosts {
		if err := validateTarget(name, target); err != nil {
			return configuration{}, err
		}
	}
	return config, nil
}

func (rules *scopeRules) normalize() error {
	var err error
	if rules.Forwards, err = prefixConfig(core.NormalizeRememberedForwards(rules.Forwards)); err != nil {
		return err
	}
	if rules.Published, err = prefixConfig(core.NormalizePublishedForwards(rules.Published)); err != nil {
		return err
	}
	if rules.Directories, err = prefixConfig(core.NormalizeWorkingDirectoryRules(rules.Directories)); err != nil {
		return err
	}
	if rules.IgnoredApps, err = prefixConfig(core.NormalizeIgnoredApps(rules.IgnoredApps)); err != nil {
		return err
	}
	return validateLocalPortReservations(rules.Forwards, rules.Published)
}

func prefixConfig[T any](value T, err error) (T, error) {
	if err != nil {
		err = fmt.Errorf("config.jsonc: %w", err)
	}
	return value, err
}

func normalizedRememberedForward(forward core.RememberedForward) (core.RememberedForward, error) {
	normalized, err := prefixConfig(core.NormalizeRememberedForwards([]core.RememberedForward{forward}))
	if err != nil {
		return core.RememberedForward{}, err
	}
	return normalized[0], nil
}

func normalizedPublishedForward(forward core.PublishedForward) (core.PublishedForward, error) {
	normalized, err := prefixConfig(core.NormalizePublishedForwards([]core.PublishedForward{forward}))
	if err != nil {
		return core.PublishedForward{}, err
	}
	return normalized[0], nil
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
