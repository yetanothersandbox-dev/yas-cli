package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(subtle).
			Padding(1, 3)
	labelStyle      = lipgloss.NewStyle().Foreground(subtle).Width(12)
	labelFocusStyle = lipgloss.NewStyle().Foreground(accent).Bold(true).Width(12)
	markerStyle     = lipgloss.NewStyle().Foreground(accent)
)

// RunCreateForm collects a new box's shape. Every field is optional: enter on
// an untouched form creates with the generated name and the server defaults,
// which is the right default for "just give me a box". A non-empty note is
// shown as a warning — it is how a refused create (name taken) comes BACK to
// the form instead of ending the program with the user's input on the floor.
func RunCreateForm(suggestedName, note string) (CreateOpts, bool, error) {
	fields := []struct{ label, placeholder string }{
		{"name", suggestedName},
		{"memory", "server default (MiB)"},
		{"vcpus", "server default"},
		{"disk", "server default (MiB)"},
		{"idle ttl", "server default (sec)"},
	}
	inputs := make([]textinput.Model, len(fields))
	for i, f := range fields {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = f.placeholder
		ti.PlaceholderStyle = faintStyle
		ti.CharLimit = 64
		// Width is load-bearing: textinput truncates the PLACEHOLDER to it,
		// and the zero value renders exactly one character.
		ti.Width = 28
		ti.Cursor.Style = markerStyle
		inputs[i] = ti
	}
	inputs[0].Focus()
	labels := make([]string, len(fields))
	for i, f := range fields {
		labels[i] = f.label
	}
	m := formModel{labels: labels, inputs: inputs, suggested: suggestedName, note: note}
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
	note      string
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
	var rows []string
	rows = append(rows, brandStyle.Render("yas")+dimStyle.Render("  ·  new box"), "")
	if m.note != "" {
		rows = append(rows, warnStyle.Render(m.note), "")
	}
	for i, in := range m.inputs {
		marker := "  "
		label := labelStyle.Render(m.labels[i])
		if i == m.focus {
			marker = markerStyle.Render("▸ ")
			label = labelFocusStyle.Render(m.labels[i])
		}
		rows = append(rows, marker+label+in.View())
	}
	rows = append(rows, "", faintStyle.Render("↵ create   tab next   esc cancel"))
	return "\n" + cardStyle.Render(strings.Join(rows, "\n")) + "\n"
}
