package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/presentation"
)

func TestPromptSetupRequestUsesMachineDefaults(t *testing.T) {
	var output bytes.Buffer
	got, err := promptSetupRequest(strings.NewReader("\n\n\n\n\n"), &output, setupPromptDefaults{
		Source:  "/home/test/test-camp-robot",
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	want := InitRequest{
		Root:           "/home/test/test-camp-robot",
		Capsule:        "test-camp-robot",
		Backend:        "file:///home/test/.local/share/camp/backend",
		DevPodProvider: "docker", DevPodContext: "default",
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestSetupPromptCollectsCampIdentityBeforeMachineDefaults(t *testing.T) {
	var output bytes.Buffer
	got, err := promptSetupRequest(strings.NewReader("\n\n\n\n\n"), &output, setupPromptDefaults{
		Source:  "/home/test/test-camp-robot",
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != "/home/test/test-camp-robot" || got.Capsule != "test-camp-robot" {
		t.Fatalf("setup camp identity = %#v", got)
	}
	if got.Backend == "" || got.DevPodProvider != "docker" || got.DevPodContext != "default" {
		t.Fatalf("machine defaults = %#v", got)
	}
}

func TestPromptSetupRequestUsesExplicitValues(t *testing.T) {
	got, err := promptSetupRequest(strings.NewReader("/srv/test-camp-robot\ntest_camp\nfile:///srv/camp\npodman\nror\n"), &bytes.Buffer{}, setupPromptDefaults{
		Source:  "/home/test/default",
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	want := InitRequest{
		Root:           "/srv/test-camp-robot",
		Capsule:        "test_camp",
		Backend:        "file:///srv/camp",
		DevPodProvider: "podman", DevPodContext: "ror",
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestPromptSetupRequestRejectsEOF(t *testing.T) {
	if _, err := promptSetupRequest(strings.NewReader(""), &bytes.Buffer{}, setupPromptDefaults{
		Source:  "/home/test/test-camp-robot",
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalPlain, presentation.ScreenSize{}); err == nil || !strings.Contains(err.Error(), "camp root") {
		t.Fatalf("error = %v, want camp root EOF failure", err)
	}
}

func TestPromptSetupRequestColorRendersIntegratedConfigureScene(t *testing.T) {
	var output bytes.Buffer
	got, err := promptSetupRequest(strings.NewReader("\n\nfile:///store\n\n\n"), &output, setupPromptDefaults{
		Source:  "/home/test/test-camp-robot",
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalColor, presentation.ScreenSize{Width: 120, Height: 40})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	if got.Root != "/home/test/test-camp-robot" || got.Capsule != "test-camp-robot" || got.DevPodProvider != "docker" || got.DevPodContext != "default" {
		t.Fatalf("request = %#v", got)
	}
	rendered := output.String()
	if count := strings.Count(rendered, "\x1b[2J\x1b[H"); count != 5 {
		t.Fatalf("full-screen redraws = %d, want 5", count)
	}
	for _, want := range []string{"⛺ CAMP", "CONFIGURE", "Camp root", "Camp name", "Default backend URL", "DevPod provider", "DevPod context"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("configure scene missing %q:\n%s", want, rendered)
		}
	}
}

func TestPromptSetupRequestColorEOFWritesNoPartialConfiguration(t *testing.T) {
	var output bytes.Buffer
	if _, err := promptSetupRequest(strings.NewReader(""), &output, setupPromptDefaults{
		Source:  "/home/test/test-camp-robot",
		Backend: "file:///home/test/.local/share/camp/backend",
	}, presentation.TerminalColor, presentation.ScreenSize{Width: 120, Height: 40}); err == nil {
		t.Fatal("promptSetupRequest accepted EOF")
	}
}
