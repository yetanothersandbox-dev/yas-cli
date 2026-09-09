package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/cliio"
	"github.com/yetanothersandbox-dev/yas-cli/internal/sshutil"
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

	// BEFORE a box is created, not after.
	//
	// An agent with no credential fails inside the guest, on its first model
	// call, behind whatever error surface that agent happens to have — by which
	// point a microVM has booted and the person is reading a 401 in somebody
	// else's UI. The account already knows the answer, so ask it here, where
	// the message can name the command that fixes it and nothing has been
	// created yet.
	if err := requireProviderCredential(ctx, cl, remote[0]); err != nil {
		return err
	}

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
	// The session holder and the reconnect are for a HUMAN at a terminal, and
	// only there.
	//
	// A scripted passthrough must stay exactly as it was: remapExit hands the
	// caller the remote command's exit code, and tmux does not propagate one —
	// an attach returns 0 whatever happened inside. Wrapping a script would
	// turn every failure into a success. Reconnecting is wrong there too: with
	// nobody watching, a retry loop is a hang.
	//
	// The cost, stated: an interactive `yas claude` now exits with tmux's
	// status rather than the agent's. For a session somebody is sitting in
	// front of, that is a fair trade for the session surviving.
	interactive := cliio.IsTTY(os.Stdin) && cliio.IsTTY(os.Stdout)
	return remapExit(sshutil.ConnectWith(ctx, cl, cfg, boxID, remote,
		sshutil.Options{Mux: interactive, Reconnect: interactive}))
}

// credentialFor is which stored credential a command cannot run without.
//
// Only the two agents. Everything else `yas` passes through — a shell, an
// editor, htop — needs no model and must not be gated on one; a check that
// refused `yas bash` because nobody had stored an LLM key would be a worse bug
// than the one this exists to fix.
func credentialFor(cmd string) string {
	switch cmd {
	case "claude":
		return "anthropic"
	case "codex":
		return "openai"
	default:
		return ""
	}
}

// requireProviderCredential refuses to build a box for an agent that has no key
// to think with.
//
// # It fails OPEN, on purpose
//
// Any error asking — an operator tenant with no user row (409), an older
// gateway, a network blip — returns nil and lets the create proceed. Blocking
// somebody because a PROBE failed would turn a working setup into a broken one,
// and the failure this replaces is merely ugly, not fatal: the agent still
// reports a 401 from inside, exactly as it does today. The check is here to
// make the common case pleasant, not to be an authority.
func requireProviderCredential(ctx context.Context, cl *api.Client, cmd string) error {
	need := credentialFor(cmd)
	if need == "" {
		return nil
	}
	who, err := cl.Whoami(ctx)
	if err != nil {
		return nil
	}
	held, login, vendor := who.AnthropicKey, "yas login -anthropic", "an Anthropic"
	if need == "openai" {
		held, login, vendor = who.OpenAIKey, "yas login -openai", "an OpenAI"
	}
	if held {
		return nil
	}
	// Named as a credential problem, with the fix, and with the fact that
	// nothing was created — because the next question after "it did not work"
	// is "am I being charged for something".
	msg := fmt.Sprintf("`%s` needs %s credential and your account holds none, so no box was created.\n"+
		"  Run `%s`, then try again.", cmd, vendor, login)
	if need == "openai" {
		msg += "\n  It opens a browser to sign in to ChatGPT; `yas login -openai -key` pastes a platform key instead."
	} else {
		msg += "\n  It opens a browser to sign in to Claude; `yas login -anthropic -key` pastes a console key instead."
	}
	return errors.New(msg)
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
