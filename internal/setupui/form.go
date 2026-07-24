package setupui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// FormField is one labeled configuration input with a default value.
type FormField struct {
	Key         string
	Label       string
	Placeholder string
	Default     string
	input       textinput.Model
}

// ConfigForm collects the five first-run settings inside the scene. It renders
// as a compact panel overlaid on the valley; the terminal's own cursor (shown
// only while a field is focused) is the focus indicator, exactly as the
// deterministic plain path relies on the shell cursor.
type ConfigForm struct {
	fields  []FormField
	focus   int
	width   int
	errText string
	pal     Palette
}

// NewConfigForm builds the form with Camp's setup fields and their defaults.
func NewConfigForm(pal Palette, defaults map[string]string) ConfigForm {
	specs := []FormField{
		{Key: "source", Label: "Source path", Placeholder: "/path/to/notes"},
		{Key: "capsule", Label: "Capsule name", Placeholder: "capsule"},
		{Key: "backend", Label: "Backend URL", Placeholder: "file://…"},
		{Key: "provider", Label: "DevPod provider", Placeholder: "docker"},
		{Key: "context", Label: "DevPod context", Placeholder: "default"},
	}
	f := ConfigForm{pal: pal}
	for i := range specs {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = specs[i].Placeholder
		if d, ok := defaults[specs[i].Key]; ok {
			specs[i].Default = d
			ti.SetValue(d)
		}
		ti.SetWidth(40)
		specs[i].input = ti
		f.fields = append(f.fields, specs[i])
	}
	f.fields[0].input.Focus()
	return f
}

// SetWidth adjusts input widths to the available panel width.
func (f *ConfigForm) SetWidth(w int) {
	f.width = w
	iw := min(w-8, 48)
	if iw < 10 {
		iw = 10
	}
	for i := range f.fields {
		f.fields[i].input.SetWidth(iw)
	}
}

// Values returns the current field values keyed by field key, substituting the
// default when a field was left blank.
func (f ConfigForm) Values() map[string]string {
	out := make(map[string]string, len(f.fields))
	for _, fld := range f.fields {
		v := strings.TrimSpace(fld.input.Value())
		if v == "" {
			v = strings.TrimSpace(fld.Default)
		}
		out[fld.Key] = v
	}
	return out
}

// Complete reports whether every field resolves to a non-empty value.
func (f ConfigForm) Complete() bool {
	for _, v := range f.Values() {
		if v == "" {
			return false
		}
	}
	return true
}

// FormSubmitMsg is emitted when the user submits a valid form.
type FormSubmitMsg struct{ Values map[string]string }

// FormCancelMsg is emitted on cancellation from the form.
type FormCancelMsg struct{}

func (f ConfigForm) Init() tea.Cmd { return textinput.Blink }

// Update handles focus movement, submission, and per-field editing.
func (f ConfigForm) Update(msg tea.Msg) (ConfigForm, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab", "down":
			return f.advance(1), textinput.Blink
		case "shift+tab", "up":
			return f.advance(-1), textinput.Blink
		case "enter":
			if f.focus == len(f.fields)-1 {
				if f.Complete() {
					vals := f.Values()
					return f, func() tea.Msg { return FormSubmitMsg{Values: vals} }
				}
				f.errText = "all fields are required"
				return f, nil
			}
			return f.advance(1), textinput.Blink
		case "esc":
			return f, func() tea.Msg { return FormCancelMsg{} }
		}
	}
	f.errText = ""
	var cmd tea.Cmd
	f.fields[f.focus].input, cmd = f.fields[f.focus].input.Update(msg)
	return f, cmd
}

func (f ConfigForm) advance(delta int) ConfigForm {
	f.fields[f.focus].input.Blur()
	f.focus = (f.focus + delta + len(f.fields)) % len(f.fields)
	f.fields[f.focus].input.Focus()
	return f
}

// View renders the form panel: a title, each labeled field with the focused one
// highlighted, and an optional error line.
func (f ConfigForm) View() string {
	title := lipgloss.NewStyle().Foreground(f.pal.Amber).Bold(true).Render("CONFIGURE  ")
	var rows []string
	rows = append(rows, title)
	for i, fld := range f.fields {
		labelStyle := lipgloss.NewStyle().Foreground(f.pal.Dim)
		marker := "  "
		if i == f.focus {
			labelStyle = lipgloss.NewStyle().Foreground(f.pal.Active).Bold(true)
			marker = lipgloss.NewStyle().Foreground(f.pal.Active).Render("❯ ")
		}
		label := labelStyle.Render(padLabel(fld.Label))
		box := f.inputBox(fld, i == f.focus)
		rows = append(rows, marker+label+" "+box)
	}
	if f.errText != "" {
		rows = append(rows, "", lipgloss.NewStyle().Foreground(f.pal.Fail).Render(f.errText))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (f ConfigForm) inputBox(fld FormField, focused bool) string {
	border := f.pal.Dim
	if focused {
		border = f.pal.Active
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(0, 1)
	return style.Render(fld.input.View())
}

func padLabel(s string) string {
	const w = 16
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
