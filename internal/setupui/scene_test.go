package setupui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func loadForTest(t *testing.T) map[string]Sprite {
	t.Helper()
	sprites, err := LoadSprites()
	if err != nil {
		t.Fatalf("LoadSprites: %v", err)
	}
	return sprites
}

func TestSpritesAreRectangularAndNonEmpty(t *testing.T) {
	sprites := loadForTest(t)
	want := []string{"crate", "helm", "tent", "campfire", "sun", "pine_large", "pine_med", "pine_small"}
	for _, name := range want {
		sp, ok := sprites[name]
		if !ok {
			t.Fatalf("missing sprite %q", name)
		}
		if sp.Height() == 0 || sp.Width() == 0 {
			t.Fatalf("sprite %q is empty", name)
		}
		if len(sp.Colors) != len(sp.Glyphs) {
			t.Fatalf("sprite %q row count mismatch: glyphs=%d colors=%d", name, len(sp.Glyphs), len(sp.Colors))
		}
		for i := range sp.Glyphs {
			if len([]rune(sp.Glyphs[i])) != len([]rune(sp.Colors[i])) {
				t.Fatalf("sprite %q row %d width mismatch", name, i)
			}
		}
	}
}

func TestComposeNeverExceedsRequestedSize(t *testing.T) {
	sprites := loadForTest(t)
	pal := DefaultPalette()
	sizes := [][2]int{{69, 20}, {69, 23}, {80, 24}, {100, 30}, {120, 40}, {160, 48}, {200, 60}}
	for _, st := range []string{"configure", "progress", "ready", "failure"} {
		for _, sz := range sizes {
			w, h := sz[0], sz[1]
			frame := SampleFrame(st, w, h, pal, sprites)
			lines := strings.Split(frame, "\n")
			if len(lines) > h {
				t.Fatalf("%s %dx%d: %d lines exceeds height", st, w, h, len(lines))
			}
			for i, line := range lines {
				if vw := ansi.StringWidth(line); vw > w {
					t.Fatalf("%s %dx%d: line %d visible width %d exceeds %d", st, w, h, i, vw, w)
				}
			}
		}
	}
}

func TestSampleStateRegistryDispatchesEveryAdvertisedState(t *testing.T) {
	sprites := loadForTest(t)
	pal := DefaultPalette()
	for _, state := range SampleStates() {
		frame := SampleFrame(state, 80, 24, pal, sprites)
		if frame == "" {
			t.Fatalf("sample state %q rendered an empty frame", state)
		}
	}
}

func TestComposeNoTrailingWhitespace(t *testing.T) {
	sprites := loadForTest(t)
	pal := DefaultPalette()
	frame := SampleFrame("ready", 120, 40, pal, sprites)
	for i, line := range strings.Split(frame, "\n") {
		stripped := ansi.Strip(line)
		if stripped != "" && strings.HasSuffix(stripped, " ") {
			t.Fatalf("line %d has trailing whitespace: %q", i, stripped)
		}
	}
}

func TestReadyStateShowsBandAndCommandButProgressDoesNot(t *testing.T) {
	sprites := loadForTest(t)
	pal := DefaultPalette()
	ready := ansi.Strip(SampleFrame("ready", 120, 40, pal, sprites))
	if !strings.Contains(ready, "CAMP IS READY") {
		t.Fatal("ready frame missing CAMP IS READY band")
	}
	if !strings.Contains(ready, "camp open") {
		t.Fatal("ready frame missing next command")
	}
	progress := ansi.Strip(SampleFrame("progress", 120, 40, pal, sprites))
	if strings.Contains(progress, "CAMP IS READY") {
		t.Fatal("in-progress frame must not claim readiness")
	}
}

func TestFailureStateShowsCauseAndRecoveryNotReady(t *testing.T) {
	sprites := loadForTest(t)
	pal := DefaultPalette()
	frame := ansi.Strip(SampleFrame("failure", 120, 40, pal, sprites))
	if !strings.Contains(frame, "devpod provider docker is unreachable") {
		t.Fatal("failure frame lost the cause")
	}
	if !strings.Contains(frame, "camp setup") {
		t.Fatal("failure frame lost the recovery command")
	}
	if strings.Contains(frame, "CAMP IS READY") {
		t.Fatal("failure frame must never claim readiness")
	}
}

func TestConfigureStateShowsFormAndSuppressesCallouts(t *testing.T) {
	sprites := loadForTest(t)
	pal := DefaultPalette()
	frame := ansi.Strip(SampleFrame("configure", 120, 40, pal, sprites))
	if !strings.Contains(frame, "CONFIGURE") {
		t.Fatal("configure frame missing CONFIGURE panel")
	}
	if !strings.Contains(frame, "Default backend") {
		t.Fatal("configure frame missing form fields")
	}
	// Waypoint metadata callouts are suppressed while all stages are pending.
	if strings.Contains(frame, "DevPod v0.26.1") {
		t.Fatal("configure frame should suppress waypoint metadata callouts")
	}
}

// The last row belongs to the help line: no foreground block (ready band,
// command box, failure band) may reach it at any supported size.
func TestHelpRowIsNeverOverdrawnByForeground(t *testing.T) {
	sprites := loadForTest(t)
	pal := DefaultPalette()
	sizes := [][2]int{{80, 24}, {120, 40}, {160, 48}}
	for _, st := range []string{"ready", "failure"} {
		for _, sz := range sizes {
			w, h := sz[0], sz[1]
			frame := SampleFrame(st, w, h, pal, sprites)
			lines := strings.Split(frame, "\n")
			last := ansi.Strip(lines[len(lines)-1])
			if !strings.Contains(last, "enter to exit") {
				t.Fatalf("%s %dx%d: help line missing from last row: %q", st, w, h, last)
			}
			for _, corner := range []string{"╰", "╯", "─╮", "╭"} {
				if strings.Contains(last, corner) {
					t.Fatalf("%s %dx%d: foreground block bleeds into the help row: %q", st, w, h, last)
				}
			}
		}
	}
}

// Waypoint labels must stay intact over terrain: the badge, the space, and the
// label text may never be interleaved with ridge/forest glyphs.
func TestWaypointLabelsAreNotCorruptedByTerrain(t *testing.T) {
	sprites := loadForTest(t)
	pal := DefaultPalette()
	sizes := [][2]int{{80, 24}, {120, 40}, {160, 48}, {200, 60}}
	for _, st := range []string{"progress", "ready", "failure"} {
		for _, sz := range sizes {
			plain := ansi.Strip(SampleFrame(st, sz[0], sz[1], pal, sprites))
			for _, label := range []string{"TOOLCHAIN", "RUNTIME", "CAPSULE", "STORAGE"} {
				found := false
				for _, badge := range []string{"✓ ", "◈ ", "✗ ", "○ "} {
					if strings.Contains(plain, badge+label) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("%s %dx%d: label %q corrupted or missing (no intact badge+space+label)", st, sz[0], sz[1], label)
				}
			}
		}
	}
}

// The compact configure layout must fit fully inside an 80×24 terminal with
// the help line still visible on the last row.
func TestCompactConfigureFitsSmallTerminal(t *testing.T) {
	sprites := loadForTest(t)
	pal := DefaultPalette()
	frame := SampleFrame("configure", 80, 24, pal, sprites)
	lines := strings.Split(frame, "\n")
	if len(lines) > 24 {
		t.Fatalf("configure 80x24 renders %d lines", len(lines))
	}
	plain := ansi.Strip(frame)
	for _, want := range []string{"CONFIGURE", "Default backend", "DevPod context"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("configure 80x24 missing %q", want)
		}
	}
	if !strings.Contains(ansi.Strip(lines[len(lines)-1]), "tab next") {
		t.Fatal("configure 80x24 help line missing from last row")
	}
}

func TestSafeTextRejectsControlAndCredentials(t *testing.T) {
	cases := []struct {
		in   string
		safe bool
	}{
		{"devpod provider docker is unreachable", true},
		{"file:///home/user/backend", true},
		{"boom\x1b[2J", false},
		{"line\nbreak", false},
		{"https://user:secret@example.test/x", false},
		{"s3://bucket?access_key=AKIA", false},
	}
	for _, c := range cases {
		got := SafeText(c.in, "REPLACED")
		if c.safe && got != c.in {
			t.Fatalf("SafeText(%q) replaced a safe value", c.in)
		}
		if !c.safe && got != "REPLACED" {
			t.Fatalf("SafeText(%q) did not sanitize unsafe value; got %q", c.in, got)
		}
	}
}

func TestTrailPaintUsesFailColorThroughFailedPlusTwoThenDims(t *testing.T) {
	width, height := 80, 30
	pal := DefaultPalette()
	canvas := NewCanvas(width, height, pal.Bg)
	trail := NewTrail(width, 14, 3)
	anchors := trail.Anchors(4, width)

	states := []WaypointState{
		WaypointCompleted,
		WaypointFailed,
		WaypointPending,
		WaypointPending,
	}
	for i := range anchors {
		anchors[i].State = states[i]
	}
	trail.Paint(canvas, states, anchors, pal)

	failedX := anchors[1].X
	for y := 0; y < height; y++ {
		if cell := canvas.cells[y*width+failedX]; cell.set && cell.r == '✗' {
			if !sameColor(cell.fg, pal.Fail) {
				t.Fatalf("failed node at x=%d y=%d is not fail color", failedX, y)
			}
			break
		}
	}

	for x := failedX + 1; x <= failedX+2; x++ {
		found := false
		for y := 0; y < height; y++ {
			cell := canvas.cells[y*width+x]
			if !cell.set || cell.r == '✗' {
				continue
			}
			found = true
			if !sameColor(cell.fg, pal.Fail) {
				t.Fatalf("x=%d expects fail color through failed+2", x)
			}
		}
		if !found {
			t.Fatalf("x=%d has no non-node trail body cell", x)
		}
	}

	for y := 0; y < height; y++ {
		cell := canvas.cells[y*width+failedX+3]
		if !cell.set || cell.r == '✗' {
			continue
		}
		if !sameColor(cell.fg, pal.TrailDim) {
			t.Fatalf("x=%d should be dim after failed+2", failedX+3)
		}
	}
}
