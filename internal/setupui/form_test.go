package setupui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPadLabelUsesTerminalDisplayWidth(t *testing.T) {
	got := padLabel("世a界b")
	if ansi.StringWidth(got) != 16 {
		t.Fatalf("padLabel width = %d, want 16", ansi.StringWidth(got))
	}
	if ansi.StringWidth("世a界b") != 6 {
		t.Fatalf("padLabel input width is %d, want 6", ansi.StringWidth("世a界b"))
	}
	if gotSpaces := ansi.StringWidth(got) - ansi.StringWidth("世a界b"); gotSpaces != 10 {
		t.Fatalf("padLabel added %d display cells, want 10", gotSpaces)
	}
	if !strings.HasPrefix(got, "世a界b") {
		t.Fatalf("expected preserved prefix, got %q", got)
	}
}

func TestConfigFormContainsMachineDefaultsOnly(t *testing.T) {
	f := NewConfigForm(DefaultPalette(), map[string]string{"backend": "file://backend", "provider": "docker", "context": "default"})
	values := f.Values()
	if len(values) != 3 || values["backend"] != "file://backend" || values["provider"] != "docker" || values["context"] != "default" {
		t.Fatalf("machine values = %#v", values)
	}
	if _, ok := values["source"]; ok {
		t.Fatalf("setup form exposed source: %#v", values)
	}
	if _, ok := values["capsule"]; ok {
		t.Fatalf("setup form exposed capsule: %#v", values)
	}
}

func TestConfigFormNavigationMovesBothDirections(t *testing.T) {
	f := NewConfigForm(DefaultPalette(), map[string]string{"backend": "file://backend", "provider": "docker", "context": "default"})
	var cmd tea.Cmd
	f, cmd = f.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if cmd == nil {
		t.Fatal("expected blink cmd on tab navigation")
	}
	f, cmd = f.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if cmd == nil {
		t.Fatal("expected blink cmd on reverse navigation")
	}
	if f.focus != 0 {
		t.Fatalf("focus = %d, want first field", f.focus)
	}
}

func TestConfigFormEscapeMovesBackBeforeCanceling(t *testing.T) {
	f := NewConfigForm(DefaultPalette(), map[string]string{"backend": "file://backend", "provider": "docker", "context": "default"})
	f, _ = f.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	f, cmd := f.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if f.focus != 0 || cmd != nil {
		t.Fatalf("escape from later field focus=%d cmd=%v, want previous field without cancel", f.focus, cmd != nil)
	}
	_, cmd = f.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("escape from first field should cancel")
	}
	if _, ok := cmd().(FormCancelMsg); !ok {
		t.Fatalf("escape command = %T, want FormCancelMsg", cmd())
	}
}
