package statusview

import (
	"image/color"
	"strconv"

	"charm.land/lipgloss/v2"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const missing = "—"

func localTarget(address string, port uint16, hyperlink bool) string {
	target := address + ":" + strconv.Itoa(int(port))
	if !hyperlink {
		return target
	}
	url := "127.0.0.1:" + strconv.Itoa(int(port))
	return "\x1b]8;;http://" + url + "\x1b\\" + target + "\x1b]8;;\x1b\\"
}

func forwardTarget(forward core.ForwardStatus, hyperlink bool) string {
	return targetWithPreferred("0.0.0.0", forward.LocalPort, forward.PreferredLocalPort, hyperlink)
}

func publishedTarget(forward core.ForwardStatus) string {
	return targetWithPreferred("127.0.0.1", forward.RemotePort, forward.PreferredRemotePort, false)
}

func targetWithPreferred(address string, port, preferred uint16, hyperlink bool) string {
	target := localTarget(address, port, hyperlink)
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
