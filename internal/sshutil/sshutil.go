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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/config"
	"github.com/yetanothersandbox-dev/yas-cli/internal/tui"
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

// The session holder, inside the guest.
//
// # Why the shell has to live somewhere other than the connection
//
// When a connection breaks, the guest's sshd sees EOF and SIGHUPs the session
// leader — so the shell and everything under it die INSIDE the guest, where no
// amount of reconnecting can reach them. That is why `nohup`, `disown` and
// terminal multiplexers exist at all, and it is why no change to the transport
// can fix this on its own: a deploy is only one of the ways a connection ends,
// and closing a laptop is a commoner one.
//
// So the durable end moves into the guest. tmux owns the PTY, the shell belongs
// to tmux's server process, and an SSH session is only a viewport onto it. The
// connection can die however it likes; the work does not.
//
// This is the same conclusion exe.dev reached from the other direction: their
// VMs answer SSH on a routable address with nothing in the path to redeploy,
// and they STILL run a per-shell session manager, because the front door does
// not stop sshd hanging up on the shell behind it.
//
// # Why tmux and not something smaller
//
// abduco and dtach do session persistence with no keybindings and no status
// bar, which is a better fit for wrapping an agent — nothing sits between the
// user and the tool. What they do not do is REPLAY. tmux redraws its own
// scrollback on attach, so reconnecting shows what was on screen when the
// connection went; the alternatives show a blank terminal until the program
// happens to repaint. After an unexpected drop that difference is the whole
// point, so the keybinding surface is bought back with the baked config: no
// status bar, no mouse capture, and a prefix nothing collides with. Swapping to
// abduco is this line and one bake.
const (
	// muxSession is the one session per box. Whatever you were doing is what
	// you come back to, which is the behaviour somebody reconnecting wants.
	muxSession = "yas"
	// muxConf is baked beside sshd_config; see images/files/tmux.conf in the
	// server repository for what it turns off and why.
	muxConf = "/etc/yas/tmux.conf"
)

// muxCommand wraps a remote command so it runs inside the session holder, or
// returns it unchanged when the box has no tmux.
//
// The probe is a shell test rather than a capability negotiated over the API,
// because a box cloned from a golden baked before tmux existed simply has no
// tmux and never will — a binary cannot be delivered to a running guest the way
// authorized_keys can. Those boxes get exactly today's behaviour, silently, and
// converge as they are recreated.
//
// `new-session -A` is attach-or-create: reconnecting lands in the session that
// is already there, and its command argument is ignored when one exists — which
// is what makes this a reattach rather than a second copy of the agent.
func muxCommand(remoteCmd []string) []string {
	inner := "exec tmux -f " + shellQuote(muxConf) + " new-session -A -s " + muxSession
	if len(remoteCmd) > 0 {
		inner += " -- " + shellJoin(remoteCmd)
	}
	// ${SHELL:-/bin/bash}: a non-login ssh command may carry no SHELL at all,
	// and landing somebody in a shell that does not exist is worse than not
	// wrapping.
	fallback := `exec "${SHELL:-/bin/bash}" -l`
	if len(remoteCmd) > 0 {
		fallback = "exec " + shellJoin(remoteCmd)
	}
	script := "if command -v tmux >/dev/null 2>&1; then " + inner + "; else " + fallback + "; fi"
	// QUOTED, because ssh does not take an argv.
	//
	// ssh joins everything after the host with SPACES and hands the result to
	// the remote login shell as one string to parse. So returning the script as
	// its own argv word is not enough: it arrives as `sh -lc if command -v
	// tmux ...`, the shell parses the words itself, and bash stops at the first
	// `then` with a syntax error. That shipped and broke `yas claude` — the
	// script was tested by running it, which is the wrong boundary; what needed
	// testing was the string ssh actually sends.
	//
	// Args passes remote words through verbatim on purpose (a passthrough's own
	// flags must not be mangled), so the quoting belongs here rather than there.
	return []string{"sh", "-lc", shellQuote(script)}
}

// shellJoin quotes each word so the remote shell sees the argv the caller meant,
// spaces and quotes included.
func shellJoin(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = shellQuote(a)
	}
	return strings.Join(out, " ")
}

// Options are the connect path's choices. The zero value is the old behaviour:
// no session holder, no reconnect.
type Options struct {
	// Mux runs the session inside the guest's session holder, so it survives
	// the connection. Only ever set for an interactive session — see
	// muxCommand, and see Connect for why a scripted one must not be wrapped.
	Mux bool
	// Reconnect re-dials when ssh's own transport fails, rather than returning
	// to a shell prompt.
	Reconnect bool
}

// reconnect bounds. A drop is usually a deploy or a laptop changing networks,
// and both resolve in seconds to tens of seconds; past the window it is more
// honest to hand the terminal back than to sit there.
const (
	reconnectWindow = 90 * time.Second
	reconnectFirst  = 1 * time.Second
	reconnectMax    = 5 * time.Second
	// A session that lasted this long before dropping is a fresh outage rather
	// than a failing retry, so the backoff starts over. Without this a box that
	// drops twice an hour would inherit the previous outage's five seconds.
	reconnectSettled = 30 * time.Second
)

// Connect is the whole connect path: resume if suspended, install keys, pin
// the host key, exec ssh with the caller's terminal. A remote command's exit
// code comes back as an *exec.ExitError.
func Connect(ctx context.Context, cl *api.Client, cfg config.Config, id string, remoteCmd []string) error {
	return ConnectWith(ctx, cl, cfg, id, remoteCmd, Options{})
}

// ConnectWith is Connect with the session holder and the reconnect loop.
//
// The loop lives HERE and not in the ProxyCommand (cmd/yas/stdio.go), and that
// file's own comment says why: re-dialling underneath a live ssh transport
// hands ssh a new stream it has no reason to trust. A reconnect has to be a new
// ssh session, which means going round the whole path again — including
// re-authorizing the key, because a box that resumed from a rootfs suspend has
// lost the authorized_keys file that lived in tmpfs.
func ConnectWith(ctx context.Context, cl *api.Client, cfg config.Config, id string, remoteCmd []string, opt Options) error {
	started := time.Now()
	wait := reconnectFirst
	for {
		attemptAt := time.Now()
		err := connectOnce(ctx, cl, cfg, id, remoteCmd, opt)

		if err != nil && isTerminal(os.Stdin) {
			restoreTerminal(os.Stderr)
		}
		if !worthReconnecting(err) {
			return err
		}
		// Scripted callers get the error. A retry loop with nobody watching is
		// a hang, and the exit code is what a script is reading.
		if !opt.Reconnect || !isTerminal(os.Stdin) {
			explainTransportFailure(ctx, cl, id)
			return err
		}
		// A box that has actually ended is not worth re-dialling.
		if sb, gerr := cl.Get(ctx, id); gerr == nil {
			switch sb.Status {
			case "stopped", "failed", "cancelled":
				explainTransportFailure(ctx, cl, id)
				return err
			}
		}
		if time.Since(attemptAt) > reconnectSettled {
			started, wait = time.Now(), reconnectFirst
		}
		if time.Since(started) > reconnectWindow {
			fmt.Fprintf(os.Stderr, "\ngave up reconnecting to %s after %s.\n", id, reconnectWindow)
			explainTransportFailure(ctx, cl, id)
			return err
		}
		fmt.Fprintf(os.Stderr, "\rconnection dropped — reconnecting to %s…\n", id)
		// A server that named a wait outranks our backoff. It says 5 seconds
		// while fleetd restarts, and dialling sooner just spends an attempt
		// from the window on a host that is not listening yet.
		pause := wait
		if after := api.RetryAfter(err); after > pause {
			pause = after
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
		if wait *= 2; wait > reconnectMax {
			wait = reconnectMax
		}
	}
}

// connectOnce is one whole attempt: everything from asking the gateway where
// the box is to ssh exiting.
func connectOnce(ctx context.Context, cl *api.Client, cfg config.Config, id string, remoteCmd []string, opt Options) error {
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
	// The session holder goes on LAST, wrapping whatever was asked for, so the
	// argv ssh is handed is the one the guest will actually run.
	run := remoteCmd
	if opt.Mux {
		run = muxCommand(remoteCmd)
	}
	cmd := exec.Command("ssh", Args(self, identity, khPath, access, run)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// sshTransportFailed reports whether ssh itself gave up, as opposed to the
// remote command exiting non-zero.
//
// ssh reserves 255 for its own failures and passes anything else through from
// the remote side, which is what makes this distinguishable at all: `yas claude`
// whose agent exits 1 must not be told the host restarted.
func sshTransportFailed(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 255
}

// worthReconnecting reports whether this failure is the kind a second attempt
// fixes.
//
// Two shapes reach here, and only one of them used to.
//
// ssh exiting 255 is the transport dropping under a live session, which is what
// the loop was written for. But an attempt can also fail BEFORE ssh starts: the
// gateway answers 503 host_unreachable because the host is not listening. That
// is the same outage seen one step earlier — a deploy restarting fleetd severs
// the relay AND makes the next dial fail — and it arrived as an ordinary API
// error, so the loop treated it as fatal and printed "could not reach the host
// holding this sandbox" on the first retry.
//
// The effect was that reconnect worked only if the host came back within the
// single attempt it allowed, i.e. almost never during the restart it exists to
// cover. Observed live on 2026-09-08, twice, during a fleet deploy.
func worthReconnecting(err error) bool {
	return sshTransportFailed(err) || api.IsHostTransient(err)
}

// explainTransportFailure says what actually happened, when the answer is
// knowable.
//
// ssh reports what it saw — "closed by remote host", a broken pipe — and that
// reads like the box died. Usually it did not: the interactive relay is served
// by the host daemon (fleetd's ssh-stream route), so a deploy restarting that
// daemon severs every shell while the microVMs it was serving are adopted by
// the new process and carry on. The box is fine and the work inside the shell
// is not, which is a distinction worth drawing for somebody who has just lost
// a session.
//
// Best effort throughout. This runs after a failure and must not add one: any
// error asking the gateway leaves ssh's own message as the last word.
func explainTransportFailure(ctx context.Context, cl *api.Client, id string) {
	sb, err := cl.Get(ctx, id)
	if err != nil {
		return
	}
	switch sb.Status {
	case "idle", "busy":
		fmt.Fprintf(os.Stderr, "\n%s is still running — the connection dropped, not the box.\n", id)
		fmt.Fprintf(os.Stderr, "Its filesystem is untouched. `yas ssh %s` opens a new shell in it.\n", id)
		// Named as A cause and not THE cause, because from here they are
		// indistinguishable: a host deploy and a laptop losing its network
		// both arrive as ssh exiting 255 with the box still healthy. Claiming
		// the deploy would be wrong roughly whenever somebody shuts a laptop.
		fmt.Fprintln(os.Stderr, "A host deploy does this — the relay your shell runs through is restarted "+
			"and the box is handed to the new one — and so does losing your own network. "+
			"Either way, anything that was running IN the shell is gone.")
	case "suspended":
		fmt.Fprintf(os.Stderr, "\n%s parked itself. `yas ssh %s` wakes it and opens a shell.\n", id, id)
	case "stopped", "failed", "cancelled":
		fmt.Fprintf(os.Stderr, "\n%s is %s — the box itself ended.\n", id, sb.Status)
	}
}

// restoreTerminal undoes the modes an interactive remote program may have left
// on: mouse reporting in all three encodings, bracketed paste, and a hidden
// cursor.
//
// Written out rather than shelled to `tput reset`, which also clears the
// scrollback — and the scrollback after a dropped session is the only copy of
// what was on screen when it went.
func restoreTerminal(w *os.File) {
	if !isTerminal(w) {
		return
	}
	writeRestore(w)
}

// writeRestore is the sequences themselves, separated so a test can read them
// without a terminal to write to.
//
// Note what is NOT here: no clear-screen, no `\x1bc` full reset, no `tput
// reset`. After a dropped session the scrollback is the only copy of what was
// on screen when it went, and every one of those would erase it.
func writeRestore(w io.Writer) {
	const (
		mouseOff   = "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1015l"
		pasteOff   = "\x1b[?2004l"
		cursorOn   = "\x1b[?25h"
		keypadNorm = "\x1b[?1l\x1b>"
	)
	fmt.Fprint(w, mouseOff+pasteOff+cursorOn+keypadNorm)
}

// isTerminal reports whether f is a character device, which is the cheapest
// question that distinguishes a terminal from a pipe or a file.
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
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
