package setupui

import (
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type stubPipeline struct {
	started map[string]string
	done    chan struct{}
	once    sync.Once
}

func (s *stubPipeline) Done() <-chan struct{} {
	if s.done == nil {
		s.done = make(chan struct{})
	}
	return s.done
}

func (s *stubPipeline) closeDone() {
	if s.done == nil {
		s.done = make(chan struct{})
	}
	s.once.Do(func() {
		close(s.done)
	})
}

func (s *stubPipeline) Start(values map[string]string) <-chan tea.Msg {
	s.started = values
	ch := make(chan tea.Msg)
	go func() {
		defer close(ch)
		defer s.closeDone()
	}()
	return ch
}

func newTestModel(t *testing.T, p Pipeline) Model {
	t.Helper()
	sprites, err := LoadSprites()
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(DefaultPalette(), sprites, map[string]string{
		"source": "/x", "capsule": "x", "backend": "file://x", "provider": "docker", "context": "default",
	}, p)
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m2.(Model)
}

func TestModelSubmitStartsPipelineAndActivatesToolchain(t *testing.T) {
	p := &stubPipeline{}
	m := newTestModel(t, p)
	next, _ := m.Update(FormSubmitMsg{Values: map[string]string{"source": "/x"}})
	nm := next.(Model)
	if nm.phase != PhaseProvision {
		t.Fatalf("phase = %v, want provision", nm.phase)
	}
	if nm.waypoints[StageToolchain].State != WaypointActive {
		t.Fatal("toolchain should be active after submit")
	}
	if p.started == nil {
		t.Fatal("pipeline.Start was not called")
	}
}

func TestModelOnExitCallbackRunsAtMostOnce(t *testing.T) {
	p := &stubPipeline{}
	m := newTestModel(t, p)
	calls := 0
	m = m.OnExit(func() { calls++ })
	next, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = next.(Model)
	if calls != 1 {
		t.Fatalf("onExit calls = %d, want 1", calls)
	}
	_ = m
}

func TestModelWaypointCompletionAdvancesNext(t *testing.T) {
	m := newTestModel(t, &stubPipeline{})
	sub, _ := m.Update(FormSubmitMsg{Values: nil})
	m = sub.(Model)
	done, _ := m.Update(WaypointCompletedMsg{Stage: StageToolchain, Meta: []string{"DevPod v1"}})
	m = done.(Model)
	if m.waypoints[StageToolchain].State != WaypointCompleted {
		t.Fatal("toolchain should be completed")
	}
	if m.waypoints[StageRuntime].State != WaypointActive {
		t.Fatal("runtime should advance to active")
	}
}

func TestModelFailureStopsAndPreservesCause(t *testing.T) {
	m := newTestModel(t, &stubPipeline{})
	sub, _ := m.Update(FormSubmitMsg{Values: nil})
	m = sub.(Model)
	f, _ := m.Update(WaypointFailedMsg{Stage: StageRuntime, Message: "docker unreachable", Recovery: "camp setup"})
	m = f.(Model)
	failed, msg, rec := m.Failed()
	if !failed || msg != "docker unreachable" || rec != "camp setup" {
		t.Fatalf("failure not preserved: failed=%v msg=%q rec=%q", failed, msg, rec)
	}
	if m.waypoints[StageRuntime].State != WaypointFailed {
		t.Fatal("runtime should be failed")
	}
}

func TestModelReadyDismissRunsOnExitOnce(t *testing.T) {
	m := newTestModel(t, &stubPipeline{})
	calls := 0
	m = m.OnExit(func() { calls++ })
	ready, _ := m.Update(AllReadyMsg{})
	m = ready.(Model)
	done, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = done.(Model)
	if calls != 1 {
		t.Fatalf("ready dismiss onExit calls = %d, want 1", calls)
	}
	if m.Canceled() {
		t.Fatal("ready dismiss should not mark canceled")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if calls != 1 {
		t.Fatalf("ready dismiss onExit calls after extra key = %d, want 1", calls)
	}
}

func TestModelFailureDismissRunsOnExitOnce(t *testing.T) {
	m := newTestModel(t, &stubPipeline{})
	calls := 0
	m = m.OnExit(func() { calls++ })
	sub, _ := m.Update(FormSubmitMsg{Values: nil})
	m = sub.(Model)
	failed, _ := m.Update(WaypointFailedMsg{Stage: StageRuntime, Message: "boom", Recovery: "camp setup"})
	m = failed.(Model)
	done, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = done.(Model)
	if calls != 1 {
		t.Fatalf("failure dismiss onExit calls = %d, want 1", calls)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if calls != 1 {
		t.Fatalf("failure dismiss onExit calls after extra key = %d, want 1", calls)
	}
	_ = m
}

func TestModelCtrlCCancels(t *testing.T) {
	m := newTestModel(t, &stubPipeline{})
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	nm := next.(Model)
	if !nm.Canceled() {
		t.Fatal("ctrl+c should cancel")
	}
	if cmd == nil {
		t.Fatal("ctrl+c should return a quit command")
	}
}

func TestModelHelpToggleAndActivityAreTruthful(t *testing.T) {
	m := newTestModel(t, &stubPipeline{})
	next, _ := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	m = next.(Model)
	if !m.helpVisible || !strings.Contains(m.sceneData().Foreground, "KEYBOARD") {
		t.Fatalf("help overlay not visible: %#v", m.sceneData())
	}
	next, _ = m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	m = next.(Model)
	if m.helpVisible {
		t.Fatal("second ? did not close help")
	}
	next, _ = m.Update(FormSubmitMsg{Values: map[string]string{"backend": "file://backend"}})
	m = next.(Model)
	next, _ = m.Update(ActivityMsg{Stage: StageToolchain, Message: "Installing Hauler…"})
	m = next.(Model)
	if m.activity != "Installing Hauler…" || m.waypoints[StageToolchain].State != WaypointActive {
		t.Fatalf("activity advanced a milestone or was lost: activity=%q state=%v", m.activity, m.waypoints[StageToolchain].State)
	}
}

func TestWorkflowModelUsesInitFieldsAndWaypoints(t *testing.T) {
	sprites, err := LoadSprites()
	if err != nil {
		t.Fatal(err)
	}
	workflow := Workflow{
		Title: "⌂ CAMP", Subtitle: "camp initialization",
		Fields: []FormFieldSpec{
			{Key: "name", Label: "Camp name", Placeholder: "alpha"},
			{Key: "backend", Label: "Backend", Placeholder: "file://…"},
		},
		Waypoints: [4]Waypoint{
			{Label: "MANIFEST", Landmark: "tent"},
			{Label: "CAPSULE", Landmark: "crate"},
			{Label: "RUNTIME", Landmark: "helm"},
			{Label: "READY", Landmark: "campfire"},
		},
	}
	m := NewWorkflowModel(DefaultPalette(), sprites, map[string]string{"name": "alpha", "backend": "file:///backend"}, &stubPipeline{}, workflow)
	values := m.form.Values()
	if len(values) != 2 || values["name"] != "alpha" || values["backend"] != "file:///backend" {
		t.Fatalf("init form values = %#v", values)
	}
	if m.subtitle != "camp initialization" || m.waypoints[0].Label != "MANIFEST" || m.waypoints[3].Label != "READY" {
		t.Fatalf("init workflow model = subtitle %q waypoints %#v", m.subtitle, m.waypoints)
	}
}
