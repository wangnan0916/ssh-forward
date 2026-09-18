package statusview

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestRenderPlainStatus(t *testing.T) {
	status := core.Status{
		Host:      "ubuntu",
		Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
		Listeners: []core.Listener{
			{Port: 631},
			{Port: 922},
			{Port: 5173},
			{Port: 7897, App: "verge-mihomo", WorkingDirectory: "/home/shampoo"},
			{Port: 12000, App: "node", WorkingDirectory: "/home/shampoo/Workspace/project/console.cli.im"},
			{Port: 33331, App: "clash-verge", WorkingDirectory: "/home/shampoo"},
		},
		Forwards: []core.ForwardStatus{
			{RemotePort: 631, LocalPort: 10631, State: core.ForwardActive},
			{RemotePort: 5173, LocalPort: 15173, State: core.ForwardActive},
			{RemotePort: 12000, LocalPort: 12000, State: core.ForwardActive},
		},
	}
	output := renderStatus(t, status, Options{})
	want := `Host  ubuntu    Discovery  active

FORWARDS
REMOTE  TARGET         KIND        APP   WORKING DIRECTORY
   631  0.0.0.0:10631  remembered  —     —
  5173  0.0.0.0:15173  remembered  —     —
 12000  0.0.0.0:12000  remembered  node  /home/shampoo/Workspace/project/console.cli.im

AVAILABLE
 PORT  APP           WORKING DIRECTORY
  922  —             —
 7897  verge-mihomo  /home/shampoo
33331  clash-verge   /home/shampoo
`
	require.EqualValuesf(t, want, output, "output:\n%s\nwant:\n%s", output, want)
}

func TestRenderStatusFeatures(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           core.Status
		options          Options
		contains, absent []string
	}{
		{name: "narrow paths", status: core.Status{Listeners: []core.Listener{{Port: 12000, App: "node", WorkingDirectory: "/home/shampoo/Workspace/nears/worktrees/feature/console.cli.im"}}}, options: Options{Width: 48}, contains: []string{"…", "console.cli.im"}},
		{name: "missing metadata", status: core.Status{Listeners: []core.Listener{{Port: 3000, App: "node"}, {Port: 4000, WorkingDirectory: "/workspace"}}}, contains: []string{" 3000  node  —", " 4000  —     /workspace"}},
		{name: "diagnostics", status: core.Status{
			Discovery: core.DiscoveryStatus{State: core.DiscoveryFailed, Diagnostic: "authentication_failed"},
			Forwards:  []core.ForwardStatus{{RemotePort: 3000, LocalPort: 13000, State: core.ForwardStarting}, {RemotePort: 8080, LocalPort: 8080, State: core.ForwardFailed, Diagnostic: "local_port_conflict", Automatic: true}},
		}, contains: []string{"Discovery  failed", "Discovery detail  SSH authentication failed.", "STARTING", "3000  0.0.0.0:13000  remembered", "NEEDS ATTENTION", "8080  0.0.0.0:8080  automatic  the same local port is already in use"}},
		{name: "fallback", status: core.Status{Forwards: []core.ForwardStatus{{RemotePort: 3000, PreferredLocalPort: 3000, LocalPort: 3001, State: core.ForwardActive, AllowFallback: true}}}, contains: []string{"0.0.0.0:3001 (preferred 3000)"}},
		{name: "published", status: core.Status{
			Listeners: []core.Listener{{Port: 19222, App: "sshd", WorkingDirectory: "/should/not/appear"}},
			Forwards: []core.ForwardStatus{
				{Direction: core.LocalToRemote, LocalPort: 9222, PreferredRemotePort: 19222, RemotePort: 19222, State: core.ForwardActive},
				{Direction: core.LocalToRemote, LocalPort: 9333, PreferredRemotePort: 19333, RemotePort: 19333, State: core.ForwardFailed, Diagnostic: "remote_port_unavailable"},
			},
		}, options: Options{Hyperlinks: true}, contains: []string{"PUBLISHED", "LOCAL  REMOTE TARGET    KIND", " 9222  127.0.0.1:19222  published", "PUBLISH NEEDS ATTENTION", "9333  127.0.0.1:19333  published  the Development Host port could not be opened"}, absent: []string{"/should/not/appear", "http://127.0.0.1:19222"}},
		{name: "empty", contains: []string{"No loopback TCP listeners found."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := renderStatus(t, tc.status, tc.options)
			for _, text := range tc.contains {
				require.Contains(t, output, text)
			}
			for _, text := range tc.absent {
				require.NotContains(t, output, text)
			}
			if tc.options.Width > 0 {
				requireMaxWidth(t, output, tc.options.Width)
			}
		})
	}
}

func TestRenderTerminalCapabilitiesAreExplicit(t *testing.T) {
	status := core.Status{
		Listeners: []core.Listener{{Port: 3000, App: "node", WorkingDirectory: "/workspace/app"}, {Port: 8080, App: "api", WorkingDirectory: "/workspace/api"}},
		Forwards: []core.ForwardStatus{
			{RemotePort: 3000, LocalPort: 13000, State: core.ForwardActive},
			{RemotePort: 4000, LocalPort: 14000, State: core.ForwardStarting},
			{RemotePort: 5000, LocalPort: 15000, State: core.ForwardFailed, Diagnostic: "local_port_conflict"},
		},
	}
	plain := renderStatus(t, status, Options{})
	colored := renderStatus(t, status, Options{Color: true})
	require.Equal(t, plain, ansi.Strip(colored))
	codes := regexp.MustCompile(`\x1b\[[0-9;]*m`).FindAllString(colored, -1)
	slices.Sort(codes)
	require.GreaterOrEqual(t, len(slices.Compact(codes)), 8, "semantic palette")
	linked := renderStatus(t, status, Options{Width: 80, Color: true, Hyperlinks: true})
	require.Contains(t, linked, "\x1b]8;;http://127.0.0.1:13000\x1b\\0.0.0.0:13000\x1b]8;;\x1b\\")
	for _, port := range []string{"14000", "15000"} {
		require.NotContains(t, linked, "http://127.0.0.1:"+port)
	}
	requireMaxWidth(t, linked, 80)
}

func renderStatus(t *testing.T, status core.Status, options Options) string {
	t.Helper()
	if status.Host == "" {
		status.Host = "dev"
	}
	if status.Discovery.State == "" {
		status.Discovery.State = core.DiscoveryActive
	}
	var output bytes.Buffer
	require.NoError(t, Render(&output, status, options))
	return output.String()
}

func requireMaxWidth(t *testing.T, output string, width int) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), width, "%q", line)
	}
}

func TestShortenTailPreservesGraphemesAndWidth(t *testing.T) {
	for _, test := range []struct {
		value string
		width int
		want  string
	}{
		{"/work/api", 5, "…/api"},
		{"/目录", 2, "…"},
		{"/work/👨‍👩‍👦", 3, "…👨‍👩‍👦"},
		{"/work/e\u0301", 2, "…e\u0301"},
		{"short", 10, "short"},
		{"\x1b[31m/work/api\x1b[0m", 4, "…api"},
	} {
		got := shortenTail(test.value, test.width)
		if plain := ansi.Strip(got); plain != test.want {
			t.Errorf("shorten %q: %q, want %q", test.value, plain, test.want)
		}
		if ansi.StringWidth(got) > test.width {
			t.Errorf("overflow: %q", got)
		}
	}
}
