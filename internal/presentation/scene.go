package presentation

import (
	"fmt"
	"strings"
)

const sceneMaxWidth = 96
const compactHeightThreshold = 20
const minCompactSceneHeight = 13
const maxMetadataCellWidth = 32

func truncateMetadataCell(value string) string {
	return truncateVisible(value, maxMetadataCellWidth)
}

func truncateVisible(value string, maxWidth int) string {
	if maxWidth <= 0 || visibleWidth(value) <= maxWidth {
		return value
	}
	if maxWidth == 1 {
		return "…"
	}
	limit := maxWidth - 1
	width := 0
	var out strings.Builder
	for _, character := range value {
		charWidth := runeCellWidth(character)
		if width+charWidth > limit {
			break
		}
		out.WriteRune(character)
		width += charWidth
	}
	return out.String() + "…"
}

type sceneFailure struct {
	Waypoint SetupWaypoint
	Message  string
	Recovery string
}

func composeScene(model CampsiteModel, statuses [4]WaypointStatus, size ScreenSize, ready bool, failure *sceneFailure) string {
	width := size.Width
	if width < 80 {
		width = 80
	}
	contentWidth := sceneContentWidth(width, sceneMaxWidth)
	margin := (width - contentWidth) / 2
	compact := size.Height > 0 && size.Height < compactHeightThreshold

	var lines []string
	lines = append(lines, "")
	lines = append(lines, centerLine(colorCanvas+"⛺ CAMP"+colorReset, contentWidth))
	lines = append(lines, centerLine(colorDim+"trailhead setup"+colorReset, contentWidth))
	lines = append(lines, "")
	if !compact {
		lines = append(lines, skyRow(contentWidth))
		lines = append(lines, topographyRow(contentWidth))
		lines = append(lines, "")
	}
	tableRows, usedTwoRowFallback := waypointTable(model, statuses, contentWidth)
	lines = append(lines, tableRows...)
	lines = append(lines, "")
	if !usedTwoRowFallback {
		lines = append(lines, routeRule(statuses, contentWidth))
		lines = append(lines, "")
	}
	switch {
	case failure != nil:
		lines = append(lines, failureBand(*failure, contentWidth)...)
	case ready:
		lines = append(lines, readyBand(model.NextCommand, contentWidth)...)
	}

	if size.Height > 0 {
		lines = distributeVertically(lines, size.Height)
	}
	lines = indentBlock(lines, margin)

	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

func skyRow(width int) string {
	glyphs := map[int]string{}
	for i, index := range evenlySpaced(width, 5) {
		glyph := "·"
		if i%2 == 1 {
			glyph = "✦"
		}
		glyphs[index] = glyph
	}
	return colorSky + overlayGlyphs(width, " ", glyphs) + colorReset
}

func topographyRow(width int) string {
	peaks := evenlySpaced(width, 5)
	tentIndex := peaks[len(peaks)/2]
	glyphs := map[int]string{}
	for _, index := range peaks {
		glyphs[index] = "▲"
	}
	line := overlayGlyphs(width, " ", glyphs)
	out := ""
	for i, char := range []rune(line) {
		switch {
		case i == tentIndex:
			out += colorCanvas + "▲" + colorReset
		case char == '▲':
			out += colorPine + "▲" + colorReset
		default:
			out += " "
		}
	}
	// Positions past the last peak are uncolored filler spaces; trim them so
	// this row never leaves literal trailing whitespace on the line.
	return strings.TrimRight(out, " ")
}

func waypointTable(model CampsiteModel, statuses [4]WaypointStatus, width int) ([]string, bool) {
	labels := []string{"TOOLCHAIN", "RUNTIME", "CAPSULE", "STORAGE"}
	metadata := [][]string{
		{fmt.Sprintf("%s %s", model.DevPod.Name, model.DevPod.Version), fmt.Sprintf("%s %s", model.Hauler.Name, model.Hauler.Version)},
		{fmt.Sprintf("%s · %s", model.Provider, model.RuntimeKind), "context " + model.Context},
		{model.Capsule, model.Source},
		{model.BackendKind + " backend", model.Storage},
	}
	columns := make([][]string, len(labels))
	for i, label := range labels {
		column := []string{statuses[i].paint(label)}
		for _, line := range metadata[i] {
			column = append(column, colorCanvas+truncateMetadataCell(line)+colorReset)
		}
		columns[i] = column
	}
	const gap = 3
	if tableWidth := columnBlockWidth(columns, gap); tableWidth <= width {
		rows := columnBlock(columns, gap)
		if tableWidth < width {
			rows = indentBlock(rows, (width-tableWidth)/2)
		}
		return rows, false
	}

	// The four waypoint columns don't fit side by side at this width (this
	// happens at the minimum supported 80-column terminal once real metadata
	// like a home-directory path or "no committed generation" is in play).
	// Fall back to a two-row, two-column grid rather than truncating or
	// overflowing the requested width.
	var rows []string
	groups := [][][]string{columns[:2], columns[2:]}
	groupStatuses := [2][2]WaypointStatus{{statuses[0], statuses[1]}, {statuses[2], statuses[3]}}
	groupRows := make([][]string, len(groups))
	maxGroupWidth := 0
	for i, group := range groups {
		groupRows[i] = columnBlock(group, gap)
		if w := columnBlockWidth(group, gap); w > maxGroupWidth {
			maxGroupWidth = w
		}
	}
	sharedMargin := 0
	if maxGroupWidth < width {
		sharedMargin = (width - maxGroupWidth) / 2
	}
	for i, rowsForGroup := range groupRows {
		if i > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, indentBlock(rowsForGroup, sharedMargin)...)
		rows = append(rows, indentBlock([]string{miniRouteRule(groupStatuses[i], i == len(groups)-1)}, sharedMargin)...)
	}
	return rows, true
}

func routeRule(statuses [4]WaypointStatus, width int) string {
	glyphs := map[int]string{}
	for i, index := range evenlySpaced(width, len(statuses)) {
		glyphs[index] = statuses[i].glyph()
	}
	line := overlayGlyphs(width, "─", glyphs)
	switch {
	case containsStatus(statuses, WaypointFailed):
		return colorRed + line + colorReset
	case statuses[len(statuses)-1] == WaypointCompleted:
		return colorAmber + line + "🔥" + colorReset
	default:
		return colorDim + line + colorReset
	}
}

func miniRouteRule(pair [2]WaypointStatus, isLastGroup bool) string {
	glyphs := map[int]string{0: pair[0].glyph(), 6: pair[1].glyph()}
	line := overlayGlyphs(7, "─", glyphs)
	switch {
	case pair[0] == WaypointFailed || pair[1] == WaypointFailed:
		return colorRed + line + colorReset
	case pair[0] == WaypointCompleted && pair[1] == WaypointCompleted:
		suffix := ""
		if isLastGroup {
			suffix = "🔥"
		}
		return colorAmber + line + suffix + colorReset
	default:
		return colorDim + line + colorReset
	}
}

func containsStatus(statuses [4]WaypointStatus, target WaypointStatus) bool {
	for _, status := range statuses {
		if status == target {
			return true
		}
	}
	return false
}

func readyBand(nextCommand string, width int) []string {
	return []string{
		centerLine(colorAmber+"CAMP IS READY"+colorReset, width),
		"",
		centerLine(colorBlue+truncateVisible("> "+nextCommand, width)+colorReset, width),
	}
}

func failureBand(failure sceneFailure, width int) []string {
	return []string{
		centerLine(colorRed+truncateVisible("stopped: "+failure.Message, width)+colorReset, width),
		centerLine(colorRed+truncateVisible("next: "+failure.Recovery, width)+colorReset, width),
	}
}

type ConfigureAnswer struct {
	Label string
	Value string
}

func ComposeConfigureFrame(answers []ConfigureAnswer, label, defaultValue string, size ScreenSize) string {
	width := size.Width
	if width < 80 {
		width = 80
	}
	contentWidth := sceneContentWidth(width, sceneMaxWidth)
	margin := (width - contentWidth) / 2

	var lines []string
	lines = append(lines, "")
	lines = append(lines, centerLine(colorCanvas+"⛺ CAMP"+colorReset, contentWidth))
	lines = append(lines, centerLine(colorDim+"first-run setup"+colorReset, contentWidth))
	lines = append(lines, "")
	lines = append(lines, skyRow(contentWidth))
	lines = append(lines, topographyRow(contentWidth))
	lines = append(lines, "")
	lines = append(lines, centerLine(colorAmber+"CONFIGURE"+colorReset, contentWidth))
	for _, answer := range answers {
		lines = append(lines, colorGreen+"✓ "+answer.Label+colorReset+colorCanvas+": "+answer.Value+colorReset)
	}
	lines = append(lines, colorAmber+"◐ "+label+colorReset+colorCanvas+" ["+defaultValue+"]: "+colorReset)

	return strings.Join(indentBlock(lines, margin), "\n")
}
