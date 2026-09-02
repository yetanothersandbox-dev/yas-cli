package tui

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// Waiting runs work while showing a spinner and a running clock.
//
// A resume from the bucket is a few seconds of nothing, and the CLI used to
// print one static line and then sit there. A line that never changes is
// indistinguishable from a hung program — the only honest way to say "still
// going" is to keep moving.
//
// The clock is there because the wait is not instant and not long: a number
// that ticks tells somebody whether to keep watching or go and do something
// else, which "..." cannot.
//
// # Where it draws, and what it must not touch
//
// STDERR, and stdin is left alone entirely. This runs immediately before the
// process hands the terminal to ssh, so consuming a keystroke here would eat
// the first character of somebody's session. Not a terminal — a pipe, a CI log
// — falls back to the plain line, because a spinner rendered into a file is
// thousands of escape sequences nobody will read.
func Waiting(label string, work func() error) error {
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintf(os.Stderr, "%s...\n", label)
		return work()
	}

	done := make(chan error, 1)
	go func() { done <- work() }()

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = markerStyle
	m := waitModel{spin: sp, label: label, start: time.Now(), done: done}
	out, err := tea.NewProgram(m,
		tea.WithOutput(os.Stderr),
		// No input, and this is load-bearing rather than tidy. bubbletea enters
		// RAW MODE in initInput, and only when its input is a terminal file —
		// with nil it is not, so the terminal is never put into raw mode and
		// never has to be restored. The next thing to own this terminal is ssh,
		// and a spinner that left it raw, or ate the first keystroke of a
		// session, would be a worse bug than the static line it replaces.
		tea.WithInput(nil),
	).Run()
	if err != nil {
		// The spinner failing is not the work failing. Wait for the real answer
		// rather than reporting a rendering problem as a resume problem.
		return <-done
	}
	return out.(waitModel).err
}

type waitModel struct {
	spin     spinner.Model
	label    string
	start    time.Time
	done     chan error
	err      error
	finished bool
}

// doneMsg carries the work's result into the event loop.
type doneMsg struct{ err error }

func (m waitModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, func() tea.Msg { return doneMsg{err: <-m.done} })
}

func (m waitModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case doneMsg:
		m.err, m.finished = msg.err, true
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m waitModel) View() string {
	// Nothing once it is over: the line is the caller's to replace, and a
	// finished spinner left on screen is a lie about what is happening.
	if m.finished {
		return ""
	}
	return "  " + m.spin.View() + " " + dimStyle.Render(m.label) +
		faintStyle.Render(fmt.Sprintf("  %ds", int(time.Since(m.start).Seconds())))
}
