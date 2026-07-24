package presentation

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func testSceneModel() CampsiteModel {
	return CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "second_brain", Source: "/home/josh/second_brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open second_brain",
	}
}

func TestComposeSceneShowsReadyBandOnlyWhenAllWaypointsCompleted(t *testing.T) {
	model := testSceneModel()
	inProgress := composeScene(model, waypointStatuses(2, -1), ScreenSize{Width: 120, Height: 40}, false, nil)
	if strings.Contains(inProgress, "CAMP IS READY") {
		t.Fatalf("in-progress frame must not claim readiness:\n%s", inProgress)
	}
	ready := composeScene(model, waypointStatuses(4, -1), ScreenSize{Width: 120, Height: 40}, true, nil)
	if !strings.Contains(ready, "CAMP IS READY") {
		t.Fatalf("ready frame must show CAMP IS READY:\n%s", ready)
	}
	if !strings.Contains(ready, "camp open second_brain") {
		t.Fatalf("ready frame must show the next command:\n%s", ready)
	}
}

func TestComposeSceneCarriesRealMetadataPerWaypoint(t *testing.T) {
	model := testSceneModel()
	got := composeScene(model, waypointStatuses(4, -1), ScreenSize{Width: 120, Height: 40}, true, nil)
	for _, want := range []string{
		"DevPod v0.26.1", "Hauler v2.0.2",
		"docker · local DevPod", "context default",
		"second_brain", "/home/josh/second_brain",
		"file backend", "no committed generation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("scene missing real metadata %q:\n%s", want, got)
		}
	}
}

func TestComposeSceneFailurePreservesExactCauseAndRecovery(t *testing.T) {
	model := testSceneModel()
	failure := &sceneFailure{Waypoint: SetupRuntime, Message: "devpod provider docker is unreachable", Recovery: "camp setup"}
	got := composeScene(model, waypointStatuses(1, 1), ScreenSize{Width: 120, Height: 40}, false, failure)
	if !strings.Contains(got, "devpod provider docker is unreachable") {
		t.Fatalf("failure frame lost the real cause:\n%s", got)
	}
	if !strings.Contains(got, "camp setup") {
		t.Fatalf("failure frame lost the recovery command:\n%s", got)
	}
	if strings.Contains(got, "CAMP IS READY") {
		t.Fatalf("failure frame must never claim readiness:\n%s", got)
	}
	if strings.Count(got, "camp setup") != 1 {
		t.Fatalf("failure frame must print exactly one recovery command:\n%s", got)
	}
}

func TestComposeSceneNeverExceedsRequestedWidthOrHeight(t *testing.T) {
	sizes := []ScreenSize{{Width: 80, Height: 24}, {Width: 100, Height: 30}, {Width: 120, Height: 40}, {Width: 160, Height: 48}}
	model := testSceneModel()
	for _, size := range sizes {
		got := composeScene(model, waypointStatuses(4, -1), size, true, nil)
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if len(lines) > size.Height {
			t.Fatalf("size %+v: rendered %d lines, exceeds height %d", size, len(lines), size.Height)
		}
		for _, line := range lines {
			visible := ansiEscapePattern.ReplaceAllString(line, "")
			if width := utf8.RuneCountInString(visible); width > size.Width {
				t.Fatalf("size %+v: line %q has visible width %d, exceeds terminal width", size, visible, width)
			}
		}
	}
}

func TestComposeSceneHasNoTrailingWhitespacePerLine(t *testing.T) {
	model := testSceneModel()
	got := composeScene(model, waypointStatuses(4, -1), ScreenSize{Width: 120, Height: 40}, true, nil)
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if line != "" && strings.HasSuffix(line, " ") {
			t.Fatalf("line has trailing whitespace: %q", line)
		}
	}
}
