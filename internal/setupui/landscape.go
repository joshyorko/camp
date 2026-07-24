package setupui

import (
	"image/color"
	"math"
)

// ridge describes one mountain silhouette layer.
type ridge struct {
	baseFrac  float64 // vertical position of the ridge base as a fraction of scene height
	amplitude float64 // peak height in rows
	freq      float64 // horizontal wave count across the width
	phase     float64 // horizontal offset
	col       color.Color
	peakCol   color.Color
}

// PaintMountains draws several overlapping dashed ridgelines, farthest and
// bluest at the back, greener toward the front — matching the reference's
// layered topography. Ridges are computed from smooth pseudo-noise so they read
// as real mountains, not a row of triangles, and they span the full width.
func PaintMountains(c *Canvas, horizon int, pal Palette) {
	w, h := c.Width(), c.Height()
	if w == 0 || h == 0 {
		return
	}
	// Ridges are low-amplitude and stacked so they read as distant layered
	// mountains sitting in the sky band, not spikes. Back ridges are bluer and
	// taller; front ridges greener and lower, cresting just above the horizon.
	ridges := []ridge{
		{baseFrac: 0.34, amplitude: float64(h) * 0.14, freq: 1.6, phase: 0.6, col: pal.RidgeBlue},
		{baseFrac: 0.42, amplitude: float64(h) * 0.12, freq: 2.3, phase: 2.1, col: pal.RidgeTeal},
		{baseFrac: 0.50, amplitude: float64(h) * 0.10, freq: 3.1, phase: 4.0, col: pal.RidgeGreen},
	}
	for _, rg := range ridges {
		base := int(rg.baseFrac * float64(h))
		prev := -1
		for x := 0; x < w; x++ {
			t := float64(x) / float64(w)
			// Sum of sines → smooth, non-repeating ridgeline. Low harmonics
			// keep the crest rolling rather than jagged.
			n := math.Sin(t*rg.freq*2*math.Pi+rg.phase) +
				0.4*math.Sin(t*rg.freq*3.7*math.Pi+rg.phase*1.7) +
				0.15*math.Sin(t*rg.freq*6.3*math.Pi+rg.phase*0.5)
			n /= 1.55
			top := base - int(rg.amplitude*(0.5+0.5*n))
			if top < 2 {
				top = 2
			}
			// Ridge crest glyph reflects slope direction for a carved look.
			crest := '─'
			if prev >= 0 {
				switch {
				case top < prev:
					crest = '╱'
				case top > prev:
					crest = '╲'
				}
			}
			c.Set(x, top, crest, rg.col)
			// Sparse dashed contour ticks below each crest suggest elevation
			// lines without filling the mountain solid.
			for cy := top + 2; cy < base; cy += 3 {
				if (x+cy)%7 == 0 {
					c.Set(x, cy, '╌', pal.Contour)
				}
			}
			prev = top
		}
	}
}

// PaintContours lays faint dashed topographic lines across the foreground
// valley (below the trail), echoing the reference's contour map floor.
func PaintContours(c *Canvas, fromRow int, pal Palette) {
	w, h := c.Width(), c.Height()
	for y := fromRow; y < h; y++ {
		band := y - fromRow
		for x := 0; x < w; x++ {
			t := float64(x) / float64(w)
			n := math.Sin(t*3*math.Pi+float64(band)*0.6) + 0.4*math.Sin(t*7*math.Pi+float64(y))
			// Draw a dashed line only where the wave crosses a threshold, so
			// contours meander instead of marching straight.
			if math.Abs(n-math.Round(n)) < 0.08 && (x+y)%3 != 0 {
				c.Set(x, y, '╌', pal.Contour)
			}
		}
	}
}

// PaintForest scatters conifers along a band just above the trail. Sprites are
// placed at varied sizes and depths using a deterministic hash so the tree line
// reads as a forest, thinning where landmarks and labels sit.
func PaintForest(c *Canvas, band int, sprites map[string]Sprite, pal Palette, reserved func(x int) bool) {
	w := c.Width()
	pines := []string{"pine_large", "pine_med", "pine_small"}
	x := 0
	for x < w {
		h := hash(uint64(x), 7, 0xF0F0E57)
		if reserved != nil && reserved(x) {
			x += 3
			continue
		}
		// Space trees irregularly.
		gap := 3 + int(h%5)
		which := pines[h%uint64(len(pines))]
		sp, ok := sprites[which]
		if !ok || sp.Height() == 0 {
			x += gap
			continue
		}
		top := band - sp.Height()
		if top < 0 {
			top = 0
		}
		c.DrawSprite(x, top, sp, pal)
		x += sp.Width() + gap
	}
}
