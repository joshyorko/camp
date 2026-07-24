package setupui

import (
	"image/color"
	"math"
)

// Braille dot bit positions (Unicode U+2800). Each cell is 2 dots wide and 4
// dots tall. Adapted from Basecamp/ONCE internal/ui/starfield.go.
var leftDots = [4]rune{0x01, 0x02, 0x04, 0x40}
var rightDots = [4]rune{0x08, 0x10, 0x20, 0x80}

// Starfield paints a deterministic field of stars into the upper band of the
// scene. Unlike ONCE's animated warp field, Camp's stars are static: the setup
// scene advances only on real events, never on a timer, so the sky is stable
// and reduced-motion safe. Density and brightness vary with a hashed position
// to avoid a mechanical grid.
type Starfield struct {
	seed uint64
}

func NewStarfield(seed uint64) Starfield { return Starfield{seed: seed} }

// hash is a small deterministic mixer so star placement is stable per size and
// seed (no Math.random, which keeps captures/goldens reproducible).
func hash(x, y, seed uint64) uint64 {
	h := x*0x9E3779B97F4A7C15 ^ y*0xC2B2AE3D27D4EB4F ^ seed*0x165667B19E3779F9
	h ^= h >> 33
	h *= 0xFF51AFD7ED558CCD
	h ^= h >> 33
	return h
}

// Paint scatters stars across rows [0,skyRows) of the canvas. Brighter, warmer
// stars are sparse; dim ones are common, thinning toward the horizon so the
// ridges read against a darker sky.
func (s Starfield) Paint(c *Canvas, skyRows int, pal Palette) {
	if skyRows <= 0 {
		return
	}
	for y := 0; y < skyRows && y < c.Height(); y++ {
		// Fewer stars near the horizon (larger y within the sky band).
		depth := float64(y) / float64(skyRows)
		for x := 0; x < c.Width(); x++ {
			h := hash(uint64(x), uint64(y), s.seed)
			roll := float64(h%1000) / 1000.0
			threshold := 0.06 * (1.0 - 0.6*depth)
			if roll >= threshold {
				continue
			}
			glyph, col := s.starStyle(h, pal)
			c.Set(x, y, glyph, col)
		}
	}
}

func (s Starfield) starStyle(h uint64, pal Palette) (rune, color.Color) {
	switch h >> 60 & 0x7 {
	case 0:
		return '✦', pal.StarBright
	case 1:
		return '✧', pal.StarWarm
	case 2, 3:
		return '·', pal.StarMid
	default:
		return '·', pal.StarDim
	}
}

// clampf keeps a float in [lo,hi].
func clampf(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
