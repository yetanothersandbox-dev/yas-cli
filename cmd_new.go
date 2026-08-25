package main

import (
	"context"
	"fmt"
	"os"

	"flag"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
	"github.com/Gilbert09/yas/clients/yas/internal/config"
	"github.com/Gilbert09/yas/clients/yas/internal/names"
	"github.com/Gilbert09/yas/clients/yas/internal/sshutil"
)

// cmdNew creates one bare box and connects. A bare create is synchronous —
// the 202 means the guest is up with our keys installed — so there is no
// polling between the create and the ssh.
func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	name := fs.String("name", "", "sandbox id (default: generated)")
	mem := fs.Int("mem", 0, "memory MiB")
	cpus := fs.Int("cpus", 0, "vCPUs")
	disk := fs.Int("disk", 0, "disk MiB")
	ttl := fs.Int("ttl", 0, "idle TTL seconds before the box is reaped")
	lifetime := fs.Int("lifetime", 0, "max lifetime seconds")
	noConnect := fs.Bool("no-connect", false, "create only; do not open a shell")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, cl, err := loadClient()
	if err != nil {
		return err
	}
	id, err := createBox(context.Background(), cl, cfg, createOpts{
		Name: *name, MemMiB: *mem, Vcpus: *cpus, DiskMiB: *disk,
		IdleTtlSec: *ttl, MaxLifetimeSec: *lifetime,
	})
	if err != nil {
		return err
	}
	if *noConnect {
		fmt.Println(id)
		return nil
	}
	return sshutil.Connect(context.Background(), cl, cfg, id, nil)
}

type createOpts struct {
	Name           string
	MemMiB         int
	Vcpus          int
	DiskMiB        int
	IdleTtlSec     int
	MaxLifetimeSec int
}

// createBox merges flags over config defaults, generates an id when none was
// chosen, and prints the id BEFORE connecting — a session killed a second
// later still names the box it made.
func createBox(ctx context.Context, cl *api.Client, cfg config.Config, o createOpts) (string, error) {
	id := o.Name
	if id == "" {
		id = names.Generate()
	}
	identity, err := sshutil.EnsureIdentity(cfg)
	if err != nil {
		return "", err
	}
	keys, err := sshutil.PublicKeys(identity)
	if err != nil {
		return "", err
	}
	req := api.CreateRequest{
		ID:             id,
		MemMiB:         firstNonZero(o.MemMiB, cfg.Defaults.MemMiB),
		VcpuCount:      firstNonZero(o.Vcpus, cfg.Defaults.VcpuCount),
		DiskMiB:        firstNonZero(o.DiskMiB, cfg.Defaults.DiskMiB),
		IdleTtlSec:     firstNonZero(o.IdleTtlSec, cfg.Defaults.IdleTtlSec),
		MaxLifetimeSec: firstNonZero(o.MaxLifetimeSec, cfg.Defaults.MaxLifetimeSec),
		SSHKeys:        keys,
		// Host-side proxy config, never guest-visible. Sending an empty
		// string omits the field.
		AnthropicKey: cfg.AnthropicKeyResolved(),
		GitHubToken:  cfg.GitHubTokenResolved(),
		OpenAIKey:    cfg.OpenAIKeyResolved(),
	}
	fmt.Fprintf(os.Stderr, "creating %s...\n", id)
	if err := cl.Create(ctx, req); err != nil {
		switch {
		case api.ErrorKind(err) == "conflict":
			return "", fmt.Errorf("the name %q is already taken — box names are global, even across accounts; pick another", id)
		case api.IsQuota(err):
			return "", fmt.Errorf("your quota is full: %w — `yas list` and `yas rm` something", err)
		case api.IsNoCapacity(err):
			return "", fmt.Errorf("the fleet has no capacity right now: %w — try again shortly", err)
		default:
			return "", err
		}
	}
	fmt.Println(id)
	if cfg2, err := config.Load(); err == nil {
		cfg2.LastBox = id
		_ = config.Save(cfg2)
	}
	return id, nil
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
