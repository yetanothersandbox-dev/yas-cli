// Package ui is the CLI's one visual identity: its palette, its glyphs, its
// wordmark, and the rules about when any of that is allowed to appear.
//
// # Why it exists
//
// The palette was already here, in internal/tui/picker.go, and it was good — an
// adaptive colour per meaning, status dots, an accent bar. It just could not be
// reached from anywhere else, so the other nine tenths of the CLI printed
// nothing but plain text. This package is that palette lifted out so every
// command can use it, not a second identity competing with the first.
//
// # Two renderers, one per stream, deliberately
//
// Half of this CLI's human-facing output goes to stderr while stdout carries
// data — a box id, a minted key, an ssh byte stream. A single renderer keyed on
// stdout would strip the colour from stderr the moment somebody piped stdout,
// and a renderer keyed on stderr would colour stdout when it should not. So
// there is one per stream and each asks its own file whether it is a terminal.
package ui

import (
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The palette. One colour per MEANING, never per place it is used — a second
// blue for a second purpose is how a palette stops being one.
//
// # These are the website's tokens, not a second opinion
//
// The Dark values are internal/web/ui/src/styles.css converted out of oklch()
// into sRGB, token for token: --color-accent is Accent, --color-ok is Good,
// --color-text-muted is Subtle. The CLI used to run on a purple of its own,
// which meant the product had two accents and neither was the brand's. The
// amber is the site's, and the site's is amber because the thing underneath is
// Firecracker.
//
// The site is dark-only and a terminal is not, so each token also carries a
// Light value: the same hue and chroma, pulled down in lightness until it holds
// contrast on paper-white. Nothing here is a hard-coded white that vanishes on
// a light theme. When the background cannot be queried there is no terminal,
// and nothing is coloured at all.
var (
	// Accent is identity, selection, and a command the reader should type.
	Accent = lipgloss.AdaptiveColor{Light: "#AF6700", Dark: "#EEA743"}
	// AccentBright is the accent under focus: a selected row, a live cursor.
	AccentBright = lipgloss.AdaptiveColor{Light: "#8F5400", Dark: "#FFBA59"}
	// AccentDim is the accent when it is texture rather than emphasis — the
	// second box in a pool bar, the rule under a heading.
	AccentDim = lipgloss.AdaptiveColor{Light: "#C38323", Dark: "#AF7A31"}
	// Good is running, and a thing that worked.
	Good = lipgloss.AdaptiveColor{Light: "#008039", Dark: "#54C57A"}
	// Warn is busy, waiting, and a destructive question.
	Warn = lipgloss.AdaptiveColor{Light: "#9B7E00", Dark: "#E9C944"}
	// Info is a transient state: starting, resuming.
	Info = lipgloss.AdaptiveColor{Light: "#0E7397", Dark: "#64C4F0"}
	// Danger is failed, and a refusal.
	Danger = lipgloss.AdaptiveColor{Light: "#C21725", Dark: "#ED5350"}
	// Subtle is secondary text and metadata.
	Subtle = lipgloss.AdaptiveColor{Light: "#616368", Dark: "#96989D"}
	// Faint is a key legend or a hint: present, and not competing.
	Faint = lipgloss.AdaptiveColor{Light: "#8D8F94", Dark: "#6F7276"}
	// Line is a rule, a border, and the unfilled half of a gauge. It is the
	// site's --color-border-strong, which is white at 15% over near-black.
	Line = lipgloss.AdaptiveColor{Light: "#C9CBCF", Dark: "#3B3D41"}
)

// The glyph vocabulary.
//
// All single-cell, so a column never drifts. No emoji: they are double-width in
// some terminals and not in others, which breaks every table that contains one.
const (
	Bar     = "▌" // identity and section
	DotLive = "●" // running
	DotIdle = "○" // parked
	Tick    = "✓"
	Cross   = "✗"
	Arrow   = "→" // the next thing to do
	Point   = "▸" // focus

	// Solid and Dashed are the site's one recurring idea, in two characters:
	// a pool bar draws what is RUNNING solid and what is FREE dashed, and so
	// does every gauge in the CLI. See tui.PoolBar.
	Solid  = "█"
	Dashed = "┄"

	// Cursor is the block in the brand mark, and the caret the site's terminal
	// component blinks. Same shape, same meaning.
	Cursor = "▊"
)

// Out and Err render to their own stream. See the package comment.
var (
	Out = lipgloss.NewRenderer(os.Stdout)
	Err = lipgloss.NewRenderer(os.Stderr)
)

// TTY reports whether f is a terminal.
//
// Kept here beside the renderers because every "should this be decorated"
// decision in the CLI is the same question, and answering it in two places is
// how the answers drift apart.
func TTY(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// StdoutTTY and StderrTTY are the two answers everything else asks for.
func StdoutTTY() bool { return TTY(os.Stdout) }
func StderrTTY() bool { return TTY(os.Stderr) }

// Styles on the stderr renderer, which is where human output goes.
func S(fg lipgloss.TerminalColor) lipgloss.Style { return Err.NewStyle().Foreground(fg) }

// Bold is the emphasis used for a name the eye should find first.
func Bold(fg lipgloss.TerminalColor) lipgloss.Style {
	return Err.NewStyle().Bold(true).Foreground(fg)
}

// The mark: the website's, in cells.
//
// components/site-header.tsx draws a bordered rounded square with a fat amber
// block and a small faint bar inside it — a terminal with a cursor in it. That
// is three characters away from being drawable in a terminal, so it is drawn in
// a terminal: rounded border, Cursor in Accent, a low bar in Faint.
//
// Not figlet letters. Claude Code's start block is not big type either — it is a
// small mark, a bold name, and dim metadata, and that reads as deliberate where
// large ASCII reads as 2003. Every character is single-cell, so the three lines
// cannot drift out of alignment on any font.
const (
	markTop = "╭─────╮"
	markBot = "╰─────╯"
	// markMid is assembled per-piece because its two inner glyphs are two
	// different colours. Its printed width matches the other two lines.
	markMidL = "│ "
	markMidR = " │"
)

// MarkWidth is the printed width of one line of the mark, for callers laying
// text out beside it.
const MarkWidth = 7

// Mark returns the three lines of the brand mark, coloured for renderer r.
//
// Exported because the picker draws the same mark bubbletea-side, and a second
// hand-rolled copy over there is how one mark becomes two.
func Mark(r *lipgloss.Renderer) [3]string {
	edge := r.NewStyle().Foreground(Line)
	cur := r.NewStyle().Foreground(Accent)
	rest := r.NewStyle().Foreground(Faint)
	if os.Getenv("TERM") == "dumb" {
		return [3]string{"+-----+", "| |_  |", "+-----+"}
	}
	return [3]string{
		edge.Render(markTop),
		edge.Render(markMidL) + cur.Render(Cursor) + " " + rest.Render("▁") + edge.Render(markMidR),
		edge.Render(markBot),
	}
}

// GiB renders MiB as GiB with only the precision the number needs.
//
// It replaced a "%.0f", which is fine for the 10 GiB tier it was written
// against and prints the free tier's half a gigabyte as "0" — so `yas ls` said
// "pool 0.5/0 GiB", claiming a full pool with no capacity at all. Whole sizes
// stay whole ("10", not "10.0"); a half shows its half.
//
// # Why it lives here and not beside `yas list`
//
// Because the picker draws the same number. It landed in package main against
// poolLine, and the pool bar in internal/tui — which cannot reach package main
// — carried the identical "%.0f" and so carried the identical bug: a free
// tenant's picker said "0.5 of 0 GiB" above a bar that was correctly drawn.
// Two screens rendering one quantity need one function, for the same reason
// they need one palette.
func GiB(mib int) string {
	g := float64(mib) / 1024
	if g == math.Trunc(g) {
		return strconv.Itoa(int(g))
	}
	return strconv.FormatFloat(g, 'f', 1, 64)
}

// Eyebrow is the site's kicker: mono, uppercase, letter-spaced, in the accent.
//
// The tracking is real letter-spacing on the web and there is no such thing in
// a terminal, so it is spelled with the spaces — "Y A S" — which is the same
// effect by the only means a grid of cells has. Used once per screen, above the
// thing it names, exactly as the site uses it.
func Eyebrow(s string) string {
	out := make([]rune, 0, len(s)*2)
	for i, r := range strings.ToUpper(s) {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, r)
	}
	return string(out)
}

// Wordmark renders the identity block: the mark, and the site's hero beside it.
//
// The three lines to the right are the landing page's three, in its order — the
// eyebrow, the name, then the promise. A person who has seen the website has
// already read this, which is the whole point of a wordmark.
//
// It appears on exactly three commands — help, login, and version on a terminal
// — because those are the three "who am I" moments. It never appears on list,
// new, ssh or exec. A command somebody runs fifty times a day must not restate
// the brand fifty times, which is the mistake that makes a CLI feel like an
// advert.
//
// # It takes the stream it is drawing on
//
// Help writes to stdout and login writes to stderr, and an earlier version
// asked stderr whether to draw regardless — so `yas --help` on a terminal with
// stderr redirected printed no mark, and the reverse printed one into a pipe.
// The stream decides, because the stream is what the reader is looking at.
//
// # ASCII only for a terminal that cannot draw, not for one that cannot colour
//
// The box-drawing characters are a CHARSET question and colour is a separate
// one. An earlier version fell back to `+-----+` whenever the colour profile
// was plain, which meant a perfectly capable monochrome terminal — or anything
// under `script` — lost the mark for no reason. Now the mark is drawn either
// way and only the colour is conditional; TERM=dumb is the one case that gets
// ASCII.
func Wordmark(w *os.File, eyebrow, title, sub string) string {
	if !TTY(w) {
		return ""
	}
	r := Err
	if w == os.Stdout {
		r = Out
	}
	m := Mark(r)
	kick := r.NewStyle().Foreground(Accent).Render(Eyebrow(eyebrow))
	name := r.NewStyle().Bold(true).Foreground(Accent).Render(title)
	dim := r.NewStyle().Foreground(Subtle).Render(sub)

	rows := [3]string{kick, name, dim}
	var b strings.Builder
	for i, mline := range m {
		b.WriteString(mline)
		if rows[i] != "" {
			b.WriteString("  " + rows[i])
		}
		b.WriteString("\n")
	}
	return b.String()
}
