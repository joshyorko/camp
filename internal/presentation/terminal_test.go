package presentation

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSelectTerminalExperienceFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		in   TerminalInput
		want TerminalExperience
	}{
		{name: "interactive true color", in: TerminalInput{TTY: true, Width: 120, TERM: "xterm-256color", COLORTERM: "truecolor"}, want: TerminalColor},
		{name: "json", in: TerminalInput{TTY: true, Width: 120, TERM: "xterm-256color", COLORTERM: "truecolor", JSON: true}, want: TerminalPlain},
		{name: "no color", in: TerminalInput{TTY: true, Width: 120, TERM: "xterm-256color", COLORTERM: "truecolor", NoColor: true}, want: TerminalPlain},
		{name: "ci", in: TerminalInput{TTY: true, Width: 120, TERM: "xterm-256color", COLORTERM: "truecolor", CI: true}, want: TerminalPlain},
		{name: "redirected", in: TerminalInput{Width: 120, TERM: "xterm-256color", COLORTERM: "truecolor"}, want: TerminalPlain},
		{name: "dumb", in: TerminalInput{TTY: true, Width: 120, TERM: "dumb", COLORTERM: "truecolor"}, want: TerminalPlain},
		{name: "narrow", in: TerminalInput{TTY: true, Width: 79, TERM: "xterm-256color", COLORTERM: "truecolor"}, want: TerminalPlain},
		{name: "not true color", in: TerminalInput{TTY: true, Width: 120, TERM: "xterm-256color"}, want: TerminalPlain},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SelectTerminalExperience(test.in); got != test.want {
				t.Fatalf("SelectTerminalExperience() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTerminalSessionAdvancesOnlyFromEvents(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output, TerminalColor, "open")
	for _, event := range []LifecycleEvent{
		{Stage: StageStarted, Message: "opening brain"},
		{Stage: StageWorkspaceReady, Message: "workspace ready"},
		{Stage: StageComplete, Message: "opened brain (session-1)"},
	} {
		if err := session.Emit(context.Background(), event); err != nil {
			t.Fatalf("Emit(%s): %v", event.Stage, err)
		}
	}
	got := output.String()
	for _, want := range []string{"opening brain", "workspace ready", "opened brain (session-1)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
	if strings.Contains(got, "%") {
		t.Fatalf("output contains fabricated percentage: %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("color output lacks terminal controls: %q", got)
	}
}

func TestTerminalSessionPlainOutputIsDeterministicAndControlFree(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output, TerminalPlain, "sync")
	for _, event := range []LifecycleEvent{
		{Stage: StageStarted, Message: "syncing session-1"},
		{Stage: StageGenerationPublished, Message: "published generation 42"},
		{Stage: StageComplete, Message: "sync complete"},
	} {
		if err := session.Emit(context.Background(), event); err != nil {
			t.Fatalf("Emit(%s): %v", event.Stage, err)
		}
	}
	want := "sync: syncing session-1\nsync: published generation 42\nsync: sync complete\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("plain output contains controls: %q", output.String())
	}
}

func TestTerminalSessionRejectsUnsafeEventBeforeWriting(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output, TerminalColor, "close")
	err := session.Emit(context.Background(), LifecycleEvent{Stage: StageFailed, Message: "failed\x1b[2J", RecoveryCommand: "camp recover session-1"})
	if err == nil {
		t.Fatal("Emit accepted control injection")
	}
	if output.Len() != 0 {
		t.Fatalf("Emit wrote partial output %q", output.String())
	}
}

func TestTerminalSessionFailurePrintsUnderlyingErrorAndOneRecoveryCommand(t *testing.T) {
	var output bytes.Buffer
	session := NewTerminalSession(&output, TerminalPlain, "close")
	err := session.Emit(context.Background(), LifecycleEvent{Stage: StageFailed, Message: "checkpoint upload failed", RecoveryCommand: "camp recover session-1"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	want := "close: stopped: checkpoint upload failed\nclose: recover: camp recover session-1\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestSetupAnimatorRedrawsColorSceneOnlyFromOrderedWaypoints(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "ror", RuntimeKind: "remote DevPod", Context: "work", Capsule: "brain", Source: "/brain",
		BackendKind: "s3", Storage: "s3://camp/brain · generation 42", NextCommand: "camp open /brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalColor, model)
	if err != nil {
		t.Fatalf("NewSetupAnimator: %v", err)
	}
	for _, waypoint := range []SetupWaypoint{SetupToolchain, SetupRuntime, SetupCapsule, SetupStorage} {
		if err := animator.Advance(context.Background(), waypoint); err != nil {
			t.Fatalf("Advance(%s): %v", waypoint, err)
		}
	}
	got := output.String()
	if count := strings.Count(got, "\x1b[2J\x1b[H"); count != 4 {
		t.Fatalf("full-screen redraws = %d, want 4", count)
	}
	if count := strings.Count(got, "CAMP IS READY"); count != 1 {
		t.Fatalf("ready claims = %d, want final frame only", count)
	}
	if !strings.Contains(got, "DevPod v0.26.1") || !strings.Contains(got, "generation 42") {
		t.Fatalf("output lacks authoritative values: %q", got)
	}
}

func TestSetupAnimatorRejectsSkippedOrRepeatedWaypointBeforeWriting(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "ror", RuntimeKind: "remote DevPod", Context: "work", Capsule: "brain", Source: "/brain",
		BackendKind: "s3", Storage: "s3://camp/brain · generation 42", NextCommand: "camp open /brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalPlain, model)
	if err != nil {
		t.Fatal(err)
	}
	if err := animator.Advance(context.Background(), SetupRuntime); err == nil {
		t.Fatal("Advance accepted skipped toolchain waypoint")
	}
	if output.Len() != 0 {
		t.Fatalf("Advance wrote partial output %q", output.String())
	}
}

func TestSetupAnimatorPlainFallbackIsStableAndControlFree(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "ror", RuntimeKind: "remote DevPod", Context: "work", Capsule: "brain", Source: "/brain",
		BackendKind: "s3", Storage: "s3://camp/brain · generation 42", NextCommand: "camp open /brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalPlain, model)
	if err != nil {
		t.Fatal(err)
	}
	for _, waypoint := range []SetupWaypoint{SetupToolchain, SetupRuntime, SetupCapsule, SetupStorage} {
		if err := animator.Advance(context.Background(), waypoint); err != nil {
			t.Fatal(err)
		}
	}
	want := "setup: toolchain: DevPod v0.26.1 · Hauler v2.0.2\nsetup: runtime: ror · remote DevPod · context work\nsetup: capsule: brain · /brain\nsetup: storage: s3 backend · s3://camp/brain · generation 42\nsetup: camp is ready; next: camp open /brain\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("plain setup output contains controls: %q", output.String())
	}
}
