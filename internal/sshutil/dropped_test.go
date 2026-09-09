package sshutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Running the line, without the machine deciding which branch runs.
//
// The script branches on `command -v tmux`, so the branch is a fact about the
// box — and a test that hardcoded `PATH=/usr/bin:/bin` to mean "no tmux" was
// asserting something about the author's laptop. True on a Mac, where tmux is
// under /opt/homebrew; false on a GitHub runner, where /usr/bin/tmux exists. So
// the wrapped branch ran there, tmux said "open terminal failed: not a
// terminal", and CI was red for five commits while the same test passed
// locally.
//
// Two things make the branch the test's choice instead:
//
//   - the PATH is BUILT, holding only the commands the case needs, so tmux is
//     absent because nothing put one there — or present as a SHIM, which is the
//     only way to exercise the wrapped branch at all, a real tmux needing a
//     terminal that `go test` does not have.
//   - the line is run through `sh -c` rather than the `sh -lc` ssh sends. A
//     LOGIN shell reads /etc/profile, which on Debian assigns PATH outright, so
//     everything above would be thrown away before the probe ran. The -l still
//     gets its check: TestTheJoinedRemoteCommandIsValidShell parses the real
//     line, untouched, and parsing is what that regression was about.

// pathOf builds a directory holding just the named commands, plus the `sh` the
// line invokes, and returns it as a PATH. `exec` needs real binaries — it does
// not fall back to the shell's own builtins — so a case that runs `exec printf`
// needs a printf reachable under the PATH the case sets.
func pathOf(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range append([]string{"sh"}, names...) {
		real, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("this machine has no %s to run the line with: %v", name, err)
		}
		if err := os.Symlink(real, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runLine runs what ssh would send, under one built PATH. See the note above
// for the -l, which is dropped here and parsed elsewhere.
func runLine(t *testing.T, path string, remote []string) (string, error) {
	t.Helper()
	// THROUGH Args, because Args is what quotes. Joining muxCommand's own words
	// skipped that step, so a doubly-quoted line could reach a guest and die
	// there with `fi: not found` while every test here stayed green.
	out, err := exec.Command("bash", "-c",
		"PATH="+path+"; "+withoutLogin(t, remoteLine(t, muxCommand(remote)))).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// withoutLogin turns the login shell ssh sends into a plain one, and FAILS if
// it cannot find it.
//
// The failure is the point. This substitution used to be a bare
// strings.Replace on `sh -lc `, and the day Args started quoting every word the
// line became `'sh' '-lc' …`: the replace matched nothing, said nothing, and
// every case quietly went back to running a LOGIN shell — whose /etc/profile
// assigns PATH outright on Debian, throwing away the PATH each case built and
// letting the machine pick the branch again. That is the exact bug this file
// was rewritten to end, arrived at a second time through a change nobody here
// made. So the forms are named, and an unrecognised one stops the test.
func withoutLogin(t *testing.T, line string) string {
	t.Helper()
	for _, form := range []string{"'sh' '-lc' ", "sh -lc "} {
		if strings.Contains(line, form) {
			return strings.Replace(line, form, strings.Replace(form, "-lc", "-c", 1), 1)
		}
	}
	t.Fatalf("no login shell to strip in %q: the quoting changed, and running this line as it "+
		"stands would let /etc/profile rebuild PATH and choose the tmux branch for us", line)
	return ""
}

// fakeTmux is a tmux that prints what it was asked to run, one argument per
// line, instead of opening a session. Everything before `--` is the session
// holder's own flags; everything after it is the command the caller typed, and
// that is the part whose quoting this file exists to check.
const fakeTmux = `#!/bin/sh
seen=0
for a in "$@"; do
  if [ "$seen" = 1 ]; then printf '%s|' "$a"
  elif [ "$a" = "--" ]; then seen=1
  fi
done
`

func pathWithFakeTmux(t *testing.T) string {
	t.Helper()
	dir := pathOf(t)
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(fakeTmux), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

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

// Arguments survive the trip. Asserted by RUNNING the line rather than by
// matching the quoting, because the quoting is nested — the script is
// single-quoted as a whole for ssh, so the inner quotes are escaped and any
// test that matched them was testing the spelling instead of the behaviour.
//
// Built through Args, which is where the outer quoting happens. Joining
// muxCommand's own words instead skipped it, and that is how a doubly-quoted
// line reached a guest and died there with `fi: not found` while this stayed
// green. See TestTheJoinedRemoteCommandIsValidShell.
func TestTheSessionHolderQuotesArguments(t *testing.T) {
	// BOTH branches, because both carry the arguments and the one a guest
	// actually takes is the wrapped one. The two print different shapes and
	// that is the point of running each: the fallback EXECUTES printf, so the
	// output is what printf makes of the words, while the wrapped branch hands
	// the whole argv to the session holder, so the shim echoes the command name
	// and its format string too. Either way the two words with a space and an
	// apostrophe in them arrive as two words.
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"the fallback, on a box with no tmux", pathOf(t, "printf"),
			"fix the bug|it's broken|"},
		{"the session holder, on a box that has one", pathWithFakeTmux(t),
			"printf|%s||fix the bug|it's broken|"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runLine(t, tc.path, []string{"printf", "%s|", "fix the bug", "it's broken"})
			if err != nil {
				t.Fatalf("running the line failed: %v\n%s", err, got)
			}
			if got != tc.want {
				t.Errorf("arguments did not survive: %q, want %q", got, tc.want)
			}
		})
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

// THE test the first version of this needed and did not have.
//
// ssh does not take an argv. It joins everything after the host with spaces and
// hands the result to the remote login shell as ONE string to parse. So the
// thing that must be valid shell is not the script — it is the joined line.
// Testing the script by running it passes while the joined line is a syntax
// error, which is exactly what shipped: `yas claude` died with
//
//	bash: -c: line 1: syntax error near unexpected token `then'
//
// This runs the joined line through a real shell, which is the only check that
// would have caught it.
func TestTheJoinedRemoteCommandIsValidShell(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote []string
		want   string
	}{
		{"a bare shell", nil, ""},
		{"a command", []string{"echo", "hello"}, "hello"},
		{"a command with spaces", []string{"echo", "fix the bug"}, "fix the bug"},
		{"a command with quotes", []string{"echo", "it's broken"}, "it's broken"},
		{"a command with a flag", []string{"echo", "--dangerously-skip-permissions"}, "--dangerously-skip-permissions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exactly what ssh sends: the argv after the host, joined by
			// spaces — and reached THROUGH Args, because Args is what does the
			// quoting. Joining muxCommand's own output skipped that step, so
			// this test kept passing while the composed path was doubly quoted
			// and died on the guest with `fi: not found`.
			line := remoteLine(t, muxCommand(tc.remote))

			// Parse-only first, so a syntax error is reported as one.
			if out, err := exec.Command("bash", "-n", "-c", line).CombinedOutput(); err != nil {
				t.Fatalf("the line ssh sends is not valid shell: %v\n%s\nline: %s", err, out, line)
			}
			if tc.want == "" {
				return
			}
			// Then really run it, under a PATH with no tmux in it, so the
			// fallback branch executes the command with its arguments intact.
			// The PATH is built rather than assumed — see pathOf.
			got, err := runLine(t, pathOf(t, "echo"), tc.remote)
			if err != nil {
				t.Fatalf("running the line failed: %v\n%s", err, got)
			}
			if got != tc.want {
				t.Errorf("the command received %q, want %q\nline: %s", got, tc.want, line)
			}
		})
	}
}
