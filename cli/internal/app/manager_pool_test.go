package app

import (
	"context"
	"errors"
	"path/filepath"
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

func testPool(t *testing.T, config configFile) (*managerPool, map[string]*poolBackend) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.jsonc")
	require.NoError(t, saveConfig(path, config))
	backends := make(map[string]*poolBackend)
	pool := &managerPool{configPath: path, managers: make(map[string]core.Manager)}
	pool.createTarget = func(host string, _ HostTarget, intent core.ForwardingIntent) (core.Manager, error) {
		backend := &poolBackend{host: host}
		backends[host] = backend
		return core.NewManager(core.HostAlias(host), backend, intent), nil
	}
	t.Cleanup(func() {
		if err := pool.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	require.NoError(t, pool.reload(context.Background(), ""))
	return pool, backends
}

func awaitPoolStatus(t *testing.T, pool *managerPool, host string, match func(core.Status) bool) core.Status {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var status core.Status
	for time.Now().Before(deadline) {
		var err error
		status, err = pool.lookup(host).Status(context.Background())
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
	ctx := context.Background()
	pool, backends := testPool(t, configFile{
		DefaultHost: "dev",
		RememberedForwards: map[string][]core.RememberedForward{
			"dev": {{RemotePort: 3000}}, "other": {{RemotePort: 3000, LocalPort: 13000}},
			"offline": {{RemotePort: 4000}},
		},
		PublishedForwards:     map[string][]core.PublishedForward{"published": {{LocalPort: 9222}}},
		WorkingDirectoryRules: map[string][]string{"automatic": {"/workspace/**"}},
	})
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
	// New hosts are added on demand, without requiring a default-host change.
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

func TestPoolLoadsWithoutDefaultAndPreservesLiveStateOnInvalidConfig(t *testing.T) {
	pool, backends := testPool(t, configFile{RememberedForwards: map[string][]core.RememberedForward{"a": {{RemotePort: 3000}}, "b": {{RemotePort: 4000}}}})
	for _, host := range []string{"a", "b"} {
		awaitPoolStatus(t, pool, host, allPoolForwardsActive(1))
	}
	statuses, err := pool.AllStatuses(context.Background())
	require.Falsef(t, err != nil || len(statuses) != 2, "statuses: %+v, %v", statuses, err)
	require.Nil(t, pool.lookup("unknown"), "unknown host exists")
	require.EqualValues(t, 2, len(backends), "status created a host runtime")
	require.NoError(t, writeAtomic(pool.configPath, []byte(`{"schema_version":`)))
	require.Error(t, pool.reload(context.Background(), "c"), "invalid config accepted")
	for _, host := range []string{"a", "b"} {
		awaitPoolStatus(t, pool, host, allPoolForwardsActive(1))
		require.EqualValuesf(t, 0, backends[host].stops.Load(), "invalid config disrupted %s", host)
	}
}

func TestPoolReservesPublishedServicePortsAcrossHosts(t *testing.T) {
	pool, _ := testPool(t, configFile{
		RememberedForwards: map[string][]core.RememberedForward{"import": {{RemotePort: 9222, AllowFallback: true}}, "strict": {{RemotePort: 9222, LocalPort: 9222}}},
		PublishedForwards:  map[string][]core.PublishedForward{"publish": {{LocalPort: 9222}}},
	})
	status := awaitPoolStatus(t, pool, "import", allPoolForwardsActive(1))
	require.EqualValuesf(t, 9223, status.Forwards[0].LocalPort, "import occupied published service port: %+v", status.Forwards)
	awaitPoolStatus(t, pool, "publish", allPoolForwardsActive(1))
	awaitPoolStatus(t, pool, "strict", func(s core.Status) bool {
		return len(s.Forwards) == 1 && s.Forwards[0].State == core.ForwardFailed && s.Forwards[0].Diagnostic == "local_port_reserved"
	})
	// Adding a publish must move an already active import on another host too.
	if _, err := EditPublishedForward(pool.configPath, "publish", &core.PublishedForward{LocalPort: 9223}, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, pool.reload(context.Background(), "publish"))
	awaitPoolStatus(t, pool, "import", func(s core.Status) bool {
		return allPoolForwardsActive(1)(s) && s.Forwards[0].LocalPort == 9224
	})
}
