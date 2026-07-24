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

func TestConfigFormDerivesCapsuleFromSourceWhenNotEdited(t *testing.T) {
	f := NewConfigForm(DefaultPalette(), map[string]string{"source": "/tmp/notes/journal", "backend": "file://backend"})
	f.fields[1].input.SetValue("")
	values := f.Values()
	if got, want := values["capsule"], "journal"; got != want {
		t.Fatalf("capsule = %q, want %q", got, want)
	}
}

func TestConfigFormNavigationDoesNotMarkCapsuleEdited(t *testing.T) {
	f := NewConfigForm(DefaultPalette(), map[string]string{"source": "/tmp/notes/journal", "backend": "file://backend"})
	var cmd tea.Cmd
	f, cmd = f.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if cmd == nil {
		t.Fatal("expected blink cmd on tab navigation")
	}
	f, cmd = f.Update(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if cmd == nil {
		t.Fatal("expected blink cmd on reverse navigation")
	}
	f.fields[0].input.SetValue("/tmp/notes/renamed")
	f, _ = f.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if f.capsuleEdited {
		t.Fatal("capsuleEdited should stay false on non-capsule interaction")
	}
}

func TestConfigFormPrefersExplicitCapsuleEdit(t *testing.T) {
	f := NewConfigForm(DefaultPalette(), map[string]string{"source": "/tmp/notes/journal", "backend": "file://backend"})
	f.fields[0].input.SetValue("/tmp/notes/journal")
	f.fields[1].input.SetValue("manual-capsule")
	// Simulate a real capsule edit to avoid source auto-derivation.
	f.capsuleEdited = true
	values := f.Values()
	if got, want := values["capsule"], "manual-capsule"; got != want {
		t.Fatalf("capsule = %q, want %q", got, want)
	}
}
