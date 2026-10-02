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

// Identity is resolved SSH user/hostname, separate from connection arguments.
type hostIdentity struct {
	User     string `json:"user"`
	Hostname string `json:"hostname"`
}

type discoveredHost struct {
	HostTarget
	Identity *hostIdentity `json:"identity,omitempty"`
}

func discoveryPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "discovered-hosts.json")
}
func loadDiscovered(configPath string) (map[string]discoveredHost, error) {
	targets := make(map[string]discoveredHost)
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
		targets = make(map[string]discoveredHost)
	}
	for name, target := range targets {
		if err := validateTarget(name, target.HostTarget); err != nil {
			return nil, err
		}
		if target.Identity != nil && (target.Identity.User == "" || target.Identity.Hostname == "") {
			return nil, errors.New("invalid discovered host identity")
		}
	}
	return targets, nil
}

func rememberDiscovered(ctx context.Context, configPath string, found map[string]HostTarget) error {
	return mergeDiscovered(ctx, configPath, found, nil)
}

// Process discovery and Manager identity updates share the existing file lock.
// Resolution happens before this merge; host listing never calls it.
func mergeDiscovered(ctx context.Context, configPath string, found map[string]HostTarget, resolved map[string]discoveredHost) error {
	if len(found) == 0 && len(resolved) == 0 {
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
		existing[name] = discoveredHost{HostTarget: target}
		changed = true
	}
	for name, fresh := range resolved {
		old, ok := existing[name]
		if !ok || old.Target != fresh.Target || !slices.Equal(old.Arguments, fresh.Arguments) {
			continue // Do not recreate a removed record or update changed settings.
		}
		if old.Identity == nil || *old.Identity != *fresh.Identity {
			old.Identity = fresh.Identity
			existing[name] = old
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return writeJSONC(discoveryPath(configPath), existing)
}

// Only a genuinely missing -F file is expired, not a parse or permission error.
func missingHostConfig(target HostTarget) bool {
	path := ""
	for i := 0; i+1 < len(target.Arguments); i += 2 {
		if target.Arguments[i] == "-F" {
			path = target.Arguments[i+1] // OpenSSH uses the last -F.
		}
	}
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

// EditHost updates connection settings or toggles ignore state. A nil target
// preserves discovered connection settings when re-enabling a destination.
func EditHost(path, name string, target *HostTarget, ignore bool) error {
	if !core.ValidHostName(name) {
		return errors.New("invalid host name")
	}
	if target != nil {
		target.Target = cmp.Or(target.Target, name)
		if err := validateTarget(name, *target); err != nil {
			return err
		}
	}
	_, err := editConfig(path, func(config *configuration) (bool, error) {
		config.IgnoredHosts = slices.DeleteFunc(config.IgnoredHosts, func(s string) bool { return s == name })
		if ignore {
			config.IgnoredHosts = append(config.IgnoredHosts, name)
		}
		if target != nil {
			if config.Hosts == nil {
				config.Hosts = make(map[string]HostTarget)
			}
			config.Hosts[name] = *target
		}
		slices.Sort(config.IgnoredHosts)
		return true, nil
	})
	return err
}

func HostList(path string) (map[string]HostTarget, []string, error) {
	config, err := loadConfigForWrite(path)
	if err != nil {
		return nil, nil, err
	}
	targets, _, err := config.hostTargets(context.Background(), path, "", nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return targets, config.IgnoredHosts, nil
}

// hostTargets is the only host set. Discovered targets, explicit records, and
// any extra names are collapsed here, so later status and diagnostics see one
// entry per user and hostname.
func (config configuration) hostTargets(ctx context.Context, path, sshConfig string, extra map[string]HostTarget, resolve destinationResolver) (map[string]HostTarget, map[string]discoveredHost, error) {
	records, err := loadDiscovered(path)
	if err != nil {
		return nil, nil, err
	}
	targets := make(map[string]HostTarget, len(records))
	cached := make(map[string]hostIdentity)
	for name, record := range records {
		if _, explicit := config.Hosts[name]; explicit {
			continue
		}
		if missingHostConfig(record.HostTarget) {
			if record.Identity == nil {
				continue // Keep the legacy disk record, but not a phantom runtime.
			}
			cached[name] = *record.Identity
		}
		targets[name] = record.HostTarget
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
	targets, resolved := collapseSameHosts(ctx, sshConfig, targets, config.Hosts, resolve, cached)
	return targets, resolved, nil
}
