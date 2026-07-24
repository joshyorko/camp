package setupui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// cell is one terminal character position in the scene grid.
type cell struct {
	r     rune
	fg    color.Color
	set   bool // a glyph has been painted here (transparent cells stay false)
}

// Canvas is a fixed-size grid of cells that layers paint onto using a
// painter's algorithm: later paints overwrite earlier ones, and transparent
// cells (a space in a sprite) leave whatever is underneath untouched. The
// whole scene is a single composited grid, so there is no clear-and-print
// loop and no visually detached fragment — every landmark, the trail, the
// labels, and the foreground share one buffer.
type Canvas struct {
	w, h  int
	cells []cell
	bg    color.Color
}

// NewCanvas returns a w×h canvas filled with the background color.
func NewCanvas(w, h int, bg color.Color) *Canvas {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	return &Canvas{w: w, h: h, cells: make([]cell, w*h), bg: bg}
}

func (c *Canvas) Width() int  { return c.w }
func (c *Canvas) Height() int { return c.h }

func (c *Canvas) inBounds(x, y int) bool {
	return x >= 0 && x < c.w && y >= 0 && y < c.h
}

// Set paints a single glyph. A space is treated as transparent so callers can
// pass sprite rows verbatim without erasing the background.
func (c *Canvas) Set(x, y int, r rune, fg color.Color) {
	if !c.inBounds(x, y) || r == ' ' || r == 0 {
		return
	}
	c.cells[y*c.w+x] = cell{r: r, fg: fg, set: true}
}

// SetOpaque paints a glyph including spaces (used to punch a solid background
// region, e.g. behind the sunrise or a command box interior).
func (c *Canvas) SetOpaque(x, y int, r rune, fg color.Color) {
	if !c.inBounds(x, y) {
		return
	}
	c.cells[y*c.w+x] = cell{r: r, fg: fg, set: true}
}

// Text paints a plain (already unstyled) string left-to-right from x,y in one
// color, honoring wide runes so alignment matches terminal cells.
func (c *Canvas) Text(x, y int, s string, fg color.Color) int {
	col := x
	for _, r := range s {
		w := runeWidth(r)
		c.Set(col, y, r, fg)
		// A wide glyph occupies two cells; blank the trailing cell so a later
		// paint in that column does not double up.
		if w == 2 && c.inBounds(col+1, y) {
			c.cells[y*c.w+col+1] = cell{}
		}
		col += w
	}
	return col
}

// DrawSprite composites a sprite with its top-left corner at (x,y).
func (c *Canvas) DrawSprite(x, y int, s Sprite, pal Palette) {
	for row := 0; row < len(s.Glyphs); row++ {
		grow := []rune(s.Glyphs[row])
		crow := []rune("")
		if row < len(s.Colors) {
			crow = []rune(s.Colors[row])
		}
		for col := 0; col < len(grow); col++ {
			g := grow[col]
			if g == ' ' || g == 0 {
				continue
			}
			name := "canvas"
			if col < len(crow) && crow[col] != ' ' {
				if n, ok := s.Legend[string(crow[col])]; ok {
					name = n
				}
			}
			c.Set(x+col, y+row, g, pal.Color(name))
		}
	}
}

// Render converts the grid to a string of rows joined by newlines. Consecutive
// cells sharing a foreground color emit a single SGR sequence, and trailing
// unset cells on a row are dropped so committed goldens carry no trailing
// whitespace. Background is applied per-row via the terminal's own default
// (the program sets the scene background through the alt-screen view).
func (c *Canvas) Render() string {
	var out strings.Builder
	for y := 0; y < c.h; y++ {
		out.WriteString(c.renderRow(y))
		if y < c.h-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

// lastSetCol returns the rightmost column on the row holding a glyph.
func (c *Canvas) lastSetCol(y int) int {
	last := -1
	for x := 0; x < c.w; x++ {
		if c.cells[y*c.w+x].set {
			last = x
		}
	}
	return last
}

// renderRow emits one row, coalescing adjacent cells that share a foreground
// color into a single styled span and dropping trailing empty cells so no
// committed golden carries trailing whitespace.
func (c *Canvas) renderRow(y int) string {
	last := c.lastSetCol(y)
	if last < 0 {
		return ""
	}
	var b strings.Builder
	x := 0
	for x <= last {
		cl := c.cells[y*c.w+x]
		if !cl.set {
			// Transparent gap: a plain background space, no styling.
			b.WriteByte(' ')
			x++
			continue
		}
		// Gather the maximal run of set cells sharing this foreground color.
		var run strings.Builder
		fg := cl.fg
		for x <= last {
			nc := c.cells[y*c.w+x]
			if !nc.set || !sameColor(nc.fg, fg) {
				break
			}
			run.WriteRune(nc.r)
			x++
		}
		b.WriteString(lipgloss.NewStyle().Foreground(fg).Render(run.String()))
	}
	return b.String()
}

func sameColor(a, b color.Color) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func runeWidth(r rune) int {
	return ansi.StringWidth(string(r))
}
