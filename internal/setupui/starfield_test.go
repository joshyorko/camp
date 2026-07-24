package setupui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestStarfieldONCEConstants(t *testing.T) {
	if starCount != 100 || starSpeed != 0.03 || starMinZ != 0.1 || starMaxZ != 3.0 || starTick != 33*time.Millisecond {
		t.Fatal("starfield constants drifted from the ONCE reference")
	}
}

func TestStarfieldProjectsBrailleDotsDeterministically(t *testing.T) {
	field := NewStarfield(1)
	field.Resize(4, 2)
	field.stars = []star{{x: 0, y: 0, z: 1}}
	field.ComputeGrid()
	first := field.grid[1][2]
	field.ComputeGrid()
	if first.ch == 0 || field.grid[1][2] != first {
		t.Fatalf("projection is not a stable braille cell: %#v", first)
	}
}

func TestStarfieldTickMovesAndReschedules(t *testing.T) {
	field := NewStarfield(1)
	field.Resize(10, 4)
	before := field.stars[0].z
	cmd := field.Update(starfieldTickMsg{})
	if cmd == nil {
		t.Fatal("tick did not reschedule")
	}
	if field.stars[0].z == before {
		t.Fatal("tick did not move star depth")
	}
}

func TestModelOwnsAndResizesStarfield(t *testing.T) {
	model := NewModel(DefaultPalette(), nil, nil, nil)
	if model.starfield == nil {
		t.Fatal("model has no starfield")
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	got := updated.(Model)
	if got.starfield.width != 80 || got.starfield.height != layoutFor(80, 24).SkyRows {
		t.Fatalf("starfield size = %dx%d", got.starfield.width, got.starfield.height)
	}
}
