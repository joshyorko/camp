package presentation

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderCampsitePlainUsesAuthoritativeValuesWithoutControlSequences(t *testing.T) {
	model := CampsiteModel{
		DevPod:      ToolIdentity{Name: "DevPod", Version: "v0.26.1"},
		Hauler:      ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider:    "room-of-requirement",
		RuntimeKind: "Kubernetes",
		Context:     "ror",
		Capsule:     "SecondBrain",
		Source:      "~/SecondBrain",
		BackendKind: "file",
		Storage:     "generation store ready",
		NextCommand: "camp open ~/SecondBrain",
	}
	var output bytes.Buffer

	if err := RenderCampsite(&output, model, CampsiteOptions{}); err != nil {
		t.Fatalf("RenderCampsite: %v", err)
	}

	got := output.String()
	for _, want := range []string{
		"TOOLCHAIN  DevPod v0.26.1 · Hauler v2.0.2",
		"RUNTIME    room-of-requirement · Kubernetes · context ror",
		"CAPSULE    SecondBrain · ~/SecondBrain",
		"STORAGE    file backend · generation store ready",
		"CAMP IS READY",
		"camp open ~/SecondBrain",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain output contains terminal control sequence: %q", got)
	}
	if strings.Contains(got, "%") {
		t.Fatalf("output contains fake percentage: %q", got)
	}
}

func TestRenderCampsiteColorKeepsLiveMetadataVisible(t *testing.T) {
	model := CampsiteModel{
		DevPod:      ToolIdentity{Name: "DevPod", Version: "v0.26.1"},
		Hauler:      ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider:    "room-of-requirement",
		RuntimeKind: "Kubernetes",
		Context:     "ror",
		Capsule:     "SecondBrain",
		Source:      "~/SecondBrain",
		BackendKind: "file",
		Storage:     "generation store ready",
		NextCommand: "camp open ~/SecondBrain",
	}
	var output bytes.Buffer

	if err := RenderCampsite(&output, model, CampsiteOptions{Color: true, Width: 120}); err != nil {
		t.Fatalf("RenderCampsite: %v", err)
	}

	got := output.String()
	for _, want := range []string{"\x1b[", "DevPod v0.26.1", "Hauler v2.0.2", "room-of-requirement", "context ror", "SecondBrain", "file backend"} {
		if !strings.Contains(got, want) {
			t.Fatalf("color output = %q, want %q", got, want)
		}
	}
}

func TestRenderCampsiteRejectsMissingMetadataInsteadOfInventingIt(t *testing.T) {
	var output bytes.Buffer
	err := RenderCampsite(&output, CampsiteModel{}, CampsiteOptions{Color: true})
	if err == nil || !strings.Contains(err.Error(), "devpod version") {
		t.Fatalf("RenderCampsite error = %v, want missing devpod version", err)
	}
	if output.Len() != 0 {
		t.Fatalf("RenderCampsite wrote partial output %q", output.String())
	}
}

func TestRenderCampsiteRejectsUnsafeMetadataBeforeWriting(t *testing.T) {
	base := CampsiteModel{
		DevPod:      ToolIdentity{Name: "DevPod", Version: "v0.26.1"},
		Hauler:      ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider:    "room-of-requirement",
		RuntimeKind: "Kubernetes",
		Context:     "ror",
		Capsule:     "SecondBrain",
		Source:      "~/SecondBrain",
		BackendKind: "file",
		Storage:     "generation store ready",
		NextCommand: "camp open ~/SecondBrain",
	}
	tests := []struct {
		name   string
		mutate func(*CampsiteModel)
	}{
		{name: "terminal escape", mutate: func(model *CampsiteModel) { model.Provider = "docker\x1b[2J" }},
		{name: "multiline", mutate: func(model *CampsiteModel) { model.Capsule = "brain\nspoofed" }},
		{name: "url credentials", mutate: func(model *CampsiteModel) { model.Source = "https://user:secret@example.test/brain" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := base
			test.mutate(&model)
			var output bytes.Buffer
			if err := RenderCampsite(&output, model, CampsiteOptions{Color: true, Width: 120}); err == nil {
				t.Fatal("RenderCampsite accepted unsafe metadata")
			}
			if output.Len() != 0 {
				t.Fatalf("RenderCampsite wrote partial output %q", output.String())
			}
		})
	}
}
