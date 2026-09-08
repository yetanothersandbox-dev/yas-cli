package main

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/yetanothersandbox-dev/yas-cli/internal/sshutil"
)

// cmdSSH opens an interactive shell in an existing box.
//
// The shell runs inside the box's session holder and the connection re-dials
// itself, so closing a laptop, changing networks or a host deploy costs you the
// connection and not the work. `-plain` opts out of both, for the rare case
// where a bare ssh session is what is wanted.
func cmdSSH(args []string) error {
	fs := flag.NewFlagSet("ssh", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	plain := fs.Bool("plain", false, "a bare ssh session: no session holder, no reconnect")
	// The id comes off the front, so `yas ssh dev -plain` and `yas ssh -plain dev`
	// both work — Go's flag package stops at the first bare word.
	rest := args
	id := ""
	if len(rest) > 0 && !wantsHelp(rest[0]) && rest[0][0] != '-' {
		id, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return errors.New("usage: yas ssh <id> [-plain]")
	}
	if id == "" && fs.NArg() > 0 {
		id = fs.Arg(0)
	}
	if id == "" {
		return errors.New("usage: yas ssh <id> [-plain]")
	}
	cfg, cl, err := loadClient()
	if err != nil {
		return err
	}
	return remapExit(sshutil.ConnectWith(context.Background(), cl, cfg, id, nil,
		sshutil.Options{Mux: !*plain, Reconnect: !*plain}))
}
