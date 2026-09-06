package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/ui"
)

// cmdRegion shows or sets where this account's boxes run.
//
// It matters more than it looks: the control plane sits on the interactive path,
// so a box in the wrong region pays for it on every keystroke rather than once
// at boot — measured at 295ms per round trip across the Atlantic against 46ms
// within one region.
//
// With no argument it PRINTS. A command that silently moved an account when run
// bare would be a trap, and "where are my boxes?" is the more common question.
func cmdRegion(args []string) error {
	fs := flag.NewFlagSet("region", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	detect := fs.Bool("detect", false, "set it from this machine's time zone")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	ctx := context.Background()

	rest := fs.Args()
	if len(rest) > 1 {
		return errors.New("usage: yas region [<id>] [-detect]")
	}
	if *detect && len(rest) == 1 {
		return errors.New("give a region or -detect, not both")
	}

	switch {
	case *detect:
		// The machine's own zone. Same signal the dashboard uses, and for the
		// same reason: it describes where the PERSON is, and needs no
		// geolocation database to mean anything.
		s, err := cl.SetRegion(ctx, "", time.Local.String())
		if err != nil {
			return err
		}
		printRegion(s)
	case len(rest) == 1:
		s, err := cl.SetRegion(ctx, rest[0], "")
		if err != nil {
			return err
		}
		printRegion(s)
	default:
		s, err := cl.Region(ctx)
		if err != nil {
			return err
		}
		printRegion(s)
	}
	return nil
}

func printRegion(s api.RegionSettings) {
	tty := ui.StdoutTTY()
	em := func(text string) string {
		if !tty {
			return text
		}
		return ui.S(ui.Accent).Render(text)
	}
	for _, r := range s.Regions {
		switch {
		case r.ID == s.EffectiveRegion:
			fmt.Fprintf(os.Stdout, "  %s %s — %s\n", ui.Tick, em(r.ID), r.Name)
		case !r.Live:
			fmt.Fprintf(os.Stdout, "    %s — %s (not running yet)\n", r.ID, r.Name)
		default:
			fmt.Fprintf(os.Stdout, "    %s — %s\n", r.ID, r.Name)
		}
	}
	if s.Region == "" {
		fmt.Fprintln(os.Stdout, "picked for you; `yas region <id>` to choose, `-detect` to use this machine's clock")
	}
}
