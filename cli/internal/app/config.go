package app

import (
	"errors"
	"fmt"
	"os"
	"path"
	"slices"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const configSchemaVersion = 4

var ErrInvalidWorkingDirectoryRule = errors.New("invalid working-directory glob")

// configFile is the whole persistent product model. LegacyForwards is retained
// only to migrate schema versions 1 and 2.
type configFile struct {
	SchemaVersion         int                                 `json:"schema_version"`
	DefaultHost           string                              `json:"default_host,omitempty"`
	LegacyForwards        map[string][]uint16                 `json:"forwards,omitempty"`
	RememberedForwards    map[string][]core.RememberedForward `json:"remembered_forwards,omitempty"`
	WorkingDirectoryRules map[string][]string                 `json:"working_directory_rules,omitempty"`
}

func LoadConfig(path string) (configFile, error) {
	var config configFile
	if err := readJSONC(path, "config.jsonc", &config); err != nil {
		return configFile{}, err
	}
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
				core.RememberedForward{RemotePort: port, LocalPort: port, AllowFallback: true},
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

func loadConfigForWrite(path string) (configFile, error) {
	config, err := LoadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return configFile{SchemaVersion: configSchemaVersion}, nil
	}
	if err != nil {
		return configFile{}, err
	}
	return config, nil
}

func saveConfig(path string, config configFile) error {
	config.SchemaVersion = configSchemaVersion
	config.LegacyForwards = nil
	if len(config.RememberedForwards) == 0 {
		config.RememberedForwards = nil
	}
	if len(config.WorkingDirectoryRules) == 0 {
		config.WorkingDirectoryRules = nil
	}
	return writeJSONC(path, config)
}

func SetDefaultHost(path, host string) error {
	config, err := loadConfigForWrite(path)
	if err != nil {
		return err
	}
	config.DefaultHost = host
	return saveConfig(path, config)
}

// HostIntent returns all persistent forwarding intent for host. A missing
// config means no remembered forwards or working-directory rules.
func HostIntent(path, host string) (core.ForwardingIntent, error) {
	config, err := LoadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return core.ForwardingIntent{}, nil
	}
	if err != nil {
		return core.ForwardingIntent{}, err
	}
	return core.ForwardingIntent{
		RememberedForwards:    slices.Clone(config.RememberedForwards[host]),
		WorkingDirectoryRules: slices.Clone(config.WorkingDirectoryRules[host]),
	}, nil
}

func RemoveRememberedForward(path, host string, remotePort uint16) (bool, error) {
	if host == "" || remotePort == 0 {
		return false, errors.New("host and remote port are required")
	}
	config, err := loadConfigForWrite(path)
	if err != nil {
		return false, err
	}
	forwards := config.RememberedForwards[host]
	index, found := rememberedForwardIndex(forwards, remotePort)
	if !found {
		return false, nil
	}
	forwards = slices.Delete(forwards, index, index+1)
	if len(forwards) == 0 {
		delete(config.RememberedForwards, host)
	} else {
		config.RememberedForwards[host] = forwards
	}
	return true, saveConfig(path, config)
}

func SetRememberedForward(configPath, host string, forward core.RememberedForward) (bool, error) {
	if host == "" {
		return false, errors.New("host is required")
	}
	forward, err := normalizedRememberedForward(forward)
	if err != nil {
		return false, err
	}
	config, err := loadConfigForWrite(configPath)
	if err != nil {
		return false, err
	}
	if config.RememberedForwards == nil {
		config.RememberedForwards = make(map[string][]core.RememberedForward)
	}
	forwards := config.RememberedForwards[host]
	index, found := rememberedForwardIndex(forwards, forward.RemotePort)
	if found && forwards[index] == forward {
		return false, nil
	}
	for _, existing := range forwards {
		if existing.RemotePort != forward.RemotePort && existing.LocalPort == forward.LocalPort {
			return false, fmt.Errorf(
				"config.jsonc: local port %d is already used by remote port %d for %s",
				forward.LocalPort, existing.RemotePort, host,
			)
		}
	}
	if found {
		forwards[index] = forward
	} else {
		forwards = slices.Insert(forwards, index, forward)
	}
	config.RememberedForwards[host] = forwards
	return true, saveConfig(configPath, config)
}

func AddWorkingDirectoryRule(configPath, host, pattern string) (bool, error) {
	return updateWorkingDirectoryRule(configPath, host, pattern, true)
}

func RemoveWorkingDirectoryRule(configPath, host, pattern string) (bool, error) {
	return updateWorkingDirectoryRule(configPath, host, pattern, false)
}

func normalizedLegacyPorts(ports []uint16) []uint16 {
	ports = slices.Clone(ports)
	slices.Sort(ports)
	return slices.Compact(ports)
}

func rememberedForwardIndex(forwards []core.RememberedForward, remotePort uint16) (int, bool) {
	return slices.BinarySearchFunc(forwards, remotePort, func(forward core.RememberedForward, remotePort uint16) int {
		return int(forward.RemotePort) - int(remotePort)
	})
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

func updateWorkingDirectoryRule(configPath, host, pattern string, adding bool) (bool, error) {
	if host == "" {
		return false, errors.New("host is required")
	}
	if err := validateWorkingDirectoryRule(pattern); err != nil {
		return false, err
	}
	config, err := loadConfigForWrite(configPath)
	if err != nil {
		return false, err
	}
	if config.WorkingDirectoryRules == nil {
		config.WorkingDirectoryRules = make(map[string][]string)
	}
	hostPatterns := config.WorkingDirectoryRules[host]
	index, found := slices.BinarySearch(hostPatterns, pattern)
	if adding == found {
		return false, nil
	}
	if adding {
		hostPatterns = slices.Insert(hostPatterns, index, pattern)
	} else {
		hostPatterns = slices.Delete(hostPatterns, index, index+1)
	}
	if len(hostPatterns) == 0 {
		delete(config.WorkingDirectoryRules, host)
	} else {
		config.WorkingDirectoryRules[host] = hostPatterns
	}
	return true, saveConfig(configPath, config)
}

func normalizedWorkingDirectoryRules(patterns []string) ([]string, error) {
	normalized := slices.Clone(patterns)
	for _, pattern := range normalized {
		if err := validateWorkingDirectoryRule(pattern); err != nil {
			return nil, err
		}
	}
	slices.Sort(normalized)
	return slices.Compact(normalized), nil
}

func validateWorkingDirectoryRule(pattern string) error {
	if !path.IsAbs(pattern) {
		return fmt.Errorf("%w: must be an absolute remote path", ErrInvalidWorkingDirectoryRule)
	}
	if !doublestar.ValidatePattern(pattern) {
		return fmt.Errorf("%w: malformed pattern", ErrInvalidWorkingDirectoryRule)
	}
	return nil
}
