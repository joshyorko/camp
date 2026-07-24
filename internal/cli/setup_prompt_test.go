package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptSetupRequestUsesDefaultsAndDerivesCapsule(t *testing.T) {
	var output bytes.Buffer
	got, err := promptSetupRequest(strings.NewReader("\n\n\n\n\n"), &output, setupPromptDefaults{
		Source:  "/work/camp",
		Backend: "file:///home/test/.local/share/camp/backend",
	})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	want := InitRequest{
		Source: "/work/camp", Capsule: "camp",
		Backend:        "file:///home/test/.local/share/camp/backend",
		DevPodProvider: "docker", DevPodContext: "default",
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestPromptSetupRequestUsesExplicitValues(t *testing.T) {
	got, err := promptSetupRequest(strings.NewReader("/srv/brain\nmemory\nfile:///srv/camp\npodman\nror\n"), &bytes.Buffer{}, setupPromptDefaults{
		Source: "/work/camp", Backend: "file:///home/test/.local/share/camp/backend",
	})
	if err != nil {
		t.Fatalf("promptSetupRequest: %v", err)
	}
	want := InitRequest{
		Source: "/srv/brain", Capsule: "memory", Backend: "file:///srv/camp",
		DevPodProvider: "podman", DevPodContext: "ror",
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestPromptSetupRequestRejectsEOF(t *testing.T) {
	if _, err := promptSetupRequest(strings.NewReader(""), &bytes.Buffer{}, setupPromptDefaults{
		Source: "/work/camp", Backend: "file:///home/test/.local/share/camp/backend",
	}); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("error = %v, want source EOF failure", err)
	}
}
