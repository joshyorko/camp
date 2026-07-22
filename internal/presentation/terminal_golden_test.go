package presentation

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
			want, err := os.ReadFile(filepath.Join("testdata", test.golden))
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("golden mismatch\ngot:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
