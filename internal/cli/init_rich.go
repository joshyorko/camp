package cli

import (
	"context"
	"fmt"
	"io"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/joshyorko/camp/internal/setupui"
)

type richInitPipeline struct {
	ctx      context.Context
	request  InitRequest
	run      func(context.Context, InitRequest, OutputMode, io.Writer) error
	done     chan struct{}
	startMu  sync.Mutex
	started  bool
	start    sync.Once
	doneOnce sync.Once
}

func newRichInitPipeline(ctx context.Context, request InitRequest, run func(context.Context, InitRequest, OutputMode, io.Writer) error) *richInitPipeline {
	return &richInitPipeline{ctx: ctx, request: request, run: run, done: make(chan struct{})}
}

func (p *richInitPipeline) Done() <-chan struct{} { return p.done }

func (p *richInitPipeline) markDoneIfNotStarted() {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if !p.started {
		p.doneOnce.Do(func() { close(p.done) })
	}
}

func (p *richInitPipeline) Start(values map[string]string) <-chan tea.Msg {
	out := make(chan tea.Msg, 12)
	p.start.Do(func() {
		p.startMu.Lock()
		p.started = true
		p.startMu.Unlock()
		go func() {
			defer close(out)
			defer p.doneOnce.Do(func() { close(p.done) })
			request := p.request
			request.Capsule = values["name"]
			waypoints := [4]setupui.Waypoint{
				{Label: "MANIFEST", Landmark: "tent"},
				{Label: "CAPSULE", Landmark: "crate"},
				{Label: "RUNTIME", Landmark: "helm"},
				{Label: "READY", Landmark: "campfire"},
			}
			out <- setupui.ConfigAcceptedMsg{
				Waypoints: waypoints,
				NextCmd:   "cd " + request.Root + " && camp open",
				ReadyLine: request.Capsule + " is initialized",
			}
			report := func(message string) error {
				var event tea.Msg
				switch message {
				case "Writing camp manifest…":
					event = setupui.ActivityMsg{Stage: setupui.StageToolchain, Message: message}
				case "Camp manifest written.":
					event = setupui.WaypointCompletedMsg{Stage: setupui.StageToolchain, Meta: []string{request.Capsule, request.Root}}
				case "Initializing capsule…":
					event = setupui.ActivityMsg{Stage: setupui.StageRuntime, Message: message}
				case "Capsule initialized.":
					event = setupui.WaypointCompletedMsg{Stage: setupui.StageRuntime, Meta: []string{request.Capsule}}
				default:
					return nil
				}
				select {
				case out <- event:
					return nil
				case <-p.ctx.Done():
					return p.ctx.Err()
				}
			}
			runCtx := context.WithValue(p.ctx, initActivityContextKey{}, report)
			if err := p.run(runCtx, request, ModeHuman, discardWriter{}); err != nil {
				out <- setupui.WaypointFailedMsg{
					Stage: setupui.StageRuntime, Message: setupui.SafeText(err.Error(), "camp initialization failed"),
					Recovery: "camp init " + request.Root + " --name " + request.Capsule,
				}
				return
			}
			out <- setupui.WaypointCompletedMsg{Stage: setupui.StageCapsule, Meta: []string{
				firstNonEmpty(request.DevPodProvider, "machine default"),
				"context " + firstNonEmpty(request.DevPodContext, "machine default"),
			}}
			out <- setupui.WaypointCompletedMsg{Stage: setupui.StageStorage, Meta: []string{
				"backend", firstNonEmpty(request.Backend, "machine default"),
			}}
			out <- setupui.AllReadyMsg{}
		}()
	})
	return out
}

func richInitWorkflow() setupui.Workflow {
	return setupui.Workflow{
		Title: "⌂ CAMP", Subtitle: "camp initialization",
		Fields: []setupui.FormFieldSpec{{Key: "name", Label: "Camp name", Placeholder: "camp-name"}},
		Waypoints: [4]setupui.Waypoint{
			{Label: "MANIFEST", Landmark: "tent"},
			{Label: "CAPSULE", Landmark: "crate"},
			{Label: "RUNTIME", Landmark: "helm"},
			{Label: "READY", Landmark: "campfire"},
		},
	}
}

func initRecoveryCommand(request InitRequest) string {
	return fmt.Sprintf("camp init %s --name %s", request.Root, request.Capsule)
}
