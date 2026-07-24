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
	sizes := [][2]int{{80, 24}, {100, 30}, {120, 40}, {160, 48}, {200, 60}}
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
	if !strings.Contains(frame, "Source path") {
		t.Fatal("configure frame missing form fields")
	}
	// Waypoint metadata callouts are suppressed while all stages are pending.
	if strings.Contains(frame, "DevPod v0.26.1") {
		t.Fatal("configure frame should suppress waypoint metadata callouts")
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
