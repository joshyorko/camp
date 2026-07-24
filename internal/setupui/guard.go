package setupui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// SizeGuard enforces the supported-size boundary for the rich scene. Below the
// minimum the caller should not have entered rich mode at all, but if a live
// resize shrinks the window we show a legible prompt instead of a clipped
// scene. Adapted from Basecamp/ONCE internal/ui/terminal_size_guard.go.
type SizeGuard struct {
	minW, minH int
	w, h       int
}

func NewSizeGuard(minW, minH int) SizeGuard {
	return SizeGuard{minW: minW, minH: minH}
}

func (g SizeGuard) Update(w, h int) SizeGuard {
	g.w, g.h = w, h
	return g
}

// OK reports whether the current size can render the scene.
func (g SizeGuard) OK() bool {
	return g.w >= g.minW && g.h >= g.minH
}

func (g SizeGuard) View(pal Palette) string {
	l1 := fmt.Sprintf("Terminal too small: %d×%d", g.w, g.h)
	l2 := fmt.Sprintf("Trailhead needs at least %d×%d", g.minW, g.minH)
	style := lipgloss.NewStyle().Foreground(pal.Dim)
	msg := lipgloss.JoinVertical(lipgloss.Center, style.Render(l1), "", style.Render(l2))
	return lipgloss.Place(g.w, g.h, lipgloss.Center, lipgloss.Center, msg)
}
