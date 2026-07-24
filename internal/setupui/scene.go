package setupui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Waypoint carries one setup stage's label and metadata lines, plus its state.
type Waypoint struct {
	Label string
	Meta  []string
	State WaypointState
	// Landmark is the sprite name anchored at this waypoint (crate, helm, tent,
	// campfire). Empty means no sprite.
	Landmark string
}

// SceneData is the authoritative content the scene renders. It is populated
// from Camp's real setup facts; the scene never invents values.
type SceneData struct {
	Title     string
	Subtitle  string
	Waypoints [4]Waypoint
	// Foreground is the interactive/progress content overlaid on the valley
	// (the config form, or a ready/failure band). May be empty.
	Foreground string
	// Ready and Failure gate the closing band.
	Ready       bool
	ReadyLine   string
	NextCommand string
	Failure     string
	Recovery    string
	HelpLine    string
}

// Layout describes the responsive geometry for a given terminal size.
type Layout struct {
	Compact  bool
	SkyRows  int
	Horizon  int
	ForestBand int
	TrailRow int
	TrailAmp float64
}

// layoutFor picks a composition tuned to the terminal size. There is no fixed
// column ceiling: the scene always uses the full width. Height decides how much
// vertical landscape (sky depth, forest band, valley) we can afford.
func layoutFor(w, h int) Layout {
	compact := h < 30
	sky := h * 30 / 100
	horizon := h * 55 / 100
	forest := h * 62 / 100
	trail := h * 70 / 100
	amp := 2.0
	if compact {
		sky = h * 22 / 100
		horizon = h * 46 / 100
		forest = h * 55 / 100
		trail = h * 62 / 100
		amp = 1.0
	}
	if w >= 150 {
		amp += 1.0 // wider scenes get a longer, more sinuous trail
	}
	return Layout{
		Compact: compact, SkyRows: sky, Horizon: horizon,
		ForestBand: forest, TrailRow: trail, TrailAmp: amp,
	}
}

// Compose builds the full-screen frame for the scene at the given size.
func Compose(data SceneData, w, h int, pal Palette, sprites map[string]Sprite) string {
	if w < 1 || h < 1 {
		return ""
	}
	c := NewCanvas(w, h, pal.Bg)
	lay := layoutFor(w, h)

	// --- Background layers (painter's order, back to front) ---
	NewStarfield(0xCA37).Paint(c, lay.SkyRows, pal)
	PaintMountains(c, lay.Horizon, pal)

	// Trail geometry + anchors first so forest/labels can avoid them.
	trail := NewTrail(w, lay.TrailRow, lay.TrailAmp)
	anchors := trail.Anchors(4, w)
	for i := range anchors {
		anchors[i].State = data.Waypoints[i].State
	}

	// Reserve columns near each landmark so the forest doesn't overgrow them.
	reserved := func(x int) bool {
		for _, a := range anchors {
			if x >= a.X-7 && x <= a.X+7 {
				return true
			}
		}
		return false
	}
	PaintForest(c, lay.ForestBand, sprites, pal, reserved)
	PaintContours(c, lay.TrailRow+2, pal)

	// Sunrise sprite in the upper-right, echoing the reference.
	if sun, ok := sprites["sun"]; ok && sun.Height() > 1 && !lay.Compact {
		c.DrawSprite(w-sun.Width()-2, max(1, lay.SkyRows-sun.Height()/2), sun, pal)
	}

	// Trail on top of the ground.
	trail.Paint(c, stateSlice(data.Waypoints), anchors, pal)

	// --- Landmarks + labels anchored to the trail ---
	placeLandmarks(c, data, anchors, sprites, pal, lay)

	// --- Title band (top center) ---
	paintTitle(c, data, pal)

	// --- Foreground overlay (form / ready / failure) in the valley ---
	frame := c.Render()
	frame = overlayForeground(frame, data, w, h, lay, pal)
	return frame
}

func stateSlice(ws [4]Waypoint) []WaypointState {
	s := make([]WaypointState, 4)
	for i := range ws {
		s[i] = ws[i].State
	}
	return s
}

func paintTitle(c *Canvas, data SceneData, pal Palette) {
	if data.Title != "" {
		t := data.Title
		x := (c.Width() - ansi.StringWidth(t)) / 2
		c.Text(x, 0, t, pal.Amber)
	}
	if data.Subtitle != "" {
		st := data.Subtitle
		x := (c.Width() - ansi.StringWidth(st)) / 2
		c.Text(x, 1, st, pal.Dim)
	}
}

// placeLandmarks draws each waypoint as a vertical callout stack sitting above
// its trail node — label, then metadata, then the landmark sprite resting on
// the path — matching the reference composition. The whole stack is measured
// bottom-up from one row above the node so nothing collides with the trail.
func placeLandmarks(c *Canvas, data SceneData, anchors []TrailPoint, sprites map[string]Sprite, pal Palette, lay Layout) {
	for i, a := range anchors {
		wp := data.Waypoints[i]
		sp, hasSprite := sprites[wp.Landmark]
		spriteH := 0
		if hasSprite && sp.Height() > 1 {
			spriteH = sp.Height()
		}
		metaCount := 0
		if !lay.Compact {
			metaCount = len(wp.Meta)
		}
		// Bottom of the sprite sits one row above the trail node.
		spriteBottom := a.Y - 1
		spriteTop := spriteBottom - spriteH + 1
		// Metadata lines stack directly above the sprite, label above those.
		metaTop := spriteTop - metaCount
		labelRow := metaTop - 1
		if labelRow < 2 {
			// Not enough vertical room: collapse toward the node.
			labelRow = 2
			metaTop = labelRow + 1
			spriteTop = metaTop + metaCount
		}

		if spriteH > 0 {
			sx := a.X - sp.Width()/2
			c.DrawSprite(sx, spriteTop, sp, pal)
		}
		if !lay.Compact {
			for j, m := range wp.Meta {
				mx := a.X - ansi.StringWidth(m)/2
				c.Text(mx, metaTop+j, m, pal.LabelMeta)
			}
		}
		label := stateBadge(wp.State) + " " + wp.Label
		lx := a.X - ansi.StringWidth(label)/2
		c.Text(lx, labelRow, label, stateColor(wp.State, pal))
	}
}

func stateColor(s WaypointState, pal Palette) color.Color {
	switch s {
	case WaypointCompleted:
		return pal.OK
	case WaypointActive:
		return pal.Active
	case WaypointFailed:
		return pal.Fail
	default:
		return pal.Pending
	}
}

func stateBadge(s WaypointState) string {
	switch s {
	case WaypointCompleted:
		return "✓"
	case WaypointActive:
		return "◈"
	case WaypointFailed:
		return "✗"
	default:
		return "○"
	}
}

// overlayForeground composites the interactive/closing content as a centered
// block over the valley portion of the frame, leaving the landscape visible
// around it. This keeps configuration and progress inside the same scene.
func overlayForeground(frame string, data SceneData, w, h int, lay Layout, pal Palette) string {
	var block string
	switch {
	case data.Failure != "":
		block = failureBlock(data, w, pal)
	case data.Ready:
		block = readyBlock(data, w, pal)
	case data.Foreground != "":
		block = data.Foreground
	}
	lines := strings.Split(frame, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	if block != "" {
		// Place the block centered in the lower valley region.
		blockLines := strings.Split(block, "\n")
		top := lay.TrailRow + 3
		if top+len(blockLines) > h {
			top = h - len(blockLines) - 1
		}
		if top < 0 {
			top = 0
		}
		lines = overlayBlock(lines, blockLines, top, w)
	}
	// Help line pinned to the last row.
	if data.HelpLine != "" && h > 0 {
		hl := lipgloss.NewStyle().Foreground(pal.Dim).Render(data.HelpLine)
		lines[h-1] = centerOnto(lines[h-1], hl, w)
	}
	return strings.Join(lines[:h], "\n")
}

// overlayBlock composites fg lines centered horizontally onto bg lines starting
// at row top, preserving the surrounding landscape on each affected row.
func overlayBlock(bg, fg []string, top, w int) []string {
	fgW := 0
	for _, l := range fg {
		if x := ansi.StringWidth(l); x > fgW {
			fgW = x
		}
	}
	left := (w - fgW) / 2
	if left < 0 {
		left = 0
	}
	for i, fl := range fg {
		row := top + i
		if row < 0 || row >= len(bg) {
			continue
		}
		bg[row] = spliceLine(bg[row], fl, left, fgW, w)
	}
	return bg
}

// spliceLine overwrites [left,left+fgW) of base with fg, padding as needed and
// keeping the base content on either side.
func spliceLine(base, fg string, left, fgW, w int) string {
	leftPart := ansi.Truncate(base, left, "")
	if lw := ansi.StringWidth(leftPart); lw < left {
		leftPart += strings.Repeat(" ", left-lw)
	}
	if fw := ansi.StringWidth(fg); fw < fgW {
		fg += strings.Repeat(" ", fgW-fw)
	}
	right := ansi.TruncateLeft(base, left+fgW, "")
	return leftPart + ansi.ResetStyle + fg + ansi.ResetStyle + right
}

func centerOnto(base, fg string, w int) string {
	fgW := ansi.StringWidth(fg)
	left := (w - fgW) / 2
	if left < 0 {
		left = 0
	}
	return spliceLine(base, fg, left, fgW, w)
}

func readyBlock(data SceneData, w int, pal Palette) string {
	title := lipgloss.NewStyle().Foreground(pal.Amber).Bold(true).Render("CAMP IS READY")
	rule := ruleAround(title, w*2/3, pal)
	sub := ""
	if data.ReadyLine != "" {
		sub = lipgloss.NewStyle().Foreground(pal.LabelMeta).Render(data.ReadyLine)
	}
	cmd := commandBox(data.NextCommand, pal)
	parts := []string{rule}
	if sub != "" {
		parts = append(parts, "", sub)
	}
	parts = append(parts, "", cmd)
	return lipgloss.JoinVertical(lipgloss.Center, parts...)
}

func failureBlock(data SceneData, w int, pal Palette) string {
	head := lipgloss.NewStyle().Foreground(pal.Fail).Bold(true).Render("SETUP STOPPED")
	msg := lipgloss.NewStyle().Foreground(pal.Fail).Render(data.Failure)
	rec := lipgloss.NewStyle().Foreground(pal.LabelMeta).Render("next: " + data.Recovery)
	return lipgloss.JoinVertical(lipgloss.Center, head, "", msg, "", rec)
}

func ruleAround(label string, width int, pal Palette) string {
	lw := ansi.StringWidth(label)
	if width < lw+6 {
		width = lw + 6
	}
	side := (width - lw - 2) / 2
	dash := lipgloss.NewStyle().Foreground(pal.Amber)
	seg := strings.Repeat("─", side)
	return dash.Render(seg) + " " + label + " " + dash.Render(seg)
}

func commandBox(cmd string, pal Palette) string {
	if cmd == "" {
		return ""
	}
	inner := "  " + lipgloss.NewStyle().Foreground(pal.Amber).Render("❯") + " " +
		lipgloss.NewStyle().Foreground(pal.Cmd).Render(cmd) + "  "
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pal.Box).
		Padding(0, 0)
	return box.Render(inner)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
