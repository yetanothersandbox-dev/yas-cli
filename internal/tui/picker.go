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
	Action string // "connect" | "create" | "quit"
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
}

const newBoxID = "\x00new"

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

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	activeStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#005f87", Dark: "#5fd7ff"})
	statusHue   = map[string]lipgloss.Style{
		"idle":      lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		"busy":      lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		"suspended": lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		"failed":    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	}
)

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
	cursor := "  "
	line := ""
	if b.id == newBoxID {
		line = "+ new box"
	} else {
		status := b.status
		if status == "" {
			status = d.spin.View()
		} else if s, ok := statusHue[status]; ok {
			status = s.Render(status)
		}
		mem := ""
		if b.memMiB > 0 {
			mem = dimStyle.Render(fmt.Sprintf("  %d/%dMiB", b.memUsed, b.memMiB))
		}
		line = fmt.Sprintf("%-28s %s%s  %s", b.id, status, mem, dimStyle.Render(compactAge(b.created)))
	}
	if index == m.Index() {
		cursor = activeStyle.Render("> ")
		line = activeStyle.Render(line)
	}
	fmt.Fprint(w, cursor+line)
}

func compactAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Messages.
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

type pickerModel struct {
	cl      *api.Client
	title   string
	list    list.Model
	del     delegate
	spin    spinner.Model
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

func (m pickerModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.fetchList())
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-3)
		return m, nil

	case listedMsg:
		m.loading = false
		if msg.err != nil {
			m.note = "list failed: " + msg.err.Error()
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
				m.note = "deleting " + id + "..."
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

func (m pickerModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(m.title) + "\n")
	b.WriteString(m.list.View())
	b.WriteString("\n")
	switch {
	case m.confirm != "":
		b.WriteString(activeStyle.Render("delete " + m.confirm + "? press y to confirm"))
	case m.note != "":
		b.WriteString(dimStyle.Render(m.note))
	default:
		b.WriteString(dimStyle.Render("enter connect · n new · d delete · r refresh · q quit"))
	}
	return b.String()
}

// RunPicker shows the box list and returns what the user chose. An Action of
// "form" means "they want a new box; show the form" — the caller decides,
// because the form is its own program.
func RunPicker(cl *api.Client, title string) (Result, error) {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	del := delegate{spin: sp}
	l := list.New(nil, del, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	m := pickerModel{cl: cl, title: title, list: l, del: del, spin: sp, loading: true}
	out, err := tea.NewProgram(m).Run()
	if err != nil {
		return Result{}, err
	}
	return out.(pickerModel).result, nil
}
