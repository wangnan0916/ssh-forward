package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func writeIdentityConfig(t *testing.T, path, user, host string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("Host *\n  User "+user+"\n  HostName "+host+"\n"), 0o600))
}

func TestDiscoveredIdentityAfterConfigExpiryAndRestart(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	for _, tc := range []struct {
		name, user, host string
		count            int
	}{
		{"same host", "dev", "127.0.0.1", 1},
		{"different host", "dev", "127.0.0.2", 2},
		{"different user", "other", "127.0.0.1", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, backends := testPool(t, configuration{
				Hosts: map[string]HostTarget{"ubuntu": {Target: "ubuntu"}},
				Rules: map[string]*scopeRules{"": {Directories: []string{"/workspace/**"}}},
			})
			dir := t.TempDir()
			base, first, second := filepath.Join(dir, "base"), filepath.Join(dir, "first"), filepath.Join(dir, "second")
			writeIdentityConfig(t, base, "dev", "127.0.0.1")
			writeIdentityConfig(t, first, "dev", "127.0.0.1")
			writeIdentityConfig(t, second, tc.user, tc.host)
			pool.sshConfig, pool.resolve = base, resolveUserHost
			a := HostTarget{Target: "session", Arguments: []string{"-F", first}}
			b := HostTarget{Target: "session", Arguments: []string{"-F", second}}
			require.NoError(t, rememberDiscovered(t.Context(), pool.configPath, map[string]HostTarget{targetID(a): a, targetID(b): b}))
			require.NoError(t, pool.reload(t.Context(), ""))
			records, err := loadDiscovered(pool.configPath)
			require.NoError(t, err)
			require.Equal(t, &hostIdentity{User: tc.user, Hostname: tc.host}, records[targetID(b)].Identity)
			original := pool.lookup("ubuntu")
			awaitPoolStatus(t, pool, "ubuntu", allPoolForwardsActive(1))
			require.NoError(t, os.Remove(first))
			require.NoError(t, os.Remove(second))
			require.NoError(t, pool.reload(t.Context(), ""))
			require.Same(t, original, pool.lookup("ubuntu"))
			require.EqualValues(t, 0, backends["ubuntu"].stops.Load())
			require.NoError(t, pool.Close(t.Context()))
			restarted := &managerPool{configPath: pool.configPath, sshConfig: base, resolve: resolveUserHost,
				managers: make(map[string]core.Manager), createTarget: pool.createTarget}
			t.Cleanup(func() { require.NoError(t, restarted.Close(context.Background())) })
			require.NoError(t, restarted.reload(t.Context(), ""))
			statuses, err := restarted.AllStatuses(t.Context())
			require.NoError(t, err)
			require.Len(t, statuses, tc.count)
			awaitPoolStatus(t, restarted, "ubuntu", allPoolForwardsActive(1))
		})
	}
}

func TestMissingLegacyConfigsPreserveExplicitHostsAndReadOnlyListing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	stale := HostTarget{Target: "ubuntu", Arguments: []string{"-F", filepath.Join(t.TempDir(), "gone")}}
	name := targetID(stale)
	require.NoError(t, writeJSONC(discoveryPath(path), map[string]HostTarget{name: stale}))
	before, err := os.ReadFile(discoveryPath(path))
	require.NoError(t, err)
	config := configuration{Hosts: map[string]HostTarget{"ubuntu": {Target: "ubuntu"}}}
	resolve := func(_ context.Context, _ string, target HostTarget) (string, string, bool) {
		return "dev", "host", !missingHostConfig(target)
	}
	targets, _, err := config.hostTargets(t.Context(), path, "/dev/null", nil, resolve)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	config.Hosts[name] = stale
	targets, _, err = config.hostTargets(t.Context(), path, "/dev/null", nil, resolve)
	require.NoError(t, err)
	require.Equal(t, stale, targets[name])
	after, err := os.ReadFile(discoveryPath(path))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestSingleDiscoveredIdentityRefresh(t *testing.T) {
	pool, _ := testPool(t, configuration{})
	target := HostTarget{Target: "session"}
	require.NoError(t, rememberDiscovered(t.Context(), pool.configPath, map[string]HostTarget{"session": target}))
	for _, host := range []string{"first", "second"} {
		pool.resolve = func(context.Context, string, HostTarget) (string, string, bool) { return "dev", host, true }
		require.NoError(t, pool.reload(t.Context(), ""))
		records, err := loadDiscovered(pool.configPath)
		require.NoError(t, err)
		require.Equal(t, &hostIdentity{User: "dev", Hostname: host}, records["session"].Identity)
	}
}

func TestExpiredRouteDoesNotWinByName(t *testing.T) {
	stale := HostTarget{Target: "aaa"}
	live := HostTarget{Target: "zzz"}
	resolve := func(_ context.Context, _ string, target HostTarget) (string, string, bool) {
		return "dev", "host", target.Target == "zzz"
	}
	targets, _ := collapseSameHosts(t.Context(), "/dev/null", map[string]HostTarget{"aaa": stale, "zzz": live}, nil, resolve,
		map[string]hostIdentity{"aaa": {User: "dev", Hostname: "host"}})
	require.Equal(t, map[string]HostTarget{"zzz": live}, targets)
}

func TestIdentityMergePreservesConcurrentDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	target := HostTarget{Target: "a"}
	identity := &hostIdentity{User: "dev", Hostname: "host"}
	require.NoError(t, rememberDiscovered(t.Context(), path, map[string]HostTarget{"a": target}))
	results := make(chan error, 2)
	go func() {
		results <- mergeDiscovered(t.Context(), path, nil, map[string]discoveredHost{"a": {HostTarget: target, Identity: identity}})
	}()
	go func() { results <- rememberDiscovered(t.Context(), path, map[string]HostTarget{"b": {Target: "b"}}) }()
	for range 2 {
		require.NoError(t, <-results)
	}
	records, err := loadDiscovered(path)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, identity, records["a"].Identity)
}
