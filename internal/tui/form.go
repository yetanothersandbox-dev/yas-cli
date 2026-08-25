package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// RunCreateForm collects a new box's shape. Every field is optional: enter on
// an untouched form creates with the generated name and the server defaults,
// which is the right default for "just give me a box".
func RunCreateForm(suggestedName string) (CreateOpts, bool, error) {
	fields := []struct{ label, placeholder string }{
		{"name", suggestedName},
		{"memory MiB", "server default"},
		{"vCPUs", "server default"},
		{"disk MiB", "server default"},
		{"idle TTL sec", "server default"},
	}
	inputs := make([]textinput.Model, len(fields))
	for i, f := range fields {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = f.placeholder
		ti.CharLimit = 64
		inputs[i] = ti
	}
	inputs[0].Focus()
	m := formModel{labels: fieldLabels(fields), inputs: inputs, suggested: suggestedName}
	out, err := tea.NewProgram(m).Run()
	if err != nil {
		return CreateOpts{}, false, err
	}
	fm := out.(formModel)
	if fm.cancelled {
		return CreateOpts{}, false, nil
	}
	name := strings.TrimSpace(fm.inputs[0].Value())
	if name == "" {
		name = suggestedName
	}
	return CreateOpts{
		Name:       name,
		MemMiB:     atoiOrZero(fm.inputs[1].Value()),
		Vcpus:      atoiOrZero(fm.inputs[2].Value()),
		DiskMiB:    atoiOrZero(fm.inputs[3].Value()),
		IdleTtlSec: atoiOrZero(fm.inputs[4].Value()),
	}, true, nil
}

func fieldLabels(fields []struct{ label, placeholder string }) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.label
	}
	return out
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

type formModel struct {
	labels    []string
	inputs    []textinput.Model
	focus     int
	suggested string
	cancelled bool
}

func (m formModel) Init() tea.Cmd { return textinput.Blink }

func (m formModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit
		case "enter":
			return m, tea.Quit
		case "tab", "down":
			m.focus = (m.focus + 1) % len(m.inputs)
			return m.refocus()
		case "shift+tab", "up":
			m.focus = (m.focus + len(m.inputs) - 1) % len(m.inputs)
			return m.refocus()
		}
	}
	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	return m, cmd
}

func (m formModel) refocus() (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	for i := range m.inputs {
		if i == m.focus {
			cmd = m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
	return m, cmd
}

func (m formModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("new box") + "\n\n")
	for i, in := range m.inputs {
		label := m.labels[i]
		if i == m.focus {
			label = activeStyle.Render(label)
		}
		b.WriteString("  " + pad(label, 14) + in.View() + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("enter create · tab next field · esc cancel"))
	return b.String()
}

func pad(s string, n int) string {
	// lipgloss styles carry escape codes, so measure the visible width.
	visible := len(stripANSI(s))
	if visible >= n {
		return s + " "
	}
	return s + strings.Repeat(" ", n-visible)
}

func stripANSI(s string) string {
	var out strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if r == 'm' {
				inEsc = false
			}
		case r == '\x1b':
			inEsc = true
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
