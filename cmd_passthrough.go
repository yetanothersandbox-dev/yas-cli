package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Gilbert09/yas/clients/yas/internal/cliio"
	"github.com/Gilbert09/yas/clients/yas/internal/sshutil"
)

// cmdPassthrough is `yas <command> [args...]`: run a command interactively
// inside a box. `yas claude --dangerously-skip-permissions` is the canonical
// use — a claude console in a fresh box, keys pre-wired host-side.
//
// Only two flags belong to yas here, scanned off the FRONT of the argv:
// -b/--box <id> targets a box, --new forces a fresh one. The first token that
// is neither starts the remote command, and from there everything is passed
// verbatim — flag.Parse would eat the remote command's own flags.
func cmdPassthrough(args []string) error {
	boxID, forceNew, remote, err := splitPassthrough(args)
	if err != nil {
		return err
	}

	cfg, cl, err := loadClient()
	if err != nil {
		return err
	}
	ctx := context.Background()

	switch {
	case boxID != "":
	case forceNew || !cliio.IsTTY(os.Stdin) || !cliio.IsTTY(os.Stdout):
		// Scripted (or asked): a fresh box, no questions.
		boxID, err = createBox(ctx, cl, cfg, createOpts{Provider: providerForCommand(remote[0])})
		if err != nil {
			return err
		}
	default:
		// Interactive: offer the picker, with "new box" as the first entry.
		boxID, err = pickBox(cl, cfg, fmt.Sprintf("run `%s` in...", remote[0]), providerForCommand(remote[0]))
		if err != nil {
			return err
		}
	}
	return remapExit(sshutil.Connect(ctx, cl, cfg, boxID, remote))
}

// providerForCommand is which provider a box has to be for, to run this command.
//
// `yas <anything>` runs <anything> inside a box, and a box is wired to exactly
// one provider before it boots — so for the two commands that ARE an agent, the
// word the user typed is the only statement of intent available. Without this,
// `yas codex` made an Anthropic box, and codex started in it with no
// OPENAI_BASE_URL and reached for the real internet, which a box has no route
// to. It failed as a network timeout, naming neither the provider nor the
// create that chose it.
//
// Only the two agent CLIs are listed. Everything else — `yas bash`, `yas vim` —
// gets the default, because nothing about those words says which model the box
// should be able to reach.
func providerForCommand(cmd string) string {
	switch cmd {
	case "codex":
		return "openai"
	default:
		return ""
	}
}

// splitPassthrough peels yas's own flags off the FRONT and leaves the remote
// command untouched from its first token on — the property that keeps
// `yas claude --dangerously-skip-permissions` from having its flags eaten.
func splitPassthrough(args []string) (boxID string, forceNew bool, remote []string, err error) {
	i := 0
scan:
	for i < len(args) {
		switch args[i] {
		case "-b", "--box":
			if i+1 >= len(args) {
				return "", false, nil, errors.New(args[i] + " needs a box id")
			}
			boxID = args[i+1]
			i += 2
		case "--new":
			forceNew = true
			i++
		default:
			break scan
		}
	}
	remote = args[i:]
	if len(remote) == 0 {
		return "", false, nil, errors.New("nothing to run")
	}
	return boxID, forceNew, remote, nil
}
