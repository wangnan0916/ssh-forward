// Package statusview renders the human-readable status surface.
package statusview

import (
	"fmt"
	"image/color"
	"io"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	columnGap              = 2
	portWidth              = 5
	missing                = "—"
	portHeader             = "PORT"
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
		forwardedPorts[forward.Port] = struct{}{}
	}

	for _, state := range []core.ForwardState{core.ForwardActive, core.ForwardStarting, core.ForwardFailed} {
		rows := forwardsInState(status.Forwards, state)
		if len(rows) != 0 {
			sections = append(sections, renderForwards(rows, listenersByPort, state, options))
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
		detail := diagnosticText(status.Discovery.Diagnostic)
		if options.Color {
			detailLabel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.BrightRed).Render(detailLabel)
			detail = lipgloss.NewStyle().Foreground(lipgloss.BrightRed).Render(detail)
		}
		summary += "\n" + detailLabel + "  " + detail
	}
	return summary
}

func forwardsInState(forwards []core.ForwardStatus, state core.ForwardState) []core.ForwardStatus {
	rows := make([]core.ForwardStatus, 0, len(forwards))
	for _, forward := range forwards {
		if forward.State == state {
			rows = append(rows, forward)
		}
	}
	return rows
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
				strconv.Itoa(int(forward.Port)),
				localTarget(forward.Port, false),
				forwardKind(forward),
				diagnosticText(forward.Diagnostic),
			})
		}
		return renderSection(
			"NEEDS ATTENTION",
			[]string{portHeader, targetHeader, kindHeader, issueHeader},
			rows,
			stateColor(state),
			options,
		)
	}

	rows := make([][]string, 0, len(forwards))
	for _, forward := range forwards {
		listener := listeners[forward.Port]
		rows = append(rows, []string{
			strconv.Itoa(int(forward.Port)),
			localTarget(forward.Port, options.Hyperlinks && state == core.ForwardActive),
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
		[]string{portHeader, targetHeader, kindHeader, appHeader, workingDirectoryHeader},
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

func renderSection(
	title string,
	headers []string,
	rows [][]string,
	accent color.Color,
	options Options,
) string {
	workingDirectoryColumn := slices.Index(headers, workingDirectoryHeader)
	if workingDirectoryColumn >= 0 && options.Width > 0 {
		headers, rows = fitWorkingDirectories(headers, rows, workingDirectoryColumn, options.Width)
	}

	titleStyle := lipgloss.NewStyle()
	if options.Color {
		titleStyle = titleStyle.Bold(true).Foreground(accent)
	}

	view := table.New().
		Headers(headers...).
		Rows(rows...).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderHeader(false).
		BorderColumn(false).
		BorderRow(false).
		Wrap(false).
		StyleFunc(tableStyle(options.Color, accent, headers, workingDirectoryColumn))
	return titleStyle.Render(title) + "\n" + trimLinePadding(view.Render())
}

func tableStyle(colored bool, accent color.Color, headers []string, workingDirectoryColumn int) table.StyleFunc {
	return func(row, column int) lipgloss.Style {
		style := lipgloss.NewStyle()
		if column == 0 {
			style = style.Width(portWidth + columnGap).Align(lipgloss.Right)
		}
		if column < len(headers)-1 {
			style = style.PaddingRight(columnGap)
		}
		if !colored {
			return style
		}
		if row == table.HeaderRow {
			return style.Bold(true).Foreground(lipgloss.BrightBlack)
		}
		if column == 0 {
			return style.Foreground(accent)
		}
		if column == workingDirectoryColumn {
			return style.Foreground(lipgloss.BrightBlack)
		}
		switch headers[column] {
		case targetHeader:
			return style.Foreground(lipgloss.Cyan)
		case appHeader:
			return style.Foreground(lipgloss.BrightMagenta)
		case issueHeader:
			return style.Foreground(lipgloss.BrightRed)
		}
		return style
	}
}

func trimLinePadding(value string) string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

func fitWorkingDirectories(
	headers []string,
	rows [][]string,
	workingDirectoryColumn int,
	width int,
) ([]string, [][]string) {
	headers = slices.Clone(headers)
	rows = cloneRows(rows)
	columnWidths := contentWidths(headers, rows)
	columnWidths[0] = max(columnWidths[0], portWidth)
	fixedWidth := columnGap * (len(headers) - 1)
	for column, columnWidth := range columnWidths {
		if column != workingDirectoryColumn {
			fixedWidth += columnWidth
		}
	}
	workingDirectoryWidth := max(width-fixedWidth, 1)
	if lipgloss.Width(headers[workingDirectoryColumn]) > workingDirectoryWidth {
		headers[workingDirectoryColumn] = shortenTail("CWD", workingDirectoryWidth)
	}
	for _, row := range rows {
		row[workingDirectoryColumn] = shortenPath(row[workingDirectoryColumn], workingDirectoryWidth)
	}
	return headers, rows
}

func cloneRows(rows [][]string) [][]string {
	cloned := make([][]string, len(rows))
	for index, row := range rows {
		cloned[index] = slices.Clone(row)
	}
	return cloned
}

func contentWidths(headers []string, rows [][]string) []int {
	widths := make([]int, len(headers))
	for column, header := range headers {
		widths[column] = lipgloss.Width(header)
	}
	for _, row := range rows {
		for column, cell := range row {
			widths[column] = max(widths[column], lipgloss.Width(cell))
		}
	}
	return widths
}

func shortenPath(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	return shortenTail(value, width)
}

func shortenTail(value string, width int) string {
	if width <= 1 {
		return "…"
	}
	runes := []rune(value)
	for start := 0; start < len(runes); start++ {
		candidate := "…" + string(runes[start:])
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
	}
	return "…"
}

func localTarget(port uint16, hyperlink bool) string {
	target := "127.0.0.1:" + strconv.Itoa(int(port))
	if !hyperlink {
		return target
	}
	return "\x1b]8;;http://" + target + "\x1b\\" + target + "\x1b]8;;\x1b\\"
}

func valueOrMissing(value string) string {
	if value == "" {
		return missing
	}
	return value
}

func discoveryColor(state core.DiscoveryState) color.Color {
	switch state {
	case core.DiscoveryActive:
		return lipgloss.BrightGreen
	case core.DiscoveryFailed:
		return lipgloss.BrightRed
	default:
		return lipgloss.BrightYellow
	}
}

func stateColor(state core.ForwardState) color.Color {
	switch state {
	case core.ForwardActive:
		return lipgloss.BrightGreen
	case core.ForwardFailed:
		return lipgloss.BrightRed
	default:
		return lipgloss.BrightYellow
	}
}

func diagnosticText(diagnostic string) string {
	switch diagnostic {
	case "invalid_alias":
		return "SSH does not know this host alias."
	case "authentication_failed":
		return "SSH authentication failed."
	case "host_key_failed":
		return "SSH host key verification failed."
	case "local_port_conflict":
		return "the same local port is already in use"
	case "transport_unavailable":
		return "SSH connection unavailable"
	case "discovery_invalid":
		return "the remote listener scan returned invalid data"
	case "forward_start_timeout":
		return "OpenSSH did not open the local port in time"
	default:
		return diagnostic
	}
}
