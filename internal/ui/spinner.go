package ui

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// A spinner for the waits that are genuinely open-ended.
//
// # Why not bubbletea
//
// Bubbletea owns the picker and the form, and it is the right tool there: they
// are full-screen, stateful, and take the keyboard. A one-line "working…" that
// prints beside ordinary output is not that. Starting a whole program for it
// would take the terminal away from the command that is trying to use it.
//
// # Why it writes to stderr and nowhere else
//
// It erases its own line with a carriage return, so it must never touch a
// stream somebody is capturing. stdout carries a box id, a minted key, an ssh
// byte stream — real data — and a spinner in the middle of that is corruption.
// When stderr is not a terminal the spinner does not exist at all and the
// caller's plain line is printed once instead.
type Spinner struct {
	mu      sync.Mutex
	stop    chan struct{}
	done    chan struct{}
	label   string
	started time.Time
	on      bool
}

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Start begins a spinner, or prints one plain line when it cannot.
//
// The grace delay is not politeness. A create is usually finished inside
// 400ms, and a spinner that appeared and vanished within one frame reads as a
// glitch rather than as progress.
func Start(label string) *Spinner {
	s := &Spinner{label: label, started: time.Now()}
	if !StderrTTY() {
		fmt.Fprintln(os.Stderr, label)
		return s
	}
	s.on = true
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.run()
	return s
}

func (s *Spinner) run() {
	defer close(s.done)
	t := time.NewTicker(80 * time.Millisecond)
	defer t.Stop()
	grace := time.After(150 * time.Millisecond)
	shown := false
	i := 0
	for {
		select {
		case <-s.stop:
			if shown {
				fmt.Fprint(os.Stderr, "\r\033[K")
			}
			return
		case <-grace:
			shown = true
		case <-t.C:
			if !shown {
				continue
			}
			s.mu.Lock()
			label := s.label
			s.mu.Unlock()
			// The elapsed time appears only once the wait has stopped being
			// the normal case. Printing it from the first frame makes a
			// four-hundred-millisecond create look like something went wrong.
			el := time.Since(s.started)
			suffix := ""
			if el > 2*time.Second {
				suffix = fmt.Sprintf(" %ds", int(el.Seconds()))
			}
			fmt.Fprintf(os.Stderr, "\r\033[K  %s %s%s",
				S(Accent).Render(frames[i%len(frames)]), label, S(Subtle).Render(suffix))
			i++
		}
	}
}

// Relabel changes what the spinner says, for a wait that has turned into a
// different kind of wait — a create that is now queueing, a resume that turned
// out to be a cold restore.
func (s *Spinner) Relabel(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
	if !s.on {
		fmt.Fprintln(os.Stderr, label)
	}
}

// Stop erases the spinner and prints a final line, if there is one.
func (s *Spinner) Stop(final string) {
	if s.on {
		close(s.stop)
		<-s.done
		s.on = false
	}
	if final != "" {
		fmt.Fprintln(os.Stderr, final)
	}
}

// Elapsed is how long the wait took, for a caller that wants to say so.
func (s *Spinner) Elapsed() time.Duration { return time.Since(s.started) }

// OK is the success line: a tick, and what happened.
func OK(text string) string {
	if !StderrTTY() {
		return text
	}
	return S(Good).Render(Tick+" ") + text
}
