package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
)

// A picker frame, without a gateway.
//
// The model is a pure function of the messages it has seen, so a test can feed
// it the four it would get from a real account — the list, each status, whoami,
// the pool — and then read the frame it would have drawn. No network, no
// terminal, and the thing under test is the actual View and not a copy of it.

func fixture(t *testing.T, w, h int) pickerModel {
	t.Helper()
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	del := delegate{spin: sp}
	l := list.New(nil, del, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowPagination(false)
	fi := textinput.New()
	fi.Prompt = ""
	fi.Placeholder = "name"
	fi.Width = 24

	m := pickerModel{title: "your boxes", list: l, del: del, spin: sp, filter: fi, loading: true}
	var mm tea.Model = m
	mm, _ = mm.Update(tea.WindowSizeMsg{Width: w, Height: h})

	now := time.Now()
	mm, _ = mm.Update(listedMsg{items: []boxItem{
		{id: newBoxID},
		{id: "api", created: now.Add(-2 * time.Hour)},
		{id: "ledger", created: now.Add(-26 * time.Hour)},
		{id: "agent-7", created: now.Add(-9 * time.Minute)},
		{id: "a-name-that-is-far-too-long-for-the-column", created: now.Add(-70 * time.Hour)},
	}})
	for _, s := range []api.Sandbox{
		{ID: "api", Status: "idle", MemMiB: 8192, MemUsedMiB: 3400, MilliVcpu: 2000, CostUSD: 0.42},
		{ID: "ledger", Status: "busy", MemMiB: 4096, MemUsedMiB: 3900, MilliVcpu: 500, CostUSD: 1.07},
		{ID: "agent-7", Status: "suspended", MemMiB: 12288, VcpuCount: 4},
		{ID: "a-name-that-is-far-too-long-for-the-column", Status: "failed"},
	} {
		mm, _ = mm.Update(statusMsg{id: s.ID, sb: s, ok: true})
	}
	mm, _ = mm.Update(whoamiMsg{login: "tom"})

	p := &api.Pool{}
	p.Plan.Name = "pro"
	p.MemMiB.Limit = 32768
	p.MemMiB.Used = 12288
	p.MemMiB.Free = 20480
	p.Boxes.Running, p.Boxes.Suspended, p.Boxes.Total = 2, 1, 4
	mm, _ = mm.Update(poolMsg{pool: p})

	return mm.(pickerModel)
}

// empty is the fixture with the account emptied out: a new sign-up, which is
// the state the most people see exactly once and nobody tests.
func (m pickerModel) empty() pickerModel {
	m.all = []boxItem{{id: newBoxID}}
	m.loading = false
	m.sync()
	return m
}

// No frame may be wider than the terminal it was drawn for.
//
// This is the failure the list view cannot show you: one row a few cells too
// long wraps, every row below it shifts up by one, and the screen tears in a
// way that looks like a bubbletea bug rather than a padding bug. The columns
// are budgeted by lipgloss.Width — which counts CELLS, not the bytes a styled
// string happens to carry — and this asserts that the budget holds at the sizes
// people actually have.
func TestFrameNeverExceedsItsWidth(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 40}, // a full window, detail pane and all
		{100, 30}, // just over the detail threshold
		{95, 30},  // just under it
		{80, 24},  // the classic
		{72, 18},  // no pool bar
		{60, 12},  // one-line mark
		{48, 10},  // a split pane
	}
	for _, s := range sizes {
		m := fixture(t, s.w, s.h)
		for _, view := range []struct {
			name string
			m    pickerModel
		}{
			{"list", m},
			{"loading", func() pickerModel { c := m; c.loading = true; return c }()},
			{"confirm", func() pickerModel { c := m; c.confirm = "api"; return c }()},
		} {
			for i, ln := range strings.Split(view.m.View(), "\n") {
				if got := lipgloss.Width(ln); got > s.w {
					t.Errorf("%dx%d %s: line %d is %d cells wide, terminal is %d\n%q",
						s.w, s.h, view.name, i, got, s.w, ln)
				}
			}
		}
	}
}

// The frame is EXACTLY the height it was given, in every state.
//
// Over it, the header scrolls off the top. Under it, the footer sits partway up
// the window and then drops when the missing rows arrive — which is what the
// pool block used to do on every run, because it drew nothing until /v1/pool
// answered and the first frames came out two lines short. Asserting "fits"
// rather than "fills" is what let that through, so this asserts equality and
// covers the states where a section has not loaded yet.
func TestFrameFillsItsHeight(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 40}, {80, 24}, {72, 18}, {60, 12},
		// Wide enough for the detail pane, too short to stand one beside the
		// list. The pane's own content is thirteen lines and this window has
		// room for seven.
		{120, 16}, {110, 14}, {100, 11},
	}
	for _, s := range sizes {
		m := fixture(t, s.w, s.h)
		for _, v := range []struct {
			name string
			m    pickerModel
		}{
			{"list", m},
			{"loading", func() pickerModel { c := m; c.loading = true; return c }()},
			{"empty", fixture(t, s.w, s.h).empty()},
			// The first frames of every run: the window is sized, and not one
			// of the four responses has come back.
			{"nothing loaded yet", func() pickerModel {
				c := m
				c.pool, c.login, c.loading = nil, "", true
				return c
			}()},
			// The pool endpoint refused, permanently. The bar never arrives.
			{"no pool", func() pickerModel { c := m; c.pool = nil; return c }()},
		} {
			if got := strings.Count(v.m.View(), "\n") + 1; got != s.h {
				t.Errorf("%dx%d %s: frame is %d lines, terminal is %d", s.w, s.h, v.name, got, s.h)
			}
		}
	}
}

// The pool bar is exactly as wide as it was asked to be, whatever the segments
// round to. It is one line of solid and dashed cells and a cell too many wraps
// the whole screen.
func TestPoolBarIsExactlyItsWidth(t *testing.T) {
	p := &api.Pool{}
	p.MemMiB.Limit = 32768
	cases := [][]poolSeg{
		nil,
		{{"a", 8192}},
		{{"a", 8192}, {"b", 4096}, {"c", 12288}},
		{{"a", 1}, {"b", 1}, {"c", 1}, {"d", 1}}, // each rounds to zero, each gets a cell
		{{"a", 32768}},                           // exactly full
		{{"a", 30000}, {"b", 30000}},             // over-subscribed, mid-migration
	}
	for _, w := range []int{20, 40, 76, 116} {
		for _, segs := range cases {
			bar, _ := PoolBar(p, segs, w)
			if got := lipgloss.Width(bar); got != w {
				t.Errorf("width %d, segs %v: bar is %d cells", w, segs, got)
			}
		}
	}
}

// A pool smaller than a gigabyte is described in the size it actually is.
//
// The legend rendered its limit with "%.0f", so the free tier's half a gigabyte
// came out as "0" and the line read "0.5 of 0 GiB · 0 free" — a full pool with
// no capacity, sitting directly above a bar that had drawn the same numbers
// correctly. poolLine had the identical bug and was fixed on its own; this is
// the same fix, through the same ui.GiB, so the two screens cannot drift.
func TestPoolLegendOnASubGigabytePool(t *testing.T) {
	for _, tc := range []struct {
		name              string
		limit, used       int
		wantIn, wantNotIn []string
	}{
		{
			name:  "the free tier's half a gigabyte",
			limit: 512, used: 512,
			wantIn:    []string{"0.5 of 0.5 GiB"},
			wantNotIn: []string{"of 0 GiB"},
		},
		{
			name:  "a whole number stays whole",
			limit: 32768, used: 12288,
			wantIn:    []string{"12 of 32 GiB", "20 free"},
			wantNotIn: []string{"12.0", "32.0"},
		},
		{
			name:  "a fraction shows its fraction",
			limit: 10240, used: 1536,
			wantIn: []string{"1.5 of 10 GiB", "8.5 free"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &api.Pool{}
			p.MemMiB.Limit, p.MemMiB.Used = tc.limit, tc.used
			p.Boxes.Running = 1
			_, legend := PoolBar(p, []poolSeg{{"a", tc.used}}, 60)
			for _, w := range tc.wantIn {
				if !strings.Contains(legend, w) {
					t.Errorf("legend %q is missing %q", legend, w)
				}
			}
			for _, w := range tc.wantNotIn {
				if strings.Contains(legend, w) {
					t.Errorf("legend %q still contains %q", legend, w)
				}
			}
		})
	}
}

// The filter narrows the list and never hides the offer: filtering to a name
// that does not exist yet is the exact moment somebody wants a new box.
func TestFilterKeepsTheNewBoxRow(t *testing.T) {
	m := fixture(t, 120, 40)
	m.filter.SetValue("nothing-matches-this")
	m.sync()
	items := m.list.Items()
	if len(items) != 1 {
		t.Fatalf("got %d items, want just the new-box row", len(items))
	}
	if items[0].(boxItem).id != newBoxID {
		t.Errorf("the surviving row is %q, want the new-box row", items[0].(boxItem).id)
	}
}

// Refreshing while a row is selected keeps the selection on that BOX. Keeping
// the INDEX instead moves the row out from under the key somebody is about to
// press — and the key next to `enter` is `d`.
func TestSelectionSurvivesARefresh(t *testing.T) {
	m := fixture(t, 120, 40)
	m.list.Select(3) // agent-7
	want := m.list.SelectedItem().(boxItem).id

	// A box created since the last refresh sorts in above it.
	m.all = append([]boxItem{{id: newBoxID}, {id: "fresh", created: time.Now()}}, m.all[1:]...)
	m.sync()

	if got := m.list.SelectedItem().(boxItem).id; got != want {
		t.Errorf("selection moved to %q, want %q", got, want)
	}
}

// `s` toggles between the two states it can toggle between, and does nothing
// anywhere else.
//
// "not running" is not "parked": a failed box is neither, and treating the two
// as one sent the gateway a Resume it was always going to refuse — and showed
// the person a red refusal for a key they were told to press.
func TestParkKeyOnlyActsOnAParkableBox(t *testing.T) {
	for _, tc := range []struct {
		row      int
		status   string
		wantNote string
		wantCmd  bool
	}{
		{1, "idle", "parking api…", true},
		{3, "suspended", "waking agent-7…", true},
		{4, "failed", "nothing to park or wake — a-name-that-is-far-too-long-for-the-column is failed", false},
		{0, "new box", "", false}, // the offer row has no box to act on
	} {
		t.Run(tc.status, func(t *testing.T) {
			m := fixture(t, 120, 40)
			m.list.Select(tc.row)
			out, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
			got := out.(pickerModel)
			if got.note != tc.wantNote {
				t.Errorf("note = %q, want %q", got.note, tc.wantNote)
			}
			if (cmd != nil) != tc.wantCmd {
				t.Errorf("cmd != nil is %v, want %v", cmd != nil, tc.wantCmd)
			}
		})
	}
}

// A key that acts on a box must never act on the box the filter moved under
// the cursor. The delete confirmation names the id it will delete, and the id
// it names is the one it deletes.
func TestDeleteConfirmsTheSelectedID(t *testing.T) {
	m := fixture(t, 120, 40)
	m.filter.SetValue("ledger")
	m.sync()
	m.list.Select(1) // the only match, under the new-box row

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if got := out.(pickerModel).confirm; got != "ledger" {
		t.Errorf("confirming %q, want ledger", got)
	}
}

// While the filter has the keyboard, every letter is a letter. `n`, `d`, `s`
// and `q` are all characters in box names, and a picker that opened the create
// form halfway through typing "kernel" would be unusable.
func TestFilterSwallowsTheActionKeys(t *testing.T) {
	m := fixture(t, 120, 40)
	m.filter.Focus()
	var mm tea.Model = m
	for _, r := range "ledger" {
		mm, _ = mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	got := mm.(pickerModel)
	if got.filter.Value() != "ledger" {
		t.Errorf("filter holds %q, want ledger", got.filter.Value())
	}
	if got.result.Action != "" {
		t.Errorf("typing into the filter decided %q", got.result.Action)
	}
	if got.confirm != "" {
		t.Errorf("typing into the filter armed a delete on %q", got.confirm)
	}
}

// PREVIEW=1 go test ./internal/tui -run Preview -v prints the frames. Not an
// assertion: a way to look at the thing without a gateway.
func TestPreview(t *testing.T) {
	if os.Getenv("PREVIEW") == "" {
		t.Skip("set PREVIEW=1 to print frames")
	}
	frames := []struct {
		name string
		w, h int
		set  func(*pickerModel)
	}{
		{"new box selected", 120, 30, nil},
		{"a running box selected", 120, 30, func(m *pickerModel) { m.list.Select(1) }},
		{"a parked box selected", 120, 30, func(m *pickerModel) { m.list.Select(3) }},
		{"filtering", 120, 22, func(m *pickerModel) {
			m.filter.Focus()
			m.filter.SetValue("a")
			m.sync()
		}},
		{"confirming a delete", 100, 20, func(m *pickerModel) {
			m.list.Select(1)
			m.confirm = "api"
		}},
		{"no boxes yet", 100, 20, func(m *pickerModel) { *m = m.empty() }},
		{"first frame", 100, 20, func(m *pickerModel) { m.loading = true }},
		{"a split pane", 64, 14, nil},
	}
	for _, f := range frames {
		m := fixture(t, f.w, f.h)
		if f.set != nil {
			f.set(&m)
		}
		t.Logf("\n%s %s · %dx%d %s\n%s", strings.Repeat("=", 6), f.name, f.w, f.h,
			strings.Repeat("=", 6), m.View())
	}
}
