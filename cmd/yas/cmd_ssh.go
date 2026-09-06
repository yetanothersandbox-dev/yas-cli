package main

import (
	"context"
	"errors"

	"github.com/yetanothersandbox-dev/yas-cli/internal/sshutil"
)

// cmdSSH opens an interactive shell in an existing box.
func cmdSSH(args []string) error {
	if len(args) != 1 || wantsHelp(args[0]) {
		return errors.New("usage: yas ssh <id>")
	}
	cfg, cl, err := loadClient()
	if err != nil {
		return err
	}
	return remapExit(sshutil.Connect(context.Background(), cl, cfg, args[0], nil))
}
