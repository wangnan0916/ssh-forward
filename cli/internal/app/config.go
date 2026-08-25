package app

import (
	"errors"
	"os"
	"slices"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const configSchemaVersion = 5

// configFile is the whole persistent product model. LegacyForwards is retained
// only to migrate schema versions 1 and 2.
type configFile struct {
	SchemaVersion         int                                 `json:"schema_version"`
	DefaultHost           string                              `json:"default_host,omitempty"`
	LegacyForwards        map[string][]uint16                 `json:"forwards,omitempty"`
	RememberedForwards    map[string][]core.RememberedForward `json:"remembered_forwards,omitempty"`
	PublishedForwards     map[string][]core.PublishedForward  `json:"published_forwards,omitempty"`
	WorkingDirectoryRules map[string][]string                 `json:"working_directory_rules,omitempty"`
}

func LoadConfig(path string) (configFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return configFile{}, err
	}
	return parseConfig(content)
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
	if len(config.PublishedForwards) == 0 {
		config.PublishedForwards = nil
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
// config means no persistent intent.
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
		PublishedForwards:     slices.Clone(config.PublishedForwards[host]),
		WorkingDirectoryRules: slices.Clone(config.WorkingDirectoryRules[host]),
	}, nil
}
