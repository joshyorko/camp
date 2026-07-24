//go:build linux

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/presentation"
	"golang.org/x/sys/unix"
)

type terminalProbe func(uintptr) (bool, int, int)

type lifecycleProgressReporter struct {
	mode       OutputMode
	out        io.Writer
	experience presentation.TerminalExperience
	operation  string
}

func newLifecycleProgressReporter(mode OutputMode, out io.Writer, experience presentation.TerminalExperience, operation string) *lifecycleProgressReporter {
	return &lifecycleProgressReporter{mode: mode, out: out, experience: experience, operation: operation}
}

func productionLifecycleProgressReporter(mode OutputMode, out io.Writer, operation string) app.ProgressReporter {
	experience, _, _ := resolveTerminalExperience(mode, out, environmentMap(os.Environ()), probeTerminal)
	return newLifecycleProgressReporter(mode, out, experience, operation)
}

func (r *lifecycleProgressReporter) Report(ctx context.Context, event app.ProgressEvent) error {
	if r == nil || r.mode != ModeHuman {
		return nil
	}
	message := lifecycleProgressMessage(event)
	if message == "" {
		return nil
	}
	return presentation.NewTerminalSession(r.out, r.experience, r.operation).Emit(ctx, presentation.LifecycleEvent{
		Stage: presentation.StageStarted, Message: message,
	})
}

func lifecycleProgressMessage(event app.ProgressEvent) string {
	switch event.Stage {
	case app.ProgressWorkspacePrepared:
		return "prepared workspace snapshot"
	case app.ProgressImagesCaptured:
		return fmt.Sprintf("captured %d OCI images", event.ImageCount)
	case app.ProgressRegistrySealed:
		return "sealed immutable registry snapshot"
	case app.ProgressGenerationBuilt:
		return fmt.Sprintf("built generation %d (%s)", event.Generation, formatIECBytes(event.Bytes))
	case app.ProgressGenerationUploaded:
		return fmt.Sprintf("verified generation %d in durable storage", event.Generation)
	case app.ProgressGenerationPublished:
		return fmt.Sprintf("published generation %d", event.Generation)
	case app.ProgressServingRefreshed:
		return "refreshed Hauler serving content"
	case app.ProgressWorkspaceClosed:
		return "removed workspace"
	case app.ProgressForwardersStopped:
		return "stopped forwarded services"
	case app.ProgressServicesStopped:
		return "stopped Hauler services"
	case app.ProgressSupervisorStopped:
		return "stopped session supervisor"
	case app.ProgressLeaseReleased:
		return "released writer lease"
	case app.ProgressMaterializationRemoved:
		return "removed owned materialization"
	case app.ProgressMaterializationPreserved:
		return "preserved adopted source"
	default:
		return ""
	}
}

func formatIECBytes(value int64) string {
	const mib = 1024 * 1024
	if value >= mib {
		return fmt.Sprintf("%.1f MiB", float64(value)/mib)
	}
	return fmt.Sprintf("%d B", value)
}

func resolveTerminalExperience(mode OutputMode, out io.Writer, environment map[string]string, probe terminalProbe) (presentation.TerminalExperience, int, int) {
	file, ok := out.(*os.File)
	if !ok {
		return presentation.TerminalPlain, 0, 0
	}
	tty, width, height := probe(file.Fd())
	_, noColor := environment["NO_COLOR"]
	ci := strings.TrimSpace(environment["CI"])
	experience := presentation.SelectTerminalExperience(presentation.TerminalInput{
		TTY: tty, Width: width, TERM: environment["TERM"], COLORTERM: environment["COLORTERM"],
		JSON: mode == ModeJSON, NoColor: noColor, CI: ci != "" && !strings.EqualFold(ci, "false") && ci != "0",
	})
	return experience, width, height
}

func probeTerminal(fd uintptr) (bool, int, int) {
	winsize, err := unix.IoctlGetWinsize(int(fd), unix.TIOCGWINSZ)
	if err != nil || winsize.Col == 0 {
		return false, 0, 0
	}
	return true, int(winsize.Col), int(winsize.Row)
}

func writeLifecycleEvents(out io.Writer, experience presentation.TerminalExperience, operation string, events ...presentation.LifecycleEvent) error {
	session := presentation.NewTerminalSession(out, experience, operation)
	for _, event := range events {
		if err := session.Emit(context.Background(), event); err != nil {
			return err
		}
	}
	return nil
}

func writeHumanLifecycleResult(out io.Writer, mode OutputMode, operation string, events []presentation.LifecycleEvent, legacy string) error {
	if mode != ModeHuman {
		return nil
	}
	experience, _, _ := resolveTerminalExperience(mode, out, environmentMap(os.Environ()), probeTerminal)
	if len(events) == 0 {
		_, err := io.WriteString(out, legacy)
		return err
	}
	return writeLifecycleEvents(out, experience, operation, events...)
}

func openTerminalEvents(capsule, sessionID string) []presentation.LifecycleEvent {
	return []presentation.LifecycleEvent{
		{Stage: presentation.StageWorkspaceReady, Message: fmt.Sprintf("workspace %s is ready", capsule)},
		{Stage: presentation.StageComplete, Message: fmt.Sprintf("opened %s (%s)", capsule, sessionID)},
	}
}

func syncTerminalEvents(generation uint64) []presentation.LifecycleEvent {
	return []presentation.LifecycleEvent{
		{Stage: presentation.StageGenerationPublished, Message: fmt.Sprintf("published generation %d", generation)},
		{Stage: presentation.StageComplete, Message: "sync complete"},
	}
}

func closeTerminalEvents(generation uint64, cleanupSucceeded bool) []presentation.LifecycleEvent {
	events := []presentation.LifecycleEvent{{Stage: presentation.StageGenerationPublished, Message: fmt.Sprintf("published generation %d", generation)}}
	if cleanupSucceeded {
		events = append(events, presentation.LifecycleEvent{Stage: presentation.StageCleanupComplete, Message: "cleanup complete"})
	}
	return append(events, presentation.LifecycleEvent{Stage: presentation.StageComplete, Message: "session closed"})
}

func closeDiscardTerminalEvents() []presentation.LifecycleEvent {
	return []presentation.LifecycleEvent{
		{Stage: presentation.StageCleanupComplete, Message: "cleanup complete"},
		{Stage: presentation.StageComplete, Message: "session discarded and closed"},
	}
}
