package cli

import (
	"bytes"
	"os"
	"testing"

	"github.com/joshyorko/camp/internal/presentation"
)

func TestResolveTerminalExperienceUsesOutputDescriptorAndEnvironment(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	probe := func(fd uintptr) (bool, int) {
		if fd != file.Fd() {
			t.Fatalf("fd = %d, want %d", fd, file.Fd())
		}
		return true, 120
	}
	got := resolveTerminalExperience(ModeHuman, file, map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, probe)
	if got != presentation.TerminalColor {
		t.Fatalf("resolveTerminalExperience() = %q, want color", got)
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
		{name: "no color", mode: ModeHuman, env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "NO_COLOR": ""}},
		{name: "ci", mode: ModeHuman, env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "CI": "true"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveTerminalExperience(test.mode, &bytes.Buffer{}, test.env, func(uintptr) (bool, int) {
				t.Fatal("terminal probe called for non-file output")
				return true, 120
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

func TestWriteLifecycleEventsDoesNotRunForJSON(t *testing.T) {
	var output bytes.Buffer
	if err := writeHumanLifecycleResult(&output, ModeJSON, "sync", syncTerminalEvents(42), "legacy\n"); err != nil {
		t.Fatalf("writeHumanLifecycleResult: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("JSON helper wrote %q", output.String())
	}
}
