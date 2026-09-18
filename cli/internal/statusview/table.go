package statusview

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	columnGap  = 2
	portWidth  = 5
	green      = "92"
	yellow     = "93"
	red        = "91"
	cyan       = "36"
	brightCyan = "96"
	gray       = "90"
	magenta    = "95"
)

func styled(text, code string, enabled bool) string {
	if !enabled || code == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// Rows are built for this render; fitting them in place needs no defensive
// copies. ANSI-aware widths also preserve hyperlinks and wide graphemes.
func renderSection(title string, headers []string, rows [][]string, accent string, options Options) string {
	columns := append([][]string{headers}, rows...)
	widths := make([]int, len(headers))
	widths[0] = portWidth
	for _, row := range columns {
		for col, cell := range row {
			widths[col] = max(widths[col], ansi.StringWidth(cell))
		}
	}
	cwd := slices.Index(headers, "WORKING DIRECTORY")
	if cwd >= 0 && options.Width > 0 {
		fixed := columnGap * (len(headers) - 1)
		for col, width := range widths {
			if col != cwd {
				fixed += width
			}
		}
		widths[cwd] = min(widths[cwd], max(options.Width-fixed, 1))
		if ansi.StringWidth(headers[cwd]) > widths[cwd] {
			headers[cwd] = "CWD"
		}
		for _, row := range columns {
			row[cwd] = shortenTail(row[cwd], widths[cwd])
		}
	}
	colors := map[string]string{"TARGET": cyan, "APP": magenta, "ISSUE": red, "WORKING DIRECTORY": gray}
	lines := []string{styled(title, "1;"+accent, options.Color)}
	for index, row := range columns {
		var line strings.Builder
		for col, cell := range row {
			padding := strings.Repeat(" ", max(widths[col]-ansi.StringWidth(cell), 0))
			code := colors[headers[col]]
			if col == cwd {
				code = gray
			}
			if col == 0 {
				code = accent
				line.WriteString(padding)
			}
			if index == 0 {
				code = "1;" + gray
			}
			line.WriteString(styled(cell, code, options.Color))
			if col < len(headers)-1 {
				if col > 0 {
					line.WriteString(padding)
				}
				line.WriteString("  ")
			}
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}

func shortenTail(value string, width int) string {
	total := ansi.StringWidth(value)
	if total <= width {
		return value
	}
	if width <= 1 {
		return "…"
	}
	// A cut through a wide grapheme retains it. Advance until the tail fits.
	for cut := total - width + 1; ; cut++ {
		tail := ansi.TruncateLeft(value, cut, "")
		if ansi.StringWidth(tail) < width {
			return "…" + tail
		}
	}
}
