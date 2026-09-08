package sshutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
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
