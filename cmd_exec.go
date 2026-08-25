package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
)

// cmdExec runs one non-interactive command and streams its output. For a
// shell or anything interactive, `yas ssh` — exec has no PTY and no stdin by
// design; its job is `yas exec box -- make test` from a script.
//
// The command is NOT bound to this process. A dropped stream loses the
// connection, never the command: the output lands in the box's transcript
// with a host-assigned cursor, and this resumes reading from the cursor it
// already has.
func cmdExec(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	shell := fs.Bool("shell", false, "run through the guest's shell instead of as argv")
	cwd := fs.String("cwd", "", "working directory inside the box")
	timeout := fs.Duration("timeout", 0, "kill the command after this long (guest-side)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 2 || rest[1] == "" {
		return errors.New("usage: yas exec <id> [-shell] [-cwd dir] -- cmd [args...]")
	}
	id, cmd := rest[0], rest[1:]
	if cmd[0] == "--" {
		cmd = cmd[1:]
	}
	if len(cmd) == 0 {
		return errors.New("no command after the id")
	}

	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	req := api.ExecRequest{Cmd: cmd, Shell: *shell, Cwd: *cwd}
	if *timeout > 0 {
		req.TimeoutMs = timeout.Milliseconds()
	}

	exitCode := -1
	var cursor int64
	handle := func(page api.EventsPage) {
		cursor = page.Cursor
		for _, e := range page.Events {
			switch e.Event.Type {
			case "exec_output":
				var c api.ExecChunk
				if json.Unmarshal(e.Event.Raw, &c) != nil {
					continue
				}
				if e.Event.Subtype == "stderr" {
					fmt.Fprint(os.Stderr, c.Data)
				} else {
					fmt.Fprint(os.Stdout, c.Data)
				}
			case "exec_exit":
				var x api.ExecExit
				if json.Unmarshal(e.Event.Raw, &x) == nil {
					exitCode = x.ExitCode
					if x.TimedOut {
						fmt.Fprintln(os.Stderr, "yas: the command hit its guest-side timeout")
					}
					if x.PipesHeld {
						fmt.Fprintln(os.Stderr, "yas: something the command spawned is still holding its output open (a server?)")
					}
				}
			case "exec_error":
				fmt.Fprintf(os.Stderr, "yas: exec failed in the guest: %s\n", e.Event.Raw)
				exitCode = 1
			}
		}
	}

	err = cl.ExecFollow(context.Background(), id, req, handle)
	// A dropped stream is not a dead command: poll the transcript onward from
	// the cursor until the exit lands. This degradation being possible is the
	// entire reason the stream frames and the poll share one object.
	for err != nil && exitCode == -1 {
		fmt.Fprintln(os.Stderr, "yas: stream dropped; polling from cursor", cursor)
		time.Sleep(time.Second)
		page, perr := cl.Events(context.Background(), id, cursor)
		if perr != nil {
			return perr
		}
		handle(page)
		if page.Terminal {
			break
		}
	}
	if exitCode > 0 {
		return &exitError{code: exitCode}
	}
	if exitCode == -1 {
		return errors.New("the stream ended without an exit event; the transcript has the rest (`yas exec` again or check events)")
	}
	return nil
}
