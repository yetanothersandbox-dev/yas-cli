// Package sshutil owns the connect path: key material, known_hosts pinning,
// and the ssh invocation itself.
//
// The design constraint everything here serves: the laptop talks to the
// GATEWAY and to nothing else. `sandbox-<id>` on the ssh command line is a
// known_hosts label, never resolved — the ProxyCommand (`yas stdio <id>`)
// makes the only network connection.
package sshutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
	"github.com/Gilbert09/yas/clients/yas/internal/config"
	"github.com/Gilbert09/yas/clients/yas/internal/tui"
)

// EnsureIdentity returns the private-key path to hand ssh -i, generating the
// dedicated keypair on first use.
//
// A dedicated key rather than the user's own: no passphrase prompt inside a
// TUI flow, IdentitiesOnly keeps ssh from offering five keys and hitting
// MaxAuthTries, and it can be revoked (or deleted) without touching the
// user's real identity. Generated with ssh-keygen because ssh itself is
// already a hard runtime dependency — pulling x/crypto to avoid a subprocess
// would buy nothing.
func EnsureIdentity(cfg config.Config) (string, error) {
	if cfg.SSHIdentity != "" {
		return cfg.SSHIdentity, nil
	}
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	key := filepath.Join(dir, "id_ed25519")
	if _, err := os.Stat(key); err == nil {
		return key, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-q", "-C", "yas-cli", "-f", key)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ssh-keygen: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return key, nil
}

// PublicKeys is what the create/ssh calls install: the dedicated key's public
// half, plus any ~/.ssh/id_*.pub lying around — so a user whose agent holds
// their usual key can `ssh fleet@sandbox-x` by hand too.
func PublicKeys(identity string) ([]string, error) {
	var keys []string
	if b, err := os.ReadFile(identity + ".pub"); err == nil {
		keys = append(keys, strings.TrimSpace(string(b)))
	}
	if home, err := os.UserHomeDir(); err == nil {
		matches, _ := filepath.Glob(filepath.Join(home, ".ssh", "id_*.pub"))
		for _, m := range matches {
			if b, err := os.ReadFile(m); err == nil {
				if k := strings.TrimSpace(string(b)); k != "" {
					keys = append(keys, k)
				}
			}
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no public key at %s.pub and none under ~/.ssh", identity)
	}
	return keys, nil
}

// knownHostsPath is the per-box pin file. Cache, not config: it is
// reconstructed from the API on every connect.
func knownHostsPath(id string) (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".cache")
	}
	dir := filepath.Join(base, "yas", "known_hosts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, id), nil
}

// Args builds the ssh argv. Split out from Connect so a test can hold the
// whole invocation up to the light without running ssh.
func Args(selfPath, identity, knownHosts string, access api.SSHAccess, remoteCmd []string) []string {
	args := []string{
		// The pin written before the first connection is what makes strict
		// checking work on connection one; accepting anything else would be
		// trusting whatever answered.
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "StrictHostKeyChecking=yes",
		// The only network path. %h is the pin label ssh was given below.
		"-o", "ProxyCommand=" + shellQuote(selfPath) + " stdio %h",
		// Keepalives ride INSIDE the ssh stream, so they also keep the
		// gateway's edge from idling the tunnel out.
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4",
		"-o", "IdentitiesOnly=yes",
		"-i", identity,
	}
	if len(remoteCmd) > 0 {
		// A remote command still gets a PTY: the whole point of passthrough is
		// interactive tools (claude, htop, a REPL).
		args = append(args, "-t")
	}
	args = append(args, access.User+"@"+access.Host)
	if len(remoteCmd) > 0 {
		args = append(args, "--")
		args = append(args, remoteCmd...)
	}
	return args
}

// shellQuote wraps a path for the ProxyCommand line, which ssh runs through
// $SHELL -c. Single quotes survive everything but themselves.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Connect is the whole connect path: resume if suspended, install keys, pin
// the host key, exec ssh with the caller's terminal. A remote command's exit
// code comes back as an *exec.ExitError.
func Connect(ctx context.Context, cl *api.Client, cfg config.Config, id string, remoteCmd []string) error {
	sb, err := cl.Get(ctx, id)
	if err != nil {
		return err
	}
	if sb.Status == "suspended" {
		// One spinner across BOTH halves. The resume call and the wait for the
		// box to serve again are one wait to the person watching, and two
		// indicators would just blink at them.
		err := tui.Waiting("resuming "+id, func() error {
			if err := cl.Resume(ctx, id); err != nil {
				return fmt.Errorf("resuming %s: %w", id, err)
			}
			return waitReady(ctx, cl, id)
		})
		if err != nil {
			return err
		}
	}

	identity, err := EnsureIdentity(cfg)
	if err != nil {
		return err
	}
	keys, err := PublicKeys(identity)
	if err != nil {
		return err
	}
	// Every connect, not cached: the guest's host key lives in tmpfs and is
	// fresh on every boot, the call replaces the key set idempotently, and a
	// stale pin is exactly the failure StrictHostKeyChecking turns into a
	// scary warning.
	access, err := cl.SSH(ctx, id, keys)
	if err != nil {
		return fmt.Errorf("authorizing ssh on %s: %w", id, err)
	}
	khPath, err := knownHostsPath(id)
	if err != nil {
		return err
	}
	if err := os.WriteFile(khPath, []byte(access.KnownHosts+"\n"), 0o600); err != nil {
		return err
	}

	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("ssh", Args(self, identity, khPath, access, remoteCmd)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

const (
	// firstPoll is how soon after the resume returns to look. Small, because
	// the resume call is synchronous and the answer is usually already yes.
	firstPoll = 120 * time.Millisecond
	// maxPoll is where the backoff stops.
	maxPoll = 2 * time.Second
)

// waitReady polls until the sandbox serves again after a resume.
func waitReady(ctx context.Context, cl *api.Client, id string) error {
	deadline := time.Now().Add(2 * time.Minute)
	wait := firstPoll
	for time.Now().Before(deadline) {
		sb, err := cl.Get(ctx, id)
		if err != nil {
			return err
		}
		switch sb.Status {
		case "idle", "busy":
			return nil
		case "failed", "stopped", "cancelled":
			return fmt.Errorf("%s did not come back: status %s", id, sb.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		// Fast first, then back off. Resume is synchronous, so the status is
		// usually already there on the first look and the old flat two seconds
		// was two seconds of nothing added to every wake. Backing off keeps a
		// genuinely slow resume from turning into a poll flood.
		if wait *= 2; wait > maxPoll {
			wait = maxPoll
		}
	}
	return fmt.Errorf("%s did not come back within two minutes", id)
}
