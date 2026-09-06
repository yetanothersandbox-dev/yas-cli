package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/ui"
)

// The pool, drawn — components/pool-bar.tsx, in one row of cells.
//
// The website's landing page explains the whole product with this one picture:
// a bar is the pool you bought, solid segments are boxes that are running, and
// the dashed remainder is what is free. A person who has seen the site knows
// what this means before they read a word of it, and a person who has not
// learns the pricing model by looking at a terminal.
//
// It is worth having and never worth failing the picker over: an account with
// no pool endpoint, an unbounded pool, or a two-column terminal each render
// something honest instead of an error.

// segColors alternates so two adjacent boxes are two segments and not one long
// one. Both are the accent — this is the same colour at two weights, not a
// second hue, and a bar that assigned a random colour per box would say the
// colours meant something.
var segColors = []lipgloss.TerminalColor{accent, accentDim}

// poolSeg is one box's claim on the pool.
type poolSeg struct {
	name string
	mib  int
}

// PoolBar renders the bar and the legend under it, as two strings.
//
// segs are the RUNNING boxes only; a suspended box draws nothing, which is the
// single best fact about suspending and the reason the dashed part of this bar
// grows when you park something.
func PoolBar(p *api.Pool, segs []poolSeg, width int) (bar, legend string) {
	if p == nil || width < 12 {
		return "", ""
	}
	if p.MemMiB.Limit <= 0 {
		// Unbounded: an operator-raised account. A bar needs a denominator, so
		// there is no bar — drawing "0 of 0" would read as full, which is the
		// opposite of the truth.
		return lineStyle.Render(strings.Repeat(ui.Dashed, width)),
			dimStyle.Render(fmt.Sprintf("pool unbounded · %d running · %d parked",
				p.Boxes.Running, p.Boxes.Suspended))
	}

	limit := p.MemMiB.Limit
	// Lay the segments out in cells, largest remainder first, so a small box
	// still gets a cell instead of rounding to nothing and disappearing off a
	// picture whose whole job is to show it.
	cells := make([]int, len(segs))
	used := 0
	for i, s := range segs {
		c := s.mib * width / limit
		if c < 1 && s.mib > 0 {
			c = 1
		}
		cells[i] = c
		used += c
	}
	// The bar cannot be wider than the bar. An over-subscribed pool — which the
	// server does allow, briefly, mid-migration — trims from the largest.
	for used > width {
		big := 0
		for i := range cells {
			if cells[i] > cells[big] {
				big = i
			}
		}
		if cells[big] == 0 {
			break
		}
		cells[big]--
		used--
	}

	var b strings.Builder
	for i, c := range cells {
		if c <= 0 {
			continue
		}
		st := lipgloss.NewStyle().Foreground(segColors[i%len(segColors)])
		b.WriteString(st.Render(strings.Repeat(ui.Solid, c)))
	}
	if free := width - used; free > 0 {
		b.WriteString(lineStyle.Render(strings.Repeat(ui.Dashed, free)))
	}

	usedC := subtle
	switch frac := float64(p.MemMiB.Used) / float64(p.MemMiB.Limit); {
	case frac >= 1:
		usedC = danger
	case frac >= 0.8:
		usedC = ui.Warn
	}
	// ui.GiB, not a "%.0f". The free tier's pool is half a gigabyte, which a
	// zero-decimal format renders as "0" — so this line said "0.5 of 0 GiB"
	// above a bar that was drawn perfectly correctly, which reads as the bar
	// being the thing that is wrong. Same function as poolLine's, so the two
	// screens cannot disagree about a number they both show.
	legend = lipgloss.NewStyle().Foreground(usedC).Render(
		fmt.Sprintf("%s of %s GiB", ui.GiB(p.MemMiB.Used), ui.GiB(p.MemMiB.Limit)))
	legend += dimStyle.Render(fmt.Sprintf(" · %s free · %d running",
		ui.GiB(p.MemMiB.Limit-p.MemMiB.Used), p.Boxes.Running))
	if p.Boxes.Suspended > 0 {
		legend += dimStyle.Render(fmt.Sprintf(" · %d parked, costing nothing", p.Boxes.Suspended))
	}
	return b.String(), legend
}

// gauge is the row-scale version of the same idea: how much of what a box was
// given it is actually using. Solid for used, dashed for the rest, so it reads
// as a slice of the pool bar above it — which is exactly what it is.
func gauge(used, total, width int) string {
	if total <= 0 || width <= 0 {
		return ""
	}
	filled := used * width / total
	if filled > width {
		filled = width
	}
	if filled < 1 && used > 0 {
		filled = 1
	}
	c := accent
	if float64(used) > 0.85*float64(total) {
		c = ui.Warn
	}
	return lipgloss.NewStyle().Foreground(c).Render(strings.Repeat(ui.Solid, filled)) +
		lineStyle.Render(strings.Repeat(ui.Dashed, width-filled))
}
