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
	"os"

	"github.com/charmbracelet/lipgloss"
)

// The palette. One colour per MEANING, never per place it is used — a second
// blue for a second purpose is how a palette stops being one.
//
// AdaptiveColor throughout: lipgloss asks the terminal for its background and
// picks the Light or Dark value, so nothing here is ever a hard-coded white
// that vanishes on a light theme. When the background cannot be queried there
// is no terminal, and nothing is coloured at all.
var (
	// Accent is identity, selection, and a command the reader should type.
	Accent = lipgloss.AdaptiveColor{Light: "#5A56E0", Dark: "#9D99FF"}
	// Good is running, and a thing that worked.
	Good = lipgloss.AdaptiveColor{Light: "#1A8917", Dark: "#3DDC5B"}
	// Warn is busy, waiting, and a destructive question.
	Warn = lipgloss.AdaptiveColor{Light: "#B7791F", Dark: "#F5C043"}
	// Info is a transient state: starting, resuming.
	Info = lipgloss.AdaptiveColor{Light: "#0B7285", Dark: "#4DD0E1"}
	// Danger is failed, and a refusal.
	Danger = lipgloss.AdaptiveColor{Light: "#D0342C", Dark: "#FF6B61"}
	// Subtle is secondary text and metadata.
	Subtle = lipgloss.AdaptiveColor{Light: "#9B9B9B", Dark: "#5C5C5C"}
	// Faint is a key legend or a hint: present, and not competing.
	Faint = lipgloss.AdaptiveColor{Light: "#B2B2B2", Dark: "#4A4A4A"}
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

// wordmark is the identity block: a box, because the product is a box.
//
// Not figlet letters. Claude Code's start block is not big type either — it is a
// small mark, a bold name, and dim metadata, and that reads as deliberate where
// large ASCII reads as 2003. Every character is a single-cell block element, so
// the three lines cannot drift out of alignment on any font.
const (
	markTop = "▛▀▀▀▀▀▜"
	markMid = "▌ yas ▐"
	markBot = "▙▄▄▄▄▄▟"
)

// Wordmark renders the identity block with two lines of metadata beside it.
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
// The block elements are a CHARSET question and colour is a separate one. An
// earlier version fell back to `+-----+` whenever the colour profile was plain,
// which meant a perfectly capable monochrome terminal — or anything under
// `script` — lost the mark for no reason. Now the mark is drawn either way and
// only the colour is conditional; TERM=dumb is the one case that gets ASCII.
func Wordmark(w *os.File, tagline, meta string) string {
	if !TTY(w) {
		return ""
	}
	if os.Getenv("TERM") == "dumb" {
		return "+-----+\n| yas |\n+-----+\n"
	}
	r := Err
	if w == os.Stdout {
		r = Out
	}
	edge := r.NewStyle().Foreground(Accent)
	name := r.NewStyle().Bold(true).Foreground(Accent)
	dim := r.NewStyle().Foreground(Subtle)

	top := edge.Render(markTop)
	mid := edge.Render("▌ ") + name.Render("yas") + edge.Render(" ▐")
	bot := edge.Render(markBot)

	out := top + "\n" + mid + "  " + tagline + "\n" + bot
	if meta != "" {
		out += "  " + dim.Render(meta)
	}
	return out + "\n"
}
