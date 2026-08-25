// Package statusview renders the human-readable status surface.
package statusview

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/diagnostics"
)

const (
	portHeader             = "PORT"
	remotePortHeader       = "REMOTE"
	localPortHeader        = "LOCAL"
	remoteTargetHeader     = "REMOTE TARGET"
	targetHeader           = "TARGET"
	appHeader              = "APP"
	kindHeader             = "KIND"
	workingDirectoryHeader = "WORKING DIRECTORY"
	issueHeader            = "ISSUE"
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
		rows := forwardsInState(status.Forwards, state, core.RemoteToLocal)
		if len(rows) != 0 {
			sections = append(sections, renderForwards(rows, listenersByPort, state, options))
		}
		published := forwardsInState(status.Forwards, state, core.LocalToRemote)
		if len(published) != 0 {
			sections = append(sections, renderPublished(published, state, options))
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
	hostLabel := "Host"
	discoveryLabel := "Discovery"
	host := string(status.Host)
	state := string(status.Discovery.State)
	if options.Color {
		labelStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightBlue)
		hostLabel = labelStyle.Render(hostLabel)
		discoveryLabel = labelStyle.Render(discoveryLabel)
		host = lipgloss.NewStyle().Foreground(lipgloss.BrightCyan).Render(host)
		state = lipgloss.NewStyle().Foreground(discoveryColor(status.Discovery.State)).Render(state)
	}
	summary := fmt.Sprintf("%s  %s    %s  %s", hostLabel, host, discoveryLabel, state)
	if status.Discovery.Diagnostic != "" {
		detailLabel := "Discovery detail"
		detail := diagnostics.Text(status.Discovery.Diagnostic)
		if options.Color {
			detailLabel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightRed).Render(detailLabel)
			detail = lipgloss.NewStyle().Foreground(lipgloss.BrightRed).Render(detail)
		}
		summary += "\n" + detailLabel + "  " + detail
	}
	return summary
}

func forwardsInState(
	forwards []core.ForwardStatus,
	state core.ForwardState,
	direction core.ForwardDirection,
) []core.ForwardStatus {
	rows := make([]core.ForwardStatus, 0, len(forwards))
	for _, forward := range forwards {
		if forward.State == state && effectiveForwardDirection(forward) == direction {
			rows = append(rows, forward)
		}
	}
	return rows
}

func effectiveForwardDirection(forward core.ForwardStatus) core.ForwardDirection {
	if forward.Direction == "" {
		return core.RemoteToLocal
	}
	return forward.Direction
}

func hidesAvailableListener(forward core.ForwardStatus) bool {
	return effectiveForwardDirection(forward) == core.RemoteToLocal || forward.State == core.ForwardActive
}

func renderPublished(forwards []core.ForwardStatus, state core.ForwardState, options Options) string {
	rows := make([][]string, 0, len(forwards))
	for _, forward := range forwards {
		row := []string{
			strconv.Itoa(int(forward.LocalPort)),
			publishedTarget(forward),
			"published",
		}
		if state == core.ForwardFailed {
			row = append(row, diagnostics.Text(forward.Diagnostic))
		}
		rows = append(rows, row)
	}
	title := "PUBLISHED"
	headers := []string{localPortHeader, remoteTargetHeader, kindHeader}
	switch state {
	case core.ForwardStarting:
		title = "PUBLISHING"
	case core.ForwardFailed:
		title = "PUBLISH NEEDS ATTENTION"
		headers = append(headers, issueHeader)
	}
	return renderSection(title, headers, rows, stateColor(state), options)
}

func renderForwards(
	forwards []core.ForwardStatus,
	listeners map[uint16]core.Listener,
	state core.ForwardState,
	options Options,
) string {
	if state == core.ForwardFailed {
		rows := make([][]string, 0, len(forwards))
		for _, forward := range forwards {
			rows = append(rows, []string{
				strconv.Itoa(int(forward.RemotePort)),
				forwardTarget(forward, false),
				forwardKind(forward),
				diagnostics.Text(forward.Diagnostic),
			})
		}
		return renderSection(
			"NEEDS ATTENTION",
			[]string{remotePortHeader, targetHeader, kindHeader, issueHeader},
			rows,
			stateColor(state),
			options,
		)
	}

	rows := make([][]string, 0, len(forwards))
	for _, forward := range forwards {
		listener := listeners[forward.RemotePort]
		rows = append(rows, []string{
			strconv.Itoa(int(forward.RemotePort)),
			forwardTarget(forward, options.Hyperlinks && state == core.ForwardActive),
			forwardKind(forward),
			valueOrMissing(listener.App),
			valueOrMissing(listener.WorkingDirectory),
		})
	}
	title := "FORWARDS"
	if state == core.ForwardStarting {
		title = "STARTING"
	}
	return renderSection(
		title,
		[]string{remotePortHeader, targetHeader, kindHeader, appHeader, workingDirectoryHeader},
		rows,
		stateColor(state),
		options,
	)
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
		rows = append(rows, []string{
			strconv.Itoa(int(listener.Port)),
			valueOrMissing(listener.App),
			valueOrMissing(listener.WorkingDirectory),
		})
	}
	return renderSection(
		"AVAILABLE",
		[]string{portHeader, appHeader, workingDirectoryHeader},
		rows,
		lipgloss.BrightCyan,
		options,
	)
}
