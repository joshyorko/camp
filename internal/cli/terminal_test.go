package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/presentation"
)

func TestResolveTerminalExperienceUsesOutputDescriptorAndEnvironment(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	probe := func(fd uintptr) (bool, int, int) {
		if fd != file.Fd() {
			t.Fatalf("fd = %d, want %d", fd, file.Fd())
		}
		return true, 120, 40
	}
	got, _, _ := resolveTerminalExperience(ModeHuman, file, map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, probe)
	if got != presentation.TerminalColor {
		t.Fatalf("resolveTerminalExperience() = %q, want color", got)
	}
}

func TestResolveTerminalExperienceDoesNotTreatNoColorAsMissingTerminalCapability(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	probe := func(uintptr) (bool, int, int) { return true, 120, 40 }
	got, _, _ := resolveTerminalExperience(ModeHuman, file, map[string]string{
		"TERM": "xterm-256color", "COLORTERM": "truecolor", "NO_COLOR": "1",
	}, probe)
	if got != presentation.TerminalColor {
		t.Fatalf("resolveTerminalExperience() = %q, want color", got)
	}
}

func TestResolveTerminalExperienceReturnsProbedHeightAlongsideWidth(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	probe := func(uintptr) (bool, int, int) { return true, 120, 40 }
	experience, width, height := resolveTerminalExperience(ModeHuman, file, map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, probe)
	if experience != presentation.TerminalColor || width != 120 || height != 40 {
		t.Fatalf("resolveTerminalExperience() = %q %d %d, want color 120 40", experience, width, height)
	}
}

func TestCanUseRichSetupRequires69x20Floor(t *testing.T) {
	if !canUseRichSetup(presentation.TerminalColor, 69, 20) {
		t.Fatal("69x20 should be enough for rich mode")
	}
	if canUseRichSetup(presentation.TerminalColor, 68, 20) {
		t.Fatal("68-column terminal should not enable rich mode")
	}
	if canUseRichSetup(presentation.TerminalColor, 69, 19) {
		t.Fatal("19-row terminal should not enable rich mode")
	}
}

func TestCanUseRichSetupRejectsNonRichTerminalExperience(t *testing.T) {
	if canUseRichSetup(presentation.TerminalPlain, 120, 40) {
		t.Fatal("plain terminal should not enable rich mode")
	}
}

func TestResolveTerminalExperienceTreatsNonFilesAndFallbackSignalsAsPlain(t *testing.T) {
	tests := []struct {
		name string
		mode OutputMode
		env  map[string]string
	}{
		{name: "buffer", mode: ModeHuman, env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}},
		{name: "json", mode: ModeJSON, env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}},
		{name: "ci", mode: ModeHuman, env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "CI": "true"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _, _ := resolveTerminalExperience(test.mode, &bytes.Buffer{}, test.env, func(uintptr) (bool, int, int) {
				t.Fatal("terminal probe called for non-file output")
				return true, 120, 40
			})
			if got != presentation.TerminalPlain {
				t.Fatalf("resolveTerminalExperience() = %q, want plain", got)
			}
		})
	}
}

func TestWriteLifecycleEventsUsesCompletedOpenSyncAndCloseState(t *testing.T) {
	tests := []struct {
		operation string
		events    []presentation.LifecycleEvent
		want      string
	}{
		{operation: "open", events: openTerminalEvents("brain", "session-1"), want: "open: workspace brain is ready\nopen: opened brain (session-1)\n"},
		{operation: "sync", events: syncTerminalEvents(42), want: "sync: published generation 42\nsync: sync complete\n"},
		{operation: "close", events: closeTerminalEvents(43, true), want: "close: published generation 43\nclose: cleanup complete\nclose: session closed\n"},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeLifecycleEvents(&output, presentation.TerminalPlain, test.operation, test.events...); err != nil {
				t.Fatalf("writeLifecycleEvents: %v", err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCloseDiscardTerminalEventsDoNotClaimPublication(t *testing.T) {
	got := closeDiscardTerminalEvents()
	want := []presentation.LifecycleEvent{
		{Stage: presentation.StageCleanupComplete, Message: "cleanup complete"},
		{Stage: presentation.StageComplete, Message: "session discarded and closed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestLifecycleProgressReporterStreamsTruthfulPlainStages(t *testing.T) {
	var output bytes.Buffer
	reporter := newLifecycleProgressReporter(ModeHuman, &output, presentation.TerminalPlain, "close")
	if err := reporter.Report(context.Background(), app.ProgressEvent{Stage: app.ProgressImagesCaptured, ImageCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := reporter.Report(context.Background(), app.ProgressEvent{Stage: app.ProgressGenerationBuilt, Generation: 3, Bytes: 8078231}); err != nil {
		t.Fatal(err)
	}
	want := "close: captured 2 OCI images\nclose: built generation 3 (7.7 MiB)\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestLifecycleProgressReporterIsSilentForJSON(t *testing.T) {
	var output bytes.Buffer
	reporter := newLifecycleProgressReporter(ModeJSON, &output, presentation.TerminalPlain, "close")
	if err := reporter.Report(context.Background(), app.ProgressEvent{Stage: app.ProgressGenerationPublished, Generation: 3}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("JSON progress output = %q", output.String())
	}
}

func TestWriteLifecycleEventsDoesNotRunForJSON(t *testing.T) {
	var output bytes.Buffer
	if err := writeHumanLifecycleResult(&output, ModeJSON, "sync", syncTerminalEvents(42), "legacy\n"); err != nil {
		t.Fatalf("writeHumanLifecycleResult: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("JSON helper wrote %q", output.String())
	}
}

func TestLifecycleFailurePreservesCauseAndOneRecoveryCommand(t *testing.T) {
	cause := errors.New("checkpoint upload failed")
	err := lifecycleFailure(cause, "camp recover session-1")
	if !errors.Is(err, cause) {
		t.Fatalf("lifecycleFailure does not wrap cause: %v", err)
	}
	if err.Failure.Message != cause.Error() || len(err.Failure.NextCommands) != 1 || err.Failure.NextCommands[0] != "camp recover session-1" {
		t.Fatalf("failure = %#v", err.Failure)
	}
}
