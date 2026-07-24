package presentation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func compareOrUpdateGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("CAMP_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("captured golden %s (review before committing)", name)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch for %s\ngot:\n%s\nwant:\n%s", name, got, string(want))
	}
}

func TestTerminalExperienceGoldens(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "ror", RuntimeKind: "remote DevPod", Context: "work", Capsule: "brain", Source: "/home/josh/brain",
		BackendKind: "s3", Storage: "s3://camp/brain · generation 42", NextCommand: "camp open /home/josh/brain",
	}
	tests := []struct {
		name   string
		golden string
		run    func(*bytes.Buffer) error
	}{
		{name: "plain", golden: "campsite-plain.golden", run: func(out *bytes.Buffer) error { return RenderCampsite(out, model, CampsiteOptions{}) }},
		{name: "narrow", golden: "campsite-plain.golden", run: func(out *bytes.Buffer) error {
			return RenderCampsite(out, model, CampsiteOptions{Color: true, Width: 79})
		}},
		{name: "color", golden: "campsite-color.golden", run: func(out *bytes.Buffer) error {
			return RenderCampsite(out, model, CampsiteOptions{Color: true, Width: 120})
		}},
		{name: "failure", golden: "failure-plain.golden", run: func(out *bytes.Buffer) error {
			return NewTerminalSession(out, TerminalPlain, "close").Emit(context.Background(), LifecycleEvent{Stage: StageFailed, Message: "checkpoint upload failed", RecoveryCommand: "camp recover session-1"})
		}},
		{name: "cancellation", golden: "cancellation.golden", run: func(out *bytes.Buffer) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_ = NewTerminalSession(out, TerminalColor, "open").Emit(ctx, LifecycleEvent{Stage: StageStarted, Message: "opening"})
			return nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := test.run(&output); err != nil {
				t.Fatalf("render: %v", err)
			}
			got := strings.ReplaceAll(output.String(), "\x1b", "<ESC>")
			if got == "" {
				got = "<no output>\n"
			}
			compareOrUpdateGolden(t, test.golden, got)
		})
	}
}

func TestSetupSceneGoldensAcrossSupportedTerminalSizes(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "second_brain", Source: "/home/josh/second_brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open second_brain",
	}
	sizes := []struct {
		name          string
		width, height int
	}{
		{"80x24", 80, 24},
		{"100x30", 100, 30},
		{"120x40", 120, 40},
		{"160x48", 160, 48},
		// approximates the terminal geometry behind the reported failure screenshot
		{"130x50", 130, 50},
	}
	for _, size := range sizes {
		t.Run(size.name, func(t *testing.T) {
			var output bytes.Buffer
			animator, err := NewSetupAnimator(&output, TerminalColor, model, ScreenSize{Width: size.width, Height: size.height})
			if err != nil {
				t.Fatal(err)
			}
			for _, waypoint := range []SetupWaypoint{SetupToolchain, SetupRuntime, SetupCapsule, SetupStorage} {
				if err := animator.Advance(context.Background(), waypoint); err != nil {
					t.Fatal(err)
				}
			}
			got := strings.ReplaceAll(output.String(), "\x1b", "<ESC>")
			compareOrUpdateGolden(t, "setup-scene-"+size.name+".golden", got)

			finalFrame := got[strings.LastIndex(got, "<ESC>[2J<ESC>[H"):]
			for _, line := range strings.Split(finalFrame, "\n") {
				visible := ansiEscapePattern.ReplaceAllString(strings.ReplaceAll(line, "<ESC>", "\x1b"), "")
				if width := utf8.RuneCountInString(visible); width > size.width {
					t.Fatalf("%s: line exceeds terminal width %d: %q", size.name, size.width, visible)
				}
			}
			if lines := strings.Count(finalFrame, "\n"); lines > size.height {
				t.Fatalf("%s: final frame rendered %d lines, exceeds terminal height %d", size.name, lines, size.height)
			}
		})
	}
}

func TestSetupAnimatorFailureGoldenAtStandardSize(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "second_brain", Source: "/home/josh/second_brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open second_brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalColor, model, ScreenSize{Width: 120, Height: 40})
	if err != nil {
		t.Fatal(err)
	}
	if err := animator.Advance(context.Background(), SetupToolchain); err != nil {
		t.Fatal(err)
	}
	if err := animator.Fail(context.Background(), SetupRuntime, errors.New("devpod provider docker is unreachable"), "camp setup"); err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(output.String(), "\x1b", "<ESC>")
	compareOrUpdateGolden(t, "setup-scene-failure-120x40.golden", got)
	if !strings.Contains(got, "devpod provider docker is unreachable") || !strings.Contains(got, "camp setup") {
		t.Fatalf("failure golden lost real cause or recovery command: %s", got)
	}
}
