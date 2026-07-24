package setupui

import (
	"strings"
	"testing"

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
