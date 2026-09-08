package sshutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ssh reserves 255 for its own failures and passes everything else through
// from the remote side. That distinction is the whole basis for saying "the
// host restarted": `yas claude` whose agent exits 1 must not be told that.
func TestOnlySSHsOwnFailureCountsAsATransportFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		want bool
	}{
		{"ssh could not connect", 255, true},
		{"the remote command failed", 1, false},
		{"the remote command was killed", 130, false},
		{"a test that exits 2", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := exitWith(t, tc.code)
			if got := sshTransportFailed(err); got != tc.want {
				t.Errorf("sshTransportFailed(exit %d) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
	if sshTransportFailed(nil) {
		t.Error("a clean exit was read as a transport failure")
	}
}

// The restore has to leave the SCROLLBACK alone. After a dropped session it is
// the only copy of what was on screen when the connection went, and `tput
// reset` — the obvious shortcut — clears it.
func TestTheRestoreDoesNotClearTheScreenOrScrollback(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	// A pipe is not a character device, so this must write nothing at all: a
	// piped session has no terminal modes to restore and the bytes would land
	// in whatever is reading.
	restoreTerminal(w)
	w.Close()
	buf := make([]byte, 256)
	n, _ := r.Read(buf)
	if n != 0 {
		t.Fatalf("wrote %q to a pipe; escape codes would corrupt piped output", buf[:n])
	}
}

// The sequences themselves, asserted by name so a later edit cannot quietly
// drop the one that matters. Mouse reporting is the unmistakable symptom: left
// on, every mouse movement arrives at the shell as `35;102;25M…`.
func TestTheRestoreTurnsMouseReportingOff(t *testing.T) {
	var b strings.Builder
	writeRestore(&b)
	for _, want := range []string{
		"\x1b[?1000l", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1006l", "\x1b[?1015l",
		"\x1b[?2004l", "\x1b[?25h",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the restore does not send %q", want)
		}
	}
	for _, forbidden := range []string{"\x1b[2J", "\x1b[3J", "\x1bc"} {
		if strings.Contains(b.String(), forbidden) {
			t.Errorf("the restore sends %q, which erases the screen or the scrollback", forbidden)
		}
	}
}

func exitWith(t *testing.T, code int) error {
	t.Helper()
	// `sh -c "exit N"` is the cheapest way to get a real *exec.ExitError with a
	// chosen status, which is what the function under test inspects.
	return exec.Command("sh", "-c", "exit "+itoa(code)).Run()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// The session holder wraps whatever was asked for, and degrades on a box that
// has no tmux — a golden baked before it exists cannot be given one, because a
// binary is not deliverable to a running guest the way authorized_keys is.
func TestTheSessionHolderWrapsAndFallsBack(t *testing.T) {
	got := strings.Join(muxCommand(nil), " ")
	if !strings.Contains(got, "command -v tmux") {
		t.Error("no probe: a box without tmux would get a command it cannot run")
	}
	if !strings.Contains(got, "new-session -A -s "+muxSession) {
		t.Errorf("not attach-or-create: %q", got)
	}
	if !strings.Contains(got, "${SHELL:-/bin/bash}") {
		t.Error("the fallback assumes SHELL is set; a non-login ssh command may carry none")
	}

	// With a command, BOTH branches must run it — the wrapped one and the
	// fallback. A box without tmux running no command at all would be a box
	// that silently ignored what you asked for.
	withCmd := strings.Join(muxCommand([]string{"claude", "--flag"}), " ")
	if strings.Count(withCmd, "claude") != 2 {
		t.Errorf("the command does not appear in both branches: %q", withCmd)
	}
	if strings.Contains(withCmd, "${SHELL") {
		t.Error("the fallback dropped into a shell instead of running the command")
	}
}

// Arguments survive the trip through two shells. `yas claude "fix the bug"`
// must not become three arguments on the far side.
func TestTheSessionHolderQuotesArguments(t *testing.T) {
	got := strings.Join(muxCommand([]string{"claude", "fix the bug", "it's broken"}), " ")
	if !strings.Contains(got, `'fix the bug'`) {
		t.Errorf("a spaced argument was not quoted: %q", got)
	}
	if !strings.Contains(got, `'it'\''s broken'`) {
		t.Errorf("a quoted argument was not escaped: %q", got)
	}
}

// Options{} is the old behaviour exactly. Anything that has not opted in — a
// scripted passthrough above all — must be untouched by any of this.
func TestTheZeroOptionsChangeNothing(t *testing.T) {
	var o Options
	if o.Mux || o.Reconnect {
		t.Fatal("the zero Options is not the old behaviour")
	}
}

// The backoff is bounded and climbs. An unbounded one turns a box that is
// genuinely gone into a terminal that never comes back.
func TestTheReconnectBackoffIsBoundedAndClimbs(t *testing.T) {
	wait := reconnectFirst
	seen := []time.Duration{wait}
	for i := 0; i < 8; i++ {
		if wait *= 2; wait > reconnectMax {
			wait = reconnectMax
		}
		seen = append(seen, wait)
	}
	if seen[0] >= seen[1] {
		t.Error("the backoff does not climb")
	}
	for _, w := range seen {
		if w > reconnectMax {
			t.Errorf("the backoff reached %v, past the %v cap", w, reconnectMax)
		}
	}
	if seen[len(seen)-1] != reconnectMax {
		t.Errorf("the backoff settled at %v, not the cap", seen[len(seen)-1])
	}
	// And the window has to outlast a fleetd restart, or the loop gives up
	// during exactly the outage it exists for.
	if reconnectWindow < 60*time.Second {
		t.Errorf("the reconnect window is %v, too short to outlast a deploy", reconnectWindow)
	}
}
