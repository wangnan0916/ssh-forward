package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// fakeStream replays a fixed snapshot sequence and then parks until the
// context ends, announcing each successful Next so tests can cancel at a
// deterministic point.
type fakeStream struct {
	pending  []core.Snapshot
	index    int
	notify   chan struct{}
	failWith error
}

func (s *fakeStream) Next(ctx context.Context) (core.Snapshot, error) {
	if s.index < len(s.pending) {
		snapshot := s.pending[s.index]
		s.index++
		s.notify <- struct{}{}
		return snapshot, nil
	}
	if s.failWith != nil {
		return core.Snapshot{}, s.failWith
	}
	select {
	case <-ctx.Done():
		return core.Snapshot{}, ctx.Err()
	}
}

func (s *fakeStream) Close() error { return nil }

func watchSnapshots() []core.Snapshot {
	host := func(connection core.ConnectionState, revision core.Revision) core.Snapshot {
		return core.Snapshot{
			Revision: revision,
			Host: &core.HostSnapshot{
				Alias:      core.HostAlias("development"),
				Connection: connection,
				Discovery:  core.DiscoverySnapshot{State: core.DiscoveryHealthy, BaselineEstablished: true, ScannerVersion: 1},
			},
		}
	}
	return []core.Snapshot{
		host(core.ConnectionConnecting, 1),
		host(core.ConnectionConnected, 2),
	}
}

func TestWatchEmitsHumanBlocksPerGeneration(t *testing.T) {
	stream := &fakeStream{pending: watchSnapshots(), notify: make(chan struct{}, 4)}
	manager := &fakeManager{watch: func(context.Context) (core.SnapshotStream, error) { return stream, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stream.notify
		<-stream.notify
		cancel()
	}()
	var stdout strings.Builder
	app := &App{Manager: manager, Host: core.HostAlias("development"), Options: app.Options{Stdout: &stdout}}
	if err := app.Run(ctx, []string{"watch"}); err != nil {
		t.Fatalf("watch: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "— connecting") || !strings.Contains(output, "— connected") {
		t.Fatalf("watch output missing a generation:\n%s", output)
	}
	if !strings.Contains(output, "\n\n") {
		t.Fatalf("watch human blocks are not separated:\n%q", output)
	}
}

func TestStatusWatchStreamsLikeWatch(t *testing.T) {
	stream := &fakeStream{pending: watchSnapshots(), notify: make(chan struct{}, 4)}
	manager := &fakeManager{watch: func(context.Context) (core.SnapshotStream, error) { return stream, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stream.notify
		<-stream.notify
		cancel()
	}()
	var stdout strings.Builder
	app := &App{Manager: manager, Host: core.HostAlias("development"), Options: app.Options{Stdout: &stdout}}
	if err := app.Run(ctx, []string{"status", "--watch"}); err != nil {
		t.Fatalf("status --watch: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "— connecting") || !strings.Contains(output, "— connected") {
		t.Fatalf("status --watch output missing a generation:\n%s", output)
	}
}

func TestWatchEmitsOneJSONLinePerGeneration(t *testing.T) {
	stream := &fakeStream{pending: watchSnapshots(), notify: make(chan struct{}, 4)}
	manager := &fakeManager{watch: func(context.Context) (core.SnapshotStream, error) { return stream, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-stream.notify
		<-stream.notify
		cancel()
	}()
	var stdout strings.Builder
	app := &App{Manager: manager, Host: core.HostAlias("development"), Options: app.Options{Stdout: &stdout}}
	if err := app.Run(ctx, []string{"watch", "--json"}); err != nil {
		t.Fatalf("watch --json: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("watch --json produced %d lines, want 2:\n%s", len(lines), stdout.String())
	}
	if !strings.Contains(lines[0], `"revision":1`) || !strings.Contains(lines[1], `"revision":2`) {
		t.Fatalf("watch --json lines out of order:\n%s", stdout.String())
	}
}

func TestWatchPropagatesARealStreamError(t *testing.T) {
	manager := &fakeManager{watch: func(context.Context) (core.SnapshotStream, error) {
		return &fakeStream{pending: nil, notify: make(chan struct{}), failWith: errors.New("stream died")}, nil
	}}
	var stdout strings.Builder
	app := &App{Manager: manager, Host: core.HostAlias("development"), Options: app.Options{Stdout: &stdout}}
	if err := app.Run(context.Background(), []string{"watch"}); err == nil || !strings.Contains(err.Error(), "stream died") {
		t.Fatalf("watch err = %v, want the stream error", err)
	}
}

func TestWatchRejectsPositionalArguments(t *testing.T) {
	manager := &fakeManager{}
	app := &App{Manager: manager, Host: core.HostAlias("development")}
	if err := app.Run(context.Background(), []string{"watch", "extra"}); err == nil {
		t.Fatal("watch with a positional argument succeeded")
	}
}
