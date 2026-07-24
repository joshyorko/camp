package presentation

import (
	"context"
	"fmt"
	"io"
	"strings"
)

type SetupWaypoint string

const (
	SetupToolchain SetupWaypoint = "toolchain"
	SetupRuntime   SetupWaypoint = "runtime"
	SetupCapsule   SetupWaypoint = "capsule"
	SetupStorage   SetupWaypoint = "storage"
)

var setupWaypoints = []SetupWaypoint{SetupToolchain, SetupRuntime, SetupCapsule, SetupStorage}

type SetupAnimator struct {
	writer     io.Writer
	experience TerminalExperience
	model      CampsiteModel
	size       ScreenSize
	next       int
}

func NewSetupAnimator(writer io.Writer, experience TerminalExperience, model CampsiteModel, size ScreenSize) (*SetupAnimator, error) {
	if writer == nil {
		return nil, fmt.Errorf("setup animator writer is nil")
	}
	if err := validateCampsiteModel(model); err != nil {
		return nil, err
	}
	return &SetupAnimator{writer: writer, experience: experience, model: model, size: size}, nil
}

func (a *SetupAnimator) Advance(ctx context.Context, waypoint SetupWaypoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.next >= len(setupWaypoints) || waypoint != setupWaypoints[a.next] {
		return fmt.Errorf("setup waypoint %q is out of order", waypoint)
	}
	completed := a.next + 1
	var output string
	if canRenderColorScene(a.experience == TerminalColor, a.size) {
		statuses := waypointStatuses(completed, -1)
		ready := completed == len(setupWaypoints)
		output = "\x1b[2J\x1b[H" + composeScene(a.model, statuses, a.size, ready, nil)
	} else {
		output = renderSetupPlainEvent(a.model, waypoint)
		if waypoint == SetupStorage {
			output += fmt.Sprintf("setup: camp is ready; next: %s\n", a.model.NextCommand)
		}
	}
	if _, err := io.WriteString(a.writer, output); err != nil {
		return err
	}
	a.next++
	return nil
}

func (a *SetupAnimator) Fail(ctx context.Context, waypoint SetupWaypoint, cause error, recovery string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	index := indexOfWaypoint(waypoint)
	if index == -1 || index != a.next {
		return fmt.Errorf("setup failure waypoint %q is out of order", waypoint)
	}
	message := "setup failed"
	if cause != nil {
		message = cause.Error()
	}
	message = safeFailureText(message, "unsafe failure text omitted")
	recovery = safeFailureText(recovery, "rerun camp setup")
	if canRenderColorScene(a.experience == TerminalColor, a.size) {
		statuses := waypointStatuses(index, index)
		failure := &sceneFailure{Waypoint: waypoint, Message: message, Recovery: recovery}
		_, err := io.WriteString(a.writer, "\x1b[2J\x1b[H"+composeScene(a.model, statuses, a.size, false, failure))
		return err
	}
	_, err := fmt.Fprintf(a.writer, "setup: stopped: %s\nsetup: recover: %s\n", message, recovery)
	return err
}

func safeFailureText(value, replacement string) string {
	if strings.TrimSpace(value) == "" || unsafeCampsiteValue(value) {
		return replacement
	}
	return value
}

func indexOfWaypoint(waypoint SetupWaypoint) int {
	for i, candidate := range setupWaypoints {
		if candidate == waypoint {
			return i
		}
	}
	return -1
}

func renderSetupPlainEvent(model CampsiteModel, waypoint SetupWaypoint) string {
	switch waypoint {
	case SetupToolchain:
		return fmt.Sprintf("setup: toolchain: %s %s · %s %s\n", model.DevPod.Name, model.DevPod.Version, model.Hauler.Name, model.Hauler.Version)
	case SetupRuntime:
		return fmt.Sprintf("setup: runtime: %s · %s · context %s\n", model.Provider, model.RuntimeKind, model.Context)
	case SetupCapsule:
		return fmt.Sprintf("setup: capsule: %s · %s\n", model.Capsule, model.Source)
	case SetupStorage:
		return fmt.Sprintf("setup: storage: %s backend · %s\n", model.BackendKind, model.Storage)
	default:
		return ""
	}
}
