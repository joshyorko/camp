package setupui

import (
	"image/color"
	"math"
)

// WaypointState mirrors the four setup stages' lifecycle states.
type WaypointState int

const (
	WaypointPending WaypointState = iota
	WaypointActive
	WaypointCompleted
	WaypointFailed
)

// TrailPoint is a landmark anchor on the trail: the (x,y) cell where its
// waypoint node sits, so labels and landmark sprites can align to the path.
type TrailPoint struct {
	X, Y  int
	State WaypointState
}

// Trail computes a smooth winding path across the scene at a given base row and
// returns the y for each column plus the node anchor points where the four
// waypoints attach. The path uses a low-frequency sine so it gently rises and
// falls like the reference's glowing trail rather than a straight separator.
type Trail struct {
	baseRow int
	amp     float64
	ys      []int
}

// NewTrail builds the path geometry for a scene of the given width.
func NewTrail(width, baseRow int, amp float64) Trail {
	ys := make([]int, width)
	for x := 0; x < width; x++ {
		t := float64(x) / math.Max(1, float64(width))
		wave := math.Sin(t*2.3*math.Pi+0.4) + 0.35*math.Sin(t*5.1*math.Pi)
		ys[x] = baseRow + int(math.Round(amp*wave))
	}
	return Trail{baseRow: baseRow, amp: amp, ys: ys}
}

// YAt returns the trail row at column x (clamped).
func (t Trail) YAt(x int) int {
	if len(t.ys) == 0 {
		return t.baseRow
	}
	if x < 0 {
		x = 0
	}
	if x >= len(t.ys) {
		x = len(t.ys) - 1
	}
	return t.ys[x]
}

// Anchors returns n evenly spaced anchor columns and their trail rows, for
// attaching landmarks/labels to the path.
func (t Trail) Anchors(n, width int) []TrailPoint {
	pts := make([]TrailPoint, n)
	for i := 0; i < n; i++ {
		// Distribute anchors with margins so the first/last aren't at the edge.
		x := int(float64(width) * (float64(i)+0.5) / float64(n))
		pts[i] = TrailPoint{X: x, Y: t.YAt(x)}
	}
	return pts
}

// Paint draws the beaded trail. Segments up to the active waypoint glow amber
// with a bright core bead every few cells; segments beyond are dim. A failed
// run tints the whole trail toward the failure color at/after the failed node.
func (t Trail) Paint(c *Canvas, states []WaypointState, anchors []TrailPoint, pal Palette) {
	w := c.Width()
	if w == 0 {
		return
	}
	// Determine how far the "lit" portion extends: up to and including the
	// active/last-completed anchor.
	litUntilX := 0
	failed := -1
	for _, a := range anchors {
		if a.State == WaypointFailed {
			failed = a.X
		}
		if a.State == WaypointCompleted || a.State == WaypointActive {
			litUntilX = a.X
		}
	}
	if litUntilX == 0 && len(anchors) > 0 {
		litUntilX = anchors[0].X
	}

	prevY := t.YAt(0)
	for x := 0; x < w; x++ {
		y := t.YAt(x)
		// Connect vertical jumps so the path is continuous.
		lo, hi := y, prevY
		if lo > hi {
			lo, hi = hi, lo
		}
		for yy := lo; yy <= hi; yy++ {
			glyph := trailGlyph(x, y, prevY)
			col := pal.TrailDim
			if failed >= 0 && x >= failed {
				// Beyond a failed node the trail is simply unlit; only the
				// failed node itself (and the break beside it) reads as red so
				// pending stages are not painted as failures.
				if x <= failed+2 {
					col = pal.Fail
				}
			} else if x <= litUntilX {
				// Bright beads punctuate the lit trail.
				if x%3 == 0 {
					col = pal.TrailCore
				} else {
					col = pal.TrailGlow
				}
			}
			if yy == y {
				c.Set(x, yy, glyph, col)
			} else {
				c.Set(x, yy, '│', col)
			}
		}
		prevY = y
	}

	// Draw waypoint nodes on top.
	for _, a := range anchors {
		c.Set(a.X, a.Y, nodeGlyph(a.State), nodeColor(a.State, pal))
	}
}

func trailGlyph(x, y, prevY int) rune {
	switch {
	case y < prevY:
		return '╱'
	case y > prevY:
		return '╲'
	default:
		if x%2 == 0 {
			return '━'
		}
		return '─'
	}
}

func nodeGlyph(s WaypointState) rune {
	switch s {
	case WaypointCompleted:
		return '◉'
	case WaypointActive:
		return '◈'
	case WaypointFailed:
		return '✗'
	default:
		return '○'
	}
}

func nodeColor(s WaypointState, pal Palette) color.Color {
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
