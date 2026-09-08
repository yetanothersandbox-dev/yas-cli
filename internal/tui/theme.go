package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/yetanothersandbox-dev/yas-cli/internal/ui"
)

// The theme, which is internal/ui's theme and not a second one.
//
// This file used to hold its own copy of the palette — the same four adaptive
// colours, written out again — and that copy was the ORIGINAL: internal/ui was
// lifted out of it so the rest of the CLI could reach it. Leaving the duplicate
// behind meant the product had one identity in two places, and two places drift.
// Every colour below now comes from ui.
//
// Styles are plain lipgloss, not ui.S: those render on the stderr renderer, and
// bubbletea owns its own output. Colour choice is shared; the renderer is not.
var (
	accent    = ui.Accent
	accentHi  = ui.AccentBright
	accentDim = ui.AccentDim
	subtle    = ui.Subtle
	faintFg   = ui.Faint
	line      = ui.Line
	danger    = ui.Danger

	brandStyle = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dimStyle   = lipgloss.NewStyle().Foreground(subtle)
	faintStyle = lipgloss.NewStyle().Foreground(faintFg)
	lineStyle  = lipgloss.NewStyle().Foreground(line)
	titleStyle = lipgloss.NewStyle().Bold(true)
	warnStyle  = lipgloss.NewStyle().Foreground(danger)

	// eyebrowStyle is the site's mono kicker. See ui.Eyebrow for the spacing.
	eyebrowStyle = lipgloss.NewStyle().Foreground(accent)

	selBar    = lipgloss.NewStyle().Foreground(accent).SetString(ui.Bar + " ")
	selName   = lipgloss.NewStyle().Bold(true).Foreground(accentHi)
	plainName = lipgloss.NewStyle()

	statusStyles = map[string]lipgloss.Style{
		"idle":      lipgloss.NewStyle().Foreground(ui.Good),
		"busy":      lipgloss.NewStyle().Foreground(ui.Warn),
		"starting":  lipgloss.NewStyle().Foreground(ui.Info),
		"suspended": lipgloss.NewStyle().Foreground(accentDim),
		"failed":    lipgloss.NewStyle().Foreground(danger),
		"stopped":   faintStyle,
	}
)

func statusDot(status string) string {
	dot := ui.DotLive
	if status == "suspended" || status == "stopped" {
		dot = ui.DotIdle
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

// live reports whether a box is drawing from the pool right now. It is the one
// question the pool bar, the row gauge and the s-key all ask.
func live(status string) bool {
	switch status {
	case "idle", "busy", "starting", "queued":
		return true
	}
	return false
}

// finished is a box that has run its course: it cannot be woken, connected to
// or exec'd into, and the only thing left to do with it is read what it did.
//
// The complement of parked among the states a box actually reaches, and kept
// separate from it because they answer different questions: parked asks "can I
// wake this", finished asks "is there anything here at all". An empty status is
// neither — it is a Get that has not landed.
func finished(status string) bool {
	return status == "stopped" || status == "failed" || status == "cancelled"
}

// parked is a box that can be woken. NOT the negation of live: `failed` and
// `cancelled` are neither, and the unknown status a failed Get leaves behind is
// neither either. Treating "not live" as "parked" offered `s wake it` on a
// failed box and sent a Resume the gateway was always going to refuse.
//
// `stopped` used to be in here and had exactly that bug, one status along: a
// stopped box cannot be woken — Supervisor.ResumeSandboxFor refuses anything
// whose status is not `suspended`, by name — so `s` on one showed "waking…"
// and then the refusal. Suspended is the only state a resume accepts, so it is
// the only state this returns true for.
func parked(status string) bool {
	return status == "suspended"
}

// statusWord names a status in a sentence. An empty status is a Get that has
// not landed, which reads as "still loading" and not as a state a box is in.
func statusWord(status string) string {
	switch status {
	case "":
		return "still loading"
	case "?":
		return "unreachable"
	default:
		return status
	}
}

// eyebrow renders a section kicker: uppercase, spaced, in the accent.
func eyebrow(s string) string { return eyebrowStyle.Render(ui.Eyebrow(s)) }

// key renders one entry of the key legend — the key itself in the accent, what
// it does beside it. A legend where the keys do not stand out is a paragraph.
func key(k, label string) string {
	return lipgloss.NewStyle().Foreground(accent).Render(k) + " " + faintStyle.Render(label)
}

// rule is a horizontal hairline. The site fades its rules out at both ends;
// a terminal cell has no alpha, so this fades by CHARACTER instead — dashed at
// the ends, solid through the middle.
func rule(w int) string {
	if w <= 4 {
		return lineStyle.Render(strings.Repeat(ui.Dashed, max(w, 0)))
	}
	return lineStyle.Render(ui.Dashed + strings.Repeat("─", w-2) + ui.Dashed)
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

// truncate cuts a PLAIN string to n cells with an ellipsis. Only ever called on
// unstyled text — a cut through an escape sequence would leak it to the screen.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	r := []rune(s)
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
