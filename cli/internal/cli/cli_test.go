package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

type fakeManager struct {
	status  core.Status
	reloads []string
}

func (m *fakeManager) AllStatuses(context.Context) ([]core.Status, error) {
	return []core.Status{m.status}, nil
}
func (m *fakeManager) Reload(_ context.Context, host string) error {
	m.reloads = append(m.reloads, host)
	return nil
}
func (*fakeManager) Close(context.Context) error { return nil }

func TestUnpublishJSONReportsRemovedMapping(t *testing.T) {
	configPath := t.TempDir() + "/config.jsonc"
	if _, err := app.EditPublishedForward(configPath, "dev", &core.PublishedForward{LocalPort: 9222, RemotePort: 19222}, true); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	manager := &fakeManager{status: core.Status{Host: "dev"}}
	surface := &App{Manager: manager, Options: app.Options{ConfigPath: configPath, Stdout: &stdout}}
	require.NoError(t, surface.Run(context.Background(), []string{"unpublish", "9222", "--json", "--host", "dev"}))
	want := "{\"host\":\"dev\",\"local_port\":9222,\"remote_port\":19222,\"removed\":true}\n"
	require.EqualValuesf(t, want, stdout.String(), "output = %q, want %q", stdout.String(), want)
	intent, err := app.HostIntent(configPath, "dev")
	require.Falsef(t, err != nil || len(intent.PublishedForwards) != 0, "saved intent: %+v, %v", intent, err)
	require.EqualValues(t, 1, len(manager.reloads), "manager was not reloaded")
}

func TestNoCommandDisplaysGeneratedHelp(t *testing.T) {
	var stdout bytes.Buffer
	surface := &App{Options: app.Options{Stdout: &stdout}}
	require.NoError(t, surface.Run(context.Background(), nil))
	for _, text := range []string{"publish <LOCAL>", "unpublish <LOCAL>", "Publish a local port on the Development Host"} {
		require.Contains(t, stdout.String(), text)
	}
}

func TestStatusDelegatesHumanRenderingAndPreservesJSONEnvelope(t *testing.T) {
	surface, manager, output := testCLI(t)
	manager.status = core.Status{Host: "dev", Discovery: core.DiscoveryStatus{State: core.DiscoveryActive}, Listeners: []core.Listener{{Port: 631}}}
	require.NoError(t, surface.Run(context.Background(), []string{"status"}))
	require.Contains(t, output.String(), "Host  dev    Discovery  active")
	require.Contains(t, output.String(), "AVAILABLE")
	require.NotContains(t, output.String(), "ssh-forward add")
	output.Reset()
	require.NoError(t, surface.Run(context.Background(), []string{"status", "--host", "dev", "--json"}))
	require.JSONEq(t, `{"host":"dev","discovery":{"state":"active"},"listeners":[{"port":631}],"forwards":null}`, output.String())
}

func TestForwardJSONCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		forward core.ForwardStatus
		json    string
	}{
		{"same port", core.ForwardStatus{RemotePort: 8443, PreferredLocalPort: 8443, LocalPort: 8443, State: core.ForwardActive, AllowFallback: true}, `{"port":8443,"state":"active"}`},
		{"fallback", core.ForwardStatus{RemotePort: 8443, PreferredLocalPort: 8443, LocalPort: 8444, State: core.ForwardActive, AllowFallback: true}, `{"remote_port":8443,"preferred_local_port":8443,"local_port":8444,"state":"active","allow_fallback":true}`},
		{"publication", core.ForwardStatus{Direction: core.LocalToRemote, LocalPort: 9222, PreferredRemotePort: 19222, RemotePort: 19222, State: core.ForwardActive}, `{"direction":"local_to_remote","local_port":9222,"preferred_remote_port":19222,"remote_port":19222,"state":"active","kind":"published"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, err := json.Marshal(statusJSON(core.Status{Forwards: []core.ForwardStatus{tc.forward}}).Forwards)
			require.NoError(t, err)
			require.JSONEq(t, "["+tc.json+"]", string(output))
		})
	}
}

func TestCommandSurface(t *testing.T) {
	for _, tc := range []struct {
		command         string
		present, absent []string
	}{
		{"", []string{"hostname, IP, or user@host", "doctor", "uninstall"}, []string{"  manager", "  policy", "  watch"}},
		{"add", []string{"--pwd", "--local"}, []string{"--dir"}},
		{"remove", []string{"--pwd"}, []string{"--local"}},
		{"publish", []string{"--remote"}, nil},
		{"unpublish", nil, []string{"--remote"}},
		{"status", []string{"--watch", "--json"}, nil},
	} {
		t.Run(tc.command, func(t *testing.T) {
			var output bytes.Buffer
			surface := &App{Options: app.Options{Stdout: &output}}
			args := []string{"--help"}
			if tc.command != "" {
				args = append([]string{tc.command}, args...)
			}
			require.NoError(t, surface.Run(context.Background(), args))
			for _, text := range tc.present {
				require.Contains(t, output.String(), text)
			}
			for _, text := range tc.absent {
				require.NotContains(t, output.String(), text)
			}
			require.Nil(t, surface.Manager, "help must never create a service session")
		})
	}
	for _, args := range [][]string{{"policy"}, {"watch"}, {"add", "--dir", "/workspace"}, {"remove", "5173", "--local", "15173"}, {"unpublish", "9222", "--remote", "19222"}} {
		require.ErrorIs(t, (&App{}).Run(context.Background(), args), ErrUsage)
	}
}

func TestRuleCommandsPersistIntent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		want   core.ForwardingIntent
		output string
	}{
		{"mapped import", []string{"add", "5173", "--local", "15173"}, core.ForwardingIntent{RememberedForwards: []core.RememberedForward{{RemotePort: 5173, LocalPort: 15173}}}, "Remembered remote 5173 at 0.0.0.0:15173 for dev"},
		{"fallback import", []string{"add", "5173"}, core.ForwardingIntent{RememberedForwards: []core.RememberedForward{{RemotePort: 5173, LocalPort: 5173, AllowFallback: true}}}, "prefers 0.0.0.0:5173; falls back if busy"},
		{"publication", []string{"publish", "9222", "--remote", "19222"}, core.ForwardingIntent{PublishedForwards: []core.PublishedForward{{LocalPort: 9222, RemotePort: 19222}}}, "Publishing local 127.0.0.1:9222 at dev 127.0.0.1:19222"},
		{"directory", []string{"add", "--pwd", "/workspace/**"}, core.ForwardingIntent{WorkingDirectoryRules: []string{"/workspace/**"}}, "Remembered working-directory glob /workspace/** for dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			surface, manager, output := testCLI(t)
			require.NoError(t, surface.Run(context.Background(), append(tc.args, "--host", "dev")))
			intent, err := app.HostIntent(surface.Options.ConfigPath, "dev")
			require.NoError(t, err)
			require.Equal(t, tc.want, intent)
			require.Equal(t, []string{"dev"}, manager.reloads)
			require.Contains(t, output.String(), tc.output)
		})
	}
}

func TestRuleCommandValidation(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"publish", "invalid", "--host", "dev"}, "publish requires one local port 1..65535"},
		{[]string{"unpublish", "invalid", "--host", "dev"}, "unpublish requires one local port 1..65535"},
		{[]string{"add", "--pwd", "workspace/**"}, ""},
		{[]string{"add", "--pwd", "/workspace/**", "--local", "15173"}, ""},
		{[]string{"add", "8080", "--host=-bad"}, "invalid host name"},
		{[]string{"add", "0"}, ""}, {[]string{"add", "5173", "--local", "0"}, ""},
		{[]string{"publish", "9222", "--remote", "0", "--host", "dev"}, ""},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			surface, manager, _ := testCLI(t)
			err := surface.Run(context.Background(), tc.args)
			require.ErrorIs(t, err, ErrUsage)
			if tc.message != "" {
				require.Equal(t, tc.message, err.Error())
			}
			require.Empty(t, manager.reloads, "invalid edits must not reach the manager")
		})
	}
}

func testCLI(t *testing.T) (*App, *fakeManager, *bytes.Buffer) {
	t.Helper()
	output := new(bytes.Buffer)
	manager := &fakeManager{status: core.Status{Host: "dev"}}
	return &App{Manager: manager, Options: app.Options{ConfigPath: t.TempDir() + "/config.jsonc", Stdout: output}}, manager, output
}

func TestHostCommandsPersistSettingsAcrossIgnoreAndEnable(t *testing.T) {
	surface, _, output := testCLI(t)
	args := []string{"host", "add", "dev", "--target", "me@dev", "--port", "2222", "--user", "me", "--identity", "/keys/dev", "--jump", "jump", "--ssh-config", "/ssh/config"}
	require.NoError(t, surface.Run(context.Background(), args))
	hosts, _, err := app.HostList(surface.Options.ConfigPath)
	require.NoError(t, err)
	target := hosts["dev"]
	require.Equal(t, "me@dev", target.Target)
	require.Equal(t, []string{"-l", "me", "-i", "/keys/dev", "-J", "jump", "-F", "/ssh/config", "-p", "2222"}, target.Arguments)
	for _, action := range []string{"ignore", "enable"} {
		require.NoError(t, surface.Run(context.Background(), []string{"host", action, "dev"}))
		hosts, ignored, err := app.HostList(surface.Options.ConfigPath)
		require.NoError(t, err)
		require.Equal(t, target, hosts["dev"])
		require.Equal(t, action == "ignore", slices.Contains(ignored, "dev"))
		output.Reset()
		require.NoError(t, surface.Run(context.Background(), []string{"host", "--json"}))
		require.True(t, json.Valid(output.Bytes()))
		require.Contains(t, output.String(), "me@dev")
	}
	output.Reset()
	surface.Options.Version = "test"
	require.NoError(t, surface.Run(context.Background(), []string{"--version"}))
	require.Equal(t, "ssh-forward test\n", output.String())
}
