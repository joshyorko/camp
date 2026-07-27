package setupui

import (
	"crypto/sha256"
	stdhex "encoding/hex"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/joshyorko/camp/internal/presentation"
)

func newLifecycleTestModel(t *testing.T, operation string) LifecycleModel {
	t.Helper()
	m := NewLifecycleModel(DefaultPalette(), loadForTest(t), LifecycleWorkflow{
		Operation:   operation,
		ReadyLine:   "session-alpha · docker",
		NextCommand: "camp status",
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return next.(LifecycleModel)
}

func TestLifecycleStagesAreStableAndExhaustive(t *testing.T) {
	want := []presentation.LifecycleStage{
		presentation.StageHydrate,
		presentation.StageServices,
		presentation.StageDevPod,
		presentation.StageAttach,
		presentation.StageMirror,
		presentation.StageImageCapture,
		presentation.StageArchive,
		presentation.StageUpload,
		presentation.StagePointer,
		presentation.StageCleanup,
		presentation.StageRecovery,
	}
	got := presentation.VisualLifecycleStages()
	if len(got) != len(want) {
		t.Fatalf("lifecycle stage count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stage %d = %q, want %q", i, got[i], want[i])
		}
		if presentation.LifecycleStageLabel(got[i]) == "" {
			t.Fatalf("stage %q has no stable visual label", got[i])
		}
	}
}

func TestLifecycleModelAdvancesOnlyFromTypedCompletionEvents(t *testing.T) {
	m := newLifecycleTestModel(t, "sync")
	m2, _ := m.Update(presentation.RichLifecycleEvent{
		Kind:    presentation.RichLifecycleActivity,
		Stage:   presentation.StageUpload,
		Message: "uploading generation 42",
	})
	m = m2.(LifecycleModel)
	if got := m.StageState(presentation.StageUpload); got != WaypointActive {
		t.Fatalf("upload state after activity = %v, want active", got)
	}
	if got := m.StageState(presentation.StagePointer); got != WaypointPending {
		t.Fatalf("pointer state after upload activity = %v, want pending", got)
	}

	m2, _ = m.Update(presentation.RichLifecycleEvent{
		Kind:    presentation.RichLifecycleCompleted,
		Stage:   presentation.StageUpload,
		Message: "generation uploaded",
	})
	m = m2.(LifecycleModel)
	if got := m.StageState(presentation.StageUpload); got != WaypointCompleted {
		t.Fatalf("upload state after completion = %v, want completed", got)
	}
	if got := m.StageState(presentation.StagePointer); got != WaypointActive {
		t.Fatalf("pointer state after upload completion = %v, want active", got)
	}
}

func TestLifecycleModelFailurePreservesOneSafeRecoveryCommand(t *testing.T) {
	m := newLifecycleTestModel(t, "close")
	m2, _ := m.Update(presentation.RichLifecycleEvent{
		Kind:            presentation.RichLifecycleFailed,
		Stage:           presentation.StageUpload,
		Message:         "checkpoint upload failed",
		RecoveryCommand: "camp recover session-alpha",
	})
	m = m2.(LifecycleModel)
	if m.Phase() != PhaseFailed {
		t.Fatalf("phase = %v, want failed", m.Phase())
	}
	plain := ansi.Strip(m.View().Content)
	if got := strings.Count(plain, "camp recover session-alpha"); got != 1 {
		t.Fatalf("recovery command occurrences = %d, want 1\n%s", got, plain)
	}
	if strings.Contains(plain, "CAMP IS READY") {
		t.Fatalf("failure frame claims readiness:\n%s", plain)
	}
	if !strings.Contains(plain, "LIFECYCLE STOPPED") {
		t.Fatalf("failure frame is not identified as lifecycle output:\n%s", plain)
	}
}

func TestLifecycleSuccessDoesNotClaimCampReadiness(t *testing.T) {
	m := newLifecycleTestModel(t, "sync")
	next, _ := m.Update(presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleSucceeded})
	m = next.(LifecycleModel)
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "LIFECYCLE COMPLETE") {
		t.Fatalf("success frame does not identify lifecycle completion:\n%s", plain)
	}
	if strings.Contains(plain, "CAMP IS READY") {
		t.Fatalf("sync success falsely claims camp readiness:\n%s", plain)
	}
}

func TestLifecycleModelCancellationAndResizeStayTerminalNormal(t *testing.T) {
	m := newLifecycleTestModel(t, "open")
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = sized.(LifecycleModel)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 48})
	m = resized.(LifecycleModel)
	if got := m.Width(); got != 160 {
		t.Fatalf("width after resize = %d, want 160", got)
	}
	canceled, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m = canceled.(LifecycleModel)
	if !m.Canceled() || cmd == nil {
		t.Fatal("ctrl+c must cancel the lifecycle model with a quit command")
	}
	if strings.Contains(ansi.Strip(m.View().Content), "next: camp recover") {
		t.Fatal("cancellation must not invent a recovery command")
	}
}

func TestLifecycleSceneWindowsCoverEveryStage(t *testing.T) {
	m := newLifecycleTestModel(t, "sync")
	for _, stage := range presentation.VisualLifecycleStages() {
		next, _ := m.Update(presentation.RichLifecycleEvent{Kind: presentation.RichLifecycleActivity, Stage: stage, Message: "working"})
		m = next.(LifecycleModel)
		plain := ansi.Strip(m.View().Content)
		if !strings.Contains(plain, presentation.LifecycleStageLabel(stage)) {
			t.Fatalf("active stage %q is absent from scene:\n%s", stage, plain)
		}
	}
}

func TestLifecycleSampleFramesFitGoldenSizes(t *testing.T) {
	sprites := loadForTest(t)
	for _, size := range [][2]int{{80, 24}, {120, 40}, {160, 48}} {
		frame := SampleLifecycleFrame("progress", size[0], size[1], DefaultPalette(), sprites)
		lines := strings.Split(frame, "\n")
		if len(lines) > size[1] {
			t.Fatalf("%dx%d frame has %d lines", size[0], size[1], len(lines))
		}
		for i, line := range lines {
			if width := ansi.StringWidth(line); width > size[0] {
				t.Fatalf("%dx%d line %d width = %d", size[0], size[1], i, width)
			}
		}
	}
}

func TestLifecycleSceneGoldens(t *testing.T) {
	sprites := loadForTest(t)
	cases := []struct {
		name, state, want string
		width, height     int
	}{
		{"progress-80x24", "progress", "ee4dd92ffda8547add71d92e452239a9cef94eb747bc22e9641c424efeed6fac", 80, 24},
		{"progress-120x40", "progress", "0b9d20f0d55c54d21ae6bed5e508db7cda2dcf0dd41952d9ffa00109378b638f", 120, 40},
		{"progress-160x48", "progress", "7919a70d2bb806c372ff2f86164063ba558ea3f0b7c9ed1772efae7b100d951d", 160, 48},
		{"ready-160x48", "ready", "8099b12295678072b82934d3987fdbfe423649b6ff62817626aa1d3e84332b23", 160, 48},
		{"failure-120x40", "failure", "e25c5c426bb81c890e0b4eeecd56ad60da931b41672b9c552887555d5134439b", 120, 40},
	}
	for _, tc := range cases {
		sum := sha256.Sum256([]byte(SampleLifecycleFrame(tc.state, tc.width, tc.height, DefaultPalette(), sprites)))
		got := stdhex.EncodeToString(sum[:])
		if got != tc.want {
			t.Fatalf("%s lifecycle scene golden = %s, want %s", tc.name, got, tc.want)
		}
	}
}
