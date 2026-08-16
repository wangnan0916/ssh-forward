package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ssh-forward/cli/internal/core"
)

// fakeManager is a scriptable core.Manager for CLI tests: commands do not
// touch the network, so the whole surface runs on injected state.
type fakeManager struct {
	snapshot core.Snapshot
	execute  func(context.Context, core.Command) (core.Outcome, error)
}

func (m *fakeManager) Execute(ctx context.Context, command core.Command) (core.Outcome, error) {
	if m.execute == nil {
		return core.Outcome{}, errors.New("unexpected Execute call")
	}
	return m.execute(ctx, command)
}

func (m *fakeManager) Snapshot(context.Context) (core.Snapshot, error) {
	return m.snapshot, nil
}

func (*fakeManager) Watch(context.Context) (core.SnapshotStream, error) {
	return nil, errors.New("unexpected Watch call")
}

func (*fakeManager) Close(context.Context) error { return nil }

func runApp(t *testing.T, manager core.Manager, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := &App{
		Manager: manager,
		Host:    core.HostAlias("development"),
		Stdout:  &stdout,
		Stderr:  &stderr,
	}
	err := app.Run(context.Background(), args)
	return stdout.String(), err
}

func snapshotWithHost() core.Snapshot {
	return core.Snapshot{
		Revision: 5,
		Host: &core.HostSnapshot{
			Alias:      core.HostAlias("development"),
			Connection: core.ConnectionConnected,
			Discovery: core.DiscoverySnapshot{
				State:               core.DiscoveryHealthy,
				BaselineEstablished: true,
				ScannerVersion:      1,
			},
			ListenerObservations: []core.ListenerObservation{
				{Family: core.FamilyIPv4, BindScope: core.BindLoopback, RemotePort: 8080},
				{Family: core.FamilyIPv6, BindScope: core.BindLoopback, RemotePort: 8080},
			},
			ListenerLifetimes: []core.ListenerLifetimeSnapshot{
				{Family: core.FamilyIPv4, BindScope: core.BindLoopback, RemotePort: 8080, Status: core.LifetimeContinuous, PostBaseline: true},
				{Family: core.FamilyIPv6, BindScope: core.BindLoopback, RemotePort: 8080, Status: core.LifetimeNew, PostBaseline: true},
			},
			AskListeners: []core.ListenerAskSnapshot{
				{Family: core.FamilyIPv6, BindScope: core.BindLoopback, RemotePort: 8080},
			},
			Forwards: []core.ForwardSnapshot{
				{
					ID:                 core.ForwardID("manual:op-1"),
					Kind:               core.ForwardManual,
					RemotePort:         8080,
					RemoteFamily:       core.FamilyIPv4,
					AllocatedLocalPort: 8081,
					LocalFamilies:      []core.AddressFamily{core.FamilyIPv4, core.FamilyIPv6},
				},
			},
		},
	}
}

func TestStatusHuman(t *testing.T) {
	output, err := runApp(t, &fakeManager{snapshot: snapshotWithHost()}, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	for _, want := range []string{
		"Host: development — connected",
		"Discovery: healthy (baseline true, scanner v1)",
		"8080/ipv4 loopback — continuous",
		"8080/ipv6 loopback — new — Ask",
		"manual:op-1 (manual) → ipv4:8080 (local 8081)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
}

func TestStatusJSON(t *testing.T) {
	output, err := runApp(t, &fakeManager{snapshot: snapshotWithHost()}, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	// The --json shape is the wire shape: ask_listeners and the listener
	// lifetime post_baseline flag ride along.
	for _, want := range []string{
		`"revision":5`,
		`"alias":"development"`,
		`"ask_listeners":[{"family":"ipv6","bind_scope":"loopback","remote_port":8080}]`,
		`"post_baseline":true`,
		`"kind":"manual"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status --json missing %q:\n%s", want, output)
		}
	}
}

func TestStatusNoHost(t *testing.T) {
	_, err := runApp(t, &fakeManager{snapshot: core.Snapshot{Revision: 0}}, "status")
	if err == nil || !strings.Contains(err.Error(), "no Development Host") {
		t.Fatalf("status without host err = %v, want no-host error", err)
	}
}

func TestForwardAdd(t *testing.T) {
	wantCommand := core.AddManualForward{
		CommandID:  core.CommandID("op-1"),
		Host:       core.HostAlias("development"),
		RemotePort: 8080,
		Family:     core.FamilyAuto,
	}
	manager := &fakeManager{
		execute: func(_ context.Context, command core.Command) (core.Outcome, error) {
			if command != wantCommand {
				t.Fatalf("command = %#v, want %#v", command, wantCommand)
			}
			return core.Outcome{
				Kind:     core.OutcomeForwardAdded,
				Revision: 6,
				Forward: core.ForwardSnapshot{
					ID:                 core.ForwardID("manual:op-1"),
					Kind:               core.ForwardManual,
					RemotePort:         8080,
					RemoteFamily:       core.FamilyIPv4,
					AllocatedLocalPort: 8080,
				},
			}, nil
		},
	}
	output, err := runApp(t, manager, "forward", "add", "--remote-port", "8080", "--operation-id", "op-1")
	if err != nil {
		t.Fatalf("forward add: %v", err)
	}
	if !strings.Contains(output, "forward_added") || !strings.Contains(output, "manual:op-1") {
		t.Fatalf("forward add output = %q", output)
	}
}

func TestForwardAddJSON(t *testing.T) {
	manager := &fakeManager{
		execute: func(context.Context, core.Command) (core.Outcome, error) {
			return core.Outcome{Kind: core.OutcomeForwardAdded, Revision: 6}, nil
		},
	}
	output, err := runApp(t, manager, "forward", "add", "--remote-port", "8080", "--json")
	if err != nil {
		t.Fatalf("forward add --json: %v", err)
	}
	// The --json shape is the wire shape (jsonrpc.MarshalOutcome): the
	// forward object carries local_families exactly like the IPC outcome.
	for _, want := range []string{
		`"kind":"forward_added"`,
		`"revision":6`,
		`"forward":{`,
		`"local_families"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("forward add --json missing %q: %s", want, output)
		}
	}
}

func TestForwardAddRequiresPort(t *testing.T) {
	if _, err := runApp(t, &fakeManager{}, "forward", "add"); err == nil {
		t.Fatal("forward add without --remote-port succeeded")
	}
}

func TestForwardRemove(t *testing.T) {
	manager := &fakeManager{
		execute: func(_ context.Context, command core.Command) (core.Outcome, error) {
			remove, ok := command.(core.RemoveForward)
			if !ok || remove.ForwardID != core.ForwardID("manual:op-1") {
				t.Fatalf("command = %#v, want remove of manual:op-1", command)
			}
			return core.Outcome{Kind: core.OutcomeForwardRemoved, Revision: 7}, nil
		},
	}
	output, err := runApp(t, manager, "forward", "remove", "--forward-id", "manual:op-1")
	if err != nil {
		t.Fatalf("forward remove: %v", err)
	}
	if !strings.Contains(output, "forward_removed") {
		t.Fatalf("forward remove output = %q", output)
	}
}

func TestListenerApprove(t *testing.T) {
	manager := &fakeManager{
		execute: func(_ context.Context, command core.Command) (core.Outcome, error) {
			approve, ok := command.(core.ApproveListener)
			if !ok || approve.RemotePort != 8080 || approve.Host != core.HostAlias("development") {
				t.Fatalf("command = %#v, want approve of port 8080", command)
			}
			return core.Outcome{Kind: core.OutcomeApprovalRecorded, Revision: 8}, nil
		},
	}
	output, err := runApp(t, manager, "listener", "approve", "--remote-port", "8080")
	if err != nil {
		t.Fatalf("listener approve: %v", err)
	}
	if !strings.Contains(output, "approval_recorded") {
		t.Fatalf("listener approve output = %q", output)
	}
}

func TestListenerSuppress(t *testing.T) {
	manager := &fakeManager{
		execute: func(_ context.Context, command core.Command) (core.Outcome, error) {
			suppress, ok := command.(core.SuppressListener)
			if !ok || suppress.RemotePort != 8080 {
				t.Fatalf("command = %#v, want suppress of port 8080", command)
			}
			return core.Outcome{Kind: core.OutcomeSuppressionRecorded, Revision: 9}, nil
		},
	}
	output, err := runApp(t, manager, "listener", "suppress", "--remote-port", "8080")
	if err != nil {
		t.Fatalf("listener suppress: %v", err)
	}
	if !strings.Contains(output, "suppression_recorded") {
		t.Fatalf("listener suppress output = %q", output)
	}
}

func TestUnknownCommand(t *testing.T) {
	if _, err := runApp(t, &fakeManager{}, "frobnicate"); err == nil {
		t.Fatal("unknown command succeeded")
	}
}

func writePolicies(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPolicyList(t *testing.T) {
	path := writePolicies(t, `{"schema_version": 1, "policies": [
  {"id": "web", "priority": 10, "action": "auto_forward", "conditions": [{"remote_ports": {"from": 8080, "to": 8080}}]},
  {"id": "db", "priority": 5, "action": "ignore"}
]}`)
	var stdout bytes.Buffer
	app := &App{
		Manager:      &fakeManager{},
		PoliciesPath: path,
		Stdout:       &stdout,
	}
	if err := app.Run(context.Background(), []string{"policy", "list"}); err != nil {
		t.Fatalf("policy list: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "web priority=10 action=auto_forward") || !strings.Contains(output, "db priority=5 action=ignore") {
		t.Fatalf("policy list output = %q", output)
	}
}

func TestPolicyListJSON(t *testing.T) {
	path := writePolicies(t, `{"schema_version": 1, "policies": [{"id": "web", "priority": 10, "action": "auto_forward"}]}`)
	var stdout bytes.Buffer
	app := &App{
		Manager:      &fakeManager{},
		PoliciesPath: path,
		Stdout:       &stdout,
	}
	if err := app.Run(context.Background(), []string{"policy", "list", "--json"}); err != nil {
		t.Fatalf("policy list --json: %v", err)
	}
	// --json emits the policies.jsonc file shape (app.MarshalPolicies):
	// snake_case keys and the versioned schema, not Go field names.
	output := stdout.String()
	for _, want := range []string{
		`"schema_version":1`,
		`"id":"web"`,
		`"action":"auto_forward"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("policy list --json missing %q: %s", want, output)
		}
	}
}

func TestPolicyListMissingFile(t *testing.T) {
	var stdout bytes.Buffer
	app := &App{
		Manager:      &fakeManager{},
		PoliciesPath: filepath.Join(t.TempDir(), "absent.jsonc"),
		Stdout:       &stdout,
	}
	if err := app.Run(context.Background(), []string{"policy", "list"}); err == nil {
		t.Fatal("policy list on a missing file succeeded")
	}
}
