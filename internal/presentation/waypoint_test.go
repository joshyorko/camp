package presentation

import "testing"

func TestWaypointStatusesMarksCompletedActiveAndPending(t *testing.T) {
	got := waypointStatuses(2, -1)
	want := [4]WaypointStatus{WaypointCompleted, WaypointCompleted, WaypointActive, WaypointPending}
	if got != want {
		t.Fatalf("waypointStatuses(2, -1) = %v, want %v", got, want)
	}
}

func TestWaypointStatusesAllCompletedWhenFullyDone(t *testing.T) {
	got := waypointStatuses(4, -1)
	want := [4]WaypointStatus{WaypointCompleted, WaypointCompleted, WaypointCompleted, WaypointCompleted}
	if got != want {
		t.Fatalf("waypointStatuses(4, -1) = %v, want %v", got, want)
	}
}

func TestWaypointStatusesMarksFailureAndLeavesLaterWaypointsPending(t *testing.T) {
	got := waypointStatuses(1, 1)
	want := [4]WaypointStatus{WaypointCompleted, WaypointFailed, WaypointPending, WaypointPending}
	if got != want {
		t.Fatalf("waypointStatuses(1, 1) = %v, want %v", got, want)
	}
}

func TestWaypointStatusGlyphsAreDistinctPerState(t *testing.T) {
	seen := map[string]bool{}
	for _, status := range []WaypointStatus{WaypointPending, WaypointActive, WaypointCompleted, WaypointFailed} {
		glyph := status.glyph()
		if seen[glyph] {
			t.Fatalf("glyph %q reused across states", glyph)
		}
		seen[glyph] = true
	}
}

func TestWaypointStatusColorsAreDistinctPerState(t *testing.T) {
	seen := map[string]bool{}
	for _, status := range []WaypointStatus{WaypointPending, WaypointActive, WaypointCompleted, WaypointFailed} {
		color := status.color()
		if seen[color] {
			t.Fatalf("color %q reused across states", color)
		}
		seen[color] = true
	}
}

func TestWaypointStatusPaintWrapsGlyphLabelAndReset(t *testing.T) {
	got := WaypointCompleted.paint("TOOLCHAIN")
	want := colorGreen + "✓ TOOLCHAIN" + colorReset
	if got != want {
		t.Fatalf("paint = %q, want %q", got, want)
	}
}
