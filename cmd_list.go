package main

import (
	"context"
	"fmt"
	"github.com/charmbracelet/lipgloss"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	"github.com/Gilbert09/yas/clients/yas/internal/ui"
	"time"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
)

// boxRow is one listed sandbox with its per-id detail filled in.
type boxRow struct {
	api.SandboxSummary
	Status  string
	MemMiB  int
	MemUsed int
	Err     error
}

// fetchRows is the list + bounded Get fan-out. The gateway's index cannot
// answer status — it changes every second and lives on the host — so `list`
// asks per id, eight at a time.
func fetchRows(ctx context.Context, cl *api.Client) ([]boxRow, error) {
	sums, err := cl.List(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]boxRow, len(sums))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, s := range sums {
		rows[i].SandboxSummary = s
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sb, err := cl.Get(ctx, id)
			if err != nil {
				rows[i].Err = err
				rows[i].Status = "?"
				return
			}
			rows[i].Status = sb.Status
			rows[i].MemMiB = sb.MemMiB
			rows[i].MemUsed = sb.MemUsedMiB
		}(i, s.ID)
	}
	wg.Wait()
	sort.Slice(rows, func(a, b int) bool { return rows[a].CreatedAt.After(rows[b].CreatedAt) })
	return rows, nil
}

// cmdList prints the fleet-shaped truth about this tenant's boxes — which
// deliberately does not include what machine any of them is on.
func cmdList(args []string) error {
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	rows, err := fetchRows(context.Background(), cl)
	if err != nil {
		return err
	}
	tty := ui.StdoutTTY()
	if len(rows) == 0 {
		if tty {
			fmt.Fprintln(os.Stderr, "no boxes — "+ui.S(ui.Accent).Render("yas new")+
				" makes one. It takes about 400ms; you will spend longer reading this.")
		} else {
			fmt.Fprintln(os.Stderr, "no boxes — `yas new` makes one")
		}
		return nil
	}

	// PIPED OUTPUT IS BYTE-IDENTICAL TO WHAT IT HAS ALWAYS BEEN.
	//
	// Not "the same but without colour" — the same, exactly: same header, same
	// columns, same MiB format. Scripts parse this. The pool line, the dots and
	// the extra columns below are a TERMINAL rendering, and a terminal is the
	// one place nothing is parsing.
	if !tty {
		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATUS\tAGE\tMEM")
		for _, r := range rows {
			mem := ""
			if r.MemMiB > 0 {
				mem = fmt.Sprintf("%d/%dMiB", r.MemUsed, r.MemMiB)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.Status, age(r.CreatedAt), mem)
		}
		return w.Flush()
	}

	// The pool line. Best effort on purpose: it is worth having and never worth
	// failing `yas list` over, so an error here prints no header and no excuse.
	if p, perr := cl.Pool(context.Background()); perr == nil {
		fmt.Fprintln(os.Stdout, poolLine(p))
	}

	// NOT tabwriter. It measures a cell in BYTES, and a coloured cell carries a
	// dozen escape bytes that occupy no columns — so every styled row came out
	// short by exactly the length of its escapes and the table sheared. Widths
	// come from lipgloss.Width, which counts what the terminal will actually
	// draw.
	cells := [][]string{{"NAME", "STATUS", "AGE", "MEM"}}
	for _, r := range rows {
		cells = append(cells, []string{r.ID, statusCell(r.Status), age(r.CreatedAt), memCell(r)})
	}
	widths := make([]int, 4)
	for _, row := range cells {
		for i, c := range row {
			if w := lipgloss.Width(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	for n, row := range cells {
		var b strings.Builder
		for i, c := range row {
			text := c
			if n == 0 {
				text = ui.S(ui.Subtle).Render(c)
			}
			b.WriteString(text)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-lipgloss.Width(c)+2))
			}
		}
		fmt.Fprintln(os.Stdout, b.String())
	}
	return nil
}

// poolLine is the product's own mental model, on the command people run most.
func poolLine(p *api.Pool) string {
	bar := ui.S(ui.Accent).Render(ui.Bar + " ")
	if p.MemMiB.Limit <= 0 {
		// Unbounded: an operator-raised account. Drawing "0 of 0" would read as
		// full, which is the opposite of the truth.
		return bar + ui.S(ui.Subtle).Render(fmt.Sprintf("pool unbounded · %d running · %d parked",
			p.Boxes.Running, p.Boxes.Suspended))
	}
	used := float64(p.MemMiB.Used) / 1024
	lim := float64(p.MemMiB.Limit) / 1024
	c := ui.Subtle
	switch frac := float64(p.MemMiB.Used) / float64(p.MemMiB.Limit); {
	case frac >= 1:
		c = ui.Danger
	case frac >= 0.8:
		c = ui.Warn
	}
	return bar + ui.S(c).Render(fmt.Sprintf("pool %.1f/%.0f GiB", used, lim)) +
		ui.S(ui.Subtle).Render(fmt.Sprintf(" · %d running · %d parked, costing nothing",
			p.Boxes.Running, p.Boxes.Suspended))
}

// statusCell is the picker's dot vocabulary, so the two screens agree.
func statusCell(status string) string {
	dot, c := ui.DotLive, ui.Subtle
	switch status {
	case "idle":
		c = ui.Good
	case "busy":
		c = ui.Warn
	case "starting":
		c = ui.Info
	case "failed":
		c = ui.Danger
	case "suspended":
		dot, c = ui.DotIdle, ui.Accent
	case "stopped":
		dot, c = ui.DotIdle, ui.Faint
	case "":
		return ui.S(ui.Faint).Render("? unreachable")
	}
	return ui.S(c).Render(dot+" ") + status
}

// memCell says what a box draws from the pool.
//
// A suspended box used to render an EMPTY cell, because it reports zero memory
// — so the single best fact about suspending, that it costs nothing, was shown
// as nothing at all. It says so now.
func memCell(r boxRow) string {
	switch {
	case r.Status == "suspended" || r.Status == "stopped":
		return ui.S(ui.Subtle).Render("nothing — parked")
	case r.MemMiB <= 0:
		return ui.S(ui.Faint).Render("—")
	default:
		cell := fmt.Sprintf("%.1f / %.1f GiB", float64(r.MemUsed)/1024, float64(r.MemMiB)/1024)
		if float64(r.MemUsed) > 0.85*float64(r.MemMiB) {
			return ui.S(ui.Warn).Render(cell)
		}
		return cell
	}
}

// age renders a compact duration ("3h", "2d") — enough to pick a box by.
func age(t time.Time) string {
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
