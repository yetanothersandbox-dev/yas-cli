package sshutil

import (
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

func TestArgsRemoteCommandGetsATTYAndVerbatimFlags(t *testing.T) {
	access := api.SSHAccess{User: "fleet", Host: "sandbox-b1"}
	remote := []string{"claude", "--dangerously-skip-permissions", "-p", "hello world"}
	args := Args("/bin/yas", "/k", "/kh", access, remote)
	joined := strings.Join(args, "\x00")

	if !strings.Contains(joined, "\x00-t\x00") {
		t.Errorf("a remote command did not request a tty: %v", args)
	}
	// The remote command survives verbatim, flags and all, after the `--`.
	tail := args[len(args)-len(remote):]
	for i, want := range remote {
		if tail[i] != want {
			t.Fatalf("remote argv[%d] = %q, want %q", i, tail[i], want)
		}
	}
	sep := args[len(args)-len(remote)-1]
	if sep != "--" {
		t.Fatalf("no -- separator before the remote command (got %q)", sep)
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
