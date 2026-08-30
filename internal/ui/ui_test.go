package ui

import (
	"os"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The mark's three lines are the same width, in cells, in every mode.
//
// The doc comment on Mark promises they "cannot drift out of alignment on any
// font", and that promise is only kept while every glyph in it is single-cell.
// Swapping one character for a prettier double-width one — the fullwidth plus,
// an emoji, a box-drawing character outside the BMP's single-width block — puts
// a kink in the border that nothing else in the program would catch, because
// nothing else in the program draws a box.
func TestMarkLinesAreTheSameWidth(t *testing.T) {
	for _, term := range []string{"xterm-256color", "dumb"} {
		t.Setenv("TERM", term)
		m := Mark(lipgloss.NewRenderer(os.Stdout))
		w0 := lipgloss.Width(m[0])
		for i, line := range m {
			if got := lipgloss.Width(line); got != w0 {
				t.Errorf("TERM=%s: line %d is %d cells, line 0 is %d (%q)", term, i, got, w0, line)
			}
		}
		if term != "dumb" && w0 != MarkWidth {
			t.Errorf("TERM=%s: mark is %d cells, MarkWidth says %d", term, w0, MarkWidth)
		}
	}
}

// The eyebrow is the site's letter-spaced kicker, spelled with the only
// tracking a grid of cells has.
func TestEyebrowSpacesAndUppercases(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"pool", "P O O L"},
		{"new box", "N E W   B O X"}, // the space becomes three: gap, space, gap
		{"a", "A"},
		{"", ""},
	} {
		if got := Eyebrow(tc.in); got != tc.want {
			t.Errorf("Eyebrow(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Nothing decorative reaches a pipe. Wordmark is the loudest thing this CLI
// prints, and `yas --help | head` must not be the place it shows up.
func TestWordmarkIsEmptyOffATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if got := Wordmark(w, "yet another sandbox", "yas 1.0", "a real computer"); got != "" {
		t.Errorf("Wordmark into a pipe rendered %q, want nothing", got)
	}
}
