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

const configSchemaVersion = 2

var ErrInvalidWorkingDirectoryRule = errors.New("invalid working-directory glob")

// configFile is the whole persistent product model. A host alias selects both
// remembered ports and working-directory rules.
type configFile struct {
	SchemaVersion         int                 `json:"schema_version"`
	DefaultHost           string              `json:"default_host,omitempty"`
	Forwards              map[string][]uint16 `json:"forwards,omitempty"`
	WorkingDirectoryRules map[string][]string `json:"working_directory_rules,omitempty"`
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
	for host, ports := range config.Forwards {
		if host == "" {
			return configFile{}, errors.New("config.jsonc: empty host alias")
		}
		if slices.Contains(ports, uint16(0)) {
			return configFile{}, errors.New("config.jsonc: port must be between 1 and 65535")
		}
		config.Forwards[host] = normalizedPorts(ports)
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
	if len(config.Forwards) == 0 {
		config.Forwards = nil
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
// config means no remembered ports or working-directory rules.
func HostIntent(path, host string) (core.ForwardingIntent, error) {
	config, err := LoadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return core.ForwardingIntent{}, nil
	}
	if err != nil {
		return core.ForwardingIntent{}, err
	}
	return core.ForwardingIntent{
		RememberedPorts:       slices.Clone(config.Forwards[host]),
		WorkingDirectoryRules: slices.Clone(config.WorkingDirectoryRules[host]),
	}, nil
}

func AddPort(path, host string, port uint16) (bool, error) {
	return updatePort(path, host, port, true)
}

func RemovePort(path, host string, port uint16) (bool, error) {
	return updatePort(path, host, port, false)
}

func AddWorkingDirectoryRule(configPath, host, pattern string) (bool, error) {
	return updateWorkingDirectoryRule(configPath, host, pattern, true)
}

func RemoveWorkingDirectoryRule(configPath, host, pattern string) (bool, error) {
	return updateWorkingDirectoryRule(configPath, host, pattern, false)
}

func updatePort(path, host string, port uint16, adding bool) (bool, error) {
	if host == "" || port == 0 {
		return false, errors.New("host and port are required")
	}
	config, err := loadConfigForWrite(path)
	if err != nil {
		return false, err
	}
	if config.Forwards == nil {
		config.Forwards = make(map[string][]uint16)
	}
	ports := config.Forwards[host]
	index, found := slices.BinarySearch(ports, port)
	if adding == found {
		return false, nil
	}
	if adding {
		ports = slices.Insert(ports, index, port)
	} else {
		ports = slices.Delete(ports, index, index+1)
	}
	if len(ports) == 0 {
		delete(config.Forwards, host)
	} else {
		config.Forwards[host] = ports
	}
	return true, saveConfig(path, config)
}

func normalizedPorts(ports []uint16) []uint16 {
	ports = slices.Clone(ports)
	slices.Sort(ports)
	return slices.Compact(ports)
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
