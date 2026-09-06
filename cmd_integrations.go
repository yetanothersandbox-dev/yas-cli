package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
	"github.com/Gilbert09/yas/clients/yas/internal/cliio"
)

// cmdIntegrations manages named upstreams whose credentials live server-side.
//
// The whole idea in one sentence: a box can USE a credential and can never read
// it. The secret is stored on the account, injected into outbound requests by
// the host that runs the box, and reachable from inside as an ordinary hostname.
// Nothing about it enters the guest, so a prompt-injected agent has nothing to
// exfiltrate.
func cmdIntegrations(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	ctx := context.Background()
	switch args[0] {
	case "list", "ls":
		return integrationsList(ctx)
	case "catalog":
		return integrationsCatalog(ctx, args[1:])
	case "add", "put", "edit":
		return integrationsAdd(ctx, args[1:])
	case "rm", "remove", "delete":
		return integrationsRemove(ctx, args[1:])
	case "test":
		return integrationsTest(ctx, args[1:])
	case "attach":
		return integrationsAttach(ctx, args[1:], true)
	case "detach":
		return integrationsAttach(ctx, args[1:], false)
	default:
		return fmt.Errorf("unknown integrations command %q: list, catalog, add, rm, test, attach, detach", args[0])
	}
}

func integrationsList(ctx context.Context) error {
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	rows, err := cl.Integrations(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no integrations. `yas integrations catalog` lists what you can add.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tUPSTREAM\tCREDENTIAL\tATTACHED\tEXPIRES")
	for _, in := range rows {
		cred := "none"
		if in.HasSecret {
			cred = "stored"
		}
		attached := strings.Join(in.Attach, ",")
		if attached == "" {
			attached = "-"
		}
		expires := "-"
		if in.ExpiresAt != nil {
			expires = in.ExpiresAt.Format("2006-01-02 15:04")
			if in.Lapsed {
				expires += " (lapsed)"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", in.ID, in.Upstream(), cred, attached, expires)
	}
	return w.Flush()
}

func integrationsCatalog(ctx context.Context, args []string) error {
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	entries, err := cl.IntegrationCatalog(ctx)
	if err != nil {
		return err
	}
	// A named service prints its notes in full. They are the part worth reading
	// before you paste a credential — a base URL anybody could have guessed.
	if len(args) == 1 {
		for _, e := range entries {
			if e.Handle != strings.ToLower(args[0]) {
				continue
			}
			fmt.Printf("%s — %s\n\n%s\n\n", e.Handle, e.Title, e.Summary)
			fmt.Printf("  upstream:    %s\n", e.Upstream)
			fmt.Printf("  credential:  %s\n", e.CredentialLabel)
			if e.OverridableUpstream {
				fmt.Printf("  upstream may be overridden (region, or a self-hosted install)\n")
			}
			if e.Notes != "" {
				fmt.Printf("\n%s\n", wrap(e.Notes, 76, "  "))
			}
			fmt.Printf("\nAdd it with:\n  yas integrations add %s --service %s --secret -\n", e.Handle, e.Handle)
			return nil
		}
		// An LLM vendor is not an integration: the key the agent runs on is set
		// once for the account. Said here as well as at the server, so the
		// answer arrives without a round trip and names the door that works.
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "anthropic", "claude":
			return errors.New("anthropic is not an integration: the key your agent runs on is set once " +
				"for the account, with `yas login -anthropic`. A box reaches Anthropic on its own route " +
				"with no attachment needed")
		case "openai", "codex", "chatgpt":
			return errors.New("openai is not an integration: the key your agent runs on is set once " +
				"for the account, with `yas login -openai`. A box created with `-provider openai` " +
				"reaches OpenAI on its own route with no attachment needed")
		}
		return fmt.Errorf("no catalogue service named %q; `yas integrations catalog` lists them", args[0])
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "HANDLE\tSERVICE\tWHAT IT IS")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\n", e.Handle, e.Title, e.Summary)
	}
	_ = w.Flush()
	fmt.Fprintln(os.Stderr, "\n`yas integrations catalog <handle>` for the caveats that come with one.")
	return nil
}

func integrationsAdd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("integrations add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		service  = fs.String("service", "", "catalogue handle; supplies the upstream and the injection recipe")
		upstream = fs.String("upstream", "", "scheme and host, no path (e.g. https://api.example.com)")
		desc     = fs.String("description", "", "what this is for; the agent sees it")
		secret   = fs.String("secret", "", "the credential; `-` reads it from stdin")
		bearer   = fs.Bool("bearer", false, "send the credential as Authorization: Bearer")
		header   = fs.String("header", "", "send it in this header instead (e.g. X-Api-Key)")
		hprefix  = fs.String("header-prefix", "", "a value prefix for -header (e.g. 'Token ')")
		param    = fs.String("query-param", "", "send it as this query parameter instead")
		attach   = fs.String("attach", "", "where it applies: all, box:<name>, profile:<id>; comma-separated")
		forDur   = fs.Duration("for", 0, "time-box every attachment; access lapses with nothing to revoke")
		strip    = fs.String("strip-prefix", "", "path prefix removed before forwarding")
		methods  = fs.String("methods", "", "HTTP methods permitted; comma-separated, empty is all")
		pathPfx  = fs.String("path-prefix", "", "confine it to upstream paths under this one")
	)
	// Go's flag package stops parsing at the first bare word, so
	// `add stripe -service stripe` would read the flags as positional arguments
	// and silently ignore them. `yas new` lives with that and documents "flags
	// come first"; nobody reads it, and the failure is a box with none of the
	// posture that was asked for.
	//
	// So the name is taken off the front when it is there, and the rest is
	// parsed. Both orders work, and neither silently drops half the command.
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return errors.New("usage: yas integrations add <name> [flags]\n" + flagsOf(fs))
	}
	if name == "" {
		rest := fs.Args()
		if len(rest) != 1 {
			return errors.New("usage: yas integrations add <name> [flags]\n" + flagsOf(fs))
		}
		name = rest[0]
	} else if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected argument %q; one integration at a time", fs.Args()[0])
	}

	req := api.IntegrationRequest{
		Description: *desc,
		Service:     *service,
		Upstream:    *upstream,
		StripPrefix: *strip,
		PathPrefix:  *pathPfx,
	}
	if *attach != "" {
		req.Attach = splitComma(*attach)
	}
	if *methods != "" {
		req.Methods = splitComma(*methods)
	}
	if *forDur > 0 {
		req.ExpiresInSec = int(forDur.Seconds())
	}
	switch {
	case *bearer:
		req.Inject = map[string]string{"kind": "bearer"}
	case *header != "":
		req.Inject = map[string]string{"kind": "header", "header": *header, "prefix": *hprefix}
	case *param != "":
		req.Inject = map[string]string{"kind": "query", "param": *param}
	}

	if *secret != "" {
		v, err := readSecret(*secret)
		if err != nil {
			return err
		}
		req.Secret = &v
	}

	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	in, err := cl.PutIntegration(ctx, name, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "integration %s -> %s\n", in.ID, in.Upstream())
	if len(in.Attach) == 0 {
		fmt.Fprintf(os.Stderr, "not attached to anything yet: `yas integrations attach %s all`\n", in.ID)
	} else {
		fmt.Fprintf(os.Stderr, "reachable from an attached box at http://%s.<integration domain>/\n", in.ID)
	}
	return nil
}

// readSecret reads a credential, taking `-` to mean stdin.
//
// Stdin matters more than it looks: it is what keeps a live credential out of
// the shell history and out of the process table, and what makes
// `op read op://vault/stripe/key | yas integrations add ...` work.
func readSecret(v string) (string, error) {
	if v != "-" {
		return v, nil
	}
	if cliio.IsTTY(os.Stdin) {
		fmt.Fprint(os.Stderr, "credential: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Fprintln(os.Stderr)
		return strings.TrimRight(line, "\r\n"), err
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	// Trailing newlines only: a PEM key's INTERNAL newlines are part of it, and
	// stripping those would produce a credential that looks right and is not.
	return strings.TrimRight(string(b), "\r\n"), nil
}

func integrationsRemove(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: yas integrations rm <name>")
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	if err := cl.DeleteIntegration(ctx, args[0]); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "gone. Boxes already running keep what they were created with — "+
		"a capability that changed under a live guest would not be one.")
	return nil
}

func integrationsAttach(ctx context.Context, args []string, add bool) error {
	verb := "attach"
	if !add {
		verb = "detach"
	}
	if len(args) != 2 {
		return fmt.Errorf("usage: yas integrations %s <name> <all|box:NAME|profile:ID>", verb)
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	in, err := cl.AttachTo(ctx, args[0], strings.ToLower(args[1]), add)
	if err != nil {
		return err
	}
	where := strings.Join(in.Attach, ", ")
	if where == "" {
		where = "nothing"
	}
	fmt.Fprintf(os.Stderr, "%s is now attached to: %s\n", in.ID, where)
	return nil
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// flagsOf renders a flag set's usage into a string, so a refusal can carry it.
func flagsOf(fs *flag.FlagSet) string {
	var b strings.Builder
	fs.VisitAll(func(f *flag.Flag) {
		fmt.Fprintf(&b, "  -%s\t%s\n", f.Name, f.Usage)
	})
	return b.String()
}

// wrap reflows text to width, indenting each line.
func wrap(s string, width int, indent string) string {
	var b strings.Builder
	line := indent
	for _, word := range strings.Fields(s) {
		if len(line)+len(word)+1 > width && len(line) > len(indent) {
			b.WriteString(line + "\n")
			line = indent
		}
		if len(line) > len(indent) {
			line += " "
		}
		line += word
	}
	b.WriteString(line)
	return b.String()
}

// integrationsTest asks the control plane whether a stored credential works.
//
// The gateway makes the call, because it is the only party holding the sealed
// value and the question gets asked before any box exists to ask from.
func integrationsTest(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: yas integrations test <name>")
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	res, err := cl.TestIntegration(ctx, args[0])
	if err != nil {
		return err
	}
	switch {
	case !res.Tested:
		fmt.Fprintf(os.Stderr, "no probe for %s: %s\n", args[0], res.Detail)
	case res.Ok:
		fmt.Fprintf(os.Stderr, "%s answered %d — the credential works\n", args[0], res.Status)
	case res.Status == 0:
		fmt.Fprintf(os.Stderr, "%s could not be reached: %s\n", args[0], res.Detail)
	default:
		fmt.Fprintf(os.Stderr, "%s answered %d — the credential was refused\n", args[0], res.Status)
	}
	// Printed on success as well as failure, and that is the point: it is
	// exactly the passing result that must not be trusted.
	if res.Inconclusive != "" {
		fmt.Fprintf(os.Stderr, "\nthis probe cannot tell a valid credential from a wrong one:\n%s\n",
			wrap(res.Inconclusive, 76, "  "))
	}
	return nil
}
