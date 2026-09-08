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
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/ui"
)

// Result is what a picker run decided.
type Result struct {
	Action string // "connect" | "form" | "quit"
	ID     string
	Create CreateOpts
}

// CreateOpts is the form's output; zero fields mean "the config default".
type CreateOpts struct {
	Name string
	// MemMiB and MilliVcpu are what the form's size rows resolved to. They are
	// always set — the rows start on this account's own default rather than on
	// "unset" — so the caller sends a size it can name rather than one it hopes
	// the server will pick.
	MemMiB    int
	MilliVcpu int
	// Egress is the egress preset ("" = proxy); Allow is the filtered
	// mode's comma-separated name list.
	//
	// No idle ttl: it is not the tenant's to set. No disk either — the form has
	// no honest default to show for it, and `yas new -disk` still does.
	Egress string
	Allow  string
}

const newBoxID = "\x00new"

// The layout, and the two things it gives up first.
//
// A picker has to work in the terminal it is given, and the terminal it is
// given is sometimes a 60×15 pane in a split. So the screen is built in three
// tiers and it sheds from the bottom of that list, not from the top: the box
// list is the reason the program exists and it is the last thing to lose a
// line.
//
//  1. narrow: the detail pane goes. It repeats what the row already says.
//  2. short: the pool bar goes, then the wordmark collapses to one line.
//  3. always: the list, the footer, and the ability to see which row is on.
const (
	// detailMin is the width below which a detail pane would be a column of
	// wrapped fragments rather than a pane.
	detailMin = 96
	detailW   = 32
	// detailMinH is the shortest list the pane can stand beside. Below it the
	// pane is taller than its own column and the footer falls off the frame.
	detailMinH = 12
	// poolMin and markMin are the heights that buy the pool bar and the full
	// three-line mark.
	poolMin = 20
	markMin = 13
)

// ------------------------------------------------------------------- items

// boxItem is one row: the pinned "new box" entry, or a sandbox with its
// status filled in asynchronously.
type boxItem struct {
	id      string
	created time.Time
	status  string // empty until the per-id Get lands
	memUsed int
	memMiB  int
	vcpu    int
	milli   int
	costUSD float64
	// gone is when this box stops existing: the lifetime wall while it runs,
	// the retention wall while it is parked. Zero until the detail fetch lands.
	gone time.Time
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
	width := m.Width()

	prefix := "  "
	if selected {
		prefix = selBar.String()
	}

	// The new-box row is drawn in the free-pool texture — a dashed run, the
	// same character the pool bar uses for memory nobody has claimed. Which is
	// what a box you have not made yet IS. It is the one row that is an offer
	// rather than a fact, and it should not look like a fact.
	if b.id == newBoxID {
		label := "+ new box"
		styled := dimStyle.Render(label)
		if selected {
			styled = selName.Render(label)
		}
		tail := width - lipgloss.Width(prefix) - lipgloss.Width(label) - 12
		row := prefix + styled
		if tail > 2 {
			row += " " + lineStyle.Render(strings.Repeat(ui.Dashed, tail)) +
				" " + faintStyle.Render("~400ms")
		}
		fmt.Fprint(w, row)
		return
	}

	// Column budget, widest-first. Every column below the terminal's width is
	// dropped whole rather than squeezed: half a memory figure is not a smaller
	// memory figure, it is a wrong one.
	nameW := 22
	if width < 56 {
		nameW = max(width-24, 8)
	}
	name := truncate(b.id, nameW)
	if selected {
		name = selName.Render(padRight(name, nameW))
	} else {
		name = plainName.Render(padRight(name, nameW))
	}

	var statusCol string
	if b.status == "" {
		statusCol = d.spin.View() + dimStyle.Render(" reaching it…")
	} else {
		statusCol = statusDot(b.status) + " " + statusText(b.status)
	}

	row := prefix + name + " " + padRightANSI(statusCol, 15)

	if width >= 62 {
		g := "        "
		if live(b.status) && b.memMiB > 0 {
			g = gauge(b.memUsed, b.memMiB, 8)
		}
		row += " " + padRightANSI(g, 8)
	}
	if width >= 78 {
		row += " " + padRightANSI(memCell(b), 16)
	}
	row += " " + faintStyle.Render(compactAge(b.created))
	fmt.Fprint(w, row)
}

// memCell says what a box draws from the pool.
//
// A parked box renders the SENTENCE and not an empty cell: that it costs
// nothing is the single best fact about suspending, and the column used to
// report it as blank.
func memCell(b boxItem) string {
	switch {
	case b.status == "suspended" || b.status == "stopped":
		return dimStyle.Render("nothing — parked")
	case b.memMiB <= 0:
		return faintStyle.Render("—")
	default:
		return dimStyle.Render(fmt.Sprintf("%s / %s GiB", ui.GiB(b.memUsed), ui.GiB(b.memMiB)))
	}
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

// goneAt is when a box stops existing, whichever clock is the one that applies.
//
// A parked box is not counting down its lifetime — it is not running — so
// showing the lifetime wall for one would name a time nothing happens at. What
// ends a parked box is retention.
func goneAt(sb api.Sandbox) time.Time {
	if parked(sb.Status) {
		return sb.SuspendExpiresAt
	}
	return sb.Deadline
}

// untilText renders how long is left, and the wall-clock time it runs out.
//
// Both, because they answer different questions: "how long have I got" is the
// one somebody asks at the picker, and "when exactly" is the one they need to
// decide whether that is before or after something else in their day. The
// absolute time is local — a box's deadline is only useful in the timezone of
// the person reading it.
func untilText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Until(t)
	if d <= 0 {
		return "any moment now"
	}
	// Rounded UP, unlike compactAge beside it, and the difference is the
	// direction of the error. An age that truncates says a box is slightly
	// younger than it is, which costs nothing. A countdown that truncates says
	// a box has LESS time than it has — five hours left reading as "4h" — and
	// the whole point of the row is to be trusted about that.
	ceil := func(d, unit time.Duration) int { return int((d + unit - 1) / unit) }
	var left string
	switch {
	case d < time.Minute:
		left = "<1m"
	case d < time.Hour:
		left = fmt.Sprintf("%dm", ceil(d, time.Minute))
	case d < 48*time.Hour:
		left = fmt.Sprintf("%dh", ceil(d, time.Hour))
	default:
		left = fmt.Sprintf("%dd", ceil(d, 24*time.Hour))
	}
	return left + " · " + t.Local().Format("2 Jan 15:04")
}

// soon is when a deadline is close enough to be worth a colour. An hour is the
// point at which "later" stops being a plan.
func soon(t time.Time) bool {
	return !t.IsZero() && time.Until(t) < time.Hour
}

func vcpuText(b boxItem) string {
	switch {
	case b.milli > 0:
		return fmt.Sprintf("%.2g vCPU", float64(b.milli)/1000)
	case b.vcpu > 0:
		return fmt.Sprintf("%d vCPU", b.vcpu)
	default:
		return "—"
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
type parkedMsg struct {
	id   string
	woke bool
	err  error
}
type whoamiMsg struct {
	login string
}
type poolMsg struct {
	pool *api.Pool
}

// ------------------------------------------------------------------- model

type pickerModel struct {
	cl    *api.Client
	title string
	login string
	pool  *api.Pool

	list list.Model
	del  delegate
	spin spinner.Model
	// all is the source of truth; list holds whatever survives the filter.
	all    []boxItem
	filter textinput.Model

	width, height int
	loading       bool
	// hidden is how many finished boxes sync() left out of the visible list, so
	// the footer can say so. Recomputed on every sync rather than tracked.
	hidden int
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

// fetchPool is best effort, exactly as it is on `yas list`: the bar is worth
// having and never worth failing the picker over, so a failure returns a nil
// pool and the screen simply has no bar in it.
func (m pickerModel) fetchPool() tea.Cmd {
	cl := m.cl
	return func() tea.Msg {
		p, err := cl.Pool(context.Background())
		if err != nil {
			return poolMsg{}
		}
		return poolMsg{pool: p}
	}
}

func (m pickerModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.fetchList(), m.fetchWhoami(), m.fetchPool())
}

// sync rebuilds the visible list from m.all and the filter, keeping the
// selection on the same BOX rather than the same index — a refresh that lands
// while you are hovering a row must not move the row out from under the key
// you are about to press.
func (m *pickerModel) sync() {
	var wantID string
	if b, ok := m.list.SelectedItem().(boxItem); ok {
		wantID = b.id
	}
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	items := make([]list.Item, 0, len(m.all))
	m.hidden = 0
	for _, it := range m.all {
		if it.id == newBoxID {
			// The offer is always reachable: a filter that matches nothing
			// should still let you make the box you were looking for.
			items = append(items, it)
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.id), q) {
			continue
		}
		// A finished box is hidden until you go looking for it BY NAME. This
		// list answers one question — what can I connect to — and a stopped box
		// is never an answer to it, while a schedule firing daily produces one
		// a day. Typing a name is an explicit request, so a search still finds
		// it, and `d` can still delete it once found.
		//
		// Deliberately at RENDER time and not in the fetch: the status arrives
		// after the row does, so the row is built first and drops out when its
		// status lands. Filtering earlier would mean waiting on every status
		// before showing anything, which is the slower list this picker exists
		// to avoid.
		if q == "" && finished(it.status) {
			m.hidden++
			continue
		}
		items = append(items, it)
	}
	m.list.SetItems(items)
	for i, it := range items {
		if it.(boxItem).id == wantID {
			m.list.Select(i)
			break
		}
	}
}

// resize is the layout decision, in one place. See the const block above.
func (m *pickerModel) resize() {
	if m.width == 0 {
		return
	}
	listW := m.width
	if m.width >= detailMin {
		listW = m.width - detailW - 3
	}
	m.list.SetSize(listW, max(m.height-m.chrome(), 1))
}

// chrome is every line the view spends on something that is not a box.
func (m pickerModel) chrome() int {
	n := 1 + 1 + 1 // leading blank, blank before list, footer
	if m.height >= markMin {
		n += 3
	} else {
		n++
	}
	if m.height >= poolMin {
		n += 3 // blank, bar, legend
	}
	return n
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil

	case whoamiMsg:
		m.login = msg.login
		return m, nil

	case poolMsg:
		m.pool = msg.pool
		return m, nil

	case listedMsg:
		m.loading = false
		if msg.err != nil {
			m.note = "could not load boxes: " + msg.err.Error()
			return m, nil
		}
		m.all = msg.items
		m.sync()
		cmds := make([]tea.Cmd, 0, len(msg.items))
		for _, it := range msg.items {
			if it.id != newBoxID {
				cmds = append(cmds, m.fetchStatus(it.id))
			}
		}
		return m, tea.Batch(cmds...)

	case statusMsg:
		for i, b := range m.all {
			if b.id != msg.id {
				continue
			}
			if msg.ok {
				b.status, b.memMiB, b.memUsed = msg.sb.Status, msg.sb.MemMiB, msg.sb.MemUsedMiB
				b.vcpu, b.milli, b.costUSD = msg.sb.VcpuCount, msg.sb.MilliVcpu, msg.sb.CostUSD
				b.gone = goneAt(msg.sb)
			} else {
				b.status = "?"
			}
			m.all[i] = b
		}
		m.sync()
		return m, nil

	case deletedMsg:
		if msg.err != nil {
			m.note = "delete failed: " + msg.err.Error()
			return m, nil
		}
		m.note = "deleted " + msg.id
		return m, tea.Batch(m.fetchList(), m.fetchPool())

	case parkedMsg:
		if msg.err != nil {
			m.note = msg.err.Error()
			return m, nil
		}
		if msg.woke {
			m.note = msg.id + " is awake"
		} else {
			m.note = msg.id + " is parked — the pool has its memory back"
		}
		return m, tea.Batch(m.fetchList(), m.fetchPool())

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.del.spin = m.spin
		m.list.SetDelegate(m.del)
		return m, cmd

	case tea.KeyMsg:
		// The filter owns the keyboard while it is focused, because every key
		// it would otherwise steal — n, d, s, q — is a letter somebody is
		// trying to type into a box name.
		if m.filter.Focused() {
			switch msg.String() {
			case "esc":
				m.filter.SetValue("")
				m.filter.Blur()
				m.sync()
				return m, nil
			case "enter":
				m.filter.Blur()
				m.sync()
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.sync()
			return m, cmd
		}

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
		case "/":
			m.note = ""
			m.filter.Focus()
			return m, textinput.Blink
		case "d":
			if b, ok := m.list.SelectedItem().(boxItem); ok && b.id != newBoxID {
				m.confirm = b.id
			}
			return m, nil
		case "s":
			// One key, both directions. Which one it means is a fact about the
			// box, and the box already knows it — asking the person to
			// remember whether this one is parked is asking them to do the
			// screen's job.
			b, ok := m.list.SelectedItem().(boxItem)
			if !ok || b.id == newBoxID {
				return m, nil
			}
			cl, id := m.cl, b.id
			switch {
			case live(b.status):
				m.note = "parking " + id + "…"
				return m, func() tea.Msg {
					return parkedMsg{id: id, err: cl.Suspend(context.Background(), id)}
				}
			case parked(b.status):
				m.note = "waking " + id + "…"
				return m, func() tea.Msg {
					return parkedMsg{id: id, woke: true, err: cl.Resume(context.Background(), id)}
				}
			default:
				// failed, cancelled, or a status this CLI has not seen. There
				// is no direction to move a box that is not in one of the two
				// states the key toggles between.
				m.note = "nothing to park or wake — " + id + " is " + statusWord(b.status)
				return m, nil
			}
		case "r":
			m.note = ""
			return m, tea.Batch(m.fetchList(), m.fetchPool())
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// --------------------------------------------------------------------- view

// header is the wordmark from internal/ui, drawn bubbletea-side, with the
// signed-in account on the right. Same mark, same three-line hero shape, same
// order as the landing page: kicker, name, what this screen is.
func (m pickerModel) header() string {
	right := ""
	if m.login != "" {
		right = dimStyle.Render(m.login)
		if m.pool != nil && m.pool.Plan.Name != "" {
			right += faintStyle.Render(" · " + m.pool.Plan.Name)
		}
	}

	if m.height < markMin || m.width < 60 {
		left := brandStyle.Render("yas") + dimStyle.Render("  ·  "+m.title)
		return "  " + left + m.spread(left, right)
	}

	mark := ui.Mark(lipgloss.DefaultRenderer())
	rows := [3]string{
		eyebrow("yet another sandbox"),
		brandStyle.Render("yas"),
		dimStyle.Render(m.title),
	}
	var b strings.Builder
	for i := range mark {
		lineText := mark[i] + "  " + rows[i]
		b.WriteString("  " + lineText)
		if i == 0 {
			b.WriteString(m.spread(lineText, right))
		}
		if i < 2 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// spread is the gap that pushes right to the right margin.
func (m pickerModel) spread(left, right string) string {
	if right == "" {
		return ""
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 4
	if gap < 1 {
		return ""
	}
	return strings.Repeat(" ", gap) + right
}

// poolBlock is the landing page's picture, on the screen people actually use.
//
// It is ALWAYS three lines once the layout has budgeted for it, even before
// the pool response lands and even if it never lands. Returning nothing until
// there was something to draw made the first frames two lines short, so the
// list and the footer sat two rows high and then jumped down the moment
// /v1/pool answered — a flinch on every single run of the program, in the
// first half second, which is the half second that decides whether a CLI feels
// solid.
func (m pickerModel) poolBlock() string {
	w := m.width - 4
	segs := make([]poolSeg, 0, len(m.all))
	for _, b := range m.all {
		if b.id != newBoxID && live(b.status) && b.memMiB > 0 {
			segs = append(segs, poolSeg{name: b.id, mib: b.memMiB})
		}
	}
	bar, legend := PoolBar(m.pool, segs, w)
	if bar == "" {
		// No pool, or no room for one. Hold the rows open rather than an
		// apology: a bar that has not arrived is not news.
		return "\n\n"
	}
	return "\n  " + bar + "\n  " + legend
}

// detail is the right-hand pane: what the selected row cannot fit, and the two
// or three keys that act on it.
func (m pickerModel) detail() string {
	b, ok := m.list.SelectedItem().(boxItem)
	if !ok {
		return ""
	}
	inner := detailW - 3
	var s strings.Builder

	if b.id == newBoxID {
		s.WriteString(eyebrow("new box") + "\n\n")
		s.WriteString(dimStyle.Render("a real computer you can") + "\n")
		s.WriteString(dimStyle.Render("throw away, and get back.") + "\n\n")
		s.WriteString(faintStyle.Render("root · Docker · SSH") + "\n")
		s.WriteString(faintStyle.Render("booted in about 400ms") + "\n")
		s.WriteString(faintStyle.Render("parked, it costs nothing") + "\n\n")
		s.WriteString(key("↵", "shape it and go"))
		return s.String()
	}

	s.WriteString(brandStyle.Render(truncate(b.id, inner)) + "\n")
	if b.status == "" {
		s.WriteString(dimStyle.Render("asking the gateway…") + "\n")
	} else {
		s.WriteString(statusDot(b.status) + " " + statusText(b.status) +
			faintStyle.Render("  "+compactAge(b.created)) + "\n")
	}
	s.WriteString("\n")

	switch {
	case live(b.status) && b.memMiB > 0:
		s.WriteString(gauge(b.memUsed, b.memMiB, inner) + "\n")
		s.WriteString(dimStyle.Render(fmt.Sprintf("%s of %s GiB in use", ui.GiB(b.memUsed), ui.GiB(b.memMiB))) + "\n")
	case b.status == "suspended":
		s.WriteString(lineStyle.Render(strings.Repeat(ui.Dashed, inner)) + "\n")
		s.WriteString(dimStyle.Render("drawing nothing from the pool") + "\n")
	}
	s.WriteString("\n")

	field := func(k, v string) {
		s.WriteString(faintStyle.Render(padRight(k, 7)) + dimStyle.Render(v) + "\n")
	}
	field("cpu", vcpuText(b))
	if v := untilText(b.gone); v != "" {
		// "gone" for a running box and "kept" for a parked one, because the two
		// clocks mean different things to the person reading them: one is a box
		// about to be destroyed, the other a filesystem about to stop being
		// resumable.
		k := "gone"
		if parked(b.status) {
			k = "kept"
		}
		if soon(b.gone) {
			s.WriteString(faintStyle.Render(padRight(k, 7)) + warnStyle.Render(v) + "\n")
		} else {
			field(k, v)
		}
	}
	if b.costUSD > 0 {
		field("cost", fmt.Sprintf("$%.2f so far", b.costUSD))
	}
	s.WriteString("\n")

	// Enter on a parked box does NOT fail — sshutil.Connect resumes it first
	// and then connects, which is the best thing about parking one and was the
	// thing this pane did not say. Naming it here is the difference between a
	// person suspending freely and a person leaving boxes running in case.
	if parked(b.status) {
		s.WriteString(key("↵", "wake it, then a shell") + "\n")
		s.WriteString(key("s", "wake it") + "\n")
	} else if finished(b.status) {
		// No shell, and no wake. Offering either would be offering something
		// that fails: there is no guest to connect to and a resume is refused
		// by name. What is left is reading what it did, and removing the row.
		s.WriteString(dimStyle.Render("  this box has finished — nothing runs in it now") + "\n")
	} else {
		s.WriteString(key("↵", "a shell in it") + "\n")
		if live(b.status) {
			s.WriteString(key("s", "park it") + "\n")
		}
	}
	s.WriteString(key("d", "delete it"))
	return s.String()
}

func (m pickerModel) footer() string {
	switch {
	case m.confirm != "":
		return "  " + warnStyle.Render("delete "+m.confirm+"?") + " " +
			dimStyle.Render("y confirms · any other key cancels")
	case m.filter.Focused():
		return "  " + eyebrowStyle.Render("/") + " " + m.filter.View() + "  " +
			faintStyle.Render("↵ keeps it · esc clears it")
	case m.note != "":
		return "  " + dimStyle.Render(m.note)
	case m.hidden > 0:
		// Said rather than silently dropped: a list that quietly omits rows is
		// one you cannot trust to be the whole answer. It sits where the note
		// goes, so it costs no line of its own.
		//
		// The hint sheds by MEASURING, like the legend below and for the same
		// reason: a footer one cell too wide wraps, which pushes the whole list
		// up a line. Counted against m.width, not guessed at a breakpoint.
		word := "boxes"
		if m.hidden == 1 {
			word = "box"
		}
		line := "  " + dimStyle.Render(fmt.Sprintf("%d finished %s hidden", m.hidden, word))
		hint := " " + faintStyle.Render("· / to search for one by name")
		if lipgloss.Width(line)+lipgloss.Width(hint) <= m.width {
			line += hint
		}
		return line
	}
	// The legend is trimmed by MEASURING it, not by guessing a width at which
	// it stops fitting. Both guesses were wrong — the full row is 88 cells and
	// was shown at 80, the short row is 50 and was shown at 48 — and a legend
	// one cell too wide wraps, which pushes the whole list up a line.
	//
	// It sheds by IMPORTANCE and prints in reading order, which are two
	// different orders. Shedding off the right end instead cost `q quit` first,
	// at 80 columns — taking the one key somebody needs when they are lost, and
	// keeping `r refresh`.
	type legendKey struct {
		k, label string
		rank     int // higher goes first
	}
	all := []legendKey{
		{"↵", "connect", 0},
		{"n", "new", 1},
		{"s", "park/wake", 3},
		{"d", "delete", 4},
		{"/", "filter", 5},
		{"r", "refresh", 6},
		{"q", "quit", 2},
	}
	keys := make([]string, 0, len(all))
	shed := func() {
		worst := -1
		for i, e := range all {
			if worst < 0 || e.rank > all[worst].rank {
				worst = i
			}
		}
		all = append(all[:worst], all[worst+1:]...)
	}
	render := func() {
		keys = keys[:0]
		for _, e := range all {
			keys = append(keys, key(e.k, e.label))
		}
	}
	render()
	sep := faintStyle.Render("  ·  ")

	// The count on the right is the one thing a list with no pagination hides:
	// how much of it you are looking at. It is the first thing dropped.
	count := ""
	if n := len(m.list.Items()) - 1; n > 0 {
		count = faintStyle.Render(fmt.Sprintf("%d %s", n, plural(n, "box", "boxes")))
		if q := strings.TrimSpace(m.filter.Value()); q != "" {
			count = eyebrowStyle.Render("/"+q) + faintStyle.Render(fmt.Sprintf("  %d matching", n))
		}
	}
	for {
		legend := strings.Join(keys, sep)
		room := m.width - lipgloss.Width(legend) - 2
		if room >= 0 {
			// The count needs a gap in front of it or it reads as another key.
			// spread is given the legend WITHOUT its indent, the same as the
			// header gives it the mark line without one, so the count and the
			// login land on the same right margin.
			if count != "" && room >= lipgloss.Width(count)+8 {
				legend += m.spread(legend, count)
			}
			return "  " + legend
		}
		if len(all) == 1 {
			return "  " + truncate("q quits", max(m.width-2, 0))
		}
		shed()
		render()
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (m pickerModel) View() string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(m.header())
	b.WriteString("\n")
	if m.height >= poolMin {
		b.WriteString(m.poolBlock() + "\n")
	}
	b.WriteString("\n")

	switch {
	case m.loading:
		// Not a bare spinner: the first frame of a program is the frame that
		// decides whether it feels fast, and "loading…" says nothing that the
		// spinner did not already say.
		b.WriteString("  " + m.spin.View() + dimStyle.Render(" asking the gateway what you have…"))
		b.WriteString(strings.Repeat("\n", max(m.list.Height()-1, 1)))
	case len(m.list.Items()) <= 1 && strings.TrimSpace(m.filter.Value()) == "":
		b.WriteString(m.emptyState())
	default:
		body := m.list.View()
		if m.width >= detailMin && m.list.Height() >= detailMinH {
			h := m.list.Height()
			left := lipgloss.NewStyle().Width(m.width - detailW - 3).Render(body)
			// Height, not just Width: the divider is a COLUMN, and a border
			// that stopped where the pane's text happened to stop drew a rule
			// that petered out two thirds of the way down the screen.
			//
			// The clamp above it is the short-and-wide window — a 110×16 pane
			// has room for the pane's width and not its content, and a pane
			// taller than the list it sits beside pushes the footer off the
			// bottom of the frame.
			right := lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(line).
				PaddingLeft(2).Width(detailW).Height(h).
				MaxHeight(h).Render(m.detail())
			body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
		}
		b.WriteString(body)
	}

	b.WriteString("\n")
	b.WriteString(m.footer())
	return b.String()
}

// emptyState is the first thing a new account sees, so it is the pitch and the
// keystroke, not an apology.
//
// It WRITES INTO the list's own frame rather than appending to it. The list
// already pads itself out to the height it was given, so a message printed
// after it made a block twice the height of the window — the header scrolled
// off the top and the footer was never reached. There is one frame; the message
// goes in the blank line under the offer.
func (m pickerModel) emptyState() string {
	lines := strings.Split(m.list.View(), "\n")

	msg := "  " + dimStyle.Render("no boxes yet.")
	if tail := m.width - lipgloss.Width(msg) - 2; tail >= 48 {
		msg += " " + faintStyle.Render("the first one is a keypress and about 400ms away.")
	}
	if lipgloss.Width(msg) > m.width {
		msg = "  " + dimStyle.Render(truncate("no boxes yet", max(m.width-2, 0)))
	}

	switch {
	case len(lines) > 2:
		lines[2] = msg
	default:
		lines = append(lines, msg)
	}
	if h := m.list.Height(); len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

// RunPicker shows the box list and returns what the user chose. An Action of
// "form" means "they want a new box; show the form" — the caller decides,
// because the form is its own program.
//
// # It takes the alternate screen, and gives it back
//
// The picker is a full-height program now — a wordmark, a pool bar, a list and
// a detail pane — and drawing that inline would push whatever the person was
// reading off the top of their scrollback every time they ran `yas`. The
// alternate screen is exactly the deal a picker wants: take the whole terminal,
// then leave no trace of having done so. It is also the honest one. What the
// picker leaves behind is the ssh session it chose, and that is the thing worth
// having in the scrollback.
func RunPicker(cl *api.Client, title string) (Result, error) {
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(accent)))
	del := delegate{spin: sp}
	l := list.New(nil, del, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	// Filtering is ours, not the list's: the built-in one renders into the
	// title bar this picker does not have.
	l.SetFilteringEnabled(false)
	l.SetShowPagination(false)

	fi := textinput.New()
	fi.Prompt = ""
	fi.Placeholder = "name"
	fi.PlaceholderStyle = faintStyle
	fi.CharLimit = 40
	fi.Width = 18
	fi.Cursor.Style = lipgloss.NewStyle().Foreground(accent)

	m := pickerModel{cl: cl, title: title, list: l, del: del, spin: sp, filter: fi, loading: true}
	out, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return Result{}, err
	}
	return out.(pickerModel).result, nil
}
