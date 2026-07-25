package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/presentation"
)

func TestPromptSetupRequestUsesMachineDefaults(t *testing.T) {
	var output bytes.Buffer
	got, err := promptSetupRequest(strings.NewReader("\n\n\n"), &output, setupPromptDefaults{
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	want := InitRequest{
		Backend:        "file:///home/test/.local/share/camp/backend",
		DevPodProvider: "docker", DevPodContext: "default",
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestSetupPromptCollectsMachineDefaultsOnly(t *testing.T) {
	var output bytes.Buffer
	got, err := promptSetupRequest(strings.NewReader("\n\n\n"), &output, setupPromptDefaults{
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "" || got.Capsule != "" {
		t.Fatalf("setup selected camp identity: %#v", got)
	}
	if got.Backend == "" || got.DevPodProvider != "docker" || got.DevPodContext != "default" {
		t.Fatalf("machine defaults = %#v", got)
	}
}

func TestPromptSetupRequestUsesExplicitValues(t *testing.T) {
	got, err := promptSetupRequest(strings.NewReader("file:///srv/camp\npodman\nror\n"), &bytes.Buffer{}, setupPromptDefaults{
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	want := InitRequest{
		Backend:        "file:///srv/camp",
		DevPodProvider: "podman", DevPodContext: "ror",
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestPromptSetupRequestRejectsEOF(t *testing.T) {
	if _, err := promptSetupRequest(strings.NewReader(""), &bytes.Buffer{}, setupPromptDefaults{
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{}); err == nil || !strings.Contains(err.Error(), "backend") {
		t.Fatalf("error = %v, want backend EOF failure", err)
	}
}

func TestPromptSetupRequestColorRendersIntegratedConfigureScene(t *testing.T) {
	var output bytes.Buffer
	got, err := promptSetupRequest(strings.NewReader("file:///store\n\n\n"), &output, setupPromptDefaults{
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalColor, presentation.ScreenSize{Width: 120, Height: 40})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	if got.Source != "" || got.Capsule != "" || got.DevPodProvider != "docker" || got.DevPodContext != "default" {
		t.Fatalf("request = %#v", got)
	}
	rendered := output.String()
	if count := strings.Count(rendered, "\x1b[2J\x1b[H"); count != 3 {
		t.Fatalf("full-screen redraws = %d, want 3", count)
	}
	for _, want := range []string{"⛺ CAMP", "CONFIGURE", "Default backend URL", "DevPod provider", "DevPod context"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("configure scene missing %q:\n%s", want, rendered)
		}
	}
}

func TestPromptSetupRequestColorEOFWritesNoPartialConfiguration(t *testing.T) {
	var output bytes.Buffer
	if _, err := promptSetupRequest(strings.NewReader(""), &output, setupPromptDefaults{
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalColor, presentation.ScreenSize{Width: 120, Height: 40}); err == nil {
		t.Fatal("promptSetupRequest accepted EOF")
	}
}
