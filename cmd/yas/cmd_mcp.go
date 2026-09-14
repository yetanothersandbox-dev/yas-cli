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
	"time"

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
	case "connect":
		return mcpConnect(ctx, args[1:])
	case "disconnect":
		return mcpDisconnect(ctx, args[1:])
	default:
		return fmt.Errorf("unknown mcp command %q: list, add, rm, test, attach, detach, connect, disconnect", args[0])
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
	var stale []string
	for _, m := range rows {
		if m.OAuth != nil && m.OAuth.Status == api.McpOAuthNeedsReauth {
			stale = append(stale, m.ID)
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", m.ID, m.URL, credentialCell(m), attached, expires)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// After the flush, so the table on stdout is whole before anything is said
	// about it — the two streams interleave on a terminal and only one of them
	// is the payload.
	if len(stale) > 0 {
		fmt.Fprintf(os.Stderr, "%s needs signing in again; a new box gets no tools from a row in that state: "+
			"`yas mcp connect %s`\n", strings.Join(stale, ", "), stale[0])
	}
	return nil
}

// credentialCell is the CREDENTIAL column, in one word.
//
// One word inside a column the table already has, and not a sixth column: the
// table is five wide and a sixth wraps on an ordinary terminal. So the question
// it answers has to be the useful one — can a box use this server right now,
// and if not, what fixes it:
//
//	connected  a grant the gateway holds and renews on its own
//	reconnect  a grant that died where a refresh cannot reach; `yas mcp connect`
//	stored     a key somebody pasted, which nothing here can renew
//	none       nothing stored — a public server, or one mid sign-in
//
// The OAuth status WINS over HasSecret when both are readable, because they
// disagree in exactly one direction and the status is the one that says why. A
// needs_reauth row reports hasSecret:false, and "none" would send somebody to
// paste a key at a server that wants a sign-in.
//
// Pure, and takes the whole row rather than its pieces, so a table test can put
// every combination through it — including the awkward one, a connected grant
// on a row whose ATTACHMENT has lapsed. Those are two different clocks (see
// api.McpOAuth): the lapse belongs to the EXPIRES column and never to this one.
func credentialCell(m api.McpServer) string {
	if m.OAuth != nil {
		switch m.OAuth.Status {
		case api.McpOAuthConnected:
			return "connected"
		case api.McpOAuthNeedsReauth:
			return "reconnect"
		}
	}
	if m.HasSecret {
		return "stored"
	}
	return "none"
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

// Signing in to a tool server, from here, WITHOUT the authorization code ever
// touching this machine.
//
// # This command opens a flow and watches it. It does not run it.
//
// `yas login -anthropic` beside it runs the whole OAuth dance locally: it binds
// a loopback port, receives the authorization code and exchanges it. None of
// that is possible or wanted here, for three separate reasons.
//
//   - A loopback redirect cannot be registered. The gateway registers itself
//     with the vendor dynamically (RFC 7591) and that registration names exactly
//     one redirect_uris entry. This CLI's port is not fixed and is not known at
//     registration time, so there is no URI to register.
//   - The gateway has to hold the grant anyway. The refresh runs server-side —
//     it is what keeps a 03:00 schedule working — and the guest in a box never
//     sees the credential at all. A CLI-side exchange would create a SECOND path
//     by which a credential enters custody, and the sealing path would stop
//     being the one path.
//   - So no authorization code ever reaches this machine. Worth saying out loud,
//     because it is the property that makes `yas mcp connect` safe to run on a
//     shared or borrowed machine in a way `yas login -anthropic` is not. Nothing
//     here holds a verifier, a code or a token; the browser goes to the vendor
//     and comes back to the gateway, and this process only ever asks "is it
//     done yet?".
//
// # Why there is no local listener at all
//
// An earlier sketch had one: a spectator on a loopback port that learned the
// flow had finished and nothing more. It is not here, because the gateway
// cannot reach it. The `next` it takes is run through the same safeNextPath the
// browser login uses, which refuses a scheme and a host outright — an open
// redirect immediately after an authorization is the most valuable one there
// is, so an absolute loopback URL is answered with /app and the listener would
// never be knocked on. So this sends no `next`, which gets the gateway's own
// one-sentence page telling the human to come back to their terminal, and the
// poll below is the only thing that learns the answer.

// mcpConnectTimeout bounds the wait. Long enough for a password manager and a
// second factor, short enough that a tab somebody closed does not hold a
// terminal. The flow at the gateway outlives it, so a slow sign-in that lands
// after this is still a connected server — it is only this command that gave up.
const mcpConnectTimeout = 3 * time.Minute

// mcpPollInterval is how often the gateway is asked. A variable so a test does
// not sit out a real interval.
var mcpPollInterval = 2 * time.Second

func mcpConnect(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: yas mcp connect <name>")
	}
	name := args[0]
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	// The row FIRST. Both of the answers somebody gets wrong here — a name that
	// does not exist, and a server whose credential was pasted rather than
	// granted — are known from one GET, and answering them here beats opening a
	// browser onto a page whose entire content is that same refusal.
	m, err := cl.McpServer(ctx, name)
	if err != nil {
		return err
	}
	if m.OAuth == nil && m.HasSecret {
		return fmt.Errorf("%s holds a key you pasted rather than a sign-in, so there is nothing to connect. "+
			"Replace the key with `yas mcp add %s --secret -`", name, name)
	}

	start, err := cl.ConnectMcpServer(ctx, name, "")
	if err != nil {
		return err
	}
	where := start.Issuer
	if where == "" {
		where = m.URL
	}
	fmt.Fprintf(os.Stderr, "Opening %s to sign in…\n", where)
	// A courtesy and never a dependency, the same as the two login flows: the
	// URL is printed so a machine with no browser is not stuck, and the sign-in
	// works whether or not osOpenBrowser managed anything at all.
	fmt.Fprintln(os.Stderr, "If no browser opens, visit:\n  "+start.AuthorizeURL)
	osOpenBrowser(start.AuthorizeURL)

	if err := awaitMcpConnect(ctx, cl, name, start); err != nil {
		return err
	}
	// Re-read rather than believe the poll: the poll answers about the FLOW and
	// what is worth printing now is the SERVER — the scopes that were actually
	// granted, which are narrower than the ones asked for whenever somebody
	// unticked a box.
	m, err = cl.McpServer(ctx, name)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, mcpConnectedLine(m))
	if m.OAuth != nil && m.OAuth.Detail != "" {
		// The gateway says this while somebody is watching rather than at the
		// first failure — a server that issues no refresh token has given us a
		// grant that ends and cannot be renewed.
		fmt.Fprintln(os.Stderr, "note: "+m.OAuth.Detail)
	}
	if len(m.Attach) == 0 {
		fmt.Fprintf(os.Stderr, "not attached to anything yet: `yas mcp attach %s all`\n", m.ID)
		return nil
	}
	// The set of tool servers is fixed when a box is created; the CREDENTIAL is
	// not. Saying only the first half is how somebody reconnects, sees no
	// change, and makes a new box for nothing.
	fmt.Fprintln(os.Stderr, "a box already running with this server picks the new credential up on its next call. "+
		"A box created before it was attached has no such server and will not grow one.")
	return nil
}

// mcpConnectedLine is what a finished sign-in reports.
func mcpConnectedLine(m api.McpServer) string {
	line := m.ID + " is signed in"
	if m.OAuth == nil {
		return line
	}
	if m.OAuth.Issuer != "" {
		line += " to " + m.OAuth.Issuer
	}
	if len(m.OAuth.Scopes) > 0 {
		line += ", granted " + strings.Join(m.OAuth.Scopes, " ")
	}
	if m.OAuth.ExpiresAt != nil {
		// The TOKEN's life and never the row's lapse — two clocks, and the
		// EXPIRES column in `yas mcp list` is the other one. Reported as a fact
		// rather than as a thing to do, because the gateway renews it unasked.
		line += fmt.Sprintf("; the token runs out %s and the control plane renews it",
			m.OAuth.ExpiresAt.Local().Format("2006-01-02 15:04"))
	}
	return line
}

// awaitMcpConnect asks the gateway how the flow ended, until it has.
//
// Polling is the WHOLE mechanism here rather than a fallback, so it tolerates a
// failed poll: somebody is halfway through a consent screen and one refused
// request must not end that. A refusal is remembered and reported only if the
// wait runs out, which is the difference between "your network hiccuped" and
// "you never finished".
func awaitMcpConnect(ctx context.Context, cl *api.Client, name string, start api.McpConnectStart) error {
	deadline := time.Now().Add(mcpConnectTimeout)
	// The gateway's own expiry wins when it is sooner. Asking after it only
	// repeats a question whose answer can no longer change.
	if !start.ExpiresAt.IsZero() && start.ExpiresAt.Before(deadline) {
		deadline = start.ExpiresAt
	}
	var last error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(mcpPollInterval):
		}
		state, err := cl.McpConnectStatus(ctx, name, start.FlowID)
		switch {
		case err != nil:
			if api.IsNotFound(err) {
				// The flow is gone rather than slow, and no later poll finds it.
				return err
			}
			last = err
		default:
			last = nil
			switch state.Status {
			case api.McpFlowConnected:
				return nil
			case api.McpFlowFailed, api.McpFlowExpired:
				detail := state.Detail
				if detail == "" {
					detail = "no reason given"
				}
				return fmt.Errorf("the %s sign-in did not complete: %s", name, detail)
			}
		}
		if time.Now().After(deadline) {
			if last != nil {
				return fmt.Errorf("could not tell whether %s was connected: %w", name, last)
			}
			return fmt.Errorf("timed out waiting for the %s sign-in to finish; "+
				"the browser tab still works, and `yas mcp list` says whether it landed", name)
		}
	}
}

// mcpDisconnect drops the grant.
//
// No confirmation, deliberately. This REMOVES a credential rather than spending
// one, the row and every box created from it keep working until they are made
// again, and a prompt in front of a safe action is how somebody learns to type
// past the prompt in front of an unsafe one. Reconnecting is one command.
func mcpDisconnect(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: yas mcp disconnect <name>")
	}
	name := args[0]
	_, cl, err := loadClient()
	if err != nil {
		return err
	}
	if err := cl.DisconnectMcpServer(ctx, name); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s is disconnected: the token is gone from the control plane, "+
		"and told to the vendor as revoked where the vendor accepts that\n", name)
	// What is KEPT, said here because it is the reason this is not frightening:
	// the client registration and the discovered endpoints stay, so connecting
	// again is one hop rather than the whole discovery chain over three hosts.
	fmt.Fprintf(os.Stderr, "the sign-in itself is kept, so `yas mcp connect %s` is one hop\n", name)
	fmt.Fprintln(os.Stderr, "boxes already running keep the credential they were created with — "+
		"a capability that changed under a live guest would not be one")
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
