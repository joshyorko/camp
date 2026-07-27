package setupui

import (
	"context"
	"io"

	tea "charm.land/bubbletea/v2"

	"github.com/joshyorko/camp/internal/presentation"
)

// LifecycleWorkflow supplies the authoritative facts that frame one command's
// lifecycle. Operation is a human label such as open, sync, close, or recover;
// the model does not derive lifecycle facts from it.
type LifecycleWorkflow struct {
	Operation   string
	ReadyLine   string
	NextCommand string
}

// LifecycleModel presents a non-configuring lifecycle command in the same
// campsite renderer as setup. It reacts only to RichLifecycleEvent values.
type LifecycleModel struct {
	width, height int
	phase         Phase
	workflow      LifecycleWorkflow
	stages        []presentation.LifecycleStage
	states        map[presentation.LifecycleStage]WaypointState
	activity      string
	failure       string
	recovery      string

	pal       Palette
	sprites   map[string]Sprite
	guard     SizeGuard
	starfield *Starfield
	events    <-chan presentation.RichLifecycleEvent
	done      bool
	onExit    func()
}

// NewLifecycleModel constructs a renderer-only lifecycle model. It starts in
// provisioning because lifecycle commands have already been selected by the
// CLI; no configuration form or background operation is owned by this model.
func NewLifecycleModel(pal Palette, sprites map[string]Sprite, workflow LifecycleWorkflow) LifecycleModel {
	stages := presentation.VisualLifecycleStages()
	states := make(map[presentation.LifecycleStage]WaypointState, len(stages))
	for _, stage := range stages {
		states[stage] = WaypointPending
	}
	return LifecycleModel{
		phase:     PhaseProvision,
		workflow:  workflow,
		stages:    stages,
		states:    states,
		pal:       pal,
		sprites:   sprites,
		guard:     NewSizeGuard(MinWidth, MinHeight),
		starfield: NewStarfield(0xCA37),
	}
}

// OnExit registers terminal cleanup owned by the caller. It runs at most once
// for cancellation or terminal-state dismissal.
func (m LifecycleModel) OnExit(fn func()) LifecycleModel {
	m.onExit = fn
	return m
}

func (m LifecycleModel) withExit() LifecycleModel {
	if m.done || m.onExit == nil {
		m.done = true
		return m
	}
	m.done = true
	m.onExit()
	return m
}

// RunLifecycle runs the lifecycle-only rich path. The caller owns the worker
// and feeds it typed events; this function owns only Bubble Tea restoration.
func RunLifecycle(ctx context.Context, in io.Reader, out io.Writer, pal Palette, sprites map[string]Sprite, workflow LifecycleWorkflow, events <-chan presentation.RichLifecycleEvent, onExit func()) (Result, error) {
	model := NewLifecycleModel(pal, sprites, workflow).OnExit(onExit)
	model.events = events
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithOutput(out)}
	if in != nil {
		opts = append(opts, tea.WithInput(in))
	}
	final, err := tea.NewProgram(model, opts...).Run()
	if err != nil {
		return Result{}, err
	}
	fm, ok := final.(LifecycleModel)
	if !ok {
		return Result{}, nil
	}
	failed, message, recovery := fm.Failed()
	return Result{Canceled: fm.Canceled(), Failed: failed, FailMsg: message, Recovery: recovery}, nil
}

func (m LifecycleModel) Init() tea.Cmd {
	return tea.Batch(m.starfield.Init(), m.listen())
}

func (m LifecycleModel) listen() tea.Cmd {
	if m.events == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-m.events
		if !ok {
			return nil
		}
		return event
	}
}

// Update is the lifecycle event loop. Only a completed event advances a
// waypoint. Activity can make a stage active but can never complete it.
func (m LifecycleModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.guard = m.guard.Update(msg.Width, msg.Height)
		m.starfield.Resize(msg.Width, layoutFor(msg.Width, msg.Height).SkyRows)
		return m, nil
	case starfieldTickMsg:
		return m, m.starfield.Update(msg)
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" || msg.String() == "ctrl+d" {
			m.phase = PhaseCanceled
			m = m.withExit()
			return m, tea.Quit
		}
		if (m.phase == PhaseReady || m.phase == PhaseFailed) && (msg.String() == "enter" || msg.String() == "q" || msg.String() == "esc") {
			m = m.withExit()
			return m, tea.Quit
		}
	case presentation.RichLifecycleEvent:
		return m.apply(msg)
	}
	return m, nil
}

func (m LifecycleModel) apply(event presentation.RichLifecycleEvent) (tea.Model, tea.Cmd) {
	switch event.Kind {
	case presentation.RichLifecycleActivity:
		if m.activate(event.Stage) {
			m.activity = SafeText(event.Message, "lifecycle detail unavailable")
		}
		return m, m.listen()
	case presentation.RichLifecycleCompleted:
		m.activity = ""
		m.complete(event.Stage)
		return m, m.listen()
	case presentation.RichLifecycleSucceeded:
		m.activity = ""
		m.phase = PhaseReady
		return m, nil
	case presentation.RichLifecycleFailed:
		m.activity = ""
		if m.activate(event.Stage) {
			m.states[event.Stage] = WaypointFailed
		}
		m.failure = SafeText(event.Message, "lifecycle failed")
		m.recovery = SafeText(event.RecoveryCommand, "")
		m.phase = PhaseFailed
		return m, nil
	default:
		return m, m.listen()
	}
}

func (m *LifecycleModel) activate(stage presentation.LifecycleStage) bool {
	if _, ok := m.states[stage]; !ok {
		return false
	}
	for _, candidate := range m.stages {
		if candidate != stage && m.states[candidate] == WaypointActive {
			m.states[candidate] = WaypointPending
		}
	}
	if m.states[stage] != WaypointCompleted {
		m.states[stage] = WaypointActive
	}
	return true
}

func (m *LifecycleModel) complete(stage presentation.LifecycleStage) {
	if _, ok := m.states[stage]; !ok {
		return
	}
	m.states[stage] = WaypointCompleted
	for i, candidate := range m.stages {
		if candidate == stage && i+1 < len(m.stages) && m.states[m.stages[i+1]] == WaypointPending {
			m.states[m.stages[i+1]] = WaypointActive
			return
		}
	}
}

// StageState exposes the current scene state for focused contract tests and
// lifecycle adapters without exposing mutable model maps.
func (m LifecycleModel) StageState(stage presentation.LifecycleStage) WaypointState {
	return m.states[stage]
}

func (m LifecycleModel) Phase() Phase   { return m.phase }
func (m LifecycleModel) Width() int     { return m.width }
func (m LifecycleModel) Canceled() bool { return m.phase == PhaseCanceled }
func (m LifecycleModel) Failed() (bool, string, string) {
	return m.phase == PhaseFailed, m.failure, m.recovery
}

func (m LifecycleModel) View() tea.View {
	content := ""
	if !m.guard.OK() {
		content = m.guard.View(m.pal)
	} else {
		content = composeWithStarfield(m.sceneData(), m.width, m.height, m.pal, m.sprites, m.starfield)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = m.pal.Bg
	return v
}

func (m LifecycleModel) sceneData() SceneData {
	data := SceneData{
		Title:     "⌂ CAMP",
		Subtitle:  SafeText(m.workflow.Operation+" lifecycle", "lifecycle"),
		Waypoints: m.windowWaypoints(),
		HelpLine:  m.helpLine(),
	}
	switch m.phase {
	case PhaseProvision:
	case PhaseReady:
		data.Ready = true
		data.ReadyTitle = "LIFECYCLE COMPLETE"
		data.ReadyLine = SafeText(m.workflow.ReadyLine, "")
		data.NextCommand = SafeText(m.workflow.NextCommand, "")
	case PhaseFailed:
		data.Failure = m.failure
		data.FailureTitle = "LIFECYCLE STOPPED"
		data.Recovery = m.recovery
	}
	return data
}

func (m LifecycleModel) windowWaypoints() [4]Waypoint {
	active := 0
	for i, stage := range m.stages {
		if m.states[stage] == WaypointActive || m.states[stage] == WaypointFailed {
			active = i
			break
		}
	}
	start := active / 4 * 4
	landmarks := []string{"crate", "helm", "tent", "campfire"}
	var out [4]Waypoint
	for slot := range out {
		index := start + slot
		if index < len(m.stages) {
			stage := m.stages[index]
			waypoint := Waypoint{Label: presentation.LifecycleStageLabel(stage), Landmark: landmarks[slot], State: m.states[stage]}
			if m.states[stage] == WaypointActive && m.activity != "" {
				// Activity is supporting metadata, not an opaque modal over the
				// waypoint label. That keeps the fact-bearing label legible and
				// still shows the exact real operation on full-height scenes.
				waypoint.Meta = []string{m.activity}
			}
			out[slot] = waypoint
			continue
		}
		out[slot] = Waypoint{Label: "COMPLETE", Landmark: landmarks[slot], State: WaypointPending}
	}
	return out
}

func (m LifecycleModel) helpLine() string {
	if m.phase == PhaseReady || m.phase == PhaseFailed {
		return "enter to exit · ctrl+c quit"
	}
	return "ctrl+c quit"
}

// SampleLifecycleFrame composes deterministic lifecycle frames with the same
// renderer used by the live model, for scene review and golden tests.
func SampleLifecycleFrame(state string, w, h int, pal Palette, sprites map[string]Sprite) string {
	m := NewLifecycleModel(pal, sprites, LifecycleWorkflow{
		Operation: "sync", ReadyLine: "session-alpha · docker", NextCommand: "camp status",
	})
	m.width, m.height = w, h
	m.guard = m.guard.Update(w, h)
	m.starfield.Resize(w, layoutFor(w, h).SkyRows)
	switch state {
	case "failure":
		m.phase = PhaseFailed
		m.states[presentation.StageUpload] = WaypointFailed
		m.failure = "checkpoint upload failed"
		m.recovery = "camp recover session-alpha"
	case "ready":
		m.phase = PhaseReady
		for _, stage := range m.stages {
			m.states[stage] = WaypointCompleted
		}
	default:
		m.states[presentation.StageUpload] = WaypointActive
		m.activity = "uploading generation 42"
	}
	return m.View().Content
}
