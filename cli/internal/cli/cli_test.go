package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// fakeManager is a scriptable core.Manager for CLI tests: commands do not
// touch the network, so the whole surface runs on injected state.
type fakeManager struct {
	snapshot core.Snapshot
	watch    func(context.Context) (core.SnapshotStream, error)
}

func (m *fakeManager) Snapshot(context.Context) (core.Snapshot, error) {
	return m.snapshot, nil
}

func (m *fakeManager) Watch(ctx context.Context) (core.SnapshotStream, error) {
	if m.watch == nil {
		return nil, errors.New("unexpected Watch call")
	}
	return m.watch(ctx)
}

func (*fakeManager) Close(context.Context) error { return nil }

func runApp(t *testing.T, manager core.Manager, args ...string) (string, error) {
	t.Helper()
	return runCLI(t, &App{
		Manager:      manager,
		Host:         core.HostAlias("development"),
		PoliciesPath: filepath.Join(t.TempDir(), "policies.jsonc"),
	}, args...)
}

func runCLI(t *testing.T, surface *App, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	surface.Stdout = &stdout
	if surface.Stderr == nil {
		surface.Stderr = &stderr
	}
	err := surface.Run(context.Background(), args)
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
				{Family: core.FamilyIPv6, BindScope: core.BindLoopback, RemotePort: 9090},
			},
			Forwards: []core.ForwardSnapshot{
				{
					ID:                 core.ForwardID("managed:ipv4:loopback:8080"),
					RemotePort:         8080,
					RemoteFamily:       core.FamilyIPv4,
					AllocatedLocalPort: 8080,
					LocalFamilies:      []core.AddressFamily{core.FamilyIPv4},
				},
			},
			LocalPortConflicts: []core.LocalPortConflict{{
				RemotePort:   3000,
				RemoteFamily: core.FamilyIPv4,
				BindScope:    core.BindLoopback,
			}},
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
		"New remote ports: 9090 (ssh-forward add PORT)",
		"managed:ipv4:loopback:8080 → ipv4:8080 (local 8080)",
		"Local port conflicts:",
		"loopback ipv4:3000",
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
	// The --json shape is the wire Snapshot.
	for _, want := range []string{
		`"revision":5`,
		`"alias":"development"`,
		`"remote_port":8080`,
		`"local_port_conflicts"`,
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

func TestAdd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	output, err := runCLI(t, &App{
		Manager:      &fakeManager{},
		PoliciesPath: path,
	}, "add", "5173")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if output != "added port 5173\n" {
		t.Fatalf("add output = %q", output)
	}
	policies, err := app.LoadPolicies(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].ID != "port-5173" {
		t.Fatalf("policies = %#v, want port-5173", policies)
	}
}

func TestAddJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	output, err := runCLI(t, &App{PoliciesPath: path}, "add", "--json", "5173")
	if err != nil {
		t.Fatalf("add --json: %v", err)
	}
	if !strings.Contains(output, `"added":true`) || !strings.Contains(output, `"port":5173`) {
		t.Fatalf("add --json output = %q", output)
	}
}

func TestAddRequiresPortOrDir(t *testing.T) {
	if _, err := runApp(t, &fakeManager{}, "add"); err == nil {
		t.Fatal("add without a port or --dir succeeded")
	}
}

func TestAddDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	output, err := runCLI(t, &App{PoliciesPath: path}, "add", "--dir", "/home/dev/src/app")
	if err != nil {
		t.Fatalf("add --dir: %v", err)
	}
	if output != "added directory /home/dev/src/app\n" {
		t.Fatalf("add --dir output = %q", output)
	}
}

func TestAddRejectsPortAndDir(t *testing.T) {
	_, err := runApp(t, &fakeManager{}, "add", "5173", "--dir", "/home/dev/src/app")
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("add port and --dir err = %v, want usage", err)
	}
}

func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	if _, err := runCLI(t, &App{PoliciesPath: path}, "add", "5173"); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, &App{PoliciesPath: path}, "remove", "5173")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if output != "removed port 5173\n" {
		t.Fatalf("remove output = %q", output)
	}
}

func TestRemoveMissingPort(t *testing.T) {
	_, err := runApp(t, &fakeManager{}, "remove", "5173")
	if err == nil || !strings.Contains(err.Error(), "not remembered") {
		t.Fatalf("remove missing err = %v, want not remembered", err)
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

func TestPolicyListWithReaderShowsLastValidOnCorruptFile(t *testing.T) {
	path := writePolicies(t, `{"schema_version": 1, "policies": [{"id": "web", "priority": 10, "action": "auto_forward"}]}`)
	reader := app.NewFilePolicyReader(path)
	// Prime the shared reader with a valid read.
	if _, err := reader.Read(); err != nil {
		t.Fatalf("prime read: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version": 1, "policies": [{"id": "broken", "action": "bogus"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	a := &App{
		Manager:      &fakeManager{},
		PoliciesPath: path,
		PolicyReader: reader,
		Stdout:       &stdout,
		Stderr:       &stderr,
	}
	if err := a.Run(context.Background(), []string{"policy", "list"}); err != nil {
		t.Fatalf("policy list with reader: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "web priority=10 action=auto_forward") {
		t.Fatalf("policy list output = %q, want the last valid policies", output)
	}
	if !strings.Contains(stderr.String(), "warning:") {
		t.Fatalf("policy list stderr = %q, want a corrupt-file warning", stderr.String())
	}
}

func TestPolicyListMissingFile(t *testing.T) {
	var stdout bytes.Buffer
	app := &App{
		Manager:      &fakeManager{},
		PoliciesPath: filepath.Join(t.TempDir(), "absent.jsonc"),
		Stdout:       &stdout,
	}
	if err := app.Run(context.Background(), []string{"policy", "list"}); err != nil {
		t.Fatalf("policy list on a missing file: %v", err)
	}
	if stdout.String() != "no policies\n" {
		t.Fatalf("policy list output = %q, want no policies", stdout.String())
	}
}

func TestRemoveDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	if _, err := runCLI(t, &App{PoliciesPath: path}, "add", "--dir", "/home/dev/src/app"); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, &App{PoliciesPath: path}, "remove", "--dir", "/home/dev/src/app")
	if err != nil {
		t.Fatalf("remove --dir: %v", err)
	}
	if output != "removed directory /home/dev/src/app\n" {
		t.Fatalf("remove --dir output = %q", output)
	}
}

func TestAddIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	surface := &App{PoliciesPath: path}
	if _, err := runCLI(t, surface, "add", "8080"); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, &App{PoliciesPath: path}, "add", "8080")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if output != "already added port 8080\n" {
		t.Fatalf("add output = %q, want already added", output)
	}
}

// TestStatusShowsForwardsAndNewPorts pins the focused human view: no
// listener dump, just the active forwards and a one-line new-port heads-up.
func TestStatusShowsForwardsAndNewPorts(t *testing.T) {
	output, err := runApp(t, &fakeManager{snapshot: snapshotWithHost()}, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(output, "managed:ipv4:loopback:8080 → ipv4:8080 (local 8080)") {
		t.Fatalf("status missing the forward:\n%s", output)
	}
	if !strings.Contains(output, "New remote ports: 9090") {
		t.Fatalf("status missing the new-port summary:\n%s", output)
	}
	if strings.Contains(output, "continuous") || strings.Contains(output, "631/") {
		t.Fatalf("status leaked the listener dump:\n%s", output)
	}
}

func TestHelpListsCommands(t *testing.T) {
	output, err := runApp(t, &fakeManager{}, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{
		"Available Commands:",
		"add",
		"remove",
		"status",
		"watch",
		"policy",
		"host",
		"default",
		"manager",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("--help missing %q:\n%s", want, output)
		}
	}
	for _, hide := range []string{"approve", "suppress"} {
		if strings.Contains(output, hide) {
			t.Fatalf("--help should not list %q:\n%s", hide, output)
		}
	}
}

func TestAddHelp(t *testing.T) {
	output, err := runApp(t, &fakeManager{}, "add", "--help")
	if err != nil {
		t.Fatalf("add --help: %v", err)
	}
	if !strings.Contains(output, "remember a remote port") {
		t.Fatalf("add --help missing the command summary:\n%s", output)
	}
	if !strings.Contains(output, "--dir") || !strings.Contains(output, "--json") {
		t.Fatalf("add --help missing flags:\n%s", output)
	}
	if strings.Contains(output, "--family") {
		t.Fatalf("add --help still lists --family:\n%s", output)
	}
}
