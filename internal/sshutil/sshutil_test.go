package sshutil

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

// The argv is the security surface: a wrong option here silently downgrades
// host-key checking or leaks a connection outside the gateway, so it is
// asserted piece by piece rather than as one golden string that a harmless
// reorder would break.
func TestArgsPinAndProxyEverything(t *testing.T) {
	access := api.SSHAccess{User: "fleet", Host: "sandbox-brisk-otter-4f2a"}
	args := Args("/usr/local/bin/yas", "/home/u/.config/yas/id_ed25519", "/home/u/.cache/yas/known_hosts/brisk-otter-4f2a", access, nil)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"StrictHostKeyChecking=yes",
		"UserKnownHostsFile=/home/u/.cache/yas/known_hosts/brisk-otter-4f2a",
		"ProxyCommand='/usr/local/bin/yas' stdio %h",
		"ServerAliveInterval=15",
		"IdentitiesOnly=yes",
		"fleet@sandbox-brisk-otter-4f2a",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q:\n%s", want, joined)
		}
	}
	// No remote command: no forced tty flag, and nothing after the host.
	if args[len(args)-1] != "fleet@sandbox-brisk-otter-4f2a" {
		t.Errorf("host is not last: %v", args)
	}
	// The host is a pin label. If a real hostname or address ever appears
	// here, the topology rule — the laptop talks to the gateway only — broke.
	if strings.Contains(joined, "hetzner") || strings.Contains(joined, "HostName") {
		t.Errorf("the argv names infrastructure: %s", joined)
	}
}

// This used to be TestArgsRemoteCommandGetsATTYAndVerbatimFlags, and it
// asserted that each remote word appeared as its own argv entry — the SPELLING
// of a mechanism that was wrong. ssh takes no argv: it joins everything after
// the host with spaces, so separate entries buy nothing and `-p "hello world"`
// — this test's own fixture — reached the guest as two arguments.
//
// What matters is what the remote shell ends up with, so that is what is
// asserted now. See remotecmd_test.go for the full argument-boundary table.
func TestArgsRemoteCommandGetsATTYAndKeepsItsArgumentBoundaries(t *testing.T) {
	access := api.SSHAccess{User: "fleet", Host: "sandbox-b1"}
	remote := []string{"claude", "--dangerously-skip-permissions", "-p", "hello world"}
	args := Args("/bin/yas", "/k", "/kh", access, remote)
	joined := strings.Join(args, "\x00")

	if !strings.Contains(joined, "\x00-t\x00") {
		t.Errorf("a remote command did not request a tty: %v", args)
	}
	sep := args[len(args)-2]
	if sep != "--" {
		t.Fatalf("no -- separator before the remote command (got %q)", sep)
	}

	// The flags are still passed through unaltered — quoting round-trips, so
	// `--dangerously-skip-permissions` arrives as itself and not as a word the
	// remote shell has interpreted.
	out, err := exec.Command("sh", "-c", `printf '%s\n' `+args[len(args)-1]).Output()
	if err != nil {
		t.Fatalf("the remote shell could not parse the command: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(remote) {
		t.Fatalf("the remote shell saw %q, want %q", got, remote)
	}
	for i := range remote {
		if got[i] != remote[i] {
			t.Errorf("remote argv[%d] = %q, want %q", i, got[i], remote[i])
		}
	}
}

func TestShellQuoteSurvivesSpacesAndQuotes(t *testing.T) {
	for in, want := range map[string]string{
		"/Applications/My Tools/yas": "'/Applications/My Tools/yas'",
		"/plain/yas":                 "'/plain/yas'",
		"/odd'name/yas":              `'/odd'\''name/yas'`,
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
