package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

// cmdMcp manages the tool servers a box's agents can call.
//
// The whole idea in one sentence, and it is the same one integrations have: a
// box can USE a credential and can never read it. What differs is what the box
// does with the upstream — an integration is an API the agent may curl, an MCP
// server is a tool server its harness loads at start-up, so `claude`, `codex`
// and a dispatched task all wake up with the tools already wired in.
func cmdMcp(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	ctx := context.Background()
	switch args[0] {
	case "list", "ls":
		return mcpList(ctx)
	case "add", "put", "edit":
		return mcpAdd(ctx, args[1:])
	case "rm", "remove", "delete":
		return mcpRemove(ctx, args[1:])
	case "test":
		return mcpTest(ctx, args[1:])
	case "attach":
		return mcpAttach(ctx, args[1:], true)
	case "detach":
		return mcpAttach(ctx, args[1:], false)
	default:
		return fmt.Errorf("unknown mcp command %q: list, add, rm, test, attach, detach", args[0])
	}
}

func mcpList(ctx context.Context) error {
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	rows, err := cl.McpServers(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no MCP servers. `yas mcp add <name> --url <endpoint> --secret -` adds one.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tENDPOINT\tCREDENTIAL\tATTACHED\tEXPIRES")
	for _, m := range rows {
		cred := "none"
		if m.HasSecret {
			cred = "stored"
		}
		attached := strings.Join(m.Attach, ",")
		if attached == "" {
			attached = "-"
		}
		expires := "-"
		if m.ExpiresAt != nil {
			expires = m.ExpiresAt.Format("2006-01-02 15:04")
			if m.Lapsed {
				expires += " (lapsed)"
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.ID, m.URL, cred, attached, expires)
	}
	return w.Flush()
}

func mcpAdd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		endpoint = fs.String("url", "", "the whole endpoint, path included (e.g. https://mcp.linear.app/mcp)")
		desc     = fs.String("description", "", "what this is for; the agent sees it")
		secret   = fs.String("secret", "", "the credential; `-` reads it from stdin")
		attach   = fs.String("attach", "", "where it applies: all, box:<name>, profile:<id>; comma-separated")
		forDur   = fs.Duration("for", 0, "time-box every attachment; access lapses with nothing to revoke")
	)
	// The name is taken off the front when it is there, for the reason
	// `integrations add` does it: Go's flag package stops at the first bare
	// word, so `add linear -url ...` would silently ignore every flag.
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return errors.New("usage: yas mcp add <name> --url <endpoint> [flags]\n" + flagsOf(fs))
	}
	if name == "" {
		rest := fs.Args()
		if len(rest) != 1 {
			return errors.New("usage: yas mcp add <name> --url <endpoint> [flags]\n" + flagsOf(fs))
		}
		name = rest[0]
	} else if len(fs.Args()) > 0 {
		return fmt.Errorf("unexpected argument %q; one server at a time", fs.Args()[0])
	}

	req := api.McpServerRequest{Description: *desc, URL: *endpoint}
	if *attach != "" {
		req.Attach = splitComma(*attach)
	}
	if *forDur > 0 {
		req.ExpiresInSec = int(forDur.Seconds())
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
	m, err := cl.PutMcpServer(ctx, name, req)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "mcp server %s -> %s\n", m.ID, m.URL)
	if len(m.Attach) == 0 {
		fmt.Fprintf(os.Stderr, "not attached to anything yet: `yas mcp attach %s all`\n", m.ID)
		return nil
	}
	fmt.Fprintf(os.Stderr, "its tools reach an attached box as mcp_%s_<tool>, in claude, codex and tasks alike\n", m.ID)
	// The thing people get wrong, said where they will act on it.
	fmt.Fprintln(os.Stderr, "boxes already running will not have it: a box's tool servers are fixed when it is created")
	return nil
}

func mcpRemove(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: yas mcp rm <name>")
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	if err := cl.DeleteMcpServer(ctx, args[0]); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "gone. Boxes already running keep the tools they were created with — "+
		"a capability that changed under a live guest would not be one.")
	return nil
}

func mcpAttach(ctx context.Context, args []string, add bool) error {
	verb := "attach"
	if !add {
		verb = "detach"
	}
	if len(args) != 2 {
		return fmt.Errorf("usage: yas mcp %s <name> <all|box:NAME|profile:ID>", verb)
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	m, err := cl.AttachMcpTo(ctx, args[0], strings.ToLower(args[1]), add)
	if err != nil {
		return err
	}
	where := strings.Join(m.Attach, ", ")
	if where == "" {
		where = "nothing"
	}
	fmt.Fprintf(os.Stderr, "%s is now attached to: %s\n", m.ID, where)
	return nil
}

// mcpTest asks the server to introduce itself.
//
// The control plane makes the call, because it is the only party holding the
// sealed value and the question gets asked before any box exists to ask from.
func mcpTest(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: yas mcp test <name>")
	}
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	res, err := cl.TestMcpServer(ctx, args[0])
	if err != nil {
		return err
	}
	switch {
	case res.Ok:
		// The server's own name, which is what makes this probe worth trusting:
		// a pass means a real session opened, not that some endpoint answered.
		line := fmt.Sprintf("%s answered as %q", args[0], res.ServerName)
		if res.ProtocolVersion != "" {
			line += fmt.Sprintf(" and speaks MCP %s", res.ProtocolVersion)
		}
		fmt.Fprintln(os.Stderr, line+" — the credential works")
	case res.Status == 0:
		fmt.Fprintf(os.Stderr, "%s could not be reached: %s\n", args[0], res.Detail)
	default:
		detail := res.Detail
		if detail == "" {
			detail = "no reason given"
		}
		fmt.Fprintf(os.Stderr, "%s answered %d — %s\n", args[0], res.Status, detail)
	}
	return nil
}

// parseInlineMcp turns repeated `-mcp name=url` flags and their secrets into
// the create body's `mcpServers`.
//
// The secret is looked up in the environment rather than taken on the command
// line, for the reason readSecret takes `-`: a credential in argv is a
// credential in the shell history and in the process table. `YAS_MCP_SECRET_<NAME>`
// with the name upper-cased and hyphens as underscores.
func parseInlineMcp(pairs []string, lookup func(string) string) ([]api.McpServerInline, error) {
	out := make([]api.McpServerInline, 0, len(pairs))
	seen := map[string]struct{}{}
	for _, p := range pairs {
		name, endpoint, ok := strings.Cut(p, "=")
		name = strings.TrimSpace(name)
		endpoint = strings.TrimSpace(endpoint)
		if !ok || name == "" || endpoint == "" {
			return nil, fmt.Errorf("-mcp %q is not name=url", p)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("-mcp names %q twice", name)
		}
		seen[name] = struct{}{}
		entry := api.McpServerInline{Name: name, URL: endpoint}
		if lookup != nil {
			entry.Secret = lookup(inlineMcpSecretVar(name))
		}
		out = append(out, entry)
	}
	return out, nil
}

// inlineMcpSecretVar is where an inline server's credential is read from.
func inlineMcpSecretVar(name string) string {
	return "YAS_MCP_SECRET_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}
