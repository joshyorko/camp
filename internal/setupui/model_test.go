package setupui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

type stubPipeline struct{ started map[string]string }

func (s *stubPipeline) Start(values map[string]string) <-chan tea.Msg {
	s.started = values
	ch := make(chan tea.Msg)
	close(ch)
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
