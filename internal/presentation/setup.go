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
	next       int
}

func NewSetupAnimator(writer io.Writer, experience TerminalExperience, model CampsiteModel) (*SetupAnimator, error) {
	if writer == nil {
		return nil, fmt.Errorf("setup animator writer is nil")
	}
	if err := validateCampsiteModel(model); err != nil {
		return nil, err
	}
	return &SetupAnimator{writer: writer, experience: experience, model: model}, nil
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
	if a.experience == TerminalColor {
		output = "\x1b[2J\x1b[H" + renderSetupColorFrame(a.model, completed)
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

func renderSetupColorFrame(model CampsiteModel, completed int) string {
	if completed == len(setupWaypoints) {
		return renderColorCampsite(model)
	}
	const (
		reset = "\x1b[0m"
		sky   = "\x1b[38;2;78;163;235m"
		pine  = "\x1b[38;2;69;119;74m"
		amber = "\x1b[38;2;255;171;45m"
		dim   = "\x1b[38;2;110;118;129m"
	)
	labels := []string{"TOOLCHAIN", "RUNTIME", "CAPSULE", "STORAGE"}
	markers := make([]string, len(labels))
	for index, label := range labels {
		if index < completed {
			markers[index] = "✓ " + label
		} else {
			markers[index] = "○ " + label
		}
	}
	detail := renderSetupPlainEvent(model, setupWaypoints[completed-1])
	detail = strings.TrimSpace(strings.TrimPrefix(detail, "setup: "))
	return fmt.Sprintf(
		"%s        ·          ✦                ·            ✦          ·%s\n"+
			"%s      /\\       /\\          /\\        /\\       /\\%s\n"+
			"%s  %s        %s        %s        %s%s\n"+
			"%s  ━━━━━━━━◆━━━━━━━━━━◆━━━━━━━━━━◆━━━━━━━━━━○%s\n\n"+
			"%s  %s%s\n",
		sky, reset, pine, reset, dim, markers[0], markers[1], markers[2], markers[3], reset, amber, reset, amber, detail, reset,
	)
}
