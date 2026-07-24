package setupui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Palette holds the semantic colors of the Trailhead scene. Values mirror
// tools/ansishot/assets/palette.json so a Go-composited frame and a Python
// ansishot capture of the same scene are pixel-comparable. Every color is a
// truecolor RGB value; rich mode is only entered on truecolor terminals, and
// lipgloss downsamples when a lower color profile is active.
type Palette struct {
	Bg        color.Color
	Sky       [3]color.Color
	StarDim   color.Color
	StarMid   color.Color
	StarBright color.Color
	StarWarm  color.Color

	RidgeBlue  color.Color
	RidgeTeal  color.Color
	RidgeGreen color.Color
	Contour    color.Color

	PineLight color.Color
	PineMid   color.Color
	PineDark  color.Color
	Trunk     color.Color

	TrailCore color.Color
	TrailGlow color.Color
	TrailDim  color.Color

	CrateLight color.Color
	CrateMid   color.Color
	CrateDark  color.Color
	Helm       color.Color
	HelmDark   color.Color
	TentLight  color.Color
	TentMid    color.Color
	TentDark   color.Color
	TentPole   color.Color
	FireYellow color.Color
	FireOrange color.Color
	FireRed    color.Color
	FireLog    color.Color
	SunCore    color.Color
	SunRay     color.Color

	LabelHead color.Color
	LabelMeta color.Color

	OK      color.Color
	Active  color.Color
	Pending color.Color
	Fail    color.Color
	Amber   color.Color
	Cmd     color.Color
	Box     color.Color
	Dim     color.Color
	Canvas  color.Color

	// byName resolves a sprite legend color name to a concrete color so the
	// Go compositor and the Python sprite tooling share one vocabulary.
	byName map[string]color.Color
}

func hex(s string) color.Color { return lipgloss.Color(s) }

// DefaultPalette returns the canonical Trailhead palette.
func DefaultPalette() Palette {
	p := Palette{
		Bg:         hex("#0a0e1a"),
		Sky:        [3]color.Color{hex("#05070f"), hex("#0a0e1a"), hex("#0d1526")},
		StarDim:    hex("#5a6a88"),
		StarMid:    hex("#8fa0c8"),
		StarBright: hex("#d7e0f7"),
		StarWarm:   hex("#c9a06a"),
		RidgeBlue:  hex("#34517f"),
		RidgeTeal:  hex("#2f6a63"),
		RidgeGreen: hex("#386e46"),
		Contour:    hex("#2c5647"),
		PineLight:  hex("#4c8a5a"),
		PineMid:    hex("#3a6e46"),
		PineDark:   hex("#274f38"),
		Trunk:      hex("#6e4a2a"),
		TrailCore:  hex("#ffe3a0"),
		TrailGlow:  hex("#ffab2d"),
		TrailDim:   hex("#9c6a18"),
		CrateLight: hex("#9aa0aa"),
		CrateMid:   hex("#6a7078"),
		CrateDark:  hex("#3a3f48"),
		Helm:       hex("#4a9be8"),
		HelmDark:   hex("#2f6bb0"),
		TentLight:  hex("#e3d3a3"),
		TentMid:    hex("#b6a179"),
		TentDark:   hex("#6f6047"),
		TentPole:   hex("#d9c8a0"),
		FireYellow: hex("#ffd166"),
		FireOrange: hex("#ff7a1f"),
		FireRed:    hex("#e8451f"),
		FireLog:    hex("#7a4a2a"),
		SunCore:    hex("#ffd166"),
		SunRay:     hex("#ff6a2b"),
		LabelHead:  hex("#4aa3ff"),
		LabelMeta:  hex("#eed7a9"),
		OK:         hex("#66d656"),
		Active:     hex("#ffab2d"),
		Pending:    hex("#6e7681"),
		Fail:       hex("#e94d4d"),
		Amber:      hex("#ffab2d"),
		Cmd:        hex("#66d656"),
		Box:        hex("#ffab2d"),
		Dim:        hex("#6e7681"),
		Canvas:     hex("#eed7a9"),
	}
	p.byName = map[string]color.Color{
		"bg": p.Bg, "sky0": p.Sky[0], "sky1": p.Sky[1], "sky2": p.Sky[2],
		"star_dim": p.StarDim, "star_mid": p.StarMid, "star_bright": p.StarBright, "star_warm": p.StarWarm,
		"ridge_blue": p.RidgeBlue, "ridge_teal": p.RidgeTeal, "ridge_green": p.RidgeGreen, "contour": p.Contour,
		"pine_light": p.PineLight, "pine_mid": p.PineMid, "pine_dark": p.PineDark, "trunk": p.Trunk,
		"trail_core": p.TrailCore, "trail_glow": p.TrailGlow, "trail_dim": p.TrailDim,
		"crate_light": p.CrateLight, "crate_mid": p.CrateMid, "crate_dark": p.CrateDark,
		"helm": p.Helm, "helm_dark": p.HelmDark,
		"tent_light": p.TentLight, "tent_mid": p.TentMid, "tent_dark": p.TentDark, "tent_pole": p.TentPole,
		"fire_yellow": p.FireYellow, "fire_orange": p.FireOrange, "fire_red": p.FireRed, "fire_log": p.FireLog,
		"sun_core": p.SunCore, "sun_ray": p.SunRay,
		"label_head": p.LabelHead, "label_meta": p.LabelMeta,
		"ok": p.OK, "active": p.Active, "pending": p.Pending, "fail": p.Fail,
		"amber": p.Amber, "cmd": p.Cmd, "box": p.Box, "dim": p.Dim, "canvas": p.Canvas,
	}
	return p
}

// Color resolves a sprite legend color name; unknown names fall back to canvas.
func (p Palette) Color(name string) color.Color {
	if c, ok := p.byName[name]; ok {
		return c
	}
	return p.Canvas
}
