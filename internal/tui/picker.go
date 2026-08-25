// Package tui is the interactive surface: a box picker and a create form.
//
// Every program here RETURNS A DECISION AND EXITS before anything touches
// ssh: ssh needs the real terminal, and a TUI still holding the alternate
// screen would fight it for every byte. So the flow is picker → Result →
// (main connects), never picker-spawns-ssh.
package tui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
)

// Result is what a picker run decided.
type Result struct {
	Action string // "connect" | "form" | "quit"
	ID     string
	Create CreateOpts
}

// CreateOpts is the form's output; zero fields mean "the config default".
type CreateOpts struct {
	Name       string
	MemMiB     int
	Vcpus      int
	DiskMiB    int
	IdleTtlSec int
	// Egress is the privacy preset ("" = sealed); Allow is the filtered
	// mode's comma-separated name list.
	Egress string
	Allow  string
}

const newBoxID = "\x00new"

// ------------------------------------------------------------------- theme

var (
	accent  = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#9D99FF"}
	subtle  = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"}
	faintFg = lipgloss.AdaptiveColor{Light: "#B2B2B2", Dark: "#4A4A4A"}
	danger  = lipgloss.AdaptiveColor{Light: "#D0342C", Dark: "#FF6B61"}

	brandStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dimStyle   = lipgloss.NewStyle().Foreground(subtle)
	faintStyle = lipgloss.NewStyle().Foreground(faintFg)
	titleStyle = lipgloss.NewStyle().Bold(true)
	warnStyle  = lipgloss.NewStyle().Foreground(danger)

	selBar    = lipgloss.NewStyle().Foreground(accent).SetString("▌ ")
	selName   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	plainName = lipgloss.NewStyle()

	statusStyles = map[string]lipgloss.Style{
		"idle":      lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#1A8917", Dark: "#3DDC5B"}),
		"busy":      lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#B7791F", Dark: "#F5C043"}),
		"starting":  lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#0B7285", Dark: "#4DD0E1"}),
		"suspended": lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#9D99FF"}),
		"failed":    lipgloss.NewStyle().Foreground(danger),
		"stopped":   faintStyle,
	}
)

func statusDot(status string) string {
	dot := "●"
	if status == "suspended" || status == "stopped" {
		dot = "○"
	}
	if s, ok := statusStyles[status]; ok {
		return s.Render(dot)
	}
	return faintStyle.Render(dot)
}

func statusText(status string) string {
	if s, ok := statusStyles[status]; ok {
		return s.Render(status)
	}
	return dimStyle.Render(status)
}

// ------------------------------------------------------------------- items

// boxItem is one row: the pinned "new box" entry, or a sandbox with its
// status filled in asynchronously.
type boxItem struct {
	id      string
	created time.Time
	status  string // empty until the per-id Get lands
	memUsed int
	memMiB  int
}

func (b boxItem) FilterValue() string { return b.id }

// delegate renders one line per box — compact, because a picker is for
// choosing, not for reading.
type delegate struct {
	spin spinner.Model
}

func (d delegate) Height() int                             { return 1 }
func (d delegate) Spacing() int                            { return 0 }
func (d delegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d delegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	b, ok := item.(boxItem)
	if !ok {
		return
	}
	selected := index == m.Index()

	prefix := "  "
	if selected {
		prefix = selBar.String()
	}

	if b.id == newBoxID {
		label := "＋ new box"
		if selected {
			fmt.Fprint(w, prefix+selName.Render(label))
		} else {
			fmt.Fprint(w, prefix+dimStyle.Render(label))
		}
		return
	}

	name := plainName.Render(padRight(b.id, 26))
	if selected {
		name = selName.Render(padRight(b.id, 26))
	}

	status := b.status
	var statusCol string
	if status == "" {
		statusCol = d.spin.View() + dimStyle.Render(" …")
	} else {
		statusCol = statusDot(status) + " " + statusText(padRight(status, 10))
	}

	mem := ""
	if b.memMiB > 0 {
		mem = fmt.Sprintf("%d/%d MiB", b.memUsed, b.memMiB)
	}

	fmt.Fprint(w, prefix+name+" "+padRightANSI(statusCol, 14)+" "+
		dimStyle.Render(padRight(mem, 14))+" "+faintStyle.Render(compactAge(b.created)))
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// padRightANSI pads by VISIBLE width, because styled strings carry escapes.
func padRightANSI(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func compactAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ---------------------------------------------------------------- messages

type listedMsg struct {
	items []boxItem
	err   error
}
type statusMsg struct {
	id string
	sb api.Sandbox
	ok bool
}
type deletedMsg struct {
	id  string
	err error
}
type whoamiMsg struct {
	login string
}

// ------------------------------------------------------------------- model

type pickerModel struct {
	cl      *api.Client
	title   string
	login   string
	list    list.Model
	del     delegate
	spin    spinner.Model
	width   int
	loading bool
	// confirm holds the id `d` is waiting on; y deletes, anything else drops.
	confirm string
	note    string
	result  Result
}

func (m pickerModel) fetchList() tea.Cmd {
	cl := m.cl
	return func() tea.Msg {
		sums, err := cl.List(context.Background())
		if err != nil {
			return listedMsg{err: err}
		}
		sort.Slice(sums, func(a, b int) bool { return sums[a].CreatedAt.After(sums[b].CreatedAt) })
		items := make([]boxItem, 0, len(sums)+1)
		items = append(items, boxItem{id: newBoxID})
		for _, s := range sums {
			items = append(items, boxItem{id: s.ID, created: s.CreatedAt})
		}
		return listedMsg{items: items}
	}
}

func (m pickerModel) fetchStatus(id string) tea.Cmd {
	cl := m.cl
	return func() tea.Msg {
		sb, err := cl.Get(context.Background(), id)
		return statusMsg{id: id, sb: sb, ok: err == nil}
	}
}

func (m pickerModel) fetchWhoami() tea.Cmd {
	cl := m.cl
	return func() tea.Msg {
		w, err := cl.Whoami(context.Background())
		if err != nil {
			return whoamiMsg{}
		}
		return whoamiMsg{login: w.Login}
	}
}

func (m pickerModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.fetchList(), m.fetchWhoami())
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.list.SetSize(msg.Width, msg.Height-6)
		return m, nil

	case whoamiMsg:
		m.login = msg.login
		return m, nil

	case listedMsg:
		m.loading = false
		if msg.err != nil {
			m.note = "could not load boxes: " + msg.err.Error()
			return m, nil
		}
		items := make([]list.Item, len(msg.items))
		cmds := make([]tea.Cmd, 0, len(msg.items))
		for i, it := range msg.items {
			items[i] = it
			if it.id != newBoxID {
				cmds = append(cmds, m.fetchStatus(it.id))
			}
		}
		m.list.SetItems(items)
		return m, tea.Batch(cmds...)

	case statusMsg:
		for i, it := range m.list.Items() {
			b := it.(boxItem)
			if b.id != msg.id {
				continue
			}
			if msg.ok {
				b.status, b.memMiB, b.memUsed = msg.sb.Status, msg.sb.MemMiB, msg.sb.MemUsedMiB
			} else {
				b.status = "?"
			}
			m.list.SetItem(i, b)
		}
		return m, nil

	case deletedMsg:
		if msg.err != nil {
			m.note = "delete failed: " + msg.err.Error()
			return m, nil
		}
		m.note = "deleted " + msg.id
		return m, m.fetchList()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.del.spin = m.spin
		m.list.SetDelegate(m.del)
		return m, cmd

	case tea.KeyMsg:
		// A pending delete captures the next key entirely.
		if m.confirm != "" {
			id := m.confirm
			m.confirm = ""
			if msg.String() == "y" {
				cl := m.cl
				m.note = "deleting " + id + "…"
				return m, func() tea.Msg { return deletedMsg{id: id, err: cl.Delete(context.Background(), id)} }
			}
			m.note = ""
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.result = Result{Action: "quit"}
			return m, tea.Quit
		case "enter":
			if b, ok := m.list.SelectedItem().(boxItem); ok {
				if b.id == newBoxID {
					m.result = Result{Action: "form"}
				} else {
					m.result = Result{Action: "connect", ID: b.id}
				}
				return m, tea.Quit
			}
		case "n":
			m.result = Result{Action: "form"}
			return m, tea.Quit
		case "d":
			if b, ok := m.list.SelectedItem().(boxItem); ok && b.id != newBoxID {
				m.confirm = b.id
			}
			return m, nil
		case "r":
			m.note = ""
			return m, m.fetchList()
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// header is the brand on the left and the signed-in login on the right,
// spread across the width.
func (m pickerModel) header() string {
	left := brandStyle.Render("yas") + dimStyle.Render("  ·  "+m.title)
	right := ""
	if m.login != "" {
		right = dimStyle.Render(m.login)
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if gap < 1 {
		gap = 1
	}
	return "  " + left + strings.Repeat(" ", gap) + right
}

func (m pickerModel) footer() string {
	switch {
	case m.confirm != "":
		return "  " + warnStyle.Render("delete "+m.confirm+"?") + " " + dimStyle.Render("y confirms · any other key cancels")
	case m.note != "":
		return "  " + dimStyle.Render(m.note)
	default:
		keys := []string{"↵ connect", "n new", "d delete", "r refresh", "q quit"}
		return "  " + faintStyle.Render(strings.Join(keys, "   "))
	}
}

func (m pickerModel) View() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(m.header())
	b.WriteString("\n\n")
	if m.loading {
		b.WriteString("  " + m.spin.View() + dimStyle.Render(" loading your boxes…") + "\n")
	} else if len(m.list.Items()) <= 1 {
		b.WriteString(m.list.View())
		b.WriteString("  " + dimStyle.Render("no boxes yet — the first one is a keypress away") + "\n")
	} else {
		b.WriteString(m.list.View())
	}
	b.WriteString("\n")
	b.WriteString(m.footer())
	b.WriteString("\n")
	return b.String()
}

// RunPicker shows the box list and returns what the user chose. An Action of
// "form" means "they want a new box; show the form" — the caller decides,
// because the form is its own program.
func RunPicker(cl *api.Client, title string) (Result, error) {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(lipgloss.NewStyle().Foreground(accent)))
	del := delegate{spin: sp}
	l := list.New(nil, del, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowPagination(false)
	m := pickerModel{cl: cl, title: title, list: l, del: del, spin: sp, loading: true}
	out, err := tea.NewProgram(m).Run()
	if err != nil {
		return Result{}, err
	}
	return out.(pickerModel).result, nil
}
