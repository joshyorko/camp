package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/presentation"
	"github.com/joshyorko/camp/internal/setupui"
	"golang.org/x/sys/unix"
)

func TestResolveTerminalExperienceUsesOutputDescriptorAndEnvironment(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	probe := func(fd uintptr) (bool, int, int) {
		if fd != file.Fd() {
			t.Fatalf("fd = %d, want %d", fd, file.Fd())
		}
		return true, 120, 40
	}
	got, _, _ := resolveTerminalExperience(ModeHuman, file, map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, probe)
	if got != presentation.TerminalColor {
		t.Fatalf("resolveTerminalExperience() = %q, want color", got)
	}
}

func TestResolveTerminalExperienceDoesNotTreatNoColorAsMissingTerminalCapability(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	probe := func(uintptr) (bool, int, int) { return true, 120, 40 }
	got, _, _ := resolveTerminalExperience(ModeHuman, file, map[string]string{
		"TERM": "xterm-256color", "COLORTERM": "truecolor", "NO_COLOR": "1",
	}, probe)
	if got != presentation.TerminalColor {
		t.Fatalf("resolveTerminalExperience() = %q, want color", got)
	}
}

func TestResolveTerminalExperienceReturnsProbedHeightAlongsideWidth(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	probe := func(uintptr) (bool, int, int) { return true, 120, 40 }
	experience, width, height := resolveTerminalExperience(ModeHuman, file, map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, probe)
	if experience != presentation.TerminalColor || width != 120 || height != 40 {
		t.Fatalf("resolveTerminalExperience() = %q %d %d, want color 120 40", experience, width, height)
	}
}

func TestCanUseRichSetupRequires69x20Floor(t *testing.T) {
	if !canUseRichSetup(presentation.TerminalColor, 69, 20) {
		t.Fatal("69x20 should be enough for rich mode")
	}
	if canUseRichSetup(presentation.TerminalColor, 68, 20) {
		t.Fatal("68-column terminal should not enable rich mode")
	}
	if canUseRichSetup(presentation.TerminalColor, 69, 19) {
		t.Fatal("19-row terminal should not enable rich mode")
	}
}

func TestCanUseRichSetupRejectsNonRichTerminalExperience(t *testing.T) {
	if canUseRichSetup(presentation.TerminalPlain, 120, 40) {
		t.Fatal("plain terminal should not enable rich mode")
	}
}

func TestResolveTerminalExperienceTreatsNonFilesAndFallbackSignalsAsPlain(t *testing.T) {
	tests := []struct {
		name string
		mode OutputMode
		env  map[string]string
	}{
		{name: "buffer", mode: ModeHuman, env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}},
		{name: "json", mode: ModeJSON, env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}},
		{name: "ci", mode: ModeHuman, env: map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "CI": "true"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _, _ := resolveTerminalExperience(test.mode, &bytes.Buffer{}, test.env, func(uintptr) (bool, int, int) {
				t.Fatal("terminal probe called for non-file output")
				return true, 120, 40
			})
			if got != presentation.TerminalPlain {
				t.Fatalf("resolveTerminalExperience() = %q, want plain", got)
			}
		})
	}
}

func TestRichLifecycleAvailableRequiresInteractiveTrueColorAndSceneFloor(t *testing.T) {
	in, err := os.CreateTemp(t.TempDir(), "terminal-in")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.CreateTemp(t.TempDir(), "terminal-out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	env := map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}
	if !richLifecycleAvailable(ModeHuman, in, out, env, func(uintptr) (bool, int, int) { return true, 120, 40 }) {
		t.Fatal("interactive true-color terminal should enable rich lifecycle")
	}
	for _, probe := range []terminalProbe{
		func(uintptr) (bool, int, int) { return true, 68, 40 },
		func(uintptr) (bool, int, int) { return true, 120, 19 },
		func(uintptr) (bool, int, int) { return false, 120, 40 },
	} {
		if richLifecycleAvailable(ModeHuman, in, out, env, probe) {
			t.Fatal("insufficient terminal capability enabled rich lifecycle")
		}
	}
	if richLifecycleAvailable(ModeJSON, in, out, env, func(uintptr) (bool, int, int) { return true, 120, 40 }) {
		t.Fatal("JSON output enabled rich lifecycle")
	}
}

func TestRichLifecycleAvailableRequiresBothTerminalDescriptors(t *testing.T) {
	in, err := os.CreateTemp(t.TempDir(), "terminal-in")
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.CreateTemp(t.TempDir(), "terminal-out")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	env := map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}
	probe := func(fd uintptr) (bool, int, int) {
		if fd == in.Fd() {
			return false, 0, 0
		}
		return true, 120, 40
	}
	if richLifecycleAvailable(ModeHuman, in, out, env, probe) {
		t.Fatal("rich lifecycle accepted redirected stdin with terminal stdout")
	}
}

func TestRichLifecycleAvailableWithRealPTYDescriptors(t *testing.T) {
	in := openTestPTY(t, 120, 40)
	out := openTestPTY(t, 120, 40)
	env := map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}
	if !richLifecycleAvailable(ModeHuman, in, out, env, probeTerminal) {
		t.Fatal("rich lifecycle rejected capable stdin and stdout PTYs")
	}
	redirected, err := os.CreateTemp(t.TempDir(), "redirected-input")
	if err != nil {
		t.Fatal(err)
	}
	defer redirected.Close()
	if richLifecycleAvailable(ModeHuman, redirected, out, env, probeTerminal) {
		t.Fatal("rich lifecycle accepted redirected input with a real output PTY")
	}
}

func openTestPTY(t *testing.T, width, height uint16) *os.File {
	t.Helper()
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("open /dev/ptmx: %v", err)
	}
	master := os.NewFile(uintptr(masterFD), "/dev/ptmx")
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock PTY: %v", err)
	}
	number, err := unix.IoctlGetInt(masterFD, unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("get PTY number: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open PTY slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: width, Row: height}); err != nil {
		t.Fatalf("set PTY size: %v", err)
	}
	return slave
}

func TestRichLifecycleProgressReporterEmitsOrderedTypedFacts(t *testing.T) {
	events := make(chan presentation.RichLifecycleEvent, 2)
	reporter := &richLifecycleProgressReporter{
		events: events,
		stages: []presentation.LifecycleStage{presentation.StageMirror, presentation.StageImageCapture},
	}
	for _, event := range []app.ProgressEvent{
		{Stage: app.ProgressWorkspacePrepared},
		{Stage: app.ProgressRegistrySealed},
		{Stage: app.ProgressImagesCaptured, ImageCount: 2},
	} {
		if err := reporter.Report(context.Background(), event); err != nil {
			t.Fatalf("Report(%q): %v", event.Stage, err)
		}
	}
	for _, want := range []presentation.LifecycleStage{presentation.StageMirror, presentation.StageImageCapture} {
		got := <-events
		if got.Kind != presentation.RichLifecycleCompleted || got.Stage != want {
			t.Fatalf("event = %#v, want completed %q", got, want)
		}
	}
	if got := reporter.expectedStage(); got != "" {
		t.Fatalf("expectedStage() = %q, want empty", got)
	}
}

func TestRichLifecycleProgressReporterRejectsOutOfOrderFacts(t *testing.T) {
	reporter := &richLifecycleProgressReporter{
		events: make(chan presentation.RichLifecycleEvent, 2),
		stages: []presentation.LifecycleStage{presentation.StageMirror, presentation.StageImageCapture},
	}
	if err := reporter.Report(context.Background(), app.ProgressEvent{Stage: app.ProgressWorkspacePrepared}); err != nil {
		t.Fatalf("Report(workspace prepared): %v", err)
	}
	err := reporter.Report(context.Background(), app.ProgressEvent{Stage: app.ProgressWorkspacePrepared})
	if err == nil {
		t.Fatal("Report() accepted duplicate mirror completion")
	}
}

func TestRichLifecycleProgressReporterAcceptsTruthfulResumedSuffix(t *testing.T) {
	events := make(chan presentation.RichLifecycleEvent, 3)
	reporter := &richLifecycleProgressReporter{
		events: events,
		stages: []presentation.LifecycleStage{
			presentation.StageMirror, presentation.StageImageCapture, presentation.StageArchive,
			presentation.StageUpload, presentation.StagePointer,
		},
	}
	for _, event := range []app.ProgressEvent{
		{Stage: app.ProgressGenerationUploaded, Generation: 7},
		{Stage: app.ProgressGenerationPublished, Generation: 7},
	} {
		if err := reporter.Report(context.Background(), event); err != nil {
			t.Fatalf("Report(%q): %v", event.Stage, err)
		}
	}
	resumed := <-events
	if resumed.Kind != presentation.RichLifecycleResumed || resumed.Stage != presentation.StageUpload {
		t.Fatalf("resume event = %#v, want upload suffix", resumed)
	}
	for _, want := range []presentation.LifecycleStage{presentation.StageUpload, presentation.StagePointer} {
		got := <-events
		if got.Kind != presentation.RichLifecycleCompleted || got.Stage != want {
			t.Fatalf("event = %#v, want completed %q", got, want)
		}
	}
	if len(reporter.stages) != 2 || reporter.stages[0] != presentation.StageUpload {
		t.Fatalf("reporter stages = %#v, want truthful upload/pointer suffix", reporter.stages)
	}
}

func TestRichLifecycleProgressReporterUsesCleanupActivityWithoutDuplicateCompletion(t *testing.T) {
	events := make(chan presentation.RichLifecycleEvent, 4)
	reporter := &richLifecycleProgressReporter{
		events: events,
		stages: []presentation.LifecycleStage{
			presentation.StageMirror, presentation.StageImageCapture, presentation.StageArchive,
			presentation.StageUpload, presentation.StagePointer, presentation.StageCleanup,
		},
	}
	for _, event := range []app.ProgressEvent{
		{Stage: app.ProgressWorkspaceClosed},
		{Stage: app.ProgressServicesStopped},
		{Stage: app.ProgressMaterializationRemoved},
	} {
		if err := reporter.Report(context.Background(), event); err != nil {
			t.Fatalf("Report(%q): %v", event.Stage, err)
		}
	}
	for i, wantKind := range []presentation.RichLifecycleEventKind{
		presentation.RichLifecycleResumed,
		presentation.RichLifecycleActivity,
		presentation.RichLifecycleActivity,
		presentation.RichLifecycleCompleted,
	} {
		got := <-events
		if got.Kind != wantKind || got.Stage != presentation.StageCleanup {
			t.Fatalf("event %d = %#v, want kind %d cleanup", i, got, wantKind)
		}
	}
	if err := reporter.Report(context.Background(), app.ProgressEvent{Stage: app.ProgressMaterializationPreserved}); err == nil {
		t.Fatal("reporter accepted duplicate cleanup completion")
	}
}

func TestFinishRichLifecycleResultPropagatesUIFailureAndRecovery(t *testing.T) {
	worker := richLifecycleWorkerResult{recovery: "camp recover session-1"}
	recovery, err := finishRichLifecycleResult(setupui.Result{
		Failed: true, FailMsg: "lifecycle completion arrived out of order", Recovery: "camp sync --session session-1",
	}, nil, worker)
	if err == nil || err.Error() != "lifecycle completion arrived out of order" {
		t.Fatalf("finishRichLifecycleResult error = %v", err)
	}
	if recovery != "camp sync --session session-1" {
		t.Fatalf("recovery = %q, want UI recovery", recovery)
	}
}

func TestRichSyncOutcomeTreatsServingRefreshFailureAsTerminal(t *testing.T) {
	outcome := richSyncOutcome(app.CheckpointResult{
		Published: true, RefreshError: "refresh helper exited", RecoveryCommand: "camp recover session-1",
	}, nil, "session-1")
	if outcome.err == nil || !strings.Contains(outcome.err.Error(), "refresh helper exited") {
		t.Fatalf("outcome error = %v", outcome.err)
	}
	if outcome.failureStage != presentation.StagePointer {
		t.Fatalf("failure stage = %q, want pointer", outcome.failureStage)
	}
	if outcome.recovery != "camp recover session-1" {
		t.Fatalf("recovery = %q", outcome.recovery)
	}
}

func TestRichSyncOutcomeNamesReadOnlyAndPublishedNoStageResultsWithoutInventingWork(t *testing.T) {
	tests := []struct {
		name   string
		result app.CheckpointResult
		want   string
	}{
		{name: "read-only", result: app.CheckpointResult{Disposition: app.CheckpointDispositionSkippedReadOnly}, want: "read-only session unchanged"},
		{name: "already-published", result: app.CheckpointResult{Disposition: app.CheckpointDispositionPublished, Published: true}, want: "checkpoint is published"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := richSyncOutcome(test.result, nil, "session-1")
			if outcome.noStageSuccess != test.want {
				t.Fatalf("no-stage success = %q, want %q", outcome.noStageSuccess, test.want)
			}
		})
	}
}

func TestCloseRichLifecycleStagesDoNotInventCheckpointWork(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    domain.SessionMode
		discard bool
	}{
		{name: "read-only", mode: domain.SessionReadOnly},
		{name: "discard", mode: domain.SessionReadWrite, discard: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := closeRichLifecycleStages(test.mode, test.discard)
			want := []presentation.LifecycleStage{presentation.StageCleanup}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("stages = %#v, want cleanup only", got)
			}
		})
	}
}

func TestRichCloseOutcomeDoesNotRenderRefreshFailureOrAlreadyClosedWorkAsFreshSuccess(t *testing.T) {
	failed := richCloseOutcome(app.CloseResult{
		RefreshError: "serving refresh remains pending", RecoveryCommand: "camp recover session-1",
	}, nil)
	if failed.err == nil || !strings.Contains(failed.err.Error(), "serving refresh remains pending") {
		t.Fatalf("refresh outcome error = %v", failed.err)
	}
	if failed.noStageSuccess != "" {
		t.Fatalf("refresh failure success text = %q", failed.noStageSuccess)
	}

	closed := richCloseOutcome(app.CloseResult{CleanupSucceeded: true}, nil)
	if closed.err != nil || closed.noStageSuccess != "session is closed" {
		t.Fatalf("already-closed outcome = %#v", closed)
	}
}

func TestRunRichLifecycleOperationProductionBoundaryUsesOnlyResumedSuffix(t *testing.T) {
	workflow := setupui.LifecycleWorkflow{
		Operation: "sync",
		Stages: []presentation.LifecycleStage{
			presentation.StageMirror, presentation.StageImageCapture, presentation.StageArchive,
			presentation.StageUpload, presentation.StagePointer,
		},
	}
	var events []presentation.RichLifecycleEvent
	runner := recordingRichLifecycleRunner(&events, nil)
	recovery, err := runRichLifecycleOperationWithRunner(
		context.Background(), strings.NewReader(""), &bytes.Buffer{}, workflow,
		func(ctx context.Context, reporter app.ProgressReporter) richLifecycleWorkerResult {
			for _, event := range []app.ProgressEvent{
				{Stage: app.ProgressGenerationUploaded, Generation: 7},
				{Stage: app.ProgressGenerationPublished, Generation: 7},
			} {
				if reportErr := reporter.Report(ctx, event); reportErr != nil {
					return richLifecycleWorkerResult{err: reportErr}
				}
			}
			return richSyncOutcome(app.CheckpointResult{
				Disposition: app.CheckpointDispositionPublished, Published: true,
			}, nil, "session-1")
		},
		runner,
	)
	if err != nil || recovery != "camp sync --session session-1" {
		t.Fatalf("run result = (%q, %v)", recovery, err)
	}
	want := []presentation.RichLifecycleEventKind{
		presentation.RichLifecycleResumed,
		presentation.RichLifecycleCompleted,
		presentation.RichLifecycleCompleted,
		presentation.RichLifecycleSucceeded,
	}
	if got := richLifecycleEventKinds(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("event kinds = %#v, want %#v", got, want)
	}
	if events[0].Stage != presentation.StageUpload {
		t.Fatalf("resume stage = %q, want upload", events[0].Stage)
	}
	for _, event := range events {
		if event.Stage == presentation.StageMirror || event.Stage == presentation.StageImageCapture || event.Stage == presentation.StageArchive {
			t.Fatalf("resumed production boundary invented prefix event %#v", event)
		}
	}
}

func TestRunRichLifecycleOperationProductionBoundaryRefreshFailureHasOneTerminalRecovery(t *testing.T) {
	workflow := setupui.LifecycleWorkflow{
		Operation: "sync",
		Stages:    []presentation.LifecycleStage{presentation.StageMirror, presentation.StagePointer},
	}
	var events []presentation.RichLifecycleEvent
	runner := recordingRichLifecycleRunner(&events, nil)
	recovery, err := runRichLifecycleOperationWithRunner(
		context.Background(), strings.NewReader(""), &bytes.Buffer{}, workflow,
		func(ctx context.Context, reporter app.ProgressReporter) richLifecycleWorkerResult {
			for _, event := range []app.ProgressEvent{
				{Stage: app.ProgressWorkspacePrepared},
				{Stage: app.ProgressGenerationPublished, Generation: 4},
			} {
				if reportErr := reporter.Report(ctx, event); reportErr != nil {
					return richLifecycleWorkerResult{err: reportErr}
				}
			}
			return richSyncOutcome(app.CheckpointResult{
				Published: true, RefreshError: "serving refresh remains pending",
				RecoveryCommand: "camp recover session-1",
			}, nil, "session-1")
		},
		runner,
	)
	if err == nil || !strings.Contains(err.Error(), "serving refresh remains pending") {
		t.Fatalf("run error = %v", err)
	}
	if recovery != "camp recover session-1" {
		t.Fatalf("recovery = %q", recovery)
	}
	var failed, succeeded, recoveries int
	for _, event := range events {
		switch event.Kind {
		case presentation.RichLifecycleFailed:
			failed++
			if event.RecoveryCommand != "" {
				recoveries++
			}
		case presentation.RichLifecycleSucceeded:
			succeeded++
		}
	}
	if failed != 1 || succeeded != 0 || recoveries != 1 {
		t.Fatalf("terminal events: failed=%d succeeded=%d recoveries=%d, events=%#v", failed, succeeded, recoveries, events)
	}
}

func TestRunRichLifecycleOperationProductionBoundaryPropagatesModelFailure(t *testing.T) {
	workflow := setupui.LifecycleWorkflow{
		Operation: "sync", Stages: []presentation.LifecycleStage{presentation.StageMirror},
	}
	runner := recordingRichLifecycleRunner(nil, func(result setupui.Result) setupui.Result {
		result.Failed = true
		result.FailMsg = "presentation protocol failed"
		result.Recovery = ""
		return result
	})
	_, err := runRichLifecycleOperationWithRunner(
		context.Background(), strings.NewReader(""), &bytes.Buffer{}, workflow,
		func(ctx context.Context, reporter app.ProgressReporter) richLifecycleWorkerResult {
			if reportErr := reporter.Report(ctx, app.ProgressEvent{Stage: app.ProgressWorkspacePrepared}); reportErr != nil {
				return richLifecycleWorkerResult{err: reportErr}
			}
			return richLifecycleWorkerResult{}
		},
		runner,
	)
	if err == nil || err.Error() != "presentation protocol failed" {
		t.Fatalf("run error = %v", err)
	}
}

func TestRunRichLifecycleOperationProductionBoundaryNoStageAndCleanupRecoveryCases(t *testing.T) {
	tests := []struct {
		name        string
		workflow    setupui.LifecycleWorkflow
		worker      richLifecycleWorker
		wantKinds   []presentation.RichLifecycleEventKind
		wantMessage string
	}{
		{
			name: "read-only-sync",
			workflow: setupui.LifecycleWorkflow{
				Operation: "sync", Stages: []presentation.LifecycleStage{presentation.StageMirror, presentation.StagePointer},
			},
			worker: func(context.Context, app.ProgressReporter) richLifecycleWorkerResult {
				return richSyncOutcome(app.CheckpointResult{Disposition: app.CheckpointDispositionSkippedReadOnly}, nil, "session-1")
			},
			wantKinds:   []presentation.RichLifecycleEventKind{presentation.RichLifecycleSucceeded},
			wantMessage: "read-only session unchanged",
		},
		{
			name: "already-closed-close",
			workflow: setupui.LifecycleWorkflow{
				Operation: "close", Stages: []presentation.LifecycleStage{presentation.StageCleanup},
			},
			worker: func(context.Context, app.ProgressReporter) richLifecycleWorkerResult {
				return richCloseOutcome(app.CloseResult{CleanupSucceeded: true}, nil)
			},
			wantKinds:   []presentation.RichLifecycleEventKind{presentation.RichLifecycleSucceeded},
			wantMessage: "session is closed",
		},
		{
			name: "resumed-serving-refresh",
			workflow: setupui.LifecycleWorkflow{
				Operation: "sync", Stages: []presentation.LifecycleStage{presentation.StageMirror, presentation.StagePointer},
			},
			worker: func(context.Context, app.ProgressReporter) richLifecycleWorkerResult {
				return richSyncOutcome(app.CheckpointResult{
					Disposition: app.CheckpointDispositionPublished, Published: true,
				}, nil, "session-1")
			},
			wantKinds:   []presentation.RichLifecycleEventKind{presentation.RichLifecycleSucceeded},
			wantMessage: "checkpoint is published",
		},
		{
			name: "resumed-cleanup",
			workflow: setupui.LifecycleWorkflow{
				Operation: "close",
				Stages: []presentation.LifecycleStage{
					presentation.StageMirror, presentation.StagePointer, presentation.StageCleanup,
				},
			},
			worker: func(ctx context.Context, reporter app.ProgressReporter) richLifecycleWorkerResult {
				for _, event := range []app.ProgressEvent{
					{Stage: app.ProgressWorkspaceClosed},
					{Stage: app.ProgressMaterializationRemoved},
				} {
					if err := reporter.Report(ctx, event); err != nil {
						return richLifecycleWorkerResult{err: err}
					}
				}
				return richCloseOutcome(app.CloseResult{CleanupSucceeded: true}, nil)
			},
			wantKinds: []presentation.RichLifecycleEventKind{
				presentation.RichLifecycleResumed,
				presentation.RichLifecycleActivity,
				presentation.RichLifecycleCompleted,
				presentation.RichLifecycleSucceeded,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var events []presentation.RichLifecycleEvent
			_, err := runRichLifecycleOperationWithRunner(
				context.Background(), strings.NewReader(""), &bytes.Buffer{}, test.workflow, test.worker,
				recordingRichLifecycleRunner(&events, nil),
			)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := richLifecycleEventKinds(events); !reflect.DeepEqual(got, test.wantKinds) {
				t.Fatalf("event kinds = %#v, want %#v", got, test.wantKinds)
			}
			if test.wantMessage != "" && events[len(events)-1].Message != test.wantMessage {
				t.Fatalf("terminal message = %q, want %q", events[len(events)-1].Message, test.wantMessage)
			}
		})
	}
}

func TestRunRichLifecycleOperationJoinsNonCancelableWorkerAfterUICancel(t *testing.T) {
	release := make(chan struct{})
	workerExited := false
	runner := func(_ context.Context, _ io.Reader, _ io.Writer, _ setupui.Palette, _ map[string]setupui.Sprite, _ setupui.LifecycleWorkflow, events <-chan presentation.RichLifecycleEvent, _ <-chan struct{}, onExit func()) (setupui.Result, error) {
		onExit()
		close(release)
		for range events {
		}
		return setupui.Result{Canceled: true}, nil
	}
	_, err := runRichLifecycleOperationWithRunner(
		context.Background(), strings.NewReader(""), &bytes.Buffer{},
		setupui.LifecycleWorkflow{Operation: "sync", Stages: []presentation.LifecycleStage{presentation.StagePointer}},
		func(context.Context, app.ProgressReporter) richLifecycleWorkerResult {
			<-release
			workerExited = true
			return richLifecycleWorkerResult{}
		},
		runner,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want canceled", err)
	}
	if !workerExited {
		t.Fatal("rich lifecycle returned before non-cancelable worker exited")
	}
}

func recordingRichLifecycleRunner(recorded *[]presentation.RichLifecycleEvent, mutate func(setupui.Result) setupui.Result) richLifecycleRunner {
	return func(_ context.Context, _ io.Reader, _ io.Writer, _ setupui.Palette, sprites map[string]setupui.Sprite, workflow setupui.LifecycleWorkflow, input <-chan presentation.RichLifecycleEvent, _ <-chan struct{}, _ func()) (setupui.Result, error) {
		model := setupui.NewLifecycleModel(setupui.DefaultPalette(), sprites, workflow)
		for event := range input {
			if recorded != nil {
				*recorded = append(*recorded, event)
			}
			next, _ := model.Update(event)
			model = next.(setupui.LifecycleModel)
		}
		failed, message, recovery := model.Failed()
		result := setupui.Result{Canceled: model.Canceled(), Failed: failed, FailMsg: message, Recovery: recovery}
		if mutate != nil {
			result = mutate(result)
		}
		return result, nil
	}
}

func richLifecycleEventKinds(events []presentation.RichLifecycleEvent) []presentation.RichLifecycleEventKind {
	kinds := make([]presentation.RichLifecycleEventKind, len(events))
	for i, event := range events {
		kinds[i] = event.Kind
	}
	return kinds
}

func TestWriteLifecycleEventsUsesCompletedOpenSyncAndCloseState(t *testing.T) {
	tests := []struct {
		operation string
		events    []presentation.LifecycleEvent
		want      string
	}{
		{operation: "open", events: openTerminalEvents("brain", "session-1"), want: "open: workspace brain is ready\nopen: opened brain (session-1)\n"},
		{operation: "sync", events: syncTerminalEvents(42), want: "sync: published generation 42\nsync: sync complete\n"},
		{operation: "close", events: closeTerminalEvents(43, true), want: "close: published generation 43\nclose: cleanup complete\nclose: session closed\n"},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeLifecycleEvents(&output, presentation.TerminalPlain, test.operation, test.events...); err != nil {
				t.Fatalf("writeLifecycleEvents: %v", err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCloseDiscardTerminalEventsDoNotClaimPublication(t *testing.T) {
	got := closeDiscardTerminalEvents()
	want := []presentation.LifecycleEvent{
		{Stage: presentation.StageCleanupComplete, Message: "cleanup complete"},
		{Stage: presentation.StageComplete, Message: "session discarded and closed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
}

func TestLifecycleProgressReporterStreamsTruthfulPlainStages(t *testing.T) {
	var output bytes.Buffer
	reporter := newLifecycleProgressReporter(ModeHuman, &output, presentation.TerminalPlain, "close")
	if err := reporter.Report(context.Background(), app.ProgressEvent{Stage: app.ProgressImagesCaptured, ImageCount: 2}); err != nil {
		t.Fatal(err)
	}
	if err := reporter.Report(context.Background(), app.ProgressEvent{Stage: app.ProgressGenerationBuilt, Generation: 3, Bytes: 8078231}); err != nil {
		t.Fatal(err)
	}
	want := "close: captured 2 OCI images\nclose: built generation 3 (7.7 MiB)\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestLifecycleProgressReporterIsSilentForJSON(t *testing.T) {
	var output bytes.Buffer
	reporter := newLifecycleProgressReporter(ModeJSON, &output, presentation.TerminalPlain, "close")
	if err := reporter.Report(context.Background(), app.ProgressEvent{Stage: app.ProgressGenerationPublished, Generation: 3}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("JSON progress output = %q", output.String())
	}
}

func TestWriteLifecycleEventsDoesNotRunForJSON(t *testing.T) {
	var output bytes.Buffer
	if err := writeHumanLifecycleResult(&output, ModeJSON, "sync", syncTerminalEvents(42), "legacy\n"); err != nil {
		t.Fatalf("writeHumanLifecycleResult: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("JSON helper wrote %q", output.String())
	}
}

func TestLifecycleFailurePreservesCauseAndOneRecoveryCommand(t *testing.T) {
	cause := errors.New("checkpoint upload failed")
	err := lifecycleFailure(cause, "camp recover session-1")
	if !errors.Is(err, cause) {
		t.Fatalf("lifecycleFailure does not wrap cause: %v", err)
	}
	if err.Failure.Message != cause.Error() || len(err.Failure.NextCommands) != 1 || err.Failure.NextCommands[0] != "camp recover session-1" {
		t.Fatalf("failure = %#v", err.Failure)
	}
}

func TestSyncFailureRecoveryRetriesSyncBeforeCheckpointRecoveryExists(t *testing.T) {
	t.Parallel()
	if got := syncFailureRecovery(app.CheckpointResult{}, "session with space"); got != "camp sync --session 'session with space'" {
		t.Fatalf("syncFailureRecovery() = %q", got)
	}
	if got := syncFailureRecovery(app.CheckpointResult{RecoveryCommand: "camp recover session-1"}, "session-1"); got != "camp recover session-1" {
		t.Fatalf("syncFailureRecovery() = %q", got)
	}
}
