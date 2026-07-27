//go:build linux

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/presentation"
	"github.com/joshyorko/camp/internal/setupui"
)

func TestExecuteProductionRichSyncFailureRendersOnceAndExitsNonzero(t *testing.T) {
	lifecycle := &ProductionLifecycle{
		prepareSync: func(context.Context) (productionSyncRun, error) {
			return productionSyncRun{
				sessionID: "session-1",
				run: func(ctx context.Context, reporter app.ProgressReporter) (app.CheckpointResult, error) {
					if err := reporter.Report(ctx, app.ProgressEvent{Stage: app.ProgressWorkspacePrepared}); err != nil {
						return app.CheckpointResult{}, err
					}
					return app.CheckpointResult{RecoveryCommand: "camp recover session-1"}, errors.New("checkpoint upload failed")
				},
			}, nil
		},
		richAvailable: func(OutputMode, io.Reader, io.Writer, map[string]string, terminalProbe) bool { return true },
		richRunner: func(_ context.Context, _ io.Reader, out io.Writer, _ setupui.Palette, _ map[string]setupui.Sprite, _ setupui.LifecycleWorkflow, events <-chan presentation.RichLifecycleEvent, _ <-chan struct{}, _ func()) (setupui.Result, error) {
			for event := range events {
				if event.Kind == presentation.RichLifecycleFailed {
					_, _ = io.WriteString(out, "rich failure: "+event.Message+"\nnext: "+event.RecoveryCommand+"\n")
					return setupui.Result{Failed: true, FailMsg: event.Message, Recovery: event.RecoveryCommand}, nil
				}
			}
			return setupui.Result{}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"sync"}, Streams{
		In: strings.NewReader(""), Out: &stdout, ErrOut: &stderr,
	})
	if code != int(ExitFailure) {
		t.Fatalf("exit = %d, want failure", code)
	}
	if got := stdout.String(); got != "rich failure: checkpoint upload failed\nnext: camp recover session-1\n" {
		t.Fatalf("stdout = %q", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("duplicate stderr failure = %q", stderr.String())
	}
}

func TestExecuteProductionRichSyncPreRenderFailuresUseNormalEnvelope(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*ProductionLifecycle)
		wantMessage string
		wantNext    string
	}{
		{
			name: "lifecycle-setup",
			configure: func(lifecycle *ProductionLifecycle) {
				lifecycle.prepareSync = func(context.Context) (productionSyncRun, error) {
					return productionSyncRun{}, errors.New("compose lifecycle: backend unavailable")
				}
			},
			wantMessage: "compose lifecycle: backend unavailable",
		},
		{
			name: "sprite-loading",
			configure: func(lifecycle *ProductionLifecycle) {
				lifecycle.richSpriteLoader = func() (map[string]setupui.Sprite, error) {
					return nil, errors.New("load lifecycle sprites: assets unavailable")
				}
			},
			wantMessage: "load lifecycle sprites: assets unavailable",
			wantNext:    "camp sync --session session-1",
		},
		{
			name: "runner-startup",
			configure: func(lifecycle *ProductionLifecycle) {
				lifecycle.richRunner = func(context.Context, io.Reader, io.Writer, setupui.Palette, map[string]setupui.Sprite, setupui.LifecycleWorkflow, <-chan presentation.RichLifecycleEvent, <-chan struct{}, func()) (setupui.Result, error) {
					return setupui.Result{}, errors.New("start Bubble Tea: terminal unavailable")
				}
			},
			wantMessage: "start Bubble Tea: terminal unavailable",
			wantNext:    "camp recover session-1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle := productionRichSyncLifecycle()
			test.configure(lifecycle)
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"sync"}, Streams{
				In: strings.NewReader(""), Out: &stdout, ErrOut: &stderr,
			})
			if code != int(ExitFailure) {
				t.Fatalf("exit = %d, want failure", code)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			want := "error ["
			if test.name == "lifecycle-setup" {
				want += "command_failed"
			} else {
				want += "lifecycle_failed"
			}
			want += "]: " + test.wantMessage + "\n"
			if test.wantNext != "" {
				want += "next: " + test.wantNext + "\n"
			}
			if stderr.String() != want {
				t.Fatalf("stderr = %q, want %q", stderr.String(), want)
			}
		})
	}
}

func productionRichSyncLifecycle() *ProductionLifecycle {
	return &ProductionLifecycle{
		prepareSync: func(context.Context) (productionSyncRun, error) {
			return productionSyncRun{
				sessionID: "session-1",
				run: func(context.Context, app.ProgressReporter) (app.CheckpointResult, error) {
					return app.CheckpointResult{RecoveryCommand: "camp recover session-1"}, errors.New("checkpoint upload failed")
				},
			}, nil
		},
		richAvailable: func(OutputMode, io.Reader, io.Writer, map[string]string, terminalProbe) bool { return true },
		richRunner:    recordingRichLifecycleRunner(nil, nil),
	}
}

func TestExecuteProductionRichCloseRunnerFailureUsesNormalEnvelope(t *testing.T) {
	lifecycle := &ProductionLifecycle{
		prepareClose: func(context.Context) (productionCloseRun, error) {
			return productionCloseRun{
				sessionID: "session-1",
				mode:      domain.SessionReadWrite,
				run: func(context.Context, CloseRequest, app.ProgressReporter) (app.CloseResult, error) {
					return app.CloseResult{RecoveryCommand: "camp recover session-1"}, errors.New("close worker failed")
				},
			}, nil
		},
		richAvailable: func(OutputMode, io.Reader, io.Writer, map[string]string, terminalProbe) bool { return true },
		richRunner: func(context.Context, io.Reader, io.Writer, setupui.Palette, map[string]setupui.Sprite, setupui.LifecycleWorkflow, <-chan presentation.RichLifecycleEvent, <-chan struct{}, func()) (setupui.Result, error) {
			return setupui.Result{}, errors.New("start Bubble Tea: terminal unavailable")
		},
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), NewRootWithLifecycle(lifecycle), []string{"close"}, Streams{
		In: strings.NewReader(""), Out: &stdout, ErrOut: &stderr,
	})
	if code != int(ExitFailure) {
		t.Fatalf("exit = %d, want failure", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	want := "error [lifecycle_failed]: start Bubble Tea: terminal unavailable\nnext: camp recover session-1\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestProductionRichCloseRefreshFailureEmitsTerminalFailure(t *testing.T) {
	var events []presentation.RichLifecycleEvent
	lifecycle := &ProductionLifecycle{
		prepareClose: func(context.Context) (productionCloseRun, error) {
			return productionCloseRun{
				sessionID: "session-1",
				mode:      domain.SessionReadWrite,
				run: func(ctx context.Context, _ CloseRequest, reporter app.ProgressReporter) (app.CloseResult, error) {
					for _, event := range []app.ProgressEvent{
						{Stage: app.ProgressGenerationPublished, Generation: 7},
						{Stage: app.ProgressWorkspaceClosed},
						{Stage: app.ProgressMaterializationRemoved},
					} {
						if err := reporter.Report(ctx, event); err != nil {
							return app.CloseResult{}, err
						}
					}
					return app.CloseResult{
						PublicationSucceeded: true,
						CleanupSucceeded:     true,
						RefreshError:         "helper exited",
						RecoveryCommand:      "camp recover session-1",
					}, nil
				},
			}, nil
		},
		richAvailable: func(OutputMode, io.Reader, io.Writer, map[string]string, terminalProbe) bool { return true },
		richRunner:    recordingRichLifecycleRunner(&events, nil),
	}
	err := lifecycle.Close(context.Background(), CloseRequest{}, ModeHuman, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "refresh Hauler serving content: helper exited") {
		t.Fatalf("close error = %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Kind != presentation.RichLifecycleTerminalFailed {
		t.Fatalf("terminal event = %#v", events)
	}
}
