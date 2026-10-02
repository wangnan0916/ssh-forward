package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gofrs/flock"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const configSchemaVersion = 6

// configFile is only the schema 6 JSON object. Rules live in configuration.
type configFile struct {
	Hosts                       map[string]HostTarget               `json:"hosts,omitempty"`
	IgnoredHosts                []string                            `json:"ignored_hosts,omitempty"`
	GlobalForwards              []core.RememberedForward            `json:"global_forwards,omitempty"`
	GlobalWorkingDirectoryRules []string                            `json:"global_working_directory_rules,omitempty"`
	GlobalIgnoredApps           []string                            `json:"global_ignored_apps,omitempty"`
	SchemaVersion               int                                 `json:"schema_version"`
	RememberedForwards          map[string][]core.RememberedForward `json:"remembered_forwards,omitempty"`
	PublishedForwards           map[string][]core.PublishedForward  `json:"published_forwards,omitempty"`
	WorkingDirectoryRules       map[string][]string                 `json:"working_directory_rules,omitempty"`
}

// configuration is the config. An empty scope applies to all hosts.
// Publications require a named scope. MarshalJSON writes schema 6.
type configuration struct {
	SchemaVersion int
	Hosts         map[string]HostTarget
	IgnoredHosts  []string
	Rules         map[string]*scopeRules
}

type scopeRules struct {
	Forwards    []core.RememberedForward
	Published   []core.PublishedForward
	Directories []string
	IgnoredApps []string
}

func (c *configuration) scope(host string) *scopeRules {
	if c.Rules == nil {
		c.Rules = map[string]*scopeRules{}
	}
	if c.Rules[host] == nil {
		c.Rules[host] = &scopeRules{}
	}
	return c.Rules[host]
}

func (c configuration) MarshalJSON() ([]byte, error) {
	file, err := c.wire()
	if err != nil {
		return nil, err
	}
	return json.Marshal(file)
}

func (c *configuration) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file configFile
	if err := decoder.Decode(&file); err != nil {
		return err
	}
	parsed, err := file.configuration()
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

func (file configFile) configuration() (configuration, error) {
	_, imports := file.RememberedForwards[""]
	_, publications := file.PublishedForwards[""]
	_, directories := file.WorkingDirectoryRules[""]
	if imports || publications || directories {
		return configuration{}, errors.New("empty host alias")
	}
	config := configuration{
		SchemaVersion: file.SchemaVersion,
		Hosts:         file.Hosts,
		IgnoredHosts:  file.IgnoredHosts,
		Rules:         map[string]*scopeRules{},
	}
	config.Rules[""] = &scopeRules{
		Forwards:    file.GlobalForwards,
		Directories: file.GlobalWorkingDirectoryRules,
		IgnoredApps: file.GlobalIgnoredApps,
	}
	for host, rules := range file.RememberedForwards {
		config.scope(host).Forwards = rules
	}
	for host, rules := range file.PublishedForwards {
		config.scope(host).Published = rules
	}
	for host, rules := range file.WorkingDirectoryRules {
		config.scope(host).Directories = rules
	}
	return config, nil
}

func (c configuration) wire() (configFile, error) {
	file := configFile{
		Hosts:                 c.Hosts,
		IgnoredHosts:          c.IgnoredHosts,
		SchemaVersion:         configSchemaVersion,
		RememberedForwards:    map[string][]core.RememberedForward{},
		PublishedForwards:     map[string][]core.PublishedForward{},
		WorkingDirectoryRules: map[string][]string{},
	}
	for host, rules := range c.Rules {
		if rules == nil {
			continue
		}
		if host == "" {
			if len(rules.Published) > 0 {
				return configFile{}, errors.New("published forwards require a host")
			}
			file.GlobalForwards = rules.Forwards
			file.GlobalWorkingDirectoryRules = rules.Directories
			file.GlobalIgnoredApps = rules.IgnoredApps
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
	return file, nil
}

func LoadConfig(path string) (configuration, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return configuration{}, err
	}
	return parseConfig(content)
}

func loadConfigForWrite(path string) (configuration, error) {
	config, err := LoadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return configuration{Rules: map[string]*scopeRules{}}, nil
	}
	return config, err
}

func (c configuration) save(path string) error {
	return writeJSONC(path, c)
}

// editConfig serializes the entire read-modify-write transaction across CLI
// processes. Lock a stable sidecar: atomic saves replace the config inode.
func editConfig(path string, edit func(*configuration) (bool, error)) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	lock := flock.New(path + ".lock")
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return false, err
	}
	if !locked {
		return false, ctx.Err()
	}
	config, err := loadConfigForWrite(path)
	if err != nil {
		return false, err
	}
	changed, err := edit(&config)
	if err != nil || !changed {
		return false, err
	}
	return true, config.save(path)
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
		IgnoredApps:           slices.Clone(global.IgnoredApps),
	}
}
