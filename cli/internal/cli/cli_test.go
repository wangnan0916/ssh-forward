package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fakeManager struct {
	status core.Status
	intent core.ForwardingIntent
}

func (m *fakeManager) Status(context.Context) (core.Status, error) { return m.status, nil }
func (m *fakeManager) UpdateIntent(_ context.Context, intent core.ForwardingIntent) error {
	m.intent = intent
	return nil
}
func (*fakeManager) Close(context.Context) error { return nil }

func TestAddWritesRemoteToLocalForward(t *testing.T) {
	configPath := t.TempDir() + "/config.jsonc"
	var stdout bytes.Buffer
	manager := &fakeManager{status: core.Status{Host: "dev"}}
	surface := &App{
		Manager: manager,
		Options: app.Options{ConfigPath: configPath, Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"add", "5173", "--local", "15173"}); err != nil {
		t.Fatal(err)
	}
	intent, err := app.HostIntent(configPath, "dev")
	if err != nil {
		t.Fatal(err)
	}
	want := core.RememberedForward{RemotePort: 5173, LocalPort: 15173}
	if diff := cmp.Diff([]core.RememberedForward{want}, intent.RememberedForwards); diff != "" {
		t.Fatalf("remembered forwards mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]core.RememberedForward{want}, manager.intent.RememberedForwards); diff != "" {
		t.Fatalf("manager remembered forwards mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(stdout.String(), "Remembered remote 5173 at 127.0.0.1:15173 for dev") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestAddWithoutLocalPortAllowsTemporaryFallback(t *testing.T) {
	configPath := t.TempDir() + "/config.jsonc"
	var stdout bytes.Buffer
	manager := &fakeManager{status: core.Status{Host: "dev"}}
	surface := &App{
		Manager: manager,
		Options: app.Options{ConfigPath: configPath, Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"add", "5173"}); err != nil {
		t.Fatal(err)
	}
	want := core.RememberedForward{
		RemotePort: 5173, LocalPort: 5173, AllowFallback: true,
	}
	intent, err := app.HostIntent(configPath, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]core.RememberedForward{want}, intent.RememberedForwards); diff != "" {
		t.Fatalf("remembered forwards mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(stdout.String(), "prefers 127.0.0.1:5173; falls back if busy") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestPublishWritesLocalToRemoteForward(t *testing.T) {
	configPath := t.TempDir() + "/config.jsonc"
	var stdout bytes.Buffer
	manager := &fakeManager{status: core.Status{Host: "dev"}}
	surface := &App{
		Manager: manager,
		Options: app.Options{ConfigPath: configPath, Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"publish", "9222", "--remote", "19222"}); err != nil {
		t.Fatal(err)
	}
	want := core.PublishedForward{LocalPort: 9222, RemotePort: 19222}
	intent, err := app.HostIntent(configPath, "dev")
	if err != nil {
		t.Fatal(err)
	}
	wantForwards := []core.PublishedForward{want}
	if diff := cmp.Diff(wantForwards, intent.PublishedForwards); diff != "" {
		t.Fatalf("published forwards mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantForwards, manager.intent.PublishedForwards); diff != "" {
		t.Fatalf("manager published forwards mismatch (-want +got):\n%s", diff)
	}
	if !strings.Contains(stdout.String(), "Publishing local 127.0.0.1:9222 at dev 127.0.0.1:19222") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestUnpublishJSONReportsRemovedMapping(t *testing.T) {
	configPath := t.TempDir() + "/config.jsonc"
	if _, err := app.SetPublishedForward(configPath, "dev", core.PublishedForward{
		LocalPort: 9222, RemotePort: 19222,
	}); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	manager := &fakeManager{status: core.Status{Host: "dev"}}
	surface := &App{
		Manager: manager,
		Options: app.Options{ConfigPath: configPath, Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"unpublish", "9222", "--json"}); err != nil {
		t.Fatal(err)
	}
	want := "{\"host\":\"dev\",\"local_port\":9222,\"remote_port\":19222,\"removed\":true}\n"
	if stdout.String() != want {
		t.Fatalf("output = %q, want %q", stdout.String(), want)
	}
	if len(manager.intent.PublishedForwards) != 0 {
		t.Fatalf("manager intent = %#v", manager.intent)
	}
}

func TestPublishCommandsReportInvalidLocalPort(t *testing.T) {
	for _, command := range []string{"publish", "unpublish"} {
		t.Run(command, func(t *testing.T) {
			surface := &App{
				Manager: &fakeManager{status: core.Status{Host: "dev"}},
				Options: app.Options{ConfigPath: t.TempDir() + "/config.jsonc"},
			}
			err := surface.Run(context.Background(), []string{command, "invalid"})
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("error = %v, want usage error", err)
			}
			want := command + " requires one local port 1..65535"
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err, want)
			}
		})
	}
}

func TestNoCommandPrimerIncludesPublishedForwards(t *testing.T) {
	var stdout bytes.Buffer
	surface := &App{Options: app.Options{Stdout: &stdout}}
	if err := surface.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"publish LOCAL",
		"unpublish LOCAL",
		"publish a local port on the Development Host",
	} {
		if !strings.Contains(stdout.String(), text) {
			t.Fatalf("primer = %q, missing %q", stdout.String(), text)
		}
	}
}

func TestAddWorkingDirectoryGlobWritesOneHostRuleList(t *testing.T) {
	configPath := t.TempDir() + "/config.jsonc"
	var stdout bytes.Buffer
	surface := &App{
		Manager: &fakeManager{status: core.Status{Host: "dev"}},
		Options: app.Options{ConfigPath: configPath, Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"add", "--pwd", "/workspace/**"}); err != nil {
		t.Fatal(err)
	}
	intent, err := app.HostIntent(configPath, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(intent.WorkingDirectoryRules) != 1 || intent.WorkingDirectoryRules[0] != "/workspace/**" {
		t.Fatalf("rules = %v", intent.WorkingDirectoryRules)
	}
	if !strings.Contains(stdout.String(), "Remembered working-directory glob /workspace/** for dev") {
		t.Fatalf("output = %q", stdout.String())
	}
}

func TestAddWorkingDirectoryGlobRejectsRelativePattern(t *testing.T) {
	surface := &App{
		Manager: &fakeManager{status: core.Status{Host: "dev"}},
		Options: app.Options{ConfigPath: t.TempDir() + "/config.jsonc"},
	}
	err := surface.Run(context.Background(), []string{"add", "--pwd", "workspace/**"})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("error = %v, want usage error", err)
	}
}

func TestAddRejectsLocalPortWithWorkingDirectoryGlob(t *testing.T) {
	surface := &App{
		Manager: &fakeManager{status: core.Status{Host: "dev"}},
		Options: app.Options{ConfigPath: t.TempDir() + "/config.jsonc"},
	}
	err := surface.Run(context.Background(), []string{
		"add", "--pwd", "/workspace/**", "--local", "15173",
	})
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("error = %v, want usage error", err)
	}
}

func TestStatusSeparatesForwardedAndAvailablePorts(t *testing.T) {
	var stdout bytes.Buffer
	surface := &App{
		Manager: &fakeManager{status: core.Status{
			Host: "dev", Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
			Listeners: []core.Listener{
				{Port: 631},
				{Port: 3000, App: "vite", WorkingDirectory: "/workspace/web"},
				{Port: 5173, App: "node", WorkingDirectory: "/workspace/app"},
				{Port: 12000, App: "node", WorkingDirectory: "/workspace/api"},
			},
			Forwards: []core.ForwardStatus{
				{RemotePort: 5173, LocalPort: 15173, State: core.ForwardActive},
				{RemotePort: 12000, LocalPort: 12000, State: core.ForwardActive},
			},
		}},
		Options: app.Options{ConfigPath: t.TempDir() + "/config.jsonc", Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, text := range []string{
		"Host  dev    Discovery  active",
		" 5173  127.0.0.1:15173  remembered  node  /workspace/app",
		"12000  127.0.0.1:12000  remembered  node  /workspace/api",
		"  631  —     —",
		" 3000  vite  /workspace/web",
	} {
		if !strings.Contains(output, text) {
			t.Fatalf("output = %q, missing %q", output, text)
		}
	}
	if strings.Contains(output, "ssh-forward add") {
		t.Fatalf("output still contains an add command: %q", output)
	}
}

func TestStatusJSONPreservesLegacyForwardShape(t *testing.T) {
	got := renderForwardStatusJSON(t, core.ForwardStatus{
		RemotePort: 8443, PreferredLocalPort: 8443, LocalPort: 8443,
		State: core.ForwardActive, AllowFallback: true,
	})
	want := "{\"host\":\"dev\",\"discovery\":{\"state\":\"active\"},\"listeners\":[{\"port\":631}],\"forwards\":[{\"port\":8443,\"state\":\"active\"}]}\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestStatusJSONIncludesFallbackMapping(t *testing.T) {
	got := renderForwardStatusJSON(t, core.ForwardStatus{
		RemotePort: 8443, PreferredLocalPort: 8443, LocalPort: 8444,
		State: core.ForwardActive, AllowFallback: true,
	})
	want := "{\"host\":\"dev\",\"discovery\":{\"state\":\"active\"},\"listeners\":[{\"port\":631}],\"forwards\":[{\"remote_port\":8443,\"preferred_local_port\":8443,\"local_port\":8444,\"state\":\"active\",\"allow_fallback\":true}]}\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestStatusJSONIncludesExplicitPublishedForward(t *testing.T) {
	got := renderForwardStatusJSON(t, core.ForwardStatus{
		Direction: core.LocalToRemote, LocalPort: 9222,
		PreferredRemotePort: 19222, RemotePort: 19222,
		State: core.ForwardActive,
	})
	want := "{\"host\":\"dev\",\"discovery\":{\"state\":\"active\"},\"listeners\":[{\"port\":631}],\"forwards\":[{\"direction\":\"local_to_remote\",\"local_port\":9222,\"preferred_remote_port\":19222,\"remote_port\":19222,\"state\":\"active\",\"kind\":\"published\"}]}\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func renderForwardStatusJSON(t *testing.T, forward core.ForwardStatus) string {
	t.Helper()
	var stdout bytes.Buffer
	surface := &App{
		Manager: &fakeManager{status: core.Status{
			Host:      "dev",
			Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
			Listeners: []core.Listener{{Port: 631}},
			Forwards:  []core.ForwardStatus{forward},
		}},
		Options: app.Options{ConfigPath: t.TempDir() + "/config.jsonc", Stdout: &stdout},
	}
	if err := surface.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	return stdout.String()
}

func TestRootHasNoPolicyOrDirectorySurface(t *testing.T) {
	root := (&App{}).RootCommand()
	if _, _, err := root.Find([]string{"policy"}); err == nil {
		t.Fatal("policy command still exists")
	}
	add, _, err := root.Find([]string{"add"})
	if err != nil {
		t.Fatal(err)
	}
	if add.Flags().Lookup("dir") != nil {
		t.Fatal("add --dir still exists")
	}
	if add.Flags().Lookup("pwd") == nil {
		t.Fatal("add --pwd is missing")
	}
	if add.Flags().Lookup("local") == nil {
		t.Fatal("add --local is missing")
	}
	publish, _, err := root.Find([]string{"publish"})
	if err != nil || publish.Flags().Lookup("remote") == nil {
		t.Fatal("publish --remote is missing")
	}
	unpublish, _, err := root.Find([]string{"unpublish"})
	if err != nil || unpublish.Flags().Lookup("remote") != nil {
		t.Fatal("unpublish command surface is invalid")
	}
	remove, _, err := root.Find([]string{"remove"})
	if err != nil {
		t.Fatal(err)
	}
	if remove.Flags().Lookup("local") != nil {
		t.Fatal("remove unexpectedly accepts --local")
	}
	status, _, err := root.Find([]string{"status"})
	if err != nil || status.Flags().Lookup("watch") == nil {
		t.Fatal("status --watch is missing")
	}
	if uninstall, _, err := root.Find([]string{"uninstall"}); err != nil || uninstall.Hidden {
		t.Fatalf("uninstall command is missing: %v", err)
	}
	if doctor, _, err := root.Find([]string{"doctor"}); err != nil || doctor.Hidden || needsManager(doctor) {
		t.Fatalf("read-only doctor command is unavailable: %v", err)
	}
	for _, command := range root.Commands() {
		if command.Name() == "watch" || command.Name() == "manager" && !command.Hidden {
			t.Fatalf("obsolete public command %q is still visible", command.Name())
		}
	}
}
