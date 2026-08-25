package statusview

import (
	"image/color"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

const (
	columnGap = 2
	portWidth = 5
)

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
			width := max(portWidth, lipgloss.Width(headers[0]))
			style = style.Width(width + columnGap).Align(lipgloss.Right)
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
