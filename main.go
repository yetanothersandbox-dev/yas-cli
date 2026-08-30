// yas — create, pick and connect to sandboxes from a laptop.
//
// The one hostname this program ever talks to is the gateway
// (api.yetanothersandbox.dev, or YAS_BASE_URL). The fleet behind it is not
// addressable from here and is never named: `ssh fleet@sandbox-<id>` works
// because the ProxyCommand is `yas stdio <id>`, which carries the connection
// through the gateway's ssh-stream upgrade — no host, no port, no VPN.
//
// Dispatch is fleetctl's shape: stdlib flag, a switch on the first argument.
// Anything that is not a known verb is a PASSTHROUGH command to run inside a
// box — `yas claude --dangerously-skip-permissions` reaches claude with its
// flags untouched.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
	"github.com/Gilbert09/yas/clients/yas/internal/cliio"
	"github.com/Gilbert09/yas/clients/yas/internal/config"
	"github.com/Gilbert09/yas/clients/yas/internal/ui"
)

// version is stamped by the Makefile; "dev" from a bare `go build`.
var version = "dev"

// usage is the CLI's front door, and the one place the wordmark earns its keep.
//
// Grouped by WHEN somebody needs a command rather than alphabetically: a person
// reading help for the first time wants "how do I start", and a person reading
// it for the tenth wants "what was that flag". A flat list serves neither.
//
// The text is identical piped, so `yas --help | grep suspend` still works. Only
// the colour and the mark are conditional.
func usage(w *os.File) {
	tty := ui.TTY(w)
	if tty {
		fmt.Fprint(w, ui.Wordmark(w,
			"yet another sandbox — yes, we know.",
			"a real computer you can throw away, and get back"))
		fmt.Fprintln(w)
	}

	head := func(s string) string {
		if !tty {
			return s
		}
		return ui.Err.NewStyle().Bold(true).Render(s)
	}
	cmd := func(s string) string {
		if !tty {
			return s
		}
		return ui.S(ui.Accent).Render(s)
	}
	note := func(s string) string {
		if !tty {
			return s
		}
		return ui.S(ui.Subtle).Render(s)
	}
	row := func(c, d string) {
		fmt.Fprintf(w, "  %s%s\n", cmd(fmt.Sprintf("%-26s", c)), note(d))
	}

	fmt.Fprintln(w, head("start here"))
	row("yas login", "sign in with GitHub, or paste a key")
	row("yas new [flags] [name]", "a fresh box, connected in about 400ms")
	row("yas", "pick a box (or make one) and connect")

	fmt.Fprintln(w, "\n"+head("day to day"))
	row("yas list", "your boxes, and what they draw from the pool")
	row("yas ssh <id>", "a shell in a box")
	row("yas exec <id> -- cmd...", "run one command, stream its output; script-safe")
	row("yas suspend <id>", "park it — the pool gets its memory back")
	row("yas resume <id>", "unpark it, usually before you finish blinking")
	row("yas rm <id>", "delete a box and everything in it")

	fmt.Fprintln(w, "\n"+head("account"))
	row("yas keys", "API keys for machines that are not you")
	row("yas defaults", "the size a box gets when you do not say")
	row("yas login -anthropic", "store a provider key; a box never sees it")
	row("yas version", "print the version")

	fmt.Fprintln(w, "\n"+head("anything else runs INSIDE a box"))
	row("yas claude", "a claude console in a box that is not your laptop")
	row("yas <cmd> [args...]", "any command; -b <id> picks the box, --new forces a fresh one")
}

// exitError carries a remote command's exit code to os.Exit without losing
// deferred cleanup along the way.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// verbs is the reserved namespace. Everything outside it is a command to run
// in a sandbox, so ADDING a verb is a compatibility decision: it shadows any
// program of the same name.
var verbs = map[string]func([]string) error{
	"new":      cmdNew,
	"list":     cmdList,
	"ls":       cmdList,
	"ssh":      cmdSSH,
	"connect":  cmdSSH,
	"rm":       cmdRemove,
	"delete":   cmdRemove,
	"suspend":  cmdSuspend,
	"resume":   cmdResume,
	"exec":     cmdExec,
	"login":    cmdLogin,
	"keys":     cmdKeys,
	"defaults": cmdDefaults,
	"stdio":    cmdStdio,
}

func main() {
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "yas: the connect path is built on OpenSSH ProxyCommand mechanics; Windows is not supported yet")
		os.Exit(1)
	}
	args := os.Args[1:]
	var err error
	switch {
	case len(args) == 0:
		if cliio.IsTTY(os.Stdin) && cliio.IsTTY(os.Stdout) {
			err = cmdPicker()
		} else {
			usage(os.Stderr)
			os.Exit(2)
		}
	case args[0] == "help" || args[0] == "-h" || args[0] == "--help":
		usage(os.Stdout)
		return
	case args[0] == "version" || args[0] == "--version":
		fmt.Println("yas", version)
		return
	default:
		if fn, ok := verbs[args[0]]; ok {
			err = fn(args[1:])
		} else {
			err = cmdPassthrough(args)
		}
	}
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		// Every refusal in the CLI arrives here, which is why the renderer is
		// here and not spread across the commands. On a terminal it gets a
		// headline, the server's own sentence, and the commands that fix it; in
		// a pipe it collapses to the single `yas: ...` line scripts have always
		// seen. See ui.Refusal.
		ui.Refusal(os.Stderr, api.ErrorKind(err), err.Error(), ui.StderrTTY())
		os.Exit(1)
	}
}

// loadClient builds the authenticated client every command shares. The key
// missing is the first thing a new user hits, so the message names the fix.
func loadClient() (config.Config, *api.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return cfg, nil, err
	}
	key := cfg.APIKeyResolved()
	if key == "" {
		return cfg, nil, errors.New("no API key. Run `yas login`, or set YAS_API_KEY")
	}
	base := cfg.BaseURLResolved()
	if base == "" {
		base = api.DefaultBaseURL
	}
	return cfg, &api.Client{BaseURL: base, Key: key}, nil
}
