package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"flag"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
	"github.com/Gilbert09/yas/clients/yas/internal/config"
	"github.com/Gilbert09/yas/clients/yas/internal/names"
	"github.com/Gilbert09/yas/clients/yas/internal/sshutil"
	"github.com/Gilbert09/yas/clients/yas/internal/ui"
)

// cmdNew creates one bare box and connects. A bare create is synchronous —
// the 202 means the guest is up with our keys installed — so there is no
// polling between the create and the ssh.
func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	name := fs.String("name", "", "sandbox id (default: generated)")
	mem := fs.Int("mem", 0, "memory MiB")
	cpus := fs.String("cpus", "", "vCPUs; fractions allowed, e.g. 0.5 or 2")
	disk := fs.Int("disk", 0, "disk MiB")
	lifetime := fs.Int("lifetime", 0, "max lifetime seconds")
	noConnect := fs.Bool("no-connect", false, "create only; do not open a shell")
	preset := fs.String("preset", "", "egress preset: proxy (default: no route; Claude, Codex and GitHub via the fleet proxy), filtered (routed to what -allow names), open (routed anywhere)")
	allow := fs.String("allow", "", "comma-separated names a filtered box may reach. A bare name is exact; write *.github.com for subdomains, or pypi.org:443 for one port")
	deny := fs.String("deny", "", "comma-separated names this box may never resolve. Beats every allow (e.g. gist.github.com,*.gist.github.com)")
	allowNet := fs.String("allow-net", "", "comma-separated CIDRs a filtered box may reach with no DNS involved (e.g. 203.0.113.0/24:443)")
	denyNet := fs.String("deny-net", "", "comma-separated CIDRs this box may never reach. Beats every allow")
	connect := fs.String("connect", "", "comma-separated CONNECT tunnel targets for a proxy-mode box (host or host:port, e.g. ssh.github.com:22)")
	noCreds := fs.Bool("no-creds", false, "attach no credentials to this box, whatever is stored")
	profile := fs.String("profile", "", "create from a saved profile: its posture, size and repo, unless a flag here overrides them")
	provider := fs.String("provider", "", "which LLM this box is for: anthropic (default) or openai. Fixed at create — it decides the box's route table and which agent CLI works in it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	boxName, err := reconcileName(*name, fs.Args())
	if err != nil {
		return err
	}

	milliVcpu, err := parseVcpus(*cpus)
	if err != nil {
		return err
	}

	providerName, err := parseProvider(*provider)
	if err != nil {
		return err
	}

	pol, err := buildPolicy(policyFlags{
		preset: *preset, allow: *allow, deny: *deny,
		allowNets: *allowNet, denyNets: *denyNet,
		connect: *connect, noCreds: *noCreds,
	})
	if err != nil {
		return err
	}
	// A profile carries a posture of its own, so asking for both is a create
	// whose privacy nobody chose. The server would take the caller's explicit
	// policy and drop the profile's silently; refusing here says so instead.
	if *profile != "" && pol != nil {
		return fmt.Errorf("-profile and the posture flags set the same thing; use one")
	}

	cfg, cl, err := loadClient()
	if err != nil {
		return err
	}
	id, err := createBox(context.Background(), cl, cfg, createOpts{
		Name: boxName, MemMiB: *mem, MilliVcpu: milliVcpu, DiskMiB: *disk,
		MaxLifetimeSec: *lifetime, Policy: pol,
		Profile: *profile, Provider: providerName,
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

// reconcileName resolves the box name from the -name flag and the positional
// argument.
//
// The POSITIONAL form is what everybody types, and what the README and the
// website have always shown. Until it was wired up the argument was IGNORED:
// the box got a generated name, so the user got a working box under a name they
// did not choose and nothing said so. A silently-different result is worse than
// an error, which is why disagreement is refused rather than resolved — a
// caller who typed two different names meant one of them, and we cannot tell
// which.
func reconcileName(flagName string, rest []string) (string, error) {
	switch {
	case len(rest) == 0:
		return flagName, nil
	case len(rest) > 1:
		return "", fmt.Errorf("one name at most, got %d arguments: %s", len(rest), strings.Join(rest, " "))
	case flagName == "":
		return rest[0], nil
	case rest[0] == flagName:
		return flagName, nil
	default:
		return "", fmt.Errorf("two different names: %q as an argument and %q as -name; pick one", rest[0], flagName)
	}
}

type createOpts struct {
	Name   string
	MemMiB int
	// MilliVcpu is thousandths of one vCPU (1000 = one vCPU) — the unit the
	// API sells in. parseVcpus turns what a human types ("0.5", "2") into it.
	MilliVcpu      int
	DiskMiB        int
	MaxLifetimeSec int
	Policy         *api.Policy
	// Profile names a saved profile the gateway expands at the edge. Anything
	// set explicitly here still wins over it — that is the server's rule, not
	// this client's, and it is why the two are not merged locally.
	Profile string
	// Provider is the LLM this box is for: "openai", or empty for Anthropic.
	Provider string
}

// parseProvider validates the -provider flag here rather than letting the
// server do it.
//
// The server refuses an unknown provider with a 400 and that is the enforcement
// — but this is the only place that can name the two spellings while the user
// still has the command line in front of them, and a create is slow enough that
// a round trip to learn you typed "gpt" is a bad trade.
//
// The names are the VENDORS. Not "codex" or "claude", which are products, and
// not a model id: which model a box runs is a per-task decision, and the
// provider is a per-box one.
func parseProvider(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "anthropic":
		return "", nil // empty is what an Anthropic box has always sent
	case "openai":
		return "openai", nil
	default:
		return "", fmt.Errorf("unknown provider %q; this fleet routes anthropic and openai", s)
	}
}

// parseVcpus turns the human spelling of a vCPU count — "2", "0.5", "1.25" —
// into milli-vCPU. Decimal string arithmetic, not ParseFloat: the number is an
// entitlement, and 3 decimal places is exactly what the milli unit can carry,
// so a fourth is refused rather than rounded into a figure the server never
// agreed to. Empty means "the server default", like every other size flag.
func parseVcpus(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.Atoi(whole)
	if err != nil || w < 0 {
		return 0, fmt.Errorf("-cpus %q is not a vCPU count; use e.g. 2 or 0.5", s)
	}
	milli := w * 1000
	if frac != "" {
		if len(frac) > 3 {
			return 0, fmt.Errorf("-cpus %q is finer than the API's milli-vCPU unit; use at most 3 decimals", s)
		}
		f, err := strconv.Atoi(frac)
		if err != nil || f < 0 {
			return 0, fmt.Errorf("-cpus %q is not a vCPU count; use e.g. 2 or 0.5", s)
		}
		for i := len(frac); i < 3; i++ {
			f *= 10
		}
		milli += f
	}
	if milli == 0 {
		return 0, fmt.Errorf("-cpus 0 would be a box with no CPU at all; omit the flag for the server default")
	}
	return milli, nil
}

// policyFlags is every posture flag `yas new` carries, gathered so the picker
// and the command build a policy the same way rather than through two argument
// lists that drift.
type policyFlags struct {
	preset    string
	allow     string
	deny      string
	allowNets string
	denyNets  string
	connect   string
	noCreds   bool
}

// splitList reads one comma-separated flag. Empty entries are dropped, so a
// trailing comma is a typo rather than an empty rule the server has to refuse.
func splitList(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// buildPolicy turns the preset and flags into the wire policy. Nil means proxy
// mode with no tweaks — no policy object at all, byte-identical to a pre-policy
// create.
//
// "sealed" is the old name for the proxy preset and stays accepted forever.
// It is in scripts, in shell history and in every doc written before the
// rename; refusing it would break those to no purpose, and it means exactly
// what "proxy" means.
func buildPolicy(f policyFlags) (*api.Policy, error) {
	mode := ""
	switch f.preset {
	case "", "proxy", "sealed":
	case "filtered", "open":
		mode = f.preset
	default:
		return nil, fmt.Errorf("unknown preset %q: proxy, filtered or open", f.preset)
	}
	allowList := splitList(f.allow)
	denyList := splitList(f.deny)
	allowNets := splitList(f.allowNets)
	denyNets := splitList(f.denyNets)
	var connects []api.ConnectEntry
	if f.connect != "" {
		for _, c := range strings.Split(f.connect, ",") {
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
	if mode == "filtered" && len(allowList)+len(allowNets) == 0 {
		return nil, errors.New("-preset filtered needs -allow or -allow-net: an empty filter is no policy at all")
	}
	if mode == "" && len(allowList)+len(allowNets) > 0 {
		return nil, errors.New("-allow only applies to -preset filtered")
	}
	if mode == "" && len(denyList)+len(denyNets) > 0 {
		return nil, errors.New("-deny and -deny-net need a routed preset; a proxy box routes nothing to deny")
	}
	if mode == "" && !f.noCreds && len(connects) == 0 {
		return nil, nil // plain proxy mode: send no policy at all
	}
	p := &api.Policy{Egress: &api.EgressPolicy{
		Mode: mode, Allow: allowList, Deny: denyList,
		AllowNets: allowNets, DenyNets: denyNets, Connect: connects,
	}}
	if f.noCreds {
		// -no-creds ONLY. A routed preset used to renounce credentials here too,
		// mirroring a server-side rule that no longer exists — leaving it would
		// have made `-preset filtered` keep arriving with credentials dropped,
		// with no error anywhere to say why the box could not reach Claude.
		// How far a box may reach and what it may spend are separate questions
		// now, and this flag is the only thing that answers the second one.
		p.Credentials = &api.CredentialPolicy{GitHub: "none", Anthropic: "none", OpenAI: "none"}
	}
	if mode == "" {
		p.Egress.Mode = "" // proxy mode with tweaks: proxy is the default
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
		Profile:        o.Profile,
		Provider:       o.Provider,
		MemMiB:         firstNonZero(o.MemMiB, cfg.Defaults.MemMiB),
		MilliVcpu:      firstNonZero(o.MilliVcpu, cfg.Defaults.MilliVcpu, cfg.Defaults.VcpuCount*1000),
		DiskMiB:        firstNonZero(o.DiskMiB, cfg.Defaults.DiskMiB),
		MaxLifetimeSec: firstNonZero(o.MaxLifetimeSec, cfg.Defaults.MaxLifetimeSec),
		SSHKeys:        keys,
		// Host-side proxy config, never guest-visible. Sending an empty
		// string omits the field.
		AnthropicKey: unlessSuppressed(suppressed, cfg.AnthropicKeyResolved()),
		GitHubToken:  unlessSuppressed(suppressed, cfg.GitHubTokenResolved()),
		OpenAIKey:    unlessSuppressed(suppressed, cfg.OpenAIKeyResolved()),
	}
	// The create is synchronous by contract — it returns when the guest is UP —
	// so the CLI can time it and print the product's own headline number, every
	// time, measured rather than claimed. That is the most persuasive line this
	// tool has and it was being thrown away.
	sp := ui.Start("creating " + id + "…")
	// A create can queue behind capacity, and the client retries a 503 twice
	// before giving up. Silence through that reads as a hang, so the label
	// changes once the wait stops being normal. See createWait.
	w := &createWait{sp: sp, id: id}
	c := *cl
	c.OnRetry = w.capacity
	stop := w.start()
	cerr := c.Create(ctx, req)
	stop()
	if err := cerr; err != nil {
		sp.Stop("")
		switch {
		case api.ErrorKind(err) == "conflict":
			return "", fmt.Errorf("the name %q is unavailable; pick another", id)
		case api.IsQuota(err):
			// SUSPEND FIRST, and rm second. The pool this refuses on is
			// memory, and suspending returns all of it while keeping the disk
			// — so `rm` destroys work to solve a problem suspend solves for
			// nothing. The dashboard has always said it this way round.
			return "", fmt.Errorf("your quota is full: %w — `yas suspend` a box you are not using (it keeps its disk and costs nothing), or `yas rm` one you have finished with", err)
		case api.IsNoCapacity(err):
			return "", fmt.Errorf("the fleet has no capacity right now: %w — try again shortly", err)
		default:
			return "", err
		}
	}
	sp.Stop(ui.OK(fmt.Sprintf("%s — up in %dms", id, sp.Elapsed().Milliseconds())))
	// The id is NOT printed here.
	//
	// createBox is called by `yas new`, by the picker, and by the passthrough,
	// and only one of them wants the id on stdout: `yas new -no-connect`, which
	// prints it itself. Printing it here as well put the id on stdout TWICE for
	// that one caller — so `yas new -no-connect | xargs yas ssh` was handed two
	// names and used the wrong one. The others print it and then take the
	// terminal for ssh, where a stray line is noise rather than data.
	if cfg2, err := config.Load(); err == nil {
		cfg2.LastBox = id
		_ = config.Save(cfg2)
	}
	return id, nil
}

// What the spinner is allowed to say while a create is outstanding.
//
// # It stopped naming a cause it could not know
//
// The old line relabelled once, at eight seconds, to "the fleet is finding
// room. Hold." — a specific CAUSE, asserted by a client that has no way of
// knowing it. A create is slow either because it is queueing behind fleet
// capacity or because this particular one is taking a while, and from out here
// those are the same silence. Guessing is the mistake ui.Refusal exists to
// avoid: inventing detail this CLI does not have.
//
// So there are two sources now, and they are ranked. The clock knows only how
// long it has been, and says only that. Client.OnRetry fires at the one moment
// the cause is genuinely known — the gateway answered 503 no_capacity — and
// what it says outranks the clock for the rest of the wait, because a named
// cause beats a stopwatch.
//
// # It also stopped freezing
//
// The single relabel meant eight seconds and four minutes read identically.
// The rungs below escalate, and the last two say what to do — `yas list` is
// the honest answer because a create is synchronous and a cancelled one may or
// may not have landed. Note what they do NOT say: that ctrl-c is safe, or that
// the box keeps building without you. The gateway builds on the REQUEST's
// context (see createSandbox), so hanging up cancels the create — telling
// somebody otherwise would be a comfortable lie.
var createRungs = []struct {
	after time.Duration
	say   string
}{
	{8 * time.Second, "longer than usual"},
	{30 * time.Second, "still going — `yas list` will say whether it landed"},
	{90 * time.Second, "this is unusual, and it is on us — `yas list` will say whether it landed"},
}

type createWait struct {
	sp *ui.Spinner
	id string

	mu    sync.Mutex
	cause string // set by the gateway; outranks the clock
}

// capacity is Client.OnRetry: the gateway said it has no room right now.
func (w *createWait) capacity(attempt int, wait time.Duration, _ error) {
	w.mu.Lock()
	w.cause = fmt.Sprintf("the fleet is full — trying again in %ds, attempt %d of 3",
		int(wait.Seconds()), attempt)
	w.mu.Unlock()
	w.say()
}

func (w *createWait) say() {
	w.mu.Lock()
	cause := w.cause
	w.mu.Unlock()
	if cause == "" {
		return
	}
	w.sp.Relabel("creating " + w.id + "… " + cause)
}

// start runs the clock, and returns the function that stops it.
//
// The clock is a TERMINAL affordance and runs only on one. In a pipe the
// spinner prints each label as its own line, so a ladder would put three extra
// lines in a CI log to say nothing that the timestamps beside them do not.
// OnRetry is not gated the same way: "the fleet was full" is worth a line in a
// log, because it is the difference between a slow build and a busy fleet.
func (w *createWait) start() func() {
	done := make(chan struct{})
	if !ui.StderrTTY() {
		return func() { close(done) }
	}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		began, rung := time.Now(), 0
		for {
			select {
			case <-done:
				return
			case <-t.C:
				w.mu.Lock()
				named := w.cause != ""
				w.mu.Unlock()
				if named {
					continue // a known cause outranks the clock
				}
				el := time.Since(began)
				for rung < len(createRungs) && el >= createRungs[rung].after {
					w.sp.Relabel("creating " + w.id + "… " + createRungs[rung].say)
					rung++
				}
			}
		}
	}()
	return func() { close(done) }
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
