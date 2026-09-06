package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/ui"
)

// cmdDefaults shows or sets the size a box gets when `yas new` names none.
//
// A preference rather than an entitlement: the plan decides how much may be
// held at once, this decides how a box that says nothing is shaped. Somebody
// running one large box wants a different answer from somebody running five
// small ones, and both are inside the same plan.
//
// With no flags it PRINTS rather than changing anything, because a command that
// silently rewrote a setting when run bare would be a trap — and "what am I
// getting?" is the more common question.
func cmdDefaults(args []string) error {
	fs := flag.NewFlagSet("defaults", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	mem := fs.Int("mem", -1, "memory MiB for a box that names no size; 0 restores your plan's default")
	cpus := fs.String("cpus", "", "vCPUs likewise; fractions allowed, e.g. 0.5 — 0 restores your plan's default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	ctx := context.Background()

	if *mem < 0 && *cpus == "" {
		d, err := cl.BoxDefaults(ctx)
		if err != nil {
			return err
		}
		printDefaults(d)
		return nil
	}

	// Read first, so changing one dimension does not clear the other. The API
	// takes the whole pair — it has to, since zero is how a field is cleared —
	// so the unspecified half has to be sent back as it was.
	cur, err := cl.BoxDefaults(ctx)
	if err != nil {
		return err
	}
	wantMem, wantMilli := cur.MemMiB, cur.MilliVcpu
	if *mem >= 0 {
		wantMem = *mem
	}
	if *cpus != "" {
		milli, err := parseVcpus(*cpus)
		if err != nil {
			return err
		}
		wantMilli = milli
	}

	d, err := cl.SetBoxDefaults(ctx, wantMem, wantMilli)
	if err != nil {
		return err
	}
	printDefaults(d)
	return nil
}

// printDefaults says what a box will actually be, and only mentions the
// override when there is one.
//
// The effective number is the answer to the question somebody asked; the stored
// override is bookkeeping, and printing "0" for it on every account that has
// never set one would be noise that reads like a problem.
func printDefaults(d api.BoxDefaults) {
	em := func(s string) string {
		if !ui.StdoutTTY() {
			return s
		}
		return ui.S(ui.Accent).Render(s)
	}
	fmt.Fprintf(os.Stdout, "a box that names no size gets %s and %s\n",
		em(fmt.Sprintf("%d MiB", d.EffectiveMemMiB)), em(vcpuText(d.EffectiveMilliVcpu)))
	switch {
	case d.MemMiB == 0 && d.MilliVcpu == 0:
		fmt.Fprintln(os.Stdout, "that is your plan's own default — `yas defaults -mem N` to change it")
	default:
		fmt.Fprintf(os.Stdout, "set by you; `yas defaults -mem 0 -cpus 0` restores your plan's default\n")
	}
	if d.MaxMemMiB > 0 {
		fmt.Fprintf(os.Stdout, "your pool holds %d MiB and %s in total\n",
			d.MaxMemMiB, vcpuText(d.MaxMilliVcpu))
	}
}

// vcpuText spells a milli-vCPU count the way a person would say it.
func vcpuText(milli int) string {
	if milli%1000 == 0 {
		return fmt.Sprintf("%d vCPU", milli/1000)
	}
	return fmt.Sprintf("%.3g vCPU", float64(milli)/1000)
}
