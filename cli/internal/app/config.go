package app

import (
	"errors"
	"os"
	"slices"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const configSchemaVersion = 6

// configFile is the disk format; migration and encoding stay at this boundary.
type configFile struct {
	Hosts                       map[string]HostTarget               `json:"hosts,omitempty"`
	IgnoredHosts                []string                            `json:"ignored_hosts,omitempty"`
	GlobalForwards              []core.RememberedForward            `json:"global_forwards,omitempty"`
	GlobalWorkingDirectoryRules []string                            `json:"global_working_directory_rules,omitempty"`
	SchemaVersion               int                                 `json:"schema_version"`
	DefaultHost                 string                              `json:"default_host,omitempty"`
	LegacyForwards              map[string][]uint16                 `json:"forwards,omitempty"`
	RememberedForwards          map[string][]core.RememberedForward `json:"remembered_forwards,omitempty"`
	PublishedForwards           map[string][]core.PublishedForward  `json:"published_forwards,omitempty"`
	WorkingDirectoryRules       map[string][]string                 `json:"working_directory_rules,omitempty"`
}

func LoadConfig(path string) (configFile, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return configFile{}, err
	}
	return parseConfig(content)
}

// configuration is independent of the JSON schema. The empty scope applies
// to all hosts; published forwards always require a named scope.
type configuration struct {
	Hosts        map[string]HostTarget
	IgnoredHosts []string
	Rules        map[string]*scopeRules
}

type scopeRules struct {
	Forwards    []core.RememberedForward
	Published   []core.PublishedForward
	Directories []string
}

func (c configuration) scope(host string) *scopeRules {
	if c.Rules[host] == nil {
		c.Rules[host] = &scopeRules{}
	}
	return c.Rules[host]
}

func (file configFile) model() configuration {
	c := configuration{Hosts: file.Hosts, IgnoredHosts: file.IgnoredHosts, Rules: make(map[string]*scopeRules)}
	c.Rules[""] = &scopeRules{Forwards: file.GlobalForwards, Directories: file.GlobalWorkingDirectoryRules}
	for host, rules := range file.RememberedForwards {
		c.scope(host).Forwards = rules
	}
	for host, rules := range file.PublishedForwards {
		c.scope(host).Published = rules
	}
	for host, rules := range file.WorkingDirectoryRules {
		c.scope(host).Directories = rules
	}
	return c
}

func loadConfigForWrite(path string) (configuration, error) {
	file, err := LoadConfig(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return configuration{}, err
	}
	return file.model(), nil
}

func (c configuration) save(path string) error {
	file := configFile{Hosts: c.Hosts, IgnoredHosts: c.IgnoredHosts,
		RememberedForwards:    make(map[string][]core.RememberedForward),
		PublishedForwards:     make(map[string][]core.PublishedForward),
		WorkingDirectoryRules: make(map[string][]string)}
	for host, rules := range c.Rules {
		if host == "" {
			if len(rules.Published) > 0 {
				return errors.New("published forwards require a host")
			}
			file.GlobalForwards, file.GlobalWorkingDirectoryRules = rules.Forwards, rules.Directories
			continue
		}
		if len(rules.Forwards) > 0 {
			file.RememberedForwards[host] = rules.Forwards
		}
		if len(rules.Published) > 0 {
			file.PublishedForwards[host] = rules.Published
		}
		if len(rules.Directories) > 0 {
			file.WorkingDirectoryRules[host] = rules.Directories
		}
	}
	return saveConfig(path, file)
}

func saveConfig(path string, config configFile) error {
	config.SchemaVersion = configSchemaVersion
	config.LegacyForwards = nil
	return writeJSONC(path, config)
}

// HostIntent returns persistent forwarding intent for host.
func HostIntent(path, host string) (core.ForwardingIntent, error) {
	config, err := loadConfigForWrite(path)
	if err != nil {
		return core.ForwardingIntent{}, err
	}
	return effectiveIntent(config, host), nil
}

func effectiveIntent(config configuration, host string) core.ForwardingIntent {
	global := config.scope("")
	scoped := &scopeRules{}
	if host != "" {
		scoped = config.scope(host)
	}
	return core.ForwardingIntent{
		AutoForwards:          slices.Clone(global.Forwards),
		RememberedForwards:    slices.Clone(scoped.Forwards),
		PublishedForwards:     slices.Clone(scoped.Published),
		WorkingDirectoryRules: append(slices.Clone(global.Directories), scoped.Directories...),
	}
}
