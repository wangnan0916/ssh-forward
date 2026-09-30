package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type poolBackend struct {
	host   string
	starts atomic.Int32
	stops  atomic.Int32
	closed atomic.Bool
}

func (b *poolBackend) Observe(ctx context.Context, emit func([]core.Listener)) error {
	if b.host == "offline" {
		return errors.New("host offline")
	}
	emit([]core.Listener{{Port: 8080, WorkingDirectory: "/workspace/app"}})
	<-ctx.Done()
	return ctx.Err()
}
func (b *poolBackend) Forward(ctx context.Context, _ core.ForwardTarget, ready func()) error {
	if b.host == "offline" {
		return errors.New("host offline")
	}
	b.starts.Add(1)
	ready()
	<-ctx.Done()
	b.stops.Add(1)
	return ctx.Err()
}
func (b *poolBackend) Close(context.Context) error { b.closed.Store(true); return nil }

func testPool(t *testing.T, config configuration) (*managerPool, map[string]*poolBackend) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.jsonc")
	require.NoError(t, config.save(path))
	backends := make(map[string]*poolBackend)
	pool := &managerPool{
		configPath: path,
		sshConfig:  filepath.Join(filepath.Dir(path), "missing-ssh-config"),
		managers:   make(map[string]core.Manager),
		resolve: func(_ context.Context, _ string, target HostTarget) (string, string, bool) {
			return "test", target.Target + "\x00" + strings.Join(target.Arguments, "\x00"), true
		},
	}
	pool.createTarget = func(host string, _ HostTarget, intent core.ForwardingIntent) (core.Manager, error) {
		backend := &poolBackend{host: host}
		backends[host] = backend
		return core.NewManager(core.HostAlias(host), backend, intent)
	}
	t.Cleanup(func() {
		if err := pool.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	require.NoError(t, pool.reload(t.Context(), ""))
	return pool, backends
}

func awaitPoolStatus(t *testing.T, pool *managerPool, host string, match func(core.Status) bool) core.Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var status core.Status
	for time.Now().Before(deadline) {
		var err error
		status, err = pool.lookup(host).Status(t.Context())
		require.NoError(t, err)
		if match(status) {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("host %s did not settle: %+v", host, status)
	return status
}

func allPoolForwardsActive(count int) func(core.Status) bool {
	return func(s core.Status) bool {
		if s.Discovery.State != core.DiscoveryActive || len(s.Forwards) != count {
			return false
		}
		for _, f := range s.Forwards {
			if f.State != core.ForwardActive {
				return false
			}
		}
		return true
	}
}

func TestPoolStartsAllConfiguredHostsAndIsolatesChanges(t *testing.T) {
	ctx := t.Context()
	pool, backends := testPool(t, configuration{Rules: map[string]*scopeRules{
		"dev":       new(scopeRules{Forwards: []core.RememberedForward{{RemotePort: 3000}}}),
		"other":     new(scopeRules{Forwards: []core.RememberedForward{{RemotePort: 3000, LocalPort: 13000}}}),
		"offline":   new(scopeRules{Forwards: []core.RememberedForward{{RemotePort: 4000}}}),
		"published": new(scopeRules{Published: []core.PublishedForward{{LocalPort: 9222}}}),
		"automatic": new(scopeRules{Directories: []string{"/workspace/**"}}),
	}})
	for _, host := range []string{"dev", "other", "published", "automatic"} {
		awaitPoolStatus(t, pool, host, allPoolForwardsActive(1))
	}
	awaitPoolStatus(t, pool, "offline", func(s core.Status) bool { return s.Discovery.State == core.DiscoveryFailed })
	original := pool.lookup("dev")
	if _, err := EditRememberedForward(pool.configPath, "other", &core.RememberedForward{RemotePort: 3000}, false); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, pool.reload(ctx, "other"))
	awaitPoolStatus(t, pool, "other", allPoolForwardsActive(0))
	require.False(t, pool.lookup("dev") != original || backends["dev"].starts.Load() != 1 || backends["dev"].stops.Load() != 0, "changing another host disrupted dev")
	// New hosts are added when their rules appear.
	if _, err := EditRememberedForward(pool.configPath, "new", &core.RememberedForward{RemotePort: 9000}, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, pool.reload(ctx, "new"))
	awaitPoolStatus(t, pool, "new", allPoolForwardsActive(1))
	require.EqualValues(t, 0, backends["dev"].stops.Load(), "adding a host stopped dev")
	require.NoError(t, pool.Close(ctx))
	for host, backend := range backends {
		if !backend.closed.Load() {
			t.Errorf("backend %s was not closed", host)
		}
	}
	require.ErrorIs(t, pool.reload(ctx, "new"), core.ErrManagerClosed)
}

func TestPoolLoadsConfiguredHostsAndPreservesLiveStateOnInvalidConfig(t *testing.T) {
	pool, backends := testPool(t, configuration{Rules: map[string]*scopeRules{
		"a": new(scopeRules{Forwards: []core.RememberedForward{{RemotePort: 3000}}}),
		"b": new(scopeRules{Forwards: []core.RememberedForward{{RemotePort: 4000}}}),
	}})
	for _, host := range []string{"a", "b"} {
		awaitPoolStatus(t, pool, host, allPoolForwardsActive(1))
	}
	statuses, err := pool.AllStatuses(t.Context())
	require.Falsef(t, err != nil || len(statuses) != 2, "statuses: %+v, %v", statuses, err)
	require.Nil(t, pool.lookup("unknown"), "unknown host exists")
	require.EqualValues(t, 2, len(backends), "status created a host runtime")
	require.NoError(t, writeAtomic(pool.configPath, []byte(`{"schema_version":`)))
	require.Error(t, pool.reload(t.Context(), "c"), "invalid config accepted")
	for _, host := range []string{"a", "b"} {
		awaitPoolStatus(t, pool, host, allPoolForwardsActive(1))
		require.EqualValuesf(t, 0, backends[host].stops.Load(), "invalid config disrupted %s", host)
	}
}

func TestSameUserAndHostShareOneRuntime(t *testing.T) {
	pool, backends := testPool(t, configuration{Hosts: map[string]HostTarget{
		"ubuntu": {Target: "ubuntu"}, "shampoo@ubuntu": {Target: "shampoo@ubuntu"},
	}})
	require.NotNil(t, pool.lookup("ubuntu"))
	require.NotNil(t, pool.lookup("shampoo@ubuntu"))
	pool.resolve = func(_ context.Context, _ string, target HostTarget) (string, string, bool) {
		if target.Target == "ubuntu" || target.Target == "shampoo@ubuntu" {
			return "shampoo", "100.83.29.59", true
		}
		return "", "", false
	}
	require.NoError(t, pool.reload(t.Context(), ""))
	require.NotNil(t, pool.lookup("ubuntu"))
	require.Nil(t, pool.lookup("shampoo@ubuntu"))
	require.True(t, backends["shampoo@ubuntu"].closed.Load())
}
