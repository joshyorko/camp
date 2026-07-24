package presentation

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSetupAnimatorFailRendersFailureFrameWithRealCauseAndRecovery(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "brain", Source: "/brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open /brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalColor, model, ScreenSize{Width: 120, Height: 40})
	if err != nil {
		t.Fatalf("NewSetupAnimator: %v", err)
	}
	if err := animator.Advance(context.Background(), SetupToolchain); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	cause := errors.New("devpod provider docker is unreachable")
	if err := animator.Fail(context.Background(), SetupRuntime, cause, "camp setup"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	got := output.String()
	if !strings.Contains(got, "devpod provider docker is unreachable") {
		t.Fatalf("output lost the real cause: %q", got)
	}
	if !strings.Contains(got, "camp setup") {
		t.Fatalf("output lost the recovery command: %q", got)
	}
	if strings.Contains(got, "CAMP IS READY") {
		t.Fatalf("failure output must never claim readiness: %q", got)
	}
}

func TestSetupAnimatorFailRejectsOutOfOrderWaypoint(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "brain", Source: "/brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open /brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalPlain, model, ScreenSize{})
	if err != nil {
		t.Fatal(err)
	}
	if err := animator.Fail(context.Background(), SetupCapsule, errors.New("boom"), "camp setup"); err == nil {
		t.Fatal("Fail accepted an out-of-order waypoint")
	}
	if output.Len() != 0 {
		t.Fatalf("Fail wrote partial output %q", output.String())
	}
}

func TestSetupAnimatorFailPlainPreservesExistingFailureShape(t *testing.T) {
	model := CampsiteModel{
		DevPod: ToolIdentity{Name: "DevPod", Version: "v0.26.1"}, Hauler: ToolIdentity{Name: "Hauler", Version: "v2.0.2"},
		Provider: "docker", RuntimeKind: "local DevPod", Context: "default", Capsule: "brain", Source: "/brain",
		BackendKind: "file", Storage: "no committed generation", NextCommand: "camp open /brain",
	}
	var output bytes.Buffer
	animator, err := NewSetupAnimator(&output, TerminalPlain, model, ScreenSize{})
	if err != nil {
		t.Fatal(err)
	}
	if err := animator.Fail(context.Background(), SetupToolchain, errors.New("checkpoint upload failed"), "camp recover session-1"); err != nil {
		t.Fatal(err)
	}
	want := "setup: stopped: checkpoint upload failed\nsetup: recover: camp recover session-1\n"
	if got := output.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
