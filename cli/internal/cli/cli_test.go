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
	"github.com/wangnan0916/ssh-forward/cli/internal/present"
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
		Manager: manager,
		Host:    core.HostAlias("development"),
		Options: app.Options{PoliciesPath: filepath.Join(t.TempDir(), "policies.jsonc")},
	}, args...)
}

func runCLI(t *testing.T, surface *App, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	surface.Options.Stdout = &stdout
	if surface.Options.Stderr == nil {
		surface.Options.Stderr = &stderr
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
		"8080 → 127.0.0.1:8080",
		"Available:",
		"9090  ssh-forward add 9090",
		"Needs attention:",
		"3000  could not bind a local port",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("status output missing %q:\n%s", want, output)
		}
	}
	for _, hide := range []string{"scanner", "baseline", "managed:ipv4", "Discovery:"} {
		if strings.Contains(output, hide) {
			t.Fatalf("status leaked internal detail %q:\n%s", hide, output)
		}
	}
}

func TestFormatHumanStatusSkipsIgnored(t *testing.T) {
	host := snapshotWithHost().Host
	ignore := []core.ForwardingPolicy{{
		ID: "deny-9090", Action: core.PolicyIgnore,
		Conditions: []core.PolicyCondition{{RemotePorts: &core.PortRange{From: 9090, To: 9090}}},
	}}
	text := formatHumanStatus(present.NewDocument(host, ignore, true))
	if strings.Contains(text, "9090  ssh-forward add") {
		t.Fatalf("ignored port leaked into Addable:\n%s", text)
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
		Manager: &fakeManager{},
		Options: app.Options{PoliciesPath: path},
	}, "add", "5173")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if output != "Remembered 5173. Check with: ssh-forward status\n" {
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
	output, err := runCLI(t, &App{Options: app.Options{PoliciesPath: path}}, "add", "--json", "5173")
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
	output, err := runCLI(t, &App{Options: app.Options{PoliciesPath: path}}, "add", "--dir", "/home/dev/src/app")
	if err != nil {
		t.Fatalf("add --dir: %v", err)
	}
	if output != "Remembered directory /home/dev/src/app. Check with: ssh-forward status\n" {
		t.Fatalf("add --dir output = %q", output)
	}
}

func TestAddRejectsPortAndDir(t *testing.T) {
	_, err := runApp(t, &fakeManager{}, "add", "5173", "--dir", "/home/dev/src/app")
	if err == nil || !strings.Contains(err.Error(), "remember a remote port") {
		t.Fatalf("add port and --dir err = %v, want remember usage", err)
	}
}

func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	if _, err := runCLI(t, &App{Options: app.Options{PoliciesPath: path}}, "add", "5173"); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, &App{Options: app.Options{PoliciesPath: path}}, "remove", "5173")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if output != "Forgot 5173.\n" {
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
		Manager: &fakeManager{},
		Options: app.Options{PoliciesPath: path, Stdout: &stdout},
	}
	if err := app.Run(context.Background(), []string{"policy", "list"}); err != nil {
		t.Fatalf("policy list: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "Remembered:") || !strings.Contains(output, "8080") {
		t.Fatalf("policy list missing remembered port:\n%s", output)
	}
	if !strings.Contains(output, "Other policies:") || !strings.Contains(output, "db") || !strings.Contains(output, "ignore") {
		t.Fatalf("policy list missing hand-edited rule:\n%s", output)
	}
}

func TestPolicyListJSON(t *testing.T) {
	path := writePolicies(t, `{"schema_version": 1, "policies": [{"id": "web", "priority": 10, "action": "auto_forward"}]}`)
	var stdout bytes.Buffer
	app := &App{
		Manager: &fakeManager{},
		Options: app.Options{PoliciesPath: path, Stdout: &stdout},
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
		PolicyReader: reader,
		Options:      app.Options{PoliciesPath: path, Stdout: &stdout, Stderr: &stderr},
	}
	if err := a.Run(context.Background(), []string{"policy", "list"}); err != nil {
		t.Fatalf("policy list with reader: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "web") || !strings.Contains(output, "auto-forward") {
		t.Fatalf("policy list output = %q, want the last valid policies", output)
	}
	if !strings.Contains(stderr.String(), "warning:") {
		t.Fatalf("policy list stderr = %q, want a corrupt-file warning", stderr.String())
	}
}

func TestPolicyListColdCorruptFileHasNoLastValid(t *testing.T) {
	path := writePolicies(t, `{"schema_version": 1, "policies": [{"id": "broken", "action": "bogus"}]}`)
	var stdout, stderr bytes.Buffer
	a := &App{
		Manager:      &fakeManager{},
		PolicyReader: app.NewFilePolicyReader(path),
		Options:      app.Options{PoliciesPath: path, Stdout: &stdout, Stderr: &stderr},
	}
	if err := a.Run(context.Background(), []string{"policy"}); err != nil {
		t.Fatalf("policy list: %v", err)
	}
	if stdout.String() != "Nothing remembered yet. ssh-forward add PORT\n" {
		t.Fatalf("policy list output = %q, want empty (no last-valid)", stdout.String())
	}
	if !strings.Contains(stderr.String(), "this process has no last-valid policies") {
		t.Fatalf("policy list stderr = %q, want a cold-reader warning", stderr.String())
	}
}

func TestPolicyListMissingFile(t *testing.T) {
	var stdout bytes.Buffer
	app := &App{
		Manager: &fakeManager{},
		Options: app.Options{PoliciesPath: filepath.Join(t.TempDir(), "absent.jsonc"), Stdout: &stdout},
	}
	if err := app.Run(context.Background(), []string{"policy", "list"}); err != nil {
		t.Fatalf("policy list on a missing file: %v", err)
	}
	if stdout.String() != "Nothing remembered yet. ssh-forward add PORT\n" {
		t.Fatalf("policy list output = %q, want the empty state", stdout.String())
	}
}

func TestRemoveDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	if _, err := runCLI(t, &App{Options: app.Options{PoliciesPath: path}}, "add", "--dir", "/home/dev/src/app"); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, &App{Options: app.Options{PoliciesPath: path}}, "remove", "--dir", "/home/dev/src/app")
	if err != nil {
		t.Fatalf("remove --dir: %v", err)
	}
	if output != "Forgot directory /home/dev/src/app.\n" {
		t.Fatalf("remove --dir output = %q", output)
	}
}

func TestAddIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policies.jsonc")
	surface := &App{Options: app.Options{PoliciesPath: path}}
	if _, err := runCLI(t, surface, "add", "8080"); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, &App{Options: app.Options{PoliciesPath: path}}, "add", "8080")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if output != "Already remembered 8080.\n" {
		t.Fatalf("add output = %q, want already remembered", output)
	}
}

// TestStatusShowsForwardsAndNewPorts pins the focused human view: no
// listener dump, just the active forwards and a one-line new-port heads-up.
func TestStatusShowsForwardsAndNewPorts(t *testing.T) {
	output, err := runApp(t, &fakeManager{snapshot: snapshotWithHost()}, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(output, "8080 → 127.0.0.1:8080") {
		t.Fatalf("status missing the forward:\n%s", output)
	}
	if !strings.Contains(output, "9090  ssh-forward add 9090") {
		t.Fatalf("status missing the new-port summary:\n%s", output)
	}
	if strings.Contains(output, "continuous") || strings.Contains(output, "631/") {
		t.Fatalf("status leaked the listener dump:\n%s", output)
	}
}

func TestStatusColdCorruptPoliciesOmitsAddable(t *testing.T) {
	path := writePolicies(t, `{"schema_version": 1, "policies": [{"id": "broken", "action": "bogus"}]}`)
	snapshot := snapshotWithHost()
	snapshot.Host.PolicyDiagnostic = "policies_file_invalid"
	output, err := runCLI(t, &App{
		Manager:      &fakeManager{snapshot: snapshot},
		Host:         core.HostAlias("development"),
		PolicyReader: app.NewFilePolicyReader(path),
		Options:      app.Options{PoliciesPath: path},
	}, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(output, "policies.jsonc is unreadable; last valid rules are still in effect.") {
		t.Fatalf("status missing the policy diagnostic:\n%s", output)
	}
	if strings.Contains(output, "ssh-forward add") {
		t.Fatalf("cold corrupt status offered add:\n%s", output)
	}
	if !strings.Contains(output, "8080 → 127.0.0.1:8080") {
		t.Fatalf("status dropped the live forward:\n%s", output)
	}
}

func TestStatusConnectingOmitsScannerInternals(t *testing.T) {
	snapshot := core.Snapshot{
		Host: &core.HostSnapshot{
			Alias:      core.HostAlias("ubuntu"),
			Connection: core.ConnectionConnecting,
			Discovery:  core.DiscoverySnapshot{State: core.DiscoveryStopped, ScannerVersion: 0},
		},
	}
	output, err := runApp(t, &fakeManager{snapshot: snapshot}, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(output, "Host: ubuntu — connecting") || !strings.Contains(output, "Still opening the SSH session.") {
		t.Fatalf("status missing the connecting copy:\n%s", output)
	}
	for _, hide := range []string{"scanner", "baseline", "Discovery:"} {
		if strings.Contains(output, hide) {
			t.Fatalf("connecting status leaked %q:\n%s", hide, output)
		}
	}
}

func TestStatusDegradedOmitsScannerInternals(t *testing.T) {
	snapshot := snapshotWithHost()
	snapshot.Host.Discovery.State = core.DiscoveryDegraded
	snapshot.Host.Discovery.Diagnostic = "process_metadata_unavailable"
	output, err := runApp(t, &fakeManager{snapshot: snapshot}, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(output, "Process names are unavailable on this host.") {
		t.Fatalf("status missing the degraded copy:\n%s", output)
	}
	for _, hide := range []string{"scanner", "baseline", "diagnostic:"} {
		if strings.Contains(output, hide) {
			t.Fatalf("degraded status leaked %q:\n%s", hide, output)
		}
	}
}

func TestStatusEmptyConnected(t *testing.T) {
	snapshot := core.Snapshot{
		Host: &core.HostSnapshot{
			Alias:      core.HostAlias("ubuntu"),
			Connection: core.ConnectionConnected,
			Discovery:  core.DiscoverySnapshot{State: core.DiscoveryHealthy},
		},
	}
	output, err := runApp(t, &fakeManager{snapshot: snapshot}, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(output, "No ports forwarded yet. Remember one with: ssh-forward add PORT") {
		t.Fatalf("status missing the empty state:\n%s", output)
	}
}

func TestStatusWaitsUntilConnected(t *testing.T) {
	stream := &fakeStream{pending: watchSnapshots(), notify: make(chan struct{}, 4)}
	var stderr bytes.Buffer
	output, err := runCLI(t, &App{
		Manager: &fakeManager{
			snapshot: watchSnapshots()[0],
			watch:    func(context.Context) (core.SnapshotStream, error) { return stream, nil },
		},
		Host: core.HostAlias("development"),
		Options: app.Options{
			Interactive:  true,
			Stderr:       &stderr,
			PoliciesPath: filepath.Join(t.TempDir(), "policies.jsonc"),
		},
	}, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(stderr.String(), "Connecting to development...") {
		t.Fatalf("stderr = %q, want a connecting progress line", stderr.String())
	}
	if !strings.Contains(output, "Host: development — connected") {
		t.Fatalf("status did not wait for connected:\n%s", output)
	}
	if strings.Contains(output, "— connecting") {
		t.Fatalf("status still printed the connecting snapshot:\n%s", output)
	}
}

func TestStatusJSONDoesNotWait(t *testing.T) {
	output, err := runCLI(t, &App{
		Manager: &fakeManager{snapshot: watchSnapshots()[0]},
		Host:    core.HostAlias("development"),
		Options: app.Options{
			Interactive:  true,
			PoliciesPath: filepath.Join(t.TempDir(), "policies.jsonc"),
		},
	}, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}
	if !strings.Contains(output, `"connection":"connecting"`) {
		t.Fatalf("status --json should be the current snapshot:\n%s", output)
	}
}

func TestStatusHelpNamesHostFlag(t *testing.T) {
	output, err := runApp(t, &fakeManager{}, "status", "--help")
	if err != nil {
		t.Fatalf("status --help: %v", err)
	}
	for _, want := range []string{"--host ALIAS", "-h is help", "ssh-forward default"} {
		if !strings.Contains(output, want) {
			t.Fatalf("status --help missing %q:\n%s", want, output)
		}
	}
}

func TestHelpListsCommands(t *testing.T) {
	output, err := runApp(t, &fakeManager{}, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, want := range []string{
		"Daily:",
		"Host:",
		"More:",
		"add",
		"remove",
		"status",
		"watch",
		"policy",
		"host",
		"default",
		"manager",
		"ui",
		"--host ALIAS",
		"-h is help",
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

func TestIntentCommandsSkipManager(t *testing.T) {
	root := (&App{}).RootCommand()
	skip := map[string]bool{
		"add": true, "remove": true, "policy": true, "host": true,
		"default": true, "manager": true, "ui": true, "help": true,
	}
	for _, cmd := range root.Commands() {
		got := cmd.Annotations[skipManagerKey] == "1"
		if got != skip[cmd.Name()] {
			t.Errorf("command %q skip-manager = %v, want %v", cmd.Name(), got, skip[cmd.Name()])
		}
	}
}

func TestPrimerOnNoCommand(t *testing.T) {
	output, err := runApp(t, &fakeManager{})
	if err != nil {
		t.Fatalf("no command: %v", err)
	}
	for _, want := range []string{"Daily", "status", "add PORT", "host", "default ALIAS", "ssh-forward COMMAND --help"} {
		if !strings.Contains(output, want) {
			t.Fatalf("primer missing %q:\n%s", want, output)
		}
	}
}

func TestAddWithoutArgsExplainsUsage(t *testing.T) {
	_, err := runApp(t, &fakeManager{}, "add")
	if err == nil || !strings.Contains(err.Error(), "ssh-forward add PORT") {
		t.Fatalf("add without args err = %v, want remember usage", err)
	}
}

func TestPolicyWithoutSubcommandLists(t *testing.T) {
	var stdout bytes.Buffer
	surface := &App{
		Manager: &fakeManager{},
		Options: app.Options{PoliciesPath: filepath.Join(t.TempDir(), "absent.jsonc"), Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"policy"}); err != nil {
		t.Fatalf("policy: %v", err)
	}
	if stdout.String() != "Nothing remembered yet. ssh-forward add PORT\n" {
		t.Fatalf("policy output = %q", stdout.String())
	}
}

func TestDefaultShowsPinnedHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	path := app.DefaultLayout().Config
	if err := app.SetDefaultHost(path, "ubuntu"); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, &App{Options: app.Options{ConfigPath: path}}, "default")
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if output != "default host: ubuntu\n" {
		t.Fatalf("default output = %q", output)
	}
}

func TestDefaultShowsEmptyState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	output, err := runCLI(t, &App{Options: app.Options{ConfigPath: app.DefaultLayout().Config}}, "default")
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if !strings.Contains(output, "No default host.") || !strings.Contains(output, "ssh-forward host") {
		t.Fatalf("default empty output = %q", output)
	}
}

func TestHostWithoutSubcommandLists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host ubuntu\nHost orb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("SSH_FORWARD_CONFIG_DIR", dir)
	if err := app.SetDefaultHost(app.DefaultLayout().Config, "ubuntu"); err != nil {
		t.Fatal(err)
	}
	output, err := runCLI(t, &App{Options: app.Options{ConfigPath: app.DefaultLayout().Config}}, "host")
	if err != nil {
		t.Fatalf("host: %v", err)
	}
	if !strings.Contains(output, "ubuntu  (default)") || !strings.Contains(output, "orb") {
		t.Fatalf("host output = %q", output)
	}
	if strings.Contains(output, "* ") {
		t.Fatalf("host still used a star marker:\n%s", output)
	}
}
