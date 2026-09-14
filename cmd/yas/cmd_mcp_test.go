package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

// Only the default and error branches are driven from cmd/: the real
// subcommands call loadClient() and would reach the network. What is asserted
// here is the dispatch and the pure helpers.
func TestMcpRejectsAnUnknownSubcommand(t *testing.T) {
	err := cmdMcp([]string{"frobnicate"})
	if err == nil {
		t.Fatal("an unknown subcommand was accepted")
	}
	// The refusal names every subcommand there is, so somebody who typed the
	// wrong one does not have to go and look.
	for _, want := range []string{"list", "add", "rm", "test", "attach", "detach", "connect", "disconnect"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestMcpAttachNeedsBothArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"linear"}, {"linear", "all", "extra"}} {
		if err := cmdMcp(append([]string{"attach"}, args...)); err == nil {
			t.Errorf("attach %v was accepted", args)
		}
	}
}

func TestMcpRmNeedsExactlyOneName(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}} {
		if err := cmdMcp(append([]string{"rm"}, args...)); err == nil {
			t.Errorf("rm %v was accepted", args)
		}
	}
}

// The count is checked BEFORE loadClient, which is what makes this safe to
// drive from a test: a wrong argument count never reaches the network.
func TestMcpConnectAndDisconnectNeedExactlyOneName(t *testing.T) {
	for _, verb := range []string{"connect", "disconnect"} {
		for _, args := range [][]string{{}, {"a", "b"}} {
			if err := cmdMcp(append([]string{verb}, args...)); err == nil {
				t.Errorf("%s %v was accepted", verb, args)
			}
		}
	}
}

// THE CREDENTIAL COLUMN.
//
// One word per row, and the question it answers is "can a box use this server
// now, and if not what fixes it". The two rows worth staring at are the last
// two: an OAuth status beats HasSecret in both directions, because they
// disagree in exactly one place and the status is the half that says why.
func TestCredentialCell(t *testing.T) {
	lapsed := time.Now().Add(-time.Hour)
	oauth := func(status string) *api.McpOAuth { return &api.McpOAuth{Status: status} }

	for _, tc := range []struct {
		name string
		row  api.McpServer
		want string
	}{
		{
			name: "a pasted key",
			row:  api.McpServer{ID: "linear", HasSecret: true},
			want: "stored",
		},
		{
			name: "no credential at all, and no sign-in offered",
			row:  api.McpServer{ID: "open"},
			want: "none",
		},
		{
			name: "a live grant",
			row:  api.McpServer{ID: "strava", HasSecret: true, OAuth: oauth(api.McpOAuthConnected)},
			want: "connected",
		},
		{
			// The gateway cleared the dead access token, so hasSecret is false
			// and truthfully so. "none" would send somebody to paste a key at a
			// server that wants a sign-in.
			name: "a grant that died",
			row:  api.McpServer{ID: "strava", OAuth: oauth(api.McpOAuthNeedsReauth)},
			want: "reconnect",
		},
		{
			// Never connected, or disconnected on purpose. There is nothing
			// stored, which is what the column reports.
			name: "an OAuth server holding nothing",
			row:  api.McpServer{ID: "strava", OAuth: oauth(api.McpOAuthPending)},
			want: "none",
		},
		{
			// THE ONE THAT CATCHES A CONFLATION. Two different clocks: the
			// attachment lapsed, the TOKEN is fine. The lapse belongs in the
			// EXPIRES column and must not reach this one.
			name: "connected, on a row whose attachment has lapsed",
			row: api.McpServer{
				ID: "strava", HasSecret: true, OAuth: oauth(api.McpOAuthConnected),
				ExpiresAt: &lapsed, Lapsed: true,
			},
			want: "connected",
		},
		{
			// A word this CLI has never seen, from a gateway newer than it. The
			// stored credential is still the truth about whether a box can use
			// the server, so it is reported rather than guessed at.
			name: "a status from a later gateway",
			row:  api.McpServer{ID: "strava", HasSecret: true, OAuth: oauth("quiesced")},
			want: "stored",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialCell(tc.row); got != tc.want {
				t.Fatalf("credentialCell = %q, want %q", got, tc.want)
			}
		})
	}
}

// What a finished sign-in says, which is the one place the TOKEN's expiry is
// printed — and it is never the row's lapse.
func TestMcpConnectedLine(t *testing.T) {
	expiry := time.Date(2026, 9, 14, 11, 30, 0, 0, time.Local)
	for _, tc := range []struct {
		name string
		row  api.McpServer
		want []string
		not  []string
	}{
		{
			name: "issuer, scopes and a token expiry",
			row: api.McpServer{ID: "strava", OAuth: &api.McpOAuth{
				Status:    api.McpOAuthConnected,
				Issuer:    "https://www.strava.com",
				Scopes:    []string{"read", "activity:read"},
				ExpiresAt: &expiry,
			}},
			want: []string{"strava is signed in", "https://www.strava.com", "read activity:read", "2026-09-14 11:30"},
		},
		{
			// A server that reported nothing beyond "connected" still gets a
			// sentence, rather than a line with a dangling comma in it.
			name: "a grant that reported nothing else",
			row:  api.McpServer{ID: "strava", OAuth: &api.McpOAuth{Status: api.McpOAuthConnected}},
			want: []string{"strava is signed in"},
			not:  []string{",", ";"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mcpConnectedLine(tc.row)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("%q does not contain %q", got, want)
				}
			}
			for _, no := range tc.not {
				if strings.Contains(got, no) {
					t.Errorf("%q contains %q", got, no)
				}
			}
		})
	}
}

// THE POLL IS THE FALLBACK ROAD, so what it does on each answer is pinned.
//
// The listener beside it catches the ordinary sign-in. This loop is what ends
// the wait on an ending the listener never sees — an expired link, a consent
// finished somewhere this port cannot be knocked on — and it carries the blip
// tolerance. If it stops reading one of these words correctly, `yas mcp
// connect` holds a terminal for three minutes over a flow that is already over.
func TestAwaitMcpConnect(t *testing.T) {
	for _, tc := range []struct {
		name string
		// answers are served one per poll; the last repeats.
		answers []string
		codes   []int
		wantErr string
	}{
		{
			name:    "pending, then connected",
			answers: []string{`{"status":"pending"}`, `{"status":"connected"}`},
		},
		{
			// The vendor's own words reach the terminal, because "it failed" is
			// not something anybody can act on.
			name:    "the vendor refused",
			answers: []string{`{"status":"failed","detail":"the account declined access"}`},
			wantErr: "the account declined access",
		},
		{
			name:    "a failure with nothing said",
			answers: []string{`{"status":"failed"}`},
			wantErr: "no reason given",
		},
		{
			name:    "nobody ever came back",
			answers: []string{`{"status":"expired","detail":"this connect link expired"}`},
			wantErr: "this connect link expired",
		},
		{
			// One refused poll must not end a sign-in somebody is halfway
			// through a consent screen for.
			name:    "a blip, then connected",
			answers: []string{`{"error":"store_error","message":"nope"}`, `{"status":"connected"}`},
			codes:   []int{500, 200},
		},
		{
			// A flow the gateway has never heard of is gone rather than slow,
			// and no later poll finds it.
			name:    "the flow does not exist",
			answers: []string{`{"error":"not_found","message":"no such connect flow"}`},
			codes:   []int{404},
			wantErr: "no such connect flow",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := min(n, len(tc.answers)-1)
				if len(tc.codes) > i {
					w.WriteHeader(tc.codes[i])
				}
				_, _ = io.WriteString(w, tc.answers[i])
				n++
			}))
			defer srv.Close()

			defer swapPollInterval(time.Millisecond)()
			err := awaitMcpConnect(context.Background(), &api.Client{BaseURL: srv.URL},
				"strava", api.McpConnectStart{FlowID: "f1"})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("connected flow reported %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("the wait succeeded on a flow that did not connect")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

// A flow nobody ever finishes gives up, and says where to look rather than
// implying the sign-in is lost — the gateway's flow outlives this command.
func TestAwaitMcpConnectGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"pending"}`)
	}))
	defer srv.Close()
	defer swapPollInterval(time.Millisecond)()

	start := api.McpConnectStart{FlowID: "f1", ExpiresAt: time.Now().Add(5 * time.Millisecond)}
	err := awaitMcpConnect(context.Background(), &api.Client{BaseURL: srv.URL}, "strava", start)
	if err == nil {
		t.Fatal("a flow nobody finished was reported as connected")
	}
	if !strings.Contains(err.Error(), "yas mcp list") {
		t.Errorf("the refusal does not say where to look: %v", err)
	}
}

// THE REDIRECT URI IS TWO THINGS AT ONCE: what the gateway validates and
// stores, and where this process is actually listening. This pins both halves.
//
// The first half is validateLoopbackRedirect's rules, spelled out here so a
// change to this URI fails in this repo rather than as a 400 somebody reads in
// a terminal. The second is the one that is easy to get wrong silently: a path
// that does not match the handler's is a 404 the browser shows and the code is
// lost with the flow already spent.
func TestMcpLoopbackRedirect(t *testing.T) {
	raw := mcpLoopbackRedirect(mcpLoopbackPort)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("%q is not a URL: %v", raw, err)
	}
	if u.Scheme != "http" {
		// http and never https: loopback is the one place plaintext is correct
		// (RFC 8252 §7.3), and nobody can issue a certificate for this name.
		t.Errorf("scheme = %q, want http", u.Scheme)
	}
	if u.Hostname() != "127.0.0.1" {
		// A literal address, not `localhost`: the gateway compares three exact
		// strings and resolves nothing.
		t.Errorf("host = %q, want 127.0.0.1", u.Hostname())
	}
	if u.Port() != strconv.Itoa(mcpLoopbackPort) {
		t.Errorf("port = %q, want %d", u.Port(), mcpLoopbackPort)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		// The vendor appends ?code=&state= to this URI. Anything already on it
		// makes what arrives ambiguous, and the gateway refuses it outright.
		t.Errorf("%q carries userinfo, a query or a fragment", raw)
	}
	// Distinct from the other FIXED port this CLI binds, or the two sign-ins
	// take each other's callbacks and neither reports a thing. The Claude flow
	// is not compared because it has no number of its own: it asks the kernel
	// for one, and a kernel does not hand out a port somebody is holding.
	if mcpLoopbackPort == codexPort {
		t.Errorf("port %d is the one `yas login -openai` binds", mcpLoopbackPort)
	}

	codes := make(chan string, 1)
	fails := make(chan error, 1)
	h := callbackHandler(mcpLoopbackPath, "st8", "yas mcp connect strava", codes, fails)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u.Path+"?state=st8&code=abc123", nil))
	select {
	case got := <-codes:
		if got != "abc123" {
			t.Errorf("the handler passed %q", got)
		}
	case err := <-fails:
		t.Fatalf("the handler refused its own redirect: %v", err)
	default:
		t.Fatalf("the redirect URI's path %q is not one the handler answers on (it said %d)", u.Path, rec.Code)
	}
}

// THE STATE IS THE GATEWAY'S, and this is the only place it can be had from.
//
// It is the handle the gateway claims the flow by, so an invented one would
// never match. It reaches callbackHandler's CSRF check and the submit, and an
// EMPTY one does not weaken that check but removes it — which is why a URL
// without a state is refused here rather than passed along.
func TestMcpFlowState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    string
		want  string
		error string
	}{
		{
			name: "the ordinary authorize URL",
			in:   "https://www.strava.com/oauth/authorize?client_id=1&state=abc123&code_challenge=x",
			want: "abc123",
		},
		{
			// Percent-encoded, exactly as callbackHandler will read it back off
			// the redirect. Both sides use Query(), so both see the same bytes.
			name: "a state that needed encoding",
			in:   "https://auth.example.dev/authorize?state=a%2Bb%2Fc%3D",
			want: "a+b/c=",
		},
		{
			name: "an endpoint that carries a fixed parameter of its own",
			in:   "https://auth.example.dev/authorize?audience=api&state=s1",
			want: "s1",
		},
		{
			name:  "no state at all",
			in:    "https://auth.example.dev/authorize?client_id=1",
			error: "carries no state",
		},
		{
			name:  "an empty state",
			in:    "https://auth.example.dev/authorize?state=",
			error: "carries no state",
		},
		{
			name:  "nothing at all",
			in:    "",
			error: "carries no state",
		},
		{
			name:  "not a URL",
			in:    "http://%zz",
			error: "could not be read",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpFlowState(tc.in)
			if tc.error != "" {
				if err == nil {
					t.Fatalf("mcpFlowState(%q) = %q, want a refusal", tc.in, got)
				}
				if !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("error %q does not contain %q", err, tc.error)
				}
				if got != "" {
					t.Fatalf("a refusal still returned %q; an empty state would disable the handler's check", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("mcpFlowState(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func swapPollInterval(d time.Duration) func() {
	prev := mcpPollInterval
	mcpPollInterval = d
	return func() { mcpPollInterval = prev }
}

func TestParseInlineMcp(t *testing.T) {
	env := map[string]string{
		"YAS_MCP_SECRET_LINEAR":     "lin_api_x",
		"YAS_MCP_SECRET_MY_TRACKER": "tok_y",
	}
	lookup := func(k string) string { return env[k] }

	for _, tc := range []struct {
		name  string
		in    []string
		want  []api.McpServerInline
		error string
	}{
		{
			name: "one server, key from the environment",
			in:   []string{"linear=https://mcp.linear.app/mcp"},
			want: []api.McpServerInline{
				{Name: "linear", URL: "https://mcp.linear.app/mcp", Secret: "lin_api_x"},
			},
		},
		{
			// A hyphen in the name becomes an underscore in the variable, which
			// is the only spelling a shell will accept.
			name: "a hyphenated name",
			in:   []string{"my-tracker=https://mcp.example.dev/mcp"},
			want: []api.McpServerInline{
				{Name: "my-tracker", URL: "https://mcp.example.dev/mcp", Secret: "tok_y"},
			},
		},
		{
			// A server that needs no key is legitimate.
			name: "no key stored",
			in:   []string{"open=https://mcp.example.dev/mcp"},
			want: []api.McpServerInline{{Name: "open", URL: "https://mcp.example.dev/mcp"}},
		},
		{
			// The URL may contain an `=`, so only the FIRST one splits.
			name: "an equals in the url",
			in:   []string{"x=https://mcp.example.dev/mcp"},
			want: []api.McpServerInline{{Name: "x", URL: "https://mcp.example.dev/mcp"}},
		},
		{name: "no equals", in: []string{"linear"}, error: "not name=url"},
		{name: "no name", in: []string{"=https://x/mcp"}, error: "not name=url"},
		{name: "no url", in: []string{"linear="}, error: "not name=url"},
		{
			name:  "the same name twice",
			in:    []string{"linear=https://a/mcp", "linear=https://b/mcp"},
			error: "twice",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseInlineMcp(tc.in, lookup)
			if tc.error != "" {
				if err == nil {
					t.Fatalf("accepted %v", tc.in)
				}
				if !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("error %q does not contain %q", err, tc.error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The key is read from the environment and never from argv, because a
// credential on the command line is a credential in the shell history and in
// the process table.
func TestInlineMcpSecretVar(t *testing.T) {
	for in, want := range map[string]string{
		"linear":     "YAS_MCP_SECRET_LINEAR",
		"my-tracker": "YAS_MCP_SECRET_MY_TRACKER",
		"a-b-c":      "YAS_MCP_SECRET_A_B_C",
	} {
		if got := inlineMcpSecretVar(in); got != want {
			t.Errorf("inlineMcpSecretVar(%q) = %q, want %q", in, got, want)
		}
	}
}

// `mcp` shadows any program of that name inside a box, so it being in the
// reserved set is a compatibility decision worth pinning.
func TestMcpIsAReservedVerb(t *testing.T) {
	if _, ok := verbs["mcp"]; !ok {
		t.Fatal("mcp is not a verb, so `yas mcp list` would try to run `mcp` inside a box")
	}
}
