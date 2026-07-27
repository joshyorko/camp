//go:build linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/domain"
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

type richLifecycleWorker func(context.Context, app.ProgressReporter) richLifecycleWorkerResult
type richLifecycleRunner func(context.Context, io.Reader, io.Writer, setupui.Palette, map[string]setupui.Sprite, setupui.LifecycleWorkflow, <-chan presentation.RichLifecycleEvent, <-chan struct{}, func()) (setupui.Result, error)

type richLifecycleWorkerResult struct {
	recovery        string
	err             error
	failureStage    presentation.LifecycleStage
	terminalFailure bool
	noStageSuccess  string
}

type richLifecycleProgressReporter struct {
	ctx      context.Context
	events   chan<- presentation.RichLifecycleEvent
	stages   []presentation.LifecycleStage
	complete int
	selected bool
}

func (r *richLifecycleProgressReporter) Report(ctx context.Context, event app.ProgressEvent) error {
	stage, ok := richLifecycleStage(event.Stage)
	if ok {
		resumed, err := r.selectStage(stage)
		if err != nil {
			return err
		}
		if resumed {
			if err := r.send(ctx, presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleResumed, Stage: stage}); err != nil {
				return err
			}
		}
		if r.complete >= len(r.stages) || r.stages[r.complete] != stage {
			return fmt.Errorf("rich lifecycle progress %q arrived out of order", event.Stage)
		}
		message := lifecycleProgressMessage(event)
		if err := r.send(ctx, presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleCompleted, Stage: stage, Message: message}); err != nil {
			return err
		}
		r.complete++
		return nil
	}
	stage, ok = richLifecycleActivityStage(event.Stage)
	if !ok {
		return nil
	}
	resumed, err := r.selectStage(stage)
	if err != nil {
		return err
	}
	if resumed {
		if err := r.send(ctx, presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleResumed, Stage: stage}); err != nil {
			return err
		}
	}
	if r.complete >= len(r.stages) || r.stages[r.complete] != stage {
		return fmt.Errorf("rich lifecycle progress %q arrived out of order", event.Stage)
	}
	return r.send(ctx, presentation.RichLifecycleEvent{
		Kind: presentation.RichLifecycleActivity, Stage: stage, Message: lifecycleProgressMessage(event),
	})
}

func (r *richLifecycleProgressReporter) send(ctx context.Context, event presentation.RichLifecycleEvent) error {
	select {
	case r.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *richLifecycleProgressReporter) selectStage(stage presentation.LifecycleStage) (bool, error) {
	if r.selected {
		return false, nil
	}
	for i, candidate := range r.stages {
		if candidate == stage {
			r.stages = append([]presentation.LifecycleStage(nil), r.stages[i:]...)
			r.selected = true
			return i > 0, nil
		}
	}
	return false, fmt.Errorf("rich lifecycle stage %q is not part of the workflow", stage)
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

func richLifecycleActivityStage(stage app.ProgressStage) (presentation.LifecycleStage, bool) {
	switch stage {
	case app.ProgressWorkspaceClosed, app.ProgressForwardersStopped, app.ProgressServicesStopped,
		app.ProgressSupervisorStopped, app.ProgressLeaseReleased:
		return presentation.StageCleanup, true
	default:
		return "", false
	}
}

func richLifecycleAvailable(mode OutputMode, in io.Reader, out io.Writer, environment map[string]string, probe terminalProbe) bool {
	experience, width, height := resolveTerminalExperience(mode, out, environment, probe)
	if experience != presentation.TerminalColor || width < setupui.MinWidth || height < setupui.MinHeight {
		return false
	}
	input, ok := in.(*os.File)
	if !ok {
		return false
	}
	tty, _, _ := probe(input.Fd())
	return tty
}

func runRichLifecycleOperation(ctx context.Context, out io.Writer, workflow setupui.LifecycleWorkflow, worker richLifecycleWorker) (string, error) {
	return runRichLifecycleOperationWithRunner(ctx, os.Stdin, out, workflow, worker, setupui.RunLifecycle)
}

func runRichLifecycleOperationWithRunner(ctx context.Context, in io.Reader, out io.Writer, workflow setupui.LifecycleWorkflow, worker richLifecycleWorker, runner richLifecycleRunner) (string, error) {
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
		workerResult := worker(workerCtx, reporter)
		if workerResult.err != nil {
			if workerResult.terminalFailure {
				select {
				case events <- presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleTerminalFailed, Message: workerResult.err.Error(), RecoveryCommand: workerResult.recovery}:
				case <-workerCtx.Done():
				}
				result <- workerResult
				return
			}
			stage := reporter.expectedStage()
			if workerResult.failureStage != "" {
				stage = workerResult.failureStage
				if resumed, selectErr := reporter.selectStage(stage); selectErr == nil && resumed {
					_ = reporter.send(workerCtx, presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleResumed, Stage: stage})
				}
			}
			if stage == "" && len(reporter.stages) > 0 {
				stage = reporter.stages[len(reporter.stages)-1]
			}
			if stage != "" {
				select {
				case events <- presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleFailed, Stage: stage, Message: workerResult.err.Error(), RecoveryCommand: workerResult.recovery}:
				case <-workerCtx.Done():
				}
			}
		} else {
			message := ""
			if !reporter.selected {
				message = workerResult.noStageSuccess
			}
			select {
			case events <- presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleSucceeded, Message: message}:
			case <-workerCtx.Done():
				workerResult.err = workerCtx.Err()
			}
		}
		result <- workerResult
	}()
	uiResult, uiErr := runner(
		ctx, in, out, setupui.DefaultPalette(), sprites, workflow, events, workerDone, cancel,
	)
	workerResult := <-result
	return finishRichLifecycleResult(uiResult, uiErr, workerResult)
}

func finishRichLifecycleResult(uiResult setupui.Result, uiErr error, workerResult richLifecycleWorkerResult) (string, error) {
	if uiErr != nil {
		return workerResult.recovery, uiErr
	}
	if uiResult.Canceled {
		return workerResult.recovery, context.Canceled
	}
	if uiResult.Failed {
		recovery := uiResult.Recovery
		if recovery == "" {
			recovery = workerResult.recovery
		}
		if workerResult.err != nil {
			return recovery, workerResult.err
		}
		message := uiResult.FailMsg
		if message == "" {
			message = "rich lifecycle failed"
		}
		return recovery, errors.New(message)
	}
	return workerResult.recovery, workerResult.err
}

func richSyncOutcome(result app.CheckpointResult, runErr error, sessionID string) richLifecycleWorkerResult {
	outcome := richLifecycleWorkerResult{recovery: syncFailureRecovery(result, sessionID), err: runErr}
	if runErr != nil {
		return outcome
	}
	if result.RefreshError != "" {
		outcome.err = fmt.Errorf("refresh Hauler serving content: %s", result.RefreshError)
		outcome.failureStage = presentation.StagePointer
		return outcome
	}
	switch {
	case result.Disposition == app.CheckpointDispositionSkippedReadOnly:
		outcome.noStageSuccess = "read-only session unchanged"
	case result.Published:
		outcome.noStageSuccess = "checkpoint is published"
	}
	return outcome
}

func closeRichLifecycleStages(mode domain.SessionMode, discard bool) []presentation.LifecycleStage {
	if mode != domain.SessionReadWrite || discard {
		return []presentation.LifecycleStage{presentation.StageCleanup}
	}
	return []presentation.LifecycleStage{
		presentation.StageMirror, presentation.StageImageCapture, presentation.StageArchive,
		presentation.StageUpload, presentation.StagePointer, presentation.StageCleanup,
	}
}

func richCloseOutcome(result app.CloseResult, runErr error) richLifecycleWorkerResult {
	outcome := richLifecycleWorkerResult{recovery: result.RecoveryCommand, err: runErr}
	if runErr == nil && result.RefreshError != "" {
		outcome.err = fmt.Errorf("refresh Hauler serving content: %s", result.RefreshError)
		outcome.terminalFailure = result.CleanupSucceeded
		return outcome
	}
	if runErr == nil && result.CleanupSucceeded {
		outcome.noStageSuccess = "session is closed"
	}
	return outcome
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
