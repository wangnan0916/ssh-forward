package statusview

import (
	"image/color"
	"strconv"

	"charm.land/lipgloss/v2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const missing = "—"

func loopbackTarget(port uint16, hyperlink bool) string {
	target := "127.0.0.1:" + strconv.Itoa(int(port))
	if !hyperlink {
		return target
	}
	return "\x1b]8;;http://" + target + "\x1b\\" + target + "\x1b]8;;\x1b\\"
}

func forwardTarget(forward core.ForwardStatus, hyperlink bool) string {
	return targetWithPreferred(forward.LocalPort, forward.PreferredLocalPort, hyperlink)
}

func publishedTarget(forward core.ForwardStatus) string {
	return targetWithPreferred(forward.RemotePort, forward.PreferredRemotePort, false)
}

func targetWithPreferred(port, preferred uint16, hyperlink bool) string {
	target := loopbackTarget(port, hyperlink)
	if preferred != 0 && preferred != port {
		target += " (preferred " + strconv.Itoa(int(preferred)) + ")"
	}
	return target
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
