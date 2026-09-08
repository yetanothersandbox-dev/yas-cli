package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/config"
	"github.com/yetanothersandbox-dev/yas-cli/internal/names"
	"github.com/yetanothersandbox-dev/yas-cli/internal/sshutil"
	"github.com/yetanothersandbox-dev/yas-cli/internal/tui"
)

// errQuit marks "the user chose nothing", which is not a failure.
var errQuit = errors.New("quit")

// cmdPicker is bare `yas` on a terminal: pick a box or make one, then
// connect. The TUI exits before ssh starts — ssh needs the real terminal.
func cmdPicker() error {
	cfg, cl, err := loadClient()
	if err != nil {
		return err
	}
	// Bare `yas` names no agent, so it names no provider either: a box made
	// here is an ordinary Anthropic one, exactly as it was.
	id, err := pickBox(cl, cfg, "your boxes", "")
	if errors.Is(err, errQuit) {
		return nil
	}
	if err != nil {
		return err
	}
	return remapExit(sshutil.ConnectWith(context.Background(), cl, cfg, id, nil,
		sshutil.Options{Mux: true, Reconnect: true}))
}

// pickBox runs the picker (and, on "new box", the create form + the create)
// and returns the id to connect to.
//
// provider is what a box created from here is wired to — the caller's, not the
// user's: `yas codex` needs an OpenAI box and there is no question in this UI
// that asks for one. It has no effect on picking an EXISTING box, which cannot
// be rewired; a box created for the other provider simply will not run this
// agent, and says so when the agent starts.
func pickBox(cl *api.Client, cfg config.Config, title, provider string) (string, error) {
	// Looping, because ESC out of the create form means "not that, then" and
	// not "goodbye". It used to end the program, so changing your mind about a
	// new box threw away the picker you had opened to get there.
	for {
		id, err := pickOnce(cl, cfg, title, provider)
		if errors.Is(err, errBackToPicker) {
			continue
		}
		return id, err
	}
}

// errBackToPicker asks pickBox for another turn round the menu.
var errBackToPicker = errors.New("back to the picker")

func pickOnce(cl *api.Client, cfg config.Config, title, provider string) (string, error) {
	res, err := tui.RunPicker(cl, title)
	if err != nil {
		return "", err
	}
	switch res.Action {
	case "connect":
		return res.ID, nil
	case "form":
		// The create loop: a refused name returns to the form with the
		// refusal shown and everything else kept, rather than ending the
		// program with the user's input on the floor.
		// Asked once, before the form opens, so the size rows can show this
		// account's real numbers instead of the word "default". A failure here
		// is not fatal: the form falls back to an unmarked ladder, which is the
		// old behaviour and still makes a box.
		var defs tui.Defaults
		if d, derr := cl.BoxDefaults(context.Background()); derr == nil {
			defs = tui.Defaults{
				MemMiB: d.EffectiveMemMiB, MilliVcpu: d.EffectiveMilliVcpu,
				MaxMemMiB: d.MaxMemMiB, MaxMilliVcpu: d.MaxMilliVcpu,
			}
		}
		suggestion, note := names.Generate(), ""
		for {
			opts, ok, err := tui.RunCreateForm(suggestion, note, defs)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", errBackToPicker
			}
			pol, perr := buildPolicy(policyFlags{preset: opts.Egress, allow: opts.Allow})
			if perr != nil {
				suggestion = opts.Name
				note = perr.Error()
				continue
			}
			id, err := createBox(context.Background(), cl, cfg, createOpts{
				Name: opts.Name, MemMiB: opts.MemMiB, MilliVcpu: opts.MilliVcpu,
				Policy: pol, Provider: provider,
			})
			if err == nil {
				return id, nil
			}
			if api.ErrorKind(err) != "conflict" {
				return "", err
			}
			suggestion = names.Generate()
			note = fmt.Sprintf("%q is unavailable — try %s?", opts.Name, suggestion)
		}
	default:
		return "", errQuit
	}
}
