package setupui

import (
	tea "charm.land/bubbletea/v2"
)

// Phase is the setup model's high-level state.
type Phase int

const (
	PhaseConfigure Phase = iota // collecting first-run config in the form
	PhaseProvision              // config accepted; tool/runtime/etc. events arriving
	PhaseReady                  // all waypoints completed
	PhaseFailed                 // a waypoint failed
	PhaseCanceled               // user aborted
)

// Stage identifies a provisioning waypoint in order.
type Stage int

const (
	StageToolchain Stage = iota
	StageRuntime
	StageCapsule
	StageStorage
)

// --- Messages bridged from the real setup pipeline (never invented) ---

// ConfigAcceptedMsg carries the persisted config facts back into the scene so
// the waypoint metadata reflects the user's actual answers.
type ConfigAcceptedMsg struct {
	Waypoints [4]Waypoint
	NextCmd   string
	ReadyLine string
}

// WaypointCompletedMsg marks a provisioning stage done, with its metadata.
type WaypointCompletedMsg struct {
	Stage Stage
	Meta  []string
}

// WaypointFailedMsg stops the trail at a stage with a sanitized cause/recovery.
type WaypointFailedMsg struct {
	Stage    Stage
	Message  string
	Recovery string
}

type ActivityMsg struct {
	Stage   Stage
	Message string
}

// AllReadyMsg is emitted after the storage waypoint so the ready band appears.
type AllReadyMsg struct{}

// Model is the long-lived Bubble Tea model behind the entire rich setup flow —
// one program from the first prompt through CAMP IS READY. It owns terminal
// size, phase, the form, and authoritative waypoint state; presentation logic
// lives in Compose, never here, and this model never runs setup operations
// itself — it only reacts to typed messages the caller feeds from the real
// pipeline.
type Model struct {
	width, height int
	phase         Phase

	form      ConfigForm
	waypoints [4]Waypoint

	title       string
	subtitle    string
	nextCmd     string
	readyLine   string
	failMsg     string
	recovery    string
	activity    string
	helpVisible bool

	pal       Palette
	sprites   map[string]Sprite
	guard     SizeGuard
	starfield *Starfield

	// pipeline drives the real setup operations. Start launches provisioning on
	// its own goroutine and returns a channel of typed messages; the model only
	// relays those messages, so no lifecycle logic lives in presentation.
	pipeline Pipeline
	events   <-chan tea.Msg
	// done indicates terminal teardown was requested and suppresses duplicate
	// teardown callbacks.
	done   bool
	onExit func()
}

// Pipeline runs the real first-run setup. Start is called with the accepted
// config values and returns a channel that emits ConfigAcceptedMsg, then a
// WaypointCompletedMsg per stage, then AllReadyMsg — or a WaypointFailedMsg.
// The channel is closed when provisioning ends.
type Pipeline interface {
	Start(values map[string]string) <-chan tea.Msg
	Done() <-chan struct{}
}

// NewModel constructs the setup model with landmarks assigned to waypoints.
func NewModel(pal Palette, sprites map[string]Sprite, defaults map[string]string, pipeline Pipeline) Model {
	wps := [4]Waypoint{
		{Label: "TOOLCHAIN", Landmark: "crate", State: WaypointPending},
		{Label: "RUNTIME", Landmark: "helm", State: WaypointPending},
		{Label: "CAPSULE", Landmark: "tent", State: WaypointPending},
		{Label: "STORAGE", Landmark: "campfire", State: WaypointPending},
	}
	return Model{
		phase:     PhaseConfigure,
		form:      NewConfigForm(pal, defaults),
		waypoints: wps,
		title:     "⌂ CAMP",
		subtitle:  "trailhead setup",
		pal:       pal,
		sprites:   sprites,
		guard:     NewSizeGuard(MinWidth, MinHeight),
		starfield: NewStarfield(0xCA37),
		pipeline:  pipeline,
	}
}

func (m Model) OnExit(fn func()) Model {
	m.onExit = fn
	return m
}

func (m Model) withExit() Model {
	if m.done || m.onExit == nil {
		m.done = true
		return m
	}
	m.done = true
	m.onExit()
	return m
}

// listen reads the next pipeline event. Re-issued after each relayed message so
// the model consumes the whole stream (the ONCE watch-channel pattern). Returns
// nil when the channel is closed.
func (m Model) listen() tea.Cmd {
	ch := m.events
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.form.Init(), m.starfield.Init()) }

// Update is the single event loop. Window sizing is handled continuously;
// ctrl+c cancels from any phase with a clean exit; the form drives PhaseConfigure
// and typed pipeline messages drive provisioning.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.guard = m.guard.Update(msg.Width, msg.Height)
		m.form.SetWidth(min(msg.Width-10, 72))
		m.form.SetCompact(msg.Height < 30)
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
		if msg.String() == "?" && m.phase == PhaseConfigure {
			m.helpVisible = !m.helpVisible
			return m, nil
		}
		if m.phase == PhaseReady || m.phase == PhaseFailed {
			// Any key dismisses the terminal states.
			if msg.String() == "enter" || msg.String() == "q" || msg.String() == "esc" {
				m = m.withExit()
				return m, tea.Quit
			}
		}

	case FormSubmitMsg:
		m.phase = PhaseProvision
		m.waypoints[StageToolchain].State = WaypointActive
		if m.pipeline != nil {
			m.events = m.pipeline.Start(msg.Values)
			return m, m.listen()
		}
		return m, nil

	case FormCancelMsg:
		m.phase = PhaseCanceled
		m = m.withExit()
		return m, tea.Quit

	case ConfigAcceptedMsg:
		for i := range msg.Waypoints {
			// Preserve state; adopt labels/metadata/landmarks from the facts.
			state := m.waypoints[i].State
			m.waypoints[i] = msg.Waypoints[i]
			m.waypoints[i].State = state
		}
		m.nextCmd = msg.NextCmd
		m.readyLine = msg.ReadyLine
		return m, m.listen()

	case WaypointCompletedMsg:
		m.activity = ""
		i := int(msg.Stage)
		if i >= 0 && i < len(m.waypoints) {
			m.waypoints[i].State = WaypointCompleted
			if len(msg.Meta) > 0 {
				m.waypoints[i].Meta = msg.Meta
			}
			if i+1 < len(m.waypoints) {
				m.waypoints[i+1].State = WaypointActive
			}
		}
		return m, m.listen()

	case WaypointFailedMsg:
		m.activity = ""
		i := int(msg.Stage)
		if i >= 0 && i < len(m.waypoints) {
			m.waypoints[i].State = WaypointFailed
		}
		m.phase = PhaseFailed
		m.failMsg = msg.Message
		m.recovery = msg.Recovery
		return m, nil

	case AllReadyMsg:
		m.activity = ""
		m.phase = PhaseReady
		return m, nil

	case ActivityMsg:
		i := int(msg.Stage)
		if m.phase == PhaseProvision && i >= 0 && i < len(m.waypoints) && m.waypoints[i].State == WaypointActive {
			m.activity = msg.Message
		}
		return m, m.listen()
	}

	if m.phase == PhaseConfigure {
		var cmd tea.Cmd
		m.form, cmd = m.form.Update(msg)
		return m, cmd
	}
	return m, nil
}

// View composes the full-screen scene for the current phase and returns it in
// an alternate-screen view. The cursor is visible only while configuring.
func (m Model) View() tea.View {
	var content string
	if !m.guard.OK() {
		content = m.guard.View(m.pal)
	} else {
		content = composeWithStarfield(m.sceneData(), m.width, m.height, m.pal, m.sprites, m.starfield)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.BackgroundColor = m.pal.Bg
	// The cursor is left unset (hidden) on every frame except the config form,
	// where the focused Bubbles text input renders its own inline cursor as the
	// visible focus indicator. Non-input frames (provisioning, ready, failure)
	// therefore show no cursor.
	return v
}

// Canceled reports whether the program exited via cancellation.
func (m Model) Canceled() bool { return m.phase == PhaseCanceled }

// Failed reports whether provisioning failed, with the sanitized cause.
func (m Model) Failed() (bool, string, string) {
	return m.phase == PhaseFailed, m.failMsg, m.recovery
}

// sceneData projects the model into the renderer's data contract.
func (m Model) sceneData() SceneData {
	d := SceneData{
		Title:     m.title,
		Subtitle:  m.subtitle,
		Waypoints: m.waypoints,
		HelpLine:  m.helpLine(),
	}
	switch m.phase {
	case PhaseConfigure:
		if m.helpVisible {
			d.Foreground = "KEYBOARD\n\ntab / shift+tab  move between fields\nenter            validate and continue\nesc              previous field or cancel\n?                close help\nctrl+c           cancel"
		} else {
			d.Foreground = m.form.View()
		}
	case PhaseProvision:
		d.Foreground = m.activity
	case PhaseReady:
		d.Ready = true
		d.ReadyLine = m.readyLine
		d.NextCommand = m.nextCmd
	case PhaseFailed:
		d.Failure = m.failMsg
		d.Recovery = m.recovery
	}
	return d
}

func (m Model) helpLine() string {
	switch m.phase {
	case PhaseConfigure:
		return "tab next · shift+tab prev · enter continue · esc cancel · ctrl+c quit"
	case PhaseReady, PhaseFailed:
		return "enter to exit · ctrl+c quit"
	default:
		return "ctrl+c quit"
	}
}
