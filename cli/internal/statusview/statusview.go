// Package statusview renders the human-readable status surface.
package statusview

import (
	"cmp"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/diagnostics"
)

// Options describe terminal capabilities. A zero Width keeps all content,
// Color controls ANSI styling, and Hyperlinks controls terminal hyperlinks.
type Options struct {
	Width      int
	Color      bool
	Hyperlinks bool
}

// Render writes one complete human-readable status snapshot.
func Render(writer io.Writer, status core.Status, options Options) error {
	sections := []string{renderSummary(status, options)}
	listenersByPort := make(map[uint16]core.Listener, len(status.Listeners))
	forwardedPorts := make(map[uint16]struct{}, len(status.Forwards))
	for _, listener := range status.Listeners {
		listenersByPort[listener.Port] = listener
	}
	for _, forward := range status.Forwards {
		if hidesAvailableListener(forward) {
			forwardedPorts[forward.RemotePort] = struct{}{}
		}
	}

	for _, state := range []core.ForwardState{core.ForwardActive, core.ForwardStarting, core.ForwardFailed} {
		for _, published := range []bool{false, true} {
			var rows []core.ForwardStatus
			for _, forward := range status.Forwards {
				if forward.State == state && (forward.Direction == core.LocalToRemote) == published {
					rows = append(rows, forward)
				}
			}
			if len(rows) > 0 {
				sections = append(sections, renderForwardSection(rows, listenersByPort, state, published, options))
			}
		}
	}

	available := make([]core.Listener, 0, len(status.Listeners))
	for _, listener := range status.Listeners {
		if _, found := forwardedPorts[listener.Port]; !found {
			available = append(available, listener)
		}
	}
	if len(available) != 0 {
		sections = append(sections, renderAvailable(available, options))
	}
	if len(status.Forwards) == 0 && len(available) == 0 && status.Discovery.State == core.DiscoveryActive {
		sections = append(sections, "No loopback TCP listeners found.")
	}

	_, err := fmt.Fprintln(writer, strings.Join(sections, "\n\n"))
	return err
}

func renderSummary(status core.Status, options Options) string {
	summary := fmt.Sprintf("%s  %s    %s  %s", styled("Host", "1;94", options.Color),
		styled(string(status.Host), brightCyan, options.Color), styled("Discovery", "1;94", options.Color),
		styled(string(status.Discovery.State), stateColor(string(status.Discovery.State)), options.Color))
	if status.Discovery.Diagnostic != "" {
		detailLabel := "Discovery detail"
		detail := diagnostics.Text(status.Discovery.Diagnostic)
		detailLabel = styled(detailLabel, "1;"+red, options.Color)
		detail = styled(detail, red, options.Color)
		summary += "\n" + detailLabel + "  " + detail
	}
	return summary
}

func hidesAvailableListener(forward core.ForwardStatus) bool {
	return forward.Direction != core.LocalToRemote || forward.State == core.ForwardActive
}

func renderForwardSection(forwards []core.ForwardStatus, listeners map[uint16]core.Listener, state core.ForwardState, published bool, options Options) string {
	titles := map[core.ForwardState]string{core.ForwardActive: "FORWARDS", core.ForwardStarting: "STARTING", core.ForwardFailed: "NEEDS ATTENTION"}
	headers := []string{"REMOTE", "TARGET", "KIND"}
	if published {
		titles = map[core.ForwardState]string{core.ForwardActive: "PUBLISHED", core.ForwardStarting: "PUBLISHING", core.ForwardFailed: "PUBLISH NEEDS ATTENTION"}
		headers = []string{"LOCAL", "REMOTE TARGET", "KIND"}
	}
	switch {
	case state == core.ForwardFailed:
		headers = append(headers, "ISSUE")
	case !published:
		headers = append(headers, "APP", "WORKING DIRECTORY")
	}
	rows := make([][]string, 0, len(forwards))
	for _, forward := range forwards {
		row := []string{strconv.Itoa(int(forward.RemotePort)), forwardTarget(forward, options.Hyperlinks && state == core.ForwardActive), forwardKind(forward)}
		if published {
			row = []string{strconv.Itoa(int(forward.LocalPort)), publishedTarget(forward), "published"}
		}
		switch {
		case state == core.ForwardFailed:
			row = append(row, diagnostics.Text(forward.Diagnostic))
		case !published:
			listener := listeners[forward.RemotePort]
			row = append(row, cmp.Or(listener.App, "—"), cmp.Or(listener.WorkingDirectory, "—"))
		}
		rows = append(rows, row)
	}
	return renderSection(titles[state], headers, rows, stateColor(string(state)), options)
}

func forwardKind(forward core.ForwardStatus) string {
	if forward.Automatic {
		return "automatic"
	}
	return "remembered"
}

func renderAvailable(listeners []core.Listener, options Options) string {
	rows := make([][]string, 0, len(listeners))
	for _, listener := range listeners {
		rows = append(rows, []string{strconv.Itoa(int(listener.Port)), cmp.Or(listener.App, "—"), cmp.Or(listener.WorkingDirectory, "—")})
	}
	return renderSection("AVAILABLE", []string{"PORT", "APP", "WORKING DIRECTORY"}, rows, brightCyan, options)
}
