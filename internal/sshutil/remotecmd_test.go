package sshutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

// remoteLine is the string ssh actually sends: everything after the host,
// joined with spaces. That join is the whole bug, so the test has to perform
// it rather than inspect the argv ssh was handed.
func remoteLine(t *testing.T, remoteCmd []string) string {
	t.Helper()
	args := Args("/usr/local/bin/yas", "/tmp/id", "/tmp/kh",
		api.SSHAccess{User: "fleet", Host: "sandbox-x"}, remoteCmd)
	for i, a := range args {
		if a == "--" {
			return strings.Join(args[i+1:], " ")
		}
	}
	t.Fatalf("no remote command in %v", args)
	return ""
}

// A multi-word argument survives the trip.
//
// # WATCHED FAIL
//
// Append remoteCmd verbatim, as this did, and the prompt below arrives as
// `claude -p on the homepage, ...`. -p takes the single word `on`, the rest
// become stray arguments, and the agent answers the word "on".
//
// Observed live on 2026-09-09: `yas claude -p "<a sentence>"` produced "Not
// sure what you'd like turned on", which reads as a confused model rather than
// as a command the CLI took apart on the way out.
//
// The assertion is behavioural, not spelling: the line is parsed by a real
// shell and the argv it produces is compared to the argv the caller meant. A
// test that asserted the quoting characters would pass for any convention and
// prove nothing about what the remote shell does with them.
func TestAMultiWordRemoteArgumentSurvivesSSHsSpaceJoin(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"a sentence with commas and a quoted phrase", []string{
			"claude", "-p",
			`on the homepage, we have a sign in button - replace it with an "open app" button`,
			"--dangerously-skip-permissions",
		}},
		{"single quotes in the text", []string{"claude", "-p", "don't split this"}},
		{"a dollar sign that must not expand", []string{"claude", "-p", "cost is $HOME dollars"}},
		{"backticks that must not run", []string{"claude", "-p", "run `whoami` please"}},
		{"a semicolon that must not end the command", []string{"claude", "-p", "first; second"}},
		{"an ordinary flag-only passthrough", []string{"claude", "--dangerously-skip-permissions"}},
		{"a bare command", []string{"htop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := remoteLine(t, tc.argv)

			// Parse it the way the remote login shell would, and print the argv
			// one per line so the boundaries are unambiguous.
			script := `printf '%s\n' ` + line
			out, err := exec.Command("sh", "-c", script).Output()
			if err != nil {
				t.Fatalf("the remote shell could not parse %q: %v", line, err)
			}
			got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
			if len(got) != len(tc.argv) {
				t.Fatalf("the remote shell saw %d words, want %d\n  line: %s\n  got:  %q",
					len(got), len(tc.argv), line, got)
			}
			for i := range tc.argv {
				if got[i] != tc.argv[i] {
					t.Errorf("word %d = %q, want %q", i, got[i], tc.argv[i])
				}
			}
		})
	}
}

// A command with no arguments still runs, and the side effect proves the shell
// executed the argv rather than merely parsing it.
func TestTheRemoteLineExecutesAsTheCallerMeant(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "touched by the agent")
	line := remoteLine(t, []string{"touch", marker})
	if err := exec.Command("sh", "-c", line).Run(); err != nil {
		t.Fatalf("running %q: %v", line, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the path with a space in it was split: %v", err)
	}
}
