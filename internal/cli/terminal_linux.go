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
	"github.com/joshyorko/camp/internal/setupui"
	"golang.org/x/sys/unix"
)

type terminalProbe func(uintptr) (bool, int, int)

type lifecycleProgressReporter struct {
	mode       OutputMode
	out        io.Writer
	experience presentation.TerminalExperience
	operation  string
}

type richLifecycleWorker func(context.Context, app.ProgressReporter) (string, error)

type richLifecycleWorkerResult struct {
	recovery string
	err      error
}

type richLifecycleProgressReporter struct {
	ctx      context.Context
	events   chan<- presentation.RichLifecycleEvent
	stages   []presentation.LifecycleStage
	complete int
}

func (r *richLifecycleProgressReporter) Report(ctx context.Context, event app.ProgressEvent) error {
	stage, ok := richLifecycleStage(event.Stage)
	if !ok {
		return nil
	}
	if r.complete >= len(r.stages) || r.stages[r.complete] != stage {
		return fmt.Errorf("rich lifecycle progress %q arrived out of order", event.Stage)
	}
	message := lifecycleProgressMessage(event)
	select {
	case r.events <- presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleCompleted, Stage: stage, Message: message}:
		r.complete++
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *richLifecycleProgressReporter) expectedStage() presentation.LifecycleStage {
	if r.complete < len(r.stages) {
		return r.stages[r.complete]
	}
	return ""
}

func richLifecycleStage(stage app.ProgressStage) (presentation.LifecycleStage, bool) {
	switch stage {
	case app.ProgressWorkspacePrepared:
		return presentation.StageMirror, true
	case app.ProgressImagesCaptured:
		return presentation.StageImageCapture, true
	case app.ProgressGenerationBuilt:
		return presentation.StageArchive, true
	case app.ProgressGenerationUploaded:
		return presentation.StageUpload, true
	case app.ProgressGenerationPublished:
		return presentation.StagePointer, true
	case app.ProgressMaterializationRemoved, app.ProgressMaterializationPreserved:
		return presentation.StageCleanup, true
	default:
		return "", false
	}
}

func richLifecycleAvailable(mode OutputMode, out io.Writer, environment map[string]string, probe terminalProbe) bool {
	experience, width, height := resolveTerminalExperience(mode, out, environment, probe)
	return experience == presentation.TerminalColor && width >= setupui.MinWidth && height >= setupui.MinHeight
}

func runRichLifecycleOperation(ctx context.Context, out io.Writer, workflow setupui.LifecycleWorkflow, worker richLifecycleWorker) (string, error) {
	sprites, err := setupui.LoadSprites()
	if err != nil {
		return "", err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	events := make(chan presentation.RichLifecycleEvent, 32)
	workerDone := make(chan struct{})
	result := make(chan richLifecycleWorkerResult, 1)
	reporter := &richLifecycleProgressReporter{ctx: workerCtx, events: events, stages: append([]presentation.LifecycleStage(nil), workflow.Stages...)}
	go func() {
		defer close(workerDone)
		defer close(events)
		recovery, runErr := worker(workerCtx, reporter)
		if runErr != nil {
			stage := reporter.expectedStage()
			if stage == "" && len(reporter.stages) > 0 {
				stage = reporter.stages[len(reporter.stages)-1]
			}
			if stage != "" {
				select {
				case events <- presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleFailed, Stage: stage, Message: runErr.Error(), RecoveryCommand: recovery}:
				case <-workerCtx.Done():
				}
			}
		} else {
			select {
			case events <- presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleSucceeded}:
			case <-workerCtx.Done():
				runErr = workerCtx.Err()
			}
		}
		result <- richLifecycleWorkerResult{recovery: recovery, err: runErr}
	}()
	uiResult, uiErr := setupui.RunLifecycle(
		ctx, os.Stdin, out, setupui.DefaultPalette(), sprites, workflow, events, workerDone, cancel,
	)
	workerResult := <-result
	if uiErr != nil {
		return workerResult.recovery, uiErr
	}
	if uiResult.Canceled {
		return workerResult.recovery, context.Canceled
	}
	return workerResult.recovery, workerResult.err
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
	ci := strings.TrimSpace(environment["CI"])
	experience := presentation.SelectTerminalExperience(presentation.TerminalInput{
		TTY: tty, Width: width, TERM: environment["TERM"], COLORTERM: environment["COLORTERM"],
		JSON: mode == ModeJSON, CI: ci != "" && !strings.EqualFold(ci, "false") && ci != "0",
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
