package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

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
	preset := fs.String("preset", "", "privacy preset: sealed (default: proxy egress + credentials), filtered (routed to -allow names, NO credentials), open (routed anywhere, NO credentials)")
	allow := fs.String("allow", "", "comma-separated DNS suffixes a filtered box may reach (e.g. github.com,pypi.org)")
	connect := fs.String("connect", "", "comma-separated CONNECT tunnel targets for a sealed box (host or host:port, e.g. ssh.github.com:22)")
	noCreds := fs.Bool("no-creds", false, "attach no credentials to this box, whatever is stored")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pol, err := buildPolicy(*preset, *allow, *connect, *noCreds)
	if err != nil {
		return err
	}

	cfg, cl, err := loadClient()
	if err != nil {
		return err
	}
	id, err := createBox(context.Background(), cl, cfg, createOpts{
		Name: *name, MemMiB: *mem, Vcpus: *cpus, DiskMiB: *disk,
		IdleTtlSec: *ttl, MaxLifetimeSec: *lifetime, Policy: pol,
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
	Policy         *api.Policy
}

// buildPolicy turns the preset and flags into the wire policy. Nil means
// sealed — no policy object at all, byte-identical to a pre-policy create.
func buildPolicy(preset, allow, connect string, noCreds bool) (*api.Policy, error) {
	mode := ""
	switch preset {
	case "", "sealed":
	case "filtered", "open":
		mode = preset
	default:
		return nil, fmt.Errorf("unknown preset %q: sealed, filtered or open", preset)
	}
	var allowList []string
	if allow != "" {
		for _, a := range strings.Split(allow, ",") {
			if a = strings.TrimSpace(a); a != "" {
				allowList = append(allowList, a)
			}
		}
	}
	var connects []api.ConnectEntry
	if connect != "" {
		for _, c := range strings.Split(connect, ",") {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			host, portStr, found := strings.Cut(c, ":")
			entry := api.ConnectEntry{Host: host}
			if found {
				port, err := strconv.Atoi(portStr)
				if err != nil {
					return nil, fmt.Errorf("-connect %q: the part after : must be a port", c)
				}
				entry.Ports = []int{port}
			}
			connects = append(connects, entry)
		}
	}
	if mode == "filtered" && len(allowList) == 0 {
		return nil, errors.New("-preset filtered needs -allow: an empty filter is no policy at all")
	}
	if mode == "" && len(allowList) > 0 {
		return nil, errors.New("-allow only applies to -preset filtered")
	}
	if mode == "" && !noCreds && len(connects) == 0 {
		return nil, nil // plain sealed: send no policy at all
	}
	p := &api.Policy{Egress: &api.EgressPolicy{Mode: mode, Allow: allowList, Connect: connects}}
	if mode != "" || noCreds {
		// Routed presets renounce credentials (the server enforces it; saying
		// it here keeps the request honest), and -no-creds says so in sealed
		// mode too.
		p.Credentials = &api.CredentialPolicy{GitHub: "none", Anthropic: "none", OpenAI: "none"}
	}
	if mode == "" {
		p.Egress.Mode = "" // sealed with tweaks: proxy mode is the default
	}
	return p, nil
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
	suppressed := o.Policy != nil && o.Policy.Credentials != nil
	req := api.CreateRequest{
		ID:             id,
		Policy:         o.Policy,
		MemMiB:         firstNonZero(o.MemMiB, cfg.Defaults.MemMiB),
		VcpuCount:      firstNonZero(o.Vcpus, cfg.Defaults.VcpuCount),
		DiskMiB:        firstNonZero(o.DiskMiB, cfg.Defaults.DiskMiB),
		IdleTtlSec:     firstNonZero(o.IdleTtlSec, cfg.Defaults.IdleTtlSec),
		MaxLifetimeSec: firstNonZero(o.MaxLifetimeSec, cfg.Defaults.MaxLifetimeSec),
		SSHKeys:        keys,
		// Host-side proxy config, never guest-visible. Sending an empty
		// string omits the field.
		AnthropicKey: unlessSuppressed(suppressed, cfg.AnthropicKeyResolved()),
		GitHubToken:  unlessSuppressed(suppressed, cfg.GitHubTokenResolved()),
		OpenAIKey:    unlessSuppressed(suppressed, cfg.OpenAIKeyResolved()),
	}
	fmt.Fprintf(os.Stderr, "creating %s...\n", id)
	if err := cl.Create(ctx, req); err != nil {
		switch {
		case api.ErrorKind(err) == "conflict":
			return "", fmt.Errorf("the name %q is unavailable; pick another", id)
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

func unlessSuppressed(suppressed bool, v string) string {
	if suppressed {
		return ""
	}
	return v
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}
