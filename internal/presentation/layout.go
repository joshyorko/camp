package presentation

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var ansiEscapePattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

func visibleWidth(s string) int {
	return utf8.RuneCountInString(ansiEscapePattern.ReplaceAllString(s, ""))
}

func padRight(s string, width int) string {
	if pad := width - visibleWidth(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func centerLine(s string, width int) string {
	pad := width - visibleWidth(s)
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad/2) + s
}

func horizontalRule(width int, glyph string) string {
	if width <= 0 {
		return ""
	}
	return strings.Repeat(glyph, width)
}

func evenlySpaced(width, count int) []int {
	if count <= 0 || width <= 0 {
		return nil
	}
	step := width / count
	offset := step / 2
	positions := make([]int, count)
	for i := 0; i < count; i++ {
		position := offset + i*step
		if position >= width {
			position = width - 1
		}
		positions[i] = position
	}
	return positions
}

func overlayGlyphs(width int, fill string, glyphs map[int]string) string {
	if width <= 0 {
		return ""
	}
	cells := make([]string, width)
	for i := range cells {
		cells[i] = fill
	}
	for index, glyph := range glyphs {
		if index >= 0 && index < width {
			cells[index] = glyph
		}
	}
	return strings.Join(cells, "")
}

func columnBlock(columns [][]string, gap int) []string {
	height := 0
	widths := make([]int, len(columns))
	for i, column := range columns {
		if len(column) > height {
			height = len(column)
		}
		for _, line := range column {
			if w := visibleWidth(line); w > widths[i] {
				widths[i] = w
			}
		}
	}
	spacer := strings.Repeat(" ", gap)
	rows := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		for i, column := range columns {
			if i > 0 {
				line.WriteString(spacer)
			}
			cell := ""
			if row < len(column) {
				cell = column[row]
			}
			line.WriteString(padRight(cell, widths[i]))
		}
		rows[row] = strings.TrimRight(line.String(), " ")
	}
	return rows
}

func columnBlockWidth(columns [][]string, gap int) int {
	total := 0
	for i, column := range columns {
		width := 0
		for _, line := range column {
			if w := visibleWidth(line); w > width {
				width = w
			}
		}
		total += width
		if i > 0 {
			total += gap
		}
	}
	return total
}

func distributeVertically(lines []string, height int) []string {
	if height <= len(lines) {
		return lines
	}
	extra := height - len(lines)
	top := extra / 2
	bottom := extra - top
	out := make([]string, 0, height)
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	out = append(out, lines...)
	for i := 0; i < bottom; i++ {
		out = append(out, "")
	}
	return out
}

func indentBlock(lines []string, margin int) []string {
	if margin <= 0 {
		return lines
	}
	prefix := strings.Repeat(" ", margin)
	out := make([]string, len(lines))
	for i, line := range lines {
		if line == "" {
			continue
		}
		out[i] = prefix + line
	}
	return out
}

func sceneContentWidth(terminalWidth, max int) int {
	width := terminalWidth - 4
	if width > max {
		return max
	}
	if width < 1 {
		return terminalWidth
	}
	return width
}
