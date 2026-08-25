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
)

// version is stamped by the Makefile; "dev" from a bare `go build`.
var version = "dev"

func usage(w *os.File) {
	fmt.Fprintf(w, `yas — sandboxes from your terminal

usage:
  yas                       pick a box (or create one) and connect
  yas new [flags]           create a box and connect
  yas list                  list your boxes
  yas ssh <id>              connect to a box
  yas exec <id> -- cmd...   run one command, stream its output
  yas suspend|resume <id>   pause and unpause a box
  yas rm <id>               delete a box
  yas login                 sign in with GitHub (or paste a key); provider keys via -anthropic/-openai
  yas keys                  list, create and revoke this account's API keys (service accounts)
  yas version               print the version

anything else runs INSIDE a box:
  yas claude                a claude console in a fresh (or picked) box
  yas <cmd> [args...]       any command; -b <id> targets a box, --new forces a fresh one
`)
}

// exitError carries a remote command's exit code to os.Exit without losing
// deferred cleanup along the way.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// verbs is the reserved namespace. Everything outside it is a command to run
// in a sandbox, so ADDING a verb is a compatibility decision: it shadows any
// program of the same name.
var verbs = map[string]func([]string) error{
	"new":     cmdNew,
	"list":    cmdList,
	"ls":      cmdList,
	"ssh":     cmdSSH,
	"connect": cmdSSH,
	"rm":      cmdRemove,
	"delete":  cmdRemove,
	"suspend": cmdSuspend,
	"resume":  cmdResume,
	"exec":    cmdExec,
	"login":   cmdLogin,
	"keys":    cmdKeys,
	"stdio":   cmdStdio,
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
		fmt.Fprintln(os.Stderr, "yas:", err)
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
