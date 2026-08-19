package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fakeManager struct {
	snap  core.Snapshot
	watch func(context.Context) (core.SnapshotStream, error)
}

func (m *fakeManager) Snapshot(context.Context) (core.Snapshot, error) {
	return m.snap, nil
}

func (m *fakeManager) Watch(ctx context.Context) (core.SnapshotStream, error) {
	if m.watch == nil {
		return nil, errors.New("watch unused")
	}
	return m.watch(ctx)
}

func (*fakeManager) Close(context.Context) error { return nil }

func testSnap() core.Snapshot {
	return core.Snapshot{
		Revision: 5,
		Host: &core.HostSnapshot{
			Alias:      core.HostAlias("development"),
			Connection: core.ConnectionConnected,
			Discovery:  core.DiscoverySnapshot{State: core.DiscoveryHealthy, BaselineEstablished: true, ScannerVersion: 1},
			Forwards: []core.ForwardSnapshot{{
				ID: "managed:ipv4:loopback:8080", RemotePort: 8080, RemoteFamily: core.FamilyIPv4, AllocatedLocalPort: 8080,
			}},
		},
	}
}

func startServer(t *testing.T, token string, ports PortStore) string {
	t.Helper()
	return startServerManager(t, token, ports, &fakeManager{snap: testSnap()})
}

func startServerManager(t *testing.T, token string, ports PortStore, manager core.Manager) string {
	t.Helper()
	listener, err := ListenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	tcp := listener.Addr().(*net.TCPAddr)
	if !tcp.IP.IsLoopback() || tcp.IP.To4() == nil {
		t.Fatalf("listen addr = %v, want 127.0.0.1", listener.Addr())
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := &Server{Manager: manager, Ports: ports, Token: token}
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, listener, server.Handler()) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errc:
		case <-time.After(2 * time.Second):
			t.Error("Serve did not stop")
		}
	})
	return PageURL(listener.Addr(), token)
}

func origin(pageURL string) string {
	return strings.TrimSuffix(strings.Split(pageURL, "?")[0], "/")
}

func TestListenLoopbackIsIPv4Loopback(t *testing.T) {
	listener, err := ListenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	addr := listener.Addr().(*net.TCPAddr)
	if addr.IP.String() != "127.0.0.1" {
		t.Fatalf("addr = %s, want 127.0.0.1", addr.IP)
	}
}

func TestSnapshotRequiresToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	ports := app.NewFilePolicyReader(path)
	base := startServer(t, "secret-token", ports)
	api := origin(base)

	missing, err := http.Get(api + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want 401", missing.StatusCode)
	}

	wrong, err := http.Get(api + "/api/snapshot?token=wrong")
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", wrong.StatusCode)
	}
}

func TestSnapshotJSONMatchesCodec(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	base := startServer(t, "secret-token", app.NewFilePolicyReader(path))
	res, err := http.Get(origin(base) + "/api/snapshot?token=secret-token")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"revision":5`)) || !bytes.Contains(body, []byte(`"alias":"development"`)) {
		t.Fatalf("snapshot body = %s", body)
	}
}

func TestRememberAndForgetPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	ports := app.NewFilePolicyReader(path)
	base := startServer(t, "secret-token", ports)
	api := origin(base)

	post := func(pathSuffix, raw string) *http.Response {
		t.Helper()
		res, err := http.Post(api+pathSuffix+"?token=secret-token", "application/json", strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	res := post("/api/remember", `{"port":5173}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("remember status = %d", res.StatusCode)
	}
	policies, err := ports.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := core.SimpleAutoForwardPorts(policies); len(got) != 1 || got[0] != 5173 {
		t.Fatalf("remembered ports = %v, want [5173]", got)
	}

	listed, err := http.Get(api + "/api/remembered?token=secret-token")
	if err != nil {
		t.Fatal(err)
	}
	defer listed.Body.Close()
	var remembered rememberedBody
	if err := json.NewDecoder(listed.Body).Decode(&remembered); err != nil {
		t.Fatal(err)
	}
	if len(remembered.RememberedPorts) != 1 || remembered.RememberedPorts[0] != 5173 {
		t.Fatalf("remembered API = %+v", remembered)
	}

	missing := post("/api/forget", `{"port":9}`)
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("forget missing status = %d, want 404", missing.StatusCode)
	}

	gone := post("/api/forget", `{"port":5173}`)
	defer gone.Body.Close()
	if gone.StatusCode != http.StatusOK {
		t.Fatalf("forget status = %d", gone.StatusCode)
	}
	policies, err = ports.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := core.SimpleAutoForwardPorts(policies); len(got) != 0 {
		t.Fatalf("after forget ports = %v, want empty", got)
	}
}

func TestPageIsHTML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	base := startServer(t, "secret-token", app.NewFilePolicyReader(path))
	res, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !bytes.Contains(body, []byte("Remember port")) {
		t.Fatalf("page missing remember form: %s", body)
	}
}

type oneSnapshot struct {
	snap core.Snapshot
	sent bool
}

func (s *oneSnapshot) Next(ctx context.Context) (core.Snapshot, error) {
	if !s.sent {
		s.sent = true
		return s.snap, nil
	}
	<-ctx.Done()
	return core.Snapshot{}, ctx.Err()
}

func (*oneSnapshot) Close() error { return nil }

func TestWatchStreamsSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	stream := &oneSnapshot{snap: testSnap()}
	manager := &fakeManager{
		snap:  testSnap(),
		watch: func(context.Context) (core.SnapshotStream, error) { return stream, nil },
	}
	base := startServerManager(t, "secret-token", app.NewFilePolicyReader(path), manager)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin(base)+"/api/watch?token=secret-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	buf := make([]byte, 4096)
	n, err := res.Body.Read(buf)
	if n == 0 && err != nil {
		t.Fatal(err)
	}
	body := string(buf[:n])
	if !strings.Contains(body, "data:") || !strings.Contains(body, `"revision":5`) {
		t.Fatalf("SSE body = %q", body)
	}
}

func TestPageURLIncludesLoopbackAndToken(t *testing.T) {
	listener, err := ListenLoopback()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	got := PageURL(listener.Addr(), "abc")
	wantSuffix := fmt.Sprintf(":%d/?token=abc", listener.Addr().(*net.TCPAddr).Port)
	if !strings.HasPrefix(got, "http://127.0.0.1:") || !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("PageURL = %q", got)
	}
}
