package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
	"github.com/Gilbert09/yas/clients/yas/internal/config"
	"github.com/Gilbert09/yas/clients/yas/internal/names"
	"github.com/Gilbert09/yas/clients/yas/internal/sshutil"
	"github.com/Gilbert09/yas/clients/yas/internal/tui"
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
	id, err := pickBox(cl, cfg, "your boxes")
	if errors.Is(err, errQuit) {
		return nil
	}
	if err != nil {
		return err
	}
	return remapExit(sshutil.Connect(context.Background(), cl, cfg, id, nil))
}

// pickBox runs the picker (and, on "new box", the create form + the create)
// and returns the id to connect to.
func pickBox(cl *api.Client, cfg config.Config, title string) (string, error) {
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
		suggestion, note := names.Generate(), ""
		for {
			opts, ok, err := tui.RunCreateForm(suggestion, note)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", errQuit
			}
			pol, perr := buildPolicy(opts.Egress, opts.Allow, "", false)
			if perr != nil {
				suggestion = opts.Name
				note = perr.Error()
				continue
			}
			milliVcpu, perr := parseVcpus(opts.Vcpus)
			if perr != nil {
				suggestion = opts.Name
				note = perr.Error()
				continue
			}
			id, err := createBox(context.Background(), cl, cfg, createOpts{
				Name: opts.Name, MemMiB: opts.MemMiB, MilliVcpu: milliVcpu,
				DiskMiB: opts.DiskMiB, IdleTtlSec: opts.IdleTtlSec, Policy: pol,
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
