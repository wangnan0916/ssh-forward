package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// hostIdentity is the user/hostname OpenSSH resolved, not a connection hash
// or a guarantee that two different hostnames identify one physical machine.
type hostIdentity struct {
	User     string `json:"user"`
	Hostname string `json:"hostname"`
}

// Discovery metadata stays out of config.jsonc and targetID. Old registry
// records without identity remain readable, and old CLIs ignore this field.
type discoveredHost struct {
	HostTarget
	Identity *hostIdentity `json:"identity,omitempty"`
}

// The expected identity belongs to the registry snapshot read before ssh -G.
// It is not persisted: it prevents an older observation overwriting an identity
// refreshed by another writer while connection arguments remained the same.
type discoveredIdentityUpdate struct {
	HostTarget
	Identity hostIdentity
	Previous *hostIdentity
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
		if target.Identity != nil {
			if target.Identity.User == "" || target.Identity.Hostname == "" {
				return nil, fmt.Errorf("discovered-hosts.json: incomplete identity for %s", name)
			}
			target.Identity.Hostname = strings.ToLower(target.Identity.Hostname)
		}
	}
	return targets, nil
}

// Process discovery and identity refresh share one locked registry transaction.
// Resolve outside the lock; never hold it across an OpenSSH subprocess.
func editDiscovered(ctx context.Context, configPath string, edit func(map[string]discoveredHost) bool) error {
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
	if !edit(existing) {
		return nil
	}
	return writeJSONC(discoveryPath(configPath), existing)
}

func rememberDiscovered(ctx context.Context, configPath string, found map[string]HostTarget) error {
	if len(found) == 0 {
		return nil
	}
	return editDiscovered(ctx, configPath, func(existing map[string]discoveredHost) bool {
		changed := false
		for name, target := range found {
			if _, ok := existing[name]; !ok {
				existing[name] = discoveredHost{HostTarget: target}
				changed = true
			}
		}
		return changed
	})
}

func sameHostConnection(left, right HostTarget) bool {
	return left.Target == right.Target && slices.Equal(left.Arguments, right.Arguments)
}

// A stale resolver result must not recreate a removed record or attach its
// identity to connection settings that changed while resolution was running.
func rememberHostIdentities(ctx context.Context, path string, learned map[string]discoveredIdentityUpdate) error {
	if len(learned) == 0 {
		return nil
	}
	return editDiscovered(ctx, path, func(existing map[string]discoveredHost) bool {
		changed := false
		for name, resolved := range learned {
			previous, ok := existing[name]
			if !ok || !sameHostConnection(previous.HostTarget, resolved.HostTarget) || !sameHostIdentity(previous.Identity, resolved.Previous) {
				continue
			}
			if previous.Identity == nil || *previous.Identity != resolved.Identity {
				identity := resolved.Identity
				previous.Identity = &identity
				existing[name] = previous
				changed = true
			}
		}
		return changed
	})
}

func sameHostIdentity(left, right *hostIdentity) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

// A missing explicit -F file cannot be replayed. Other failures (permissions,
// parsing, timeouts) are not evidence that a discovery record has expired.
func missingHostConfig(target HostTarget) bool {
	path := ""
	for index := 0; index+1 < len(target.Arguments); index += 2 {
		if target.Arguments[index] == "-F" {
			path = target.Arguments[index+1] // OpenSSH uses the last -F.
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
	targets, err := config.hostTargets(context.Background(), path, "", nil, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	return targets, config.IgnoredHosts, nil
}

// hostTargets is the only host set. Discovered targets, explicit records, and
// any extra names are collapsed here, so later status and diagnostics see one
// entry per user and hostname.
func (config configuration) hostTargets(ctx context.Context, path, sshConfig string, extra map[string]HostTarget, resolve destinationResolver, observe func(string, HostTarget, hostIdentity, *hostIdentity)) (map[string]HostTarget, error) {
	records, err := loadDiscovered(path)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]HostTarget, len(records)+len(config.Hosts))
	cached := make(map[string]hostIdentity)
	for name, record := range records {
		if _, explicit := config.Hosts[name]; explicit {
			continue
		}
		if missingHostConfig(record.HostTarget) {
			if record.Identity == nil {
				// Legacy temporary connections cannot safely be identified or
				// replayed. Keep their disk records, but not phantom runtimes.
				continue
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
	var learn identityObserver
	if observe != nil {
		learn = func(name string, target HostTarget, identity hostIdentity) {
			if record, ok := records[name]; ok && sameHostConnection(record.HostTarget, target) {
				observe(name, target, identity, record.Identity)
			}
		}
	}
	return collapseSameHosts(ctx, sshConfig, targets, config.Hosts, resolve, cached, learn), nil
}
