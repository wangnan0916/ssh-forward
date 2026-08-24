package statusview

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestRenderPlainStatus(t *testing.T) {
	status := core.Status{
		Host:      "ubuntu",
		Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
		Listeners: []core.Listener{
			{Port: 631},
			{Port: 5173},
			{Port: 7897, App: "verge-mihomo", WorkingDirectory: "/home/shampoo"},
			{Port: 12000, App: "node", WorkingDirectory: "/home/shampoo/Workspace/project/console.cli.im"},
			{Port: 33331, App: "clash-verge", WorkingDirectory: "/home/shampoo"},
		},
		Forwards: []core.ForwardStatus{
			{Port: 5173, State: core.ForwardActive},
			{Port: 12000, State: core.ForwardActive},
		},
	}
	output := renderStatus(t, status, Options{})
	want := `Host  ubuntu    Discovery  active

FORWARDS
 PORT  TARGET           KIND        APP   WORKING DIRECTORY
 5173  127.0.0.1:5173   remembered  —     —
12000  127.0.0.1:12000  remembered  node  /home/shampoo/Workspace/project/console.cli.im

AVAILABLE
 PORT  APP           WORKING DIRECTORY
  631  —             —
 7897  verge-mihomo  /home/shampoo
33331  clash-verge   /home/shampoo
`
	if output != want {
		t.Fatalf("output:\n%s\nwant:\n%s", output, want)
	}
}

func TestRenderFitsWorkingDirectoryToWidth(t *testing.T) {
	status := core.Status{
		Host:      "dev",
		Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
		Listeners: []core.Listener{{
			Port: 12000, App: "node",
			WorkingDirectory: "/home/shampoo/Workspace/nears/worktrees/feature/console.cli.im",
		}},
	}
	output := renderStatus(t, status, Options{Width: 48})
	requireMaxWidth(t, output, 48)
	if !strings.Contains(output, "…") || !strings.Contains(output, "console.cli.im") {
		t.Fatalf("output does not preserve the final path segment: %q", output)
	}
}

func TestRenderMissingMetadataPlaceholders(t *testing.T) {
	status := core.Status{
		Host:      "dev",
		Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
		Listeners: []core.Listener{
			{Port: 3000, App: "node"},
			{Port: 4000, WorkingDirectory: "/workspace"},
		},
	}
	output := renderStatus(t, status, Options{})
	for _, row := range []string{
		" 3000  node  —",
		" 4000  —     /workspace",
	} {
		if !strings.Contains(output, row) {
			t.Fatalf("output = %q, missing %q", output, row)
		}
	}
}

func TestRenderForwardStatesAndDiagnostics(t *testing.T) {
	status := core.Status{
		Host:      "dev",
		Discovery: core.DiscoveryStatus{State: core.DiscoveryFailed, Diagnostic: "authentication_failed"},
		Forwards: []core.ForwardStatus{
			{Port: 3000, State: core.ForwardStarting},
			{Port: 8080, State: core.ForwardFailed, Diagnostic: "local_port_conflict", Automatic: true},
		},
	}
	output := renderStatus(t, status, Options{})
	for _, text := range []string{
		"Discovery  failed",
		"Discovery detail  SSH authentication failed.",
		"STARTING",
		"3000  127.0.0.1:3000  remembered",
		"NEEDS ATTENTION",
		"8080  127.0.0.1:8080  automatic  the same local port is already in use",
	} {
		if !strings.Contains(output, text) {
			t.Fatalf("output = %q, missing %q", output, text)
		}
	}
}

func TestRenderColorIsExplicit(t *testing.T) {
	status := core.Status{
		Host:      "dev",
		Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
		Listeners: []core.Listener{
			{Port: 3000, App: "node", WorkingDirectory: "/workspace/app"},
			{Port: 8080, App: "api", WorkingDirectory: "/workspace/api"},
		},
		Forwards: []core.ForwardStatus{{Port: 3000, State: core.ForwardActive}},
	}
	plain := renderStatus(t, status, Options{})
	colored := renderStatus(t, status, Options{Color: true})
	if !strings.Contains(colored, "\x1b[") {
		t.Fatalf("styled output contains no ANSI sequences: %q", colored)
	}

	ansiPattern := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	if got := ansiPattern.ReplaceAllString(colored, ""); got != plain {
		t.Fatalf("styled output changed content:\n%s\nwant:\n%s", got, plain)
	}
	codes := make(map[string]struct{})
	for _, code := range ansiPattern.FindAllString(colored, -1) {
		codes[code] = struct{}{}
	}
	if len(codes) < 8 {
		t.Fatalf("distinct ANSI styles = %d, want semantic palette: %q", len(codes), colored)
	}
}

func TestRenderHyperlinksActiveForwardTargets(t *testing.T) {
	status := core.Status{
		Host:      "dev",
		Discovery: core.DiscoveryStatus{State: core.DiscoveryActive},
		Forwards: []core.ForwardStatus{
			{Port: 3000, State: core.ForwardActive},
			{Port: 4000, State: core.ForwardStarting},
			{Port: 5000, State: core.ForwardFailed, Diagnostic: "local_port_conflict"},
		},
	}
	output := renderStatus(t, status, Options{Width: 80, Color: true, Hyperlinks: true})
	link := "\x1b]8;;http://127.0.0.1:3000\x1b\\127.0.0.1:3000\x1b]8;;\x1b\\"
	if !strings.Contains(output, link) {
		t.Fatalf("output = %q, missing active forward hyperlink %q", output, link)
	}
	for _, port := range []string{"4000", "5000"} {
		if strings.Contains(output, "http://127.0.0.1:"+port) {
			t.Fatalf("output unexpectedly links inactive forward %s: %q", port, output)
		}
	}
	requireMaxWidth(t, output, 80)
}

func TestRenderEmptyActiveDiscovery(t *testing.T) {
	status := core.Status{Host: "dev", Discovery: core.DiscoveryStatus{State: core.DiscoveryActive}}
	output := renderStatus(t, status, Options{})
	if !strings.Contains(output, "No loopback TCP listeners found.") {
		t.Fatalf("output = %q", output)
	}
}

func renderStatus(t *testing.T, status core.Status, options Options) string {
	t.Helper()
	var output bytes.Buffer
	if err := Render(&output, status, options); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func requireMaxWidth(t *testing.T, output string, maxWidth int) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSuffix(output, "\n"), "\n") {
		if width := lipgloss.Width(line); width > maxWidth {
			t.Fatalf("line width = %d, want <= %d: %q", width, maxWidth, line)
		}
	}
}
