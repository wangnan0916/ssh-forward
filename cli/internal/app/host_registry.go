package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gofrs/flock"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func discoveryPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "discovered-hosts.json")
}
func loadDiscovered(configPath string) (map[string]HostTarget, error) {
	targets := make(map[string]HostTarget)
	content, err := os.ReadFile(discoveryPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return targets, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(content, &targets); err != nil {
		return nil, err
	}
	if targets == nil {
		targets = make(map[string]HostTarget)
	}
	for name, target := range targets {
		if err := validateTarget(name, target); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func rememberDiscovered(ctx context.Context, configPath string, found map[string]HostTarget) error {
	// The background scan and an explicit `host discover` can run together.
	// Lock only the registry merge; main config remains owned by CLI mutations.
	if len(found) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return err
	}
	lock := flock.New(discoveryPath(configPath) + ".lock")
	defer lock.Close()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	existing, err := loadDiscovered(configPath)
	if err != nil {
		return err
	}
	changed := false
	for name, target := range found {
		if _, ok := existing[name]; ok {
			continue
		}
		existing[name] = target
		changed = true
	}
	if !changed {
		return nil
	}
	return writeJSONC(discoveryPath(configPath), existing)
}

// EditHost updates connection settings or toggles ignore state. A nil target
// preserves discovered connection settings when re-enabling a destination.
func EditHost(path, name string, target *HostTarget, ignore bool) error {
	config, err := loadConfigForWrite(path)
	if err != nil {
		return err
	}
	if !core.ValidHostName(name) {
		return errors.New("invalid host name")
	}
	config.IgnoredHosts = slices.DeleteFunc(config.IgnoredHosts, func(s string) bool { return s == name })
	if ignore {
		config.IgnoredHosts = append(config.IgnoredHosts, name)
	}
	if target != nil {
		target.Target = cmp.Or(target.Target, name)
		if err := validateTarget(name, *target); err != nil {
			return err
		}
		if config.Hosts == nil {
			config.Hosts = make(map[string]HostTarget)
		}
		config.Hosts[name] = *target
	}
	slices.Sort(config.IgnoredHosts)
	return config.save(path)
}

func HostList(path string) (map[string]HostTarget, []string, error) {
	config, err := loadConfigForWrite(path)
	if err != nil {
		return nil, nil, err
	}
	targets, err := config.hostTargets(context.Background(), path, "", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return targets, config.IgnoredHosts, nil
}

// hostTargets is the only host set. Discovered targets, explicit records, and
// any extra names are collapsed here, so later status and diagnostics see one
// entry per user and hostname.
func (config configuration) hostTargets(ctx context.Context, path, sshConfig string, extra map[string]HostTarget, resolve destinationResolver) (map[string]HostTarget, error) {
	targets, err := loadDiscovered(path)
	if err != nil {
		return nil, err
	}
	maps.Copy(targets, config.Hosts)
	for name, target := range extra {
		if _, ok := targets[name]; !ok {
			targets[name] = target
		}
	}
	for _, name := range config.IgnoredHosts {
		if _, ok := targets[name]; !ok {
			targets[name] = HostTarget{Target: name}
		}
	}
	return collapseSameHosts(ctx, sshConfig, targets, config.Hosts, resolve), nil
}
