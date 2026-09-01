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

// Defaults is what this account's boxes are, so the form can SAY it.
//
// Every size row used to read "server default (MiB)", which is a placeholder
// pretending to be an answer: it tells you a default exists and not what it is,
// so the only way to find out was to make a box and look. These come from
// /v1/user/box-defaults, which already knows both the effective size and the
// ceiling the plan allows.
type Defaults struct {
	MemMiB       int
	MilliVcpu    int
	MaxMemMiB    int
	MaxMilliVcpu int
}

// choice is one option on a selectable row.
type choice struct {
	label string
	value int
	// hint explains a choice in a few words, shown dim beside it. It is on the
	// CHOICE and not the row because it changes as you move through them.
	hint string
}

type rowKind int

const (
	rowText rowKind = iota
	rowChoice
)

type row struct {
	kind    rowKind
	label   string
	input   textinput.Model
	choices []choice
	sel     int
	// dflt is the index that matches this account's default, so the form can
	// mark it. -1 when none of the offered values is the default.
	dflt int
	// hint trails a text row, for the one field whose meaning depends on
	// another row's answer.
	hint string
}

func (r row) value() int {
	if r.kind != rowChoice || len(r.choices) == 0 {
		return 0
	}
	return r.choices[r.sel].value
}

// memLadder and vcpuLadder are the sizes worth offering, bounded by the plan.
//
// The account's own default is always included even when it is not on the
// ladder — a tenant whose default was set to something unusual must still see
// it, and must not have it silently rounded to a neighbour.
func memLadder(d Defaults) ([]choice, int) {
	return ladder([]int{512, 1024, 2048, 4096, 8192, 16384, 32768, 65536},
		d.MemMiB, d.MaxMemMiB, gib)
}

func vcpuLadder(d Defaults) ([]choice, int) {
	return ladder([]int{500, 1000, 2000, 4000, 8000, 16000},
		d.MilliVcpu, d.MaxMilliVcpu, vcpuLabel)
}

func ladder(steps []int, dflt, max int, label func(int) string) ([]choice, int) {
	var vals []int
	for _, s := range steps {
		if max > 0 && s > max {
			continue
		}
		vals = append(vals, s)
	}
	if dflt > 0 {
		found := false
		for _, v := range vals {
			if v == dflt {
				found = true
			}
		}
		if !found {
			vals = append(vals, dflt)
			sortInts(vals)
		}
	}
	if len(vals) == 0 && dflt > 0 {
		vals = []int{dflt}
	}
	out := make([]choice, len(vals))
	at := -1
	for i, v := range vals {
		out[i] = choice{label: label(v), value: v}
		if v == dflt {
			at = i
		}
	}
	return out, at
}

func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// gib renders MiB the way the dashboard does, so the two agree.
func gib(mib int) string {
	g := float64(mib) / 1024
	if g == float64(int(g)) {
		return strconv.Itoa(int(g)) + " GiB"
	}
	return strings.TrimSuffix(strconv.FormatFloat(g, 'f', 1, 64), ".0") + " GiB"
}

// vcpuLabel renders milli-vCPU as vCPU. 500 is half a vCPU, not 500 of them.
func vcpuLabel(milli int) string {
	v := float64(milli) / 1000
	if v == float64(int(v)) {
		return strconv.Itoa(int(v)) + " vCPU"
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " vCPU"
}

// RunCreateForm collects a new box's shape. Every field is optional: enter on
// an untouched form creates with the generated name and this account's own
// defaults, which is the right answer to "just give me a box".
//
// A non-empty note is shown as a warning — it is how a refused create (name taken) comes BACK to
// the form instead of ending the program with the user's input on the floor.
func RunCreateForm(suggestedName, note string, d Defaults) (CreateOpts, bool, error) {
	// Size is back, and it SAYS what it is.
	//
	// The rows that carry a number are chosen with the arrow keys rather than
	// typed, and the one this account would get anyway is marked. A form that
	// only says "server default" makes you create a box to find out what the
	// default was; one that offers a free-text MiB field makes you know the
	// ladder before you can use it.
	//
	// Disk is deliberately not here. The other two have a real number to show —
	// box-defaults reports both — and disk's default is whatever the template
	// happened to be, which nothing on this side of the API knows. Offering a
	// row whose default reads "template default" would be the placeholder this
	// change exists to remove. `yas new -disk` still sets it.
	mem, memDflt := memLadder(d)
	cpu, cpuDflt := vcpuLadder(d)

	name := textinput.New()
	name.Placeholder = suggestedName
	allow := textinput.New()
	allow.Placeholder = "github.com,pypi.org"

	rows := []row{
		{kind: rowText, label: "name", input: name},
		{kind: rowChoice, label: "memory", choices: mem, sel: max0(memDflt), dflt: memDflt},
		{kind: rowChoice, label: "vcpu", choices: cpu, sel: max0(cpuDflt), dflt: cpuDflt},
		// dflt is -1: "your default" means the size this ACCOUNT is set to, and
		// egress has no such setting. Marking proxy as "your default" would
		// invent an account preference nobody chose.
		{kind: rowChoice, label: "egress", dflt: -1, choices: []choice{
			{label: "proxy", hint: "no routed network"},
			{label: "filtered", hint: "only the names you allow"},
			{label: "open", hint: "anywhere"},
		}},
		{kind: rowText, label: "allow", input: allow, hint: "filtered only"},
	}
	for i := range rows {
		if rows[i].kind != rowText {
			continue
		}
		rows[i].input.Prompt = ""
		rows[i].input.PlaceholderStyle = faintStyle
		rows[i].input.CharLimit = 200
		rows[i].input.Width = 22
		rows[i].input.Cursor.Style = markerStyle
	}

	m := formModel{rows: rows, suggested: suggestedName, note: note}
	m.rows[0].input.Focus()
	out, err := tea.NewProgram(m).Run()
	if err != nil {
		return CreateOpts{}, false, err
	}
	fm := out.(formModel)
	if fm.cancelled {
		return CreateOpts{}, false, nil
	}
	got := strings.TrimSpace(fm.rows[0].input.Value())
	if got == "" {
		got = suggestedName
	}
	egress := [...]string{"proxy", "filtered", "open"}[fm.rows[3].sel]
	return CreateOpts{
		Name:      got,
		MemMiB:    fm.rows[1].value(),
		MilliVcpu: fm.rows[2].value(),
		Egress:    egress,
		Allow:     strings.TrimSpace(fm.rows[4].input.Value()),
	}, true, nil
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

type formModel struct {
	rows      []row
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
			m.focus = (m.focus + 1) % len(m.rows)
			return m.refocus()
		case "shift+tab", "up":
			m.focus = (m.focus + len(m.rows) - 1) % len(m.rows)
			return m.refocus()
		case "left", "right":
			// Only a choice row moves. On a text row these are ordinary cursor
			// keys and must stay that way, or editing a name becomes a fight.
			r := &m.rows[m.focus]
			if r.kind == rowChoice && len(r.choices) > 0 {
				if key.String() == "left" {
					r.sel = (r.sel + len(r.choices) - 1) % len(r.choices)
				} else {
					r.sel = (r.sel + 1) % len(r.choices)
				}
				return m, nil
			}
		}
	}
	if m.rows[m.focus].kind != rowText {
		return m, nil
	}
	var cmd tea.Cmd
	m.rows[m.focus].input, cmd = m.rows[m.focus].input.Update(msg)
	return m, cmd
}

func (m formModel) refocus() (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	for i := range m.rows {
		if m.rows[i].kind != rowText {
			continue
		}
		if i == m.focus {
			cmd = m.rows[i].input.Focus()
		} else {
			m.rows[i].input.Blur()
		}
	}
	return m, cmd
}

func (m formModel) View() string {
	var out []string
	out = append(out, brandStyle.Render("yas")+dimStyle.Render("  ·  new box"), "")
	if m.note != "" {
		out = append(out, warnStyle.Render(m.note), "")
	}
	for i, r := range m.rows {
		marker, label := "  ", labelStyle.Render(r.label)
		if i == m.focus {
			marker, label = markerStyle.Render("▸ "), labelFocusStyle.Render(r.label)
		}
		out = append(out, marker+label+m.renderRow(i, r))
	}
	out = append(out, "", faintStyle.Render("↵ create   ←→ change   tab next   esc cancel"))
	return "\n" + cardStyle.Render(strings.Join(out, "\n")) + "\n"
}

func (m formModel) renderRow(i int, r row) string {
	if r.kind == rowText {
		v := r.input.View()
		if r.hint != "" {
			v += faintStyle.Render("   " + r.hint)
		}
		return v
	}
	if len(r.choices) == 0 {
		return faintStyle.Render("—")
	}
	v := r.choices[r.sel].label
	// The account's own default, named where it is relevant. Marking it on the
	// row rather than in a footnote is the whole point: the question "what do I
	// get if I do nothing" is answered without leaving the form.
	if r.dflt >= 0 && r.sel == r.dflt {
		v += dimStyle.Render(" (your default)")
	}
	if h := r.choices[r.sel].hint; h != "" {
		v += faintStyle.Render("   " + h)
	}
	if i == m.focus {
		return markerStyle.Render("‹ ") + v + markerStyle.Render(" ›")
	}
	return "  " + v
}
