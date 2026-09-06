package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/Gilbert09/yas/clients/yas/internal/cliio"
)

// remapExit converts ssh's exit into ours: a remote command's exit code is
// data to propagate, not an error to print.
func remapExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &exitError{code: ee.ExitCode()}
	}
	return err
}

// cmdRemove deletes a box. Confirms on a terminal — the box holds somebody's
// uncommitted work — and doesn't when scripted, because -f or a pipe is the
// script saying it means it.
func cmdRemove(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ExitOnError)
	force := fs.Bool("f", false, "no confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: yas rm [-f] <id>")
	}
	id := fs.Arg(0)
	if !*force && cliio.IsTTY(os.Stdin) {
		fmt.Fprintf(os.Stderr, "delete %s and everything in it? [y/N] ", id)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "y" && answer != "yes" {
			return errors.New("not deleted")
		}
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	// The 204 means the memory is back, so a follow-up create has room.
	return cl.Delete(context.Background(), id)
}

func cmdSuspend(args []string) error {
	if len(args) != 1 || wantsHelp(args[0]) {
		return errors.New("usage: yas suspend <id>")
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	return cl.Suspend(context.Background(), args[0])
}

func cmdResume(args []string) error {
	if len(args) != 1 || wantsHelp(args[0]) {
		return errors.New("usage: yas resume <id>")
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	return cl.Resume(context.Background(), args[0])
}
