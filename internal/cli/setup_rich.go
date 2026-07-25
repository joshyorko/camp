//go:build linux

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/sys/unix"

	campcontract "github.com/joshyorko/camp"
	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/presentation"
	"github.com/joshyorko/camp/internal/setupui"
)

// inputIsTTY reports whether the reader is a real terminal, so rich mode only
// engages with a live keyboard. Piped or redirected input keeps the
// deterministic line-based prompts.
func inputIsTTY(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}

// runRichSetup drives the full-screen Trailhead scene for interactive
// truecolor terminals. It reports handled=true when it owns the whole setup
// (so the caller returns immediately), with err reflecting cancellation or a
// provisioning failure. On any terminal-init problem it reports handled=false
// so the caller falls back to the deterministic line-based flow.
func (p *ProductionLifecycle) runRichSetup(ctx context.Context, in io.Reader, out io.Writer, defaults setupPromptDefaults) (bool, error) {
	sprites, err := setupui.LoadSprites()
	if err != nil {
		return false, nil
	}
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	pipeline := newRichSetupPipeline(p, pipelineCtx)
	result, err := setupui.RunWithExit(ctx, in, out, setupui.DefaultPalette(), sprites, map[string]string{
		"backend":  defaults.Backend,
		"provider": "docker",
		"context":  "default",
	}, pipeline, pipelineCancel)
	pipelineCancel()
	pipeline.markDoneIfNotStarted()
	<-pipeline.Done()
	if err != nil {
		// Terminal program failed to run at all: fall back rather than error.
		return false, nil
	}
	switch {
	case result.Canceled:
		return true, nil
	case result.Failed:
		return true, lifecycleFailure(errors.New(result.FailMsg), result.Recovery)
	default:
		return true, nil
	}
}

// richSetupPipeline adapts the real first-run setup operations to the
// setupui.Pipeline contract. All lifecycle logic (config validation,
// persistence, tool resolution, journal reads) lives here in the CLI package;
// the presentation model only relays the typed messages this emits.
type richSetupPipeline struct {
	lifecycle *ProductionLifecycle
	ctx       context.Context
	done      chan struct{}
	doneOnce  sync.Once
	startMu   sync.Mutex
	started   bool
}

func (p *richSetupPipeline) markDone() {
	p.doneOnce.Do(func() { close(p.done) })
}

func (p *richSetupPipeline) markDoneIfNotStarted() {
	p.startMu.Lock()
	defer p.startMu.Unlock()
	if !p.started {
		p.markDone()
	}
}

func newRichSetupPipeline(lifecycle *ProductionLifecycle, ctx context.Context) *richSetupPipeline {
	return &richSetupPipeline{lifecycle: lifecycle, ctx: ctx, done: make(chan struct{})}
}

// Done reports when the pipeline has reached terminal states and no further
// presentation-driving events can be produced.
func (p *richSetupPipeline) Done() <-chan struct{} {
	return p.done
}

// Start persists the config, resolves the toolchain, and reads the resulting
// authoritative campsite facts, emitting one message per real milestone. It
// returns immediately; work runs on a goroutine and the channel closes when
// provisioning ends.
func (p *richSetupPipeline) Start(values map[string]string) <-chan tea.Msg {
	out := make(chan tea.Msg, 8)
	p.startMu.Lock()
	p.started = true
	p.startMu.Unlock()
	go func() {
		defer close(out)
		defer p.markDone()
		p.run(values, out)
	}()
	return out
}

func (p *richSetupPipeline) run(values map[string]string, out chan<- tea.Msg) {
	emit := func(msg tea.Msg) bool {
		select {
		case out <- msg:
			return true
		case <-p.ctx.Done():
			return false
		}
	}
	fail := func(stage setupui.Stage, err error, recovery string) {
		msg := setupui.SafeText(err.Error(), "setup failed")
		emit(setupui.WaypointFailedMsg{Stage: stage, Message: msg, Recovery: setupui.SafeText(recovery, "camp setup")})
	}

	request := InitRequest{
		Backend:        values["backend"],
		DevPodProvider: values["provider"],
		DevPodContext:  values["context"],
	}
	if !emit(setupui.ActivityMsg{Stage: setupui.StageToolchain, Message: "Writing machine defaults…"}) {
		return
	}

	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environmentMap(os.Environ())})
	if err != nil {
		fail(setupui.StageToolchain, err, "camp setup")
		return
	}
	// Persist machine defaults (validates + writes atomically). A failure here is
	// a toolchain-stage failure with the canonical recovery command.
	if _, err := persistSetupDefaults(paths.ConfigPath, request); err != nil {
		fail(setupui.StageToolchain, err, "camp setup")
		return
	}

	lockBytes := campcontract.DistributionToolLock()
	lock, err := tooladapter.ParseLock(bytes.NewReader(lockBytes))
	if err != nil {
		fail(setupui.StageToolchain, err, "camp setup")
		return
	}

	// Toolchain: resolve DevPod and Hauler for real.
	environment := environmentMap(os.Environ())
	completed := func(name string, resolution tooladapter.Resolution) error { return nil }
	if !emit(setupui.ActivityMsg{Stage: setupui.StageToolchain, Message: "Installing DevPod and Hauler…"}) {
		return
	}
	if err := runProductionToolSetupWithEvents(p.ctx, ModeHuman, discardWriter{}, lockBytes, "", environment, runtime.GOOS, runtime.GOARCH, completed); err != nil {
		fail(setupui.StageToolchain, err, "camp setup")
		return
	}
	// Publish toolchain completion and persisted factset after DevPod/Hauler are
	// actually ready so the UI does not claim completion before the real setup is
	// done.
	if !emit(setupui.WaypointCompletedMsg{Stage: setupui.StageToolchain, Meta: []string{
		fmt.Sprintf("devpod %s", lock.Tools["devpod"].Version),
		fmt.Sprintf("hauler %s", lock.Tools["hauler"].Version),
	}}) {
		return
	}
	if !emit(setupui.ConfigAcceptedMsg{
		Waypoints: machineSetupWaypoints(lock, request),
		NextCmd:   "camp init --name <id>",
		ReadyLine: "machine ready · no camp selected",
	}) {
		return
	}

	// Runtime, Capsule, Storage are authoritative facts derived from the
	// persisted configuration; emit them in order once established.
	if !emit(setupui.WaypointCompletedMsg{Stage: setupui.StageRuntime, Meta: []string{
		request.DevPodProvider,
		"context " + request.DevPodContext,
	}}) {
		return
	}
	if !emit(setupui.WaypointCompletedMsg{Stage: setupui.StageCapsule, Meta: []string{"no camp selected", "run camp init"}}) {
		return
	}
	if !emit(setupui.WaypointCompletedMsg{Stage: setupui.StageStorage, Meta: []string{"default backend", request.Backend}}) {
		return
	}
	emit(setupui.AllReadyMsg{})
}

func machineSetupWaypoints(lock tooladapter.Lock, request InitRequest) [4]setupui.Waypoint {
	return [4]setupui.Waypoint{
		{Label: "TOOLCHAIN", Landmark: "crate", Meta: []string{
			fmt.Sprintf("devpod %s", lock.Tools["devpod"].Version),
			fmt.Sprintf("hauler %s", lock.Tools["hauler"].Version),
		}},
		{Label: "RUNTIME", Landmark: "helm", Meta: []string{request.DevPodProvider, "context " + request.DevPodContext}},
		{Label: "CAMP", Landmark: "tent", Meta: []string{"no camp selected", "run camp init"}},
		{Label: "STORAGE", Landmark: "campfire", Meta: []string{"default backend", request.Backend}},
	}
}

func waypointsFromModel(model presentation.CampsiteModel) [4]setupui.Waypoint {
	return [4]setupui.Waypoint{
		{Label: "TOOLCHAIN", Landmark: "crate", Meta: []string{
			fmt.Sprintf("%s %s", model.DevPod.Name, model.DevPod.Version),
			fmt.Sprintf("%s %s", model.Hauler.Name, model.Hauler.Version),
		}},
		{Label: "RUNTIME", Landmark: "helm", Meta: []string{
			fmt.Sprintf("%s · %s", model.Provider, model.RuntimeKind),
			"context " + model.Context,
		}},
		{Label: "CAPSULE", Landmark: "tent", Meta: []string{model.Capsule, model.Source}},
		{Label: "STORAGE", Landmark: "campfire", Meta: []string{model.BackendKind + " backend", model.Storage}},
	}
}

// discardWriter swallows the writes the reused ModeHuman operations would emit;
// the rich scene is the sole human surface during interactive setup.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
