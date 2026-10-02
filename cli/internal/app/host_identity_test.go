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

func writeIdentitySSHConfig(t *testing.T, path, user, hostname, port string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("Host *\n  HostName "+hostname+"\n  User "+user+"\n  Port "+port+"\n"), 0o600))
}

func requireIdentitySSH(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
}

func TestDiscoveredIdentitySurvivesConfigExpiryAndManagerRestart(t *testing.T) {
	requireIdentitySSH(t)
	pool, backends := testPool(t, configuration{
		Hosts: map[string]HostTarget{"ubuntu": {Target: "ubuntu"}},
		Rules: map[string]*scopeRules{"": {Directories: []string{"/workspace/**"}}},
	})
	dir := t.TempDir()
	base, first, second := filepath.Join(dir, "base"), filepath.Join(dir, "session-a"), filepath.Join(dir, "session-b")
	for _, path := range []string{base, first} {
		writeIdentitySSHConfig(t, path, "dev", "127.0.0.1", "2222")
	}
	writeIdentitySSHConfig(t, second, "dev", "127.0.0.1", "2200")
	pool.sshConfig, pool.resolve = base, resolveUserHost
	a := HostTarget{Target: "session-a", Arguments: []string{"-F", first}}
	b := HostTarget{Target: "session-b", Arguments: []string{"-F", second}}
	nameA, nameB := targetID(a), targetID(b)
	require.NoError(t, rememberDiscovered(t.Context(), pool.configPath, map[string]HostTarget{nameA: a, nameB: b}))
	before, err := os.ReadFile(pool.configPath)
	require.NoError(t, err)
	require.NoError(t, pool.reload(t.Context(), ""))
	records, err := loadDiscovered(pool.configPath)
	require.NoError(t, err)
	identity := &hostIdentity{User: "dev", Hostname: "127.0.0.1"}
	for _, name := range []string{nameA, nameB} {
		require.Equal(t, identity, records[name].Identity)
		require.Equal(t, name, targetID(records[name].HostTarget), "identity metadata must not change connection IDs")
	}
	original := pool.lookup("ubuntu")
	awaitPoolStatus(t, pool, "ubuntu", allPoolForwardsActive(1))
	require.NoError(t, os.Remove(first))
	require.NoError(t, os.Remove(second))
	require.NoError(t, pool.reload(t.Context(), ""))
	statuses, err := pool.AllStatuses(t.Context())
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Equal(t, core.HostAlias("ubuntu"), statuses[0].Host)
	require.Equal(t, original, pool.lookup("ubuntu"))
	require.EqualValues(t, 1, backends["ubuntu"].starts.Load())
	require.EqualValues(t, 0, backends["ubuntu"].stops.Load(), "expiry of an alternative must not restart the usable connection")
	require.NoError(t, pool.Close(t.Context()))

	// Build a fresh pool from only persisted state. No in-memory resolver
	// result can explain why the expired variants still belong to ubuntu.
	restarted := &managerPool{
		configPath: pool.configPath, sshConfig: base, resolve: resolveUserHost,
		managers: make(map[string]core.Manager), createTarget: pool.createTarget,
	}
	t.Cleanup(func() { require.NoError(t, restarted.Close(context.Background())) })
	require.NoError(t, restarted.reload(t.Context(), ""))
	awaitPoolStatus(t, restarted, "ubuntu", allPoolForwardsActive(1))
	statuses, err = restarted.AllStatuses(t.Context())
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Nil(t, restarted.lookup(nameA))
	require.Nil(t, restarted.lookup(nameB))
	after, err := os.ReadFile(pool.configPath)
	require.NoError(t, err)
	require.Equal(t, before, after, "runtime identity learning must not edit user intent")
}

func TestSingleDiscoveredConnectionLearnsIdentityWithoutOriginalSSHSession(t *testing.T) {
	requireIdentitySSH(t)
	pool, _ := testPool(t, configuration{})
	configPath := filepath.Join(t.TempDir(), "ssh_config")
	writeIdentitySSHConfig(t, configPath, "dev", "HOST.EXAMPLE", "2222")
	target := HostTarget{Target: "session", Arguments: []string{"-F", configPath}}
	name := targetID(target)
	require.NoError(t, rememberDiscovered(t.Context(), pool.configPath, map[string]HostTarget{name: target}))
	pool.resolve = resolveUserHost
	require.NoError(t, pool.reload(t.Context(), ""))
	records, err := loadDiscovered(pool.configPath)
	require.NoError(t, err)
	require.Equal(t, &hostIdentity{User: "dev", Hostname: "host.example"}, records[name].Identity)
	// No other route is known: retain the identity as one unavailable host,
	// rather than inventing an unconfigured plain "session" connection.
	require.NoError(t, os.Remove(configPath))
	require.NoError(t, pool.reload(t.Context(), ""))
	require.NotNil(t, pool.lookup(name))
	require.Nil(t, pool.lookup("session"))
}

func TestLegacyMissingDiscoveredConfigsDoNotCreatePhantomHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	missing := filepath.Join(t.TempDir(), "gone")
	stale := HostTarget{Target: "ubuntu", Arguments: []string{"-F", missing}}
	name := targetID(stale)
	// Write the old flat JSON format with no identity metadata.
	require.NoError(t, writeJSONC(discoveryPath(path), map[string]HostTarget{name: stale}))
	before, err := os.ReadFile(discoveryPath(path))
	require.NoError(t, err)
	config := configuration{Hosts: map[string]HostTarget{"ubuntu": {Target: "ubuntu"}}}
	resolve := func(_ context.Context, _ string, target HostTarget) (string, string, bool) {
		return "dev", "host", !missingHostConfig(target)
	}
	targets, err := config.hostTargets(t.Context(), path, "/dev/null", nil, resolve, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]HostTarget{"ubuntu": {Target: "ubuntu"}}, targets)

	// Explicit intent and explicitly requested hosts are not silently hidden.
	config.Hosts[name] = stale
	targets, err = config.hostTargets(t.Context(), path, "/dev/null", nil, resolve, nil)
	require.NoError(t, err)
	require.Equal(t, stale, targets[name])
	delete(config.Hosts, name)
	targets, err = config.hostTargets(t.Context(), path, "/dev/null", map[string]HostTarget{"requested": {Target: "requested", Arguments: []string{"-F", missing}}}, resolve, nil)
	require.NoError(t, err)
	require.Contains(t, targets, "requested")
	after, err := os.ReadFile(discoveryPath(path))
	require.NoError(t, err)
	require.Equal(t, before, after, "read-only host listing must not prune the registry")
}

func TestCachedIdentityNeverMergesDifferentUsersOrDestinations(t *testing.T) {
	requireIdentitySSH(t)
	for _, tc := range []struct{ name, user, hostname string }{
		{"different hostname", "dev", "127.0.0.2"},
		{"different user", "other", "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, _ := testPool(t, configuration{Hosts: map[string]HostTarget{"ubuntu": {Target: "ubuntu"}}})
			base, alternative := filepath.Join(t.TempDir(), "base"), filepath.Join(t.TempDir(), "alternative")
			writeIdentitySSHConfig(t, base, "dev", "127.0.0.1", "2222")
			writeIdentitySSHConfig(t, alternative, tc.user, tc.hostname, "2222")
			pool.sshConfig, pool.resolve = base, resolveUserHost
			// The same literal alias can identify a different destination under -F.
			target := HostTarget{Target: "ubuntu", Arguments: []string{"-F", alternative}}
			name := targetID(target)
			require.NoError(t, rememberDiscovered(t.Context(), pool.configPath, map[string]HostTarget{name: target}))
			require.NoError(t, pool.reload(t.Context(), ""))
			require.NoError(t, os.Remove(alternative))
			require.NoError(t, pool.reload(t.Context(), ""))
			statuses, err := pool.AllStatuses(t.Context())
			require.NoError(t, err)
			require.Len(t, statuses, 2)
			require.NotNil(t, pool.lookup(name))
			require.NotNil(t, pool.lookup("ubuntu"))
		})
	}
}

func TestFreshConnectionWinsOverExpiredCachedAlternative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	missing := filepath.Join(t.TempDir(), "gone")
	identity := &hostIdentity{User: "dev", Hostname: "host"}
	stale := HostTarget{Target: "aaa", Arguments: []string{"-F", missing}}
	live := HostTarget{Target: "zzz"}
	require.NoError(t, writeJSONC(discoveryPath(path), map[string]discoveredHost{
		"aaa": {HostTarget: stale, Identity: identity},
		"zzz": {HostTarget: live},
	}))
	resolve := func(_ context.Context, _ string, target HostTarget) (string, string, bool) {
		return "dev", "host", !missingHostConfig(target)
	}
	targets, err := (configuration{}).hostTargets(t.Context(), path, "/dev/null", nil, resolve, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]HostTarget{"zzz": live}, targets, "lexical preference must not select an expired route")
}

func TestReadOnlyListingDoesNotPersistIdentity(t *testing.T) {
	requireIdentitySSH(t)
	path := filepath.Join(t.TempDir(), "config.jsonc")
	first := HostTarget{Target: "dev", Arguments: []string{"-F", "/dev/null"}}
	second := HostTarget{Target: "dev", Arguments: []string{"-F", "/dev/null", "-p", "2222"}}
	require.NoError(t, writeJSONC(discoveryPath(path), map[string]HostTarget{targetID(first): first, targetID(second): second}))
	before, err := os.ReadFile(discoveryPath(path))
	require.NoError(t, err)
	targets, err := (configuration{}).hostTargets(t.Context(), path, "/dev/null", nil, resolveUserHost, nil)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	after, err := os.ReadFile(discoveryPath(path))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestCachedIdentityDoesNotHideOtherResolutionFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	require.NoError(t, writeJSONC(discoveryPath(path), map[string]discoveredHost{
		"old": {HostTarget: HostTarget{Target: "old", Arguments: []string{"-F", "/dev/null"}}, Identity: &hostIdentity{User: "dev", Hostname: "host"}},
	}))
	resolve := func(_ context.Context, _ string, target HostTarget) (string, string, bool) {
		return "dev", "host", target.Target == "ubuntu"
	}
	config := configuration{Hosts: map[string]HostTarget{"ubuntu": {Target: "ubuntu"}}}
	targets, err := config.hostTargets(t.Context(), path, "/dev/null", nil, resolve, nil)
	require.NoError(t, err)
	require.Len(t, targets, 2, "an existing but invalid config is not evidence that its cached identity is still correct")
}

func TestFreshResolutionUpdatesCachedIdentity(t *testing.T) {
	requireIdentitySSH(t)
	pool, _ := testPool(t, configuration{})
	file := filepath.Join(t.TempDir(), "ssh_config")
	target := HostTarget{Target: "session", Arguments: []string{"-F", file}}
	name := targetID(target)
	writeIdentitySSHConfig(t, file, "dev", "127.0.0.1", "2222")
	require.NoError(t, rememberDiscovered(t.Context(), pool.configPath, map[string]HostTarget{name: target}))
	pool.resolve = resolveUserHost
	require.NoError(t, pool.reload(t.Context(), ""))
	writeIdentitySSHConfig(t, file, "other", "127.0.0.2", "2200")
	require.NoError(t, pool.reload(t.Context(), ""))
	records, err := loadDiscovered(pool.configPath)
	require.NoError(t, err)
	require.Equal(t, &hostIdentity{User: "other", Hostname: "127.0.0.2"}, records[name].Identity)
	require.NoError(t, os.Remove(file))
	require.NoError(t, pool.reload(t.Context(), ""))
	records, err = loadDiscovered(pool.configPath)
	require.NoError(t, err)
	require.Equal(t, &hostIdentity{User: "other", Hostname: "127.0.0.2"}, records[name].Identity, "a failed resolution must not erase or revert the known identity")
}

func TestIdentityRefreshPreservesConcurrentDiscoveriesAndRejectsStaleResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	target := HostTarget{Target: "a"}
	identity := &hostIdentity{User: "dev", Hostname: "host"}
	learned := map[string]discoveredIdentityUpdate{"a": {HostTarget: target, Identity: *identity}}
	require.NoError(t, rememberDiscovered(t.Context(), path, map[string]HostTarget{"a": target}))
	results := make(chan error, 3)
	go func() { results <- rememberHostIdentities(t.Context(), path, learned) }()
	go func() { results <- rememberDiscovered(t.Context(), path, map[string]HostTarget{"b": {Target: "b"}}) }()
	go func() { results <- rememberDiscovered(t.Context(), path, map[string]HostTarget{"a": target}) }()
	for range 3 {
		require.NoError(t, <-results)
	}
	records, err := loadDiscovered(path)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, identity, records["a"].Identity)
	require.NoError(t, editDiscovered(t.Context(), path, func(records map[string]discoveredHost) bool {
		records["a"] = discoveredHost{HostTarget: HostTarget{Target: "different"}}
		return true
	}))
	require.NoError(t, rememberHostIdentities(t.Context(), path, learned))
	records, err = loadDiscovered(path)
	require.NoError(t, err)
	require.Nil(t, records["a"].Identity, "never attach an old identity to a changed connection")
	require.NoError(t, editDiscovered(t.Context(), path, func(records map[string]discoveredHost) bool {
		delete(records, "a")
		return true
	}))
	require.NoError(t, rememberHostIdentities(t.Context(), path, learned))
	records, err = loadDiscovered(path)
	require.NoError(t, err)
	require.NotContains(t, records, "a", "never resurrect an intentionally removed record")
}

func TestDelayedIdentityObservationCannotOverwriteNewerIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	target := HostTarget{Target: "session", Arguments: []string{"-F", filepath.Join(t.TempDir(), "config")}}
	name := targetID(target)
	require.NoError(t, rememberDiscovered(t.Context(), path, map[string]HostTarget{name: target}))
	older := discoveredIdentityUpdate{HostTarget: target, Identity: hostIdentity{User: "old", Hostname: "old-host"}}
	newer := discoveredIdentityUpdate{HostTarget: target, Identity: hostIdentity{User: "new", Hostname: "new-host"}}
	// Both readers started from an identity-less snapshot with identical
	// arguments. The newer identity commits before the older reader finishes.
	require.NoError(t, rememberHostIdentities(t.Context(), path, map[string]discoveredIdentityUpdate{name: newer}))
	require.NoError(t, rememberHostIdentities(t.Context(), path, map[string]discoveredIdentityUpdate{name: older}))
	records, err := loadDiscovered(path)
	require.NoError(t, err)
	require.Equal(t, &newer.Identity, records[name].Identity)

	// A legitimate refresh from the current snapshot can still update it.
	current := records[name].Identity
	next := discoveredIdentityUpdate{HostTarget: target, Identity: hostIdentity{User: "next", Hostname: "next-host"}, Previous: current}
	require.NoError(t, rememberHostIdentities(t.Context(), path, map[string]discoveredIdentityUpdate{name: next}))
	require.NoError(t, rememberHostIdentities(t.Context(), path, map[string]discoveredIdentityUpdate{name: newer}))
	records, err = loadDiscovered(path)
	require.NoError(t, err)
	require.Equal(t, &next.Identity, records[name].Identity)
}

func TestMissingConfigUsesLastOptionAndDoesNotHideOtherErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	require.False(t, missingHostConfig(HostTarget{Target: "dev", Arguments: []string{"-F", missing, "-F", "/dev/null"}}))
	require.True(t, missingHostConfig(HostTarget{Target: "dev", Arguments: []string{"-F", "/dev/null", "-F", missing}}))
	// A directory is an invalid configuration, but not an expired file.
	require.False(t, missingHostConfig(HostTarget{Target: "dev", Arguments: []string{"-F", t.TempDir()}}))
}
