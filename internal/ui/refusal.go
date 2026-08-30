package ui

import (
	"fmt"
	"io"
	"strings"
)

// How a refusal reads.
//
// # The shape, and why it is not just a red line
//
// A refusal is the moment a person most needs to know what to do next, and this
// CLI answered every one of them with `yas: <whatever the server said>`. The
// server's sentence is accurate and it is not advice. So a refusal is three
// parts: a headline this CLI owns, the server's own message kept VERBATIM
// underneath it, and the literal commands that resolve it.
//
// The server's message is never paraphrased away. It is the only part that
// knows the actual numbers — how much pool is free, which limit was hit — and a
// CLI that rewrote it would be inventing detail it does not have.
//
// # Piped, it collapses to one line
//
// A CI log wants one greppable line, not a three-line block with a box-drawing
// arrow in it. Not a terminal means the old single-line contract, unchanged.

// Advice is what to do about a refusal: a headline, and the commands that fix it.
type Advice struct {
	Headline string
	Next     []string
}

// advice maps an error slug to what a person should do about it.
//
// Keyed on the slug and not the HTTP status, because the status says how the
// transport felt and the slug says what actually happened. An unknown slug gets
// the server's message and NO advice — inventing a next step for a failure this
// CLI has never seen is how a tool sends somebody somewhere useless.
var advice = map[string]Advice{
	"quota_exceeded": {
		Headline: "your pool is full",
		// Never "try again": the client deliberately refuses to retry a 429,
		// because the caller's own cap does not clear on its own.
		Next: []string{"yas suspend <id>   gives that box's memory back at once", "yas list           shows what is holding it"},
	},
	"no_capacity": {
		Headline: "the fleet has no room right now — that one is on us",
		Next:     []string{"try again in a minute", "or ask for less: yas new -mem 2048"},
	},
	"remote_resume_required": {
		Headline: "this box was hibernated to cold storage",
		Next:     []string{"yas resume <id>    starts the restore — minutes, not the usual second"},
	},
	"sandbox_not_live": {
		Headline: "that box is not running",
		Next:     []string{"yas resume <id>"},
	},
	"not_found": {
		Headline: "no such box — yours or anybody's, and we will not say which",
		Next:     []string{"yas list"},
	},
	"unauthorized": {
		Headline: "the gateway does not know this key any more",
		Next:     []string{"yas login"},
	},
	"bad_pool_size": {
		Headline: "that is not a pool size we sell",
		Next:     []string{"yas plans   lists the sizes and what they cost"},
	},
}

// Refusal writes err to w as a refusal a person can act on.
//
// tty decides the shape and nothing else: the words are the same either way, so
// a script's log and a terminal never disagree about what happened.
func Refusal(w io.Writer, slug, message string, tty bool) {
	a, known := advice[slug]
	if !tty {
		// The old contract, kept: one line, greppable, with the next step
		// appended rather than laid out.
		line := message
		if line == "" {
			line = a.Headline
		}
		if known && len(a.Next) > 0 {
			line += " (next: " + firstWords(a.Next[0]) + ")"
		}
		fmt.Fprintf(w, "yas: %s\n", line)
		return
	}
	head := a.Headline
	if !known || head == "" {
		// No headline we own, so the server's sentence IS the headline. Better
		// a plain accurate line than a confident wrong one.
		head = message
		message = ""
	}
	fmt.Fprintln(w, S(Danger).Render(Cross+" ")+Bold(Danger).Render(head))
	if message != "" {
		detail := message
		if slug != "" {
			detail = slug + " — " + message
		}
		fmt.Fprintln(w, "  "+S(Subtle).Render(detail))
	}
	for _, n := range a.Next {
		fmt.Fprintln(w, "  "+S(Accent).Render(Arrow+" ")+S(Accent).Render(n))
	}
}

// firstWords is the command out of an advice line, without its explanation.
func firstWords(s string) string {
	if i := strings.Index(s, "  "); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
