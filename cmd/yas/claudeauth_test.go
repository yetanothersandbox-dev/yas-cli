package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The loopback flow, end to end against a stub standing in for Anthropic: the
// browser is opened at a URL carrying PKCE and our state, the callback is
// answered, and the code is exchanged for a pair.
func TestClaudeSignInExchangesTheCodeForAPair(t *testing.T) {
	var got map[string]string
	stub := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"sk-ant-oat01-at","refresh_token":"rt","expires_in":28800}`)
	})
	defer stub.Close()
	// A free port, not the real one: on a developer's machine that is quite
	// likely to be held by `claude` signing itself in, and a test that fails
	// for that reason is a test nobody trusts.
	defer swapClaudeEndpoints(t, stub.URL, freePort(t))()

	open := func(authorize string) {
		u, err := url.Parse(authorize)
		if err != nil {
			t.Error(err)
			return
		}
		q := u.Query()
		if q.Get("code_challenge") == "" || q.Get("state") == "" {
			t.Error("the authorize URL carries no PKCE challenge or no state")
		}
		if q.Get("code_challenge_method") != "S256" {
			t.Errorf("PKCE method = %q, want S256", q.Get("code_challenge_method"))
		}
		if q.Get("client_id") != claudeClientID {
			t.Errorf("client_id = %q", q.Get("client_id"))
		}
		// `code=true` on the LOOPBACK leg too. It reads like "print the code
		// for me" and it is not: Anthropic's own client sets it on both, and
		// the redirect is what picks the mode. Asserted here because the
		// obvious reading of the name is the wrong one, and dropping it is the
		// mistake somebody would make while tidying.
		if q.Get("code") != "true" {
			t.Errorf("the loopback authorize URL set code=%q, want true", q.Get("code"))
		}
		if want := fmt.Sprintf("http://localhost:%d%s", claudePort, claudeCallback); q.Get("redirect_uri") != want {
			t.Errorf("redirect_uri = %q, want %q", q.Get("redirect_uri"), want)
		}
		go func() {
			cb := fmt.Sprintf("http://127.0.0.1:%d%s?state=%s&code=the-code",
				claudePort, claudeCallback, url.QueryEscape(q.Get("state")))
			for i := 0; i < 50; i++ {
				if resp, err := http.Get(cb); err == nil {
					resp.Body.Close()
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	tok, err := signInToClaude(context.Background(), open, io.Discard)
	if err != nil {
		t.Fatalf("sign-in failed: %v", err)
	}
	if tok.AccessToken != "sk-ant-oat01-at" || tok.RefreshToken != "rt" || tok.ExpiresIn != 28800 {
		t.Fatalf("pair = %+v", tok)
	}
	// The verifier proves this is the process that started the flow. Without it
	// a stolen code is usable by whoever stole it.
	if got["code_verifier"] == "" {
		t.Error("the exchange sent no PKCE verifier")
	}
	if got["code"] != "the-code" {
		t.Errorf("exchanged %q", got["code"])
	}
	if got["grant_type"] != "authorization_code" {
		t.Errorf("grant_type = %q", got["grant_type"])
	}
}

// The printed-code flow, end to end. The browser shows `<code>#<state>` and it
// comes back through the person, so the state half is the only thing standing
// between this terminal and a code minted for somebody else's account.
func TestClaudeManualSignInReadsThePrintedCode(t *testing.T) {
	var got map[string]string
	stub := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","expires_in":28800}`)
	})
	defer stub.Close()
	defer swapClaudeEndpoints(t, stub.URL, freePort(t))()

	pasted := make(chan string, 1)
	open := func(authorize string) {
		u, err := url.Parse(authorize)
		if err != nil {
			t.Error(err)
			return
		}
		q := u.Query()
		if q.Get("code") != "true" {
			t.Error("the manual authorize URL did not ask Claude to print the code")
		}
		if q.Get("redirect_uri") != claudeCodeRedirect {
			t.Errorf("redirect_uri = %q, want the printed-code page", q.Get("redirect_uri"))
		}
		pasted <- "the-code#" + q.Get("state") + "\n"
	}

	// The reader is fed only once the URL has been built, because the state to
	// paste back is not knowable until then.
	tok, err := signInToClaudeManual(context.Background(), open, io.Discard, lazyReader(pasted))
	if err != nil {
		t.Fatalf("sign-in failed: %v", err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Fatalf("pair = %+v", tok)
	}
	if got["redirect_uri"] != claudeCodeRedirect {
		t.Errorf("the exchange sent redirect_uri %q, which must match the authorize leg", got["redirect_uri"])
	}
}

// What a person can paste, and what each answer has to be. The state check is
// the CSRF guard, and it matters more here than on the loopback: a pasted
// string came through somebody who cannot be expected to notice they copied it
// from the wrong tab.
func TestSplitClaudeCode(t *testing.T) {
	cases := []struct {
		name, pasted, want, wantErr string
	}{
		{"the whole printed string", "abc#s1", "abc", ""},
		{"surrounding space survives a copy", "  abc#s1  ", "abc", ""},
		{"nothing entered", "", "", "nothing entered"},
		{"only the code half", "abc", "", "missing the part after"},
		{"an empty state half", "abc#", "", "missing the part after"},
		{"a code from another terminal", "abc#s2", "", "did not come from this terminal"},
		{"an empty code half", "#s1", "", "empty before"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := splitClaudeCode(tc.pasted, "s1")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if got != tc.want {
					t.Errorf("code = %q, want %q", got, tc.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one naming %q", err, tc.wantErr)
			}
		})
	}
}

// A port this machine cannot have is the ONE failure worth trying another way,
// so it has to be distinguishable from every other. A timeout or a refusal is
// a person answering, and opening a second browser tab at them is not a
// recovery — see claudeSignIn.
func TestAHeldPortIsReportedAsNoLoopback(t *testing.T) {
	port := freePort(t)
	held, err := listenLoopback(port, "close the other tool")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, l := range held {
			_ = l.Close()
		}
	}()
	defer swapClaudeEndpoints(t, "http://127.0.0.1:1/token", port)()

	opened := false
	_, err = signInToClaude(context.Background(), func(string) { opened = true }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), errNoLoopback.Error()) {
		t.Fatalf("err = %v, want one wrapping errNoLoopback", err)
	}
	// And no browser was opened at a flow that could never have come back.
	if opened {
		t.Error("a browser was opened for a sign-in with nowhere to return to")
	}
}

// A sign-in with no refresh token cannot be renewed, so every schedule on it
// would stop within hours. Better to fail here, where the message can say so.
func TestAClaudeSignInWithoutARefreshTokenIsRefused(t *testing.T) {
	stub := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","expires_in":28800}`)
	})
	defer stub.Close()
	defer swapClaudeEndpoints(t, stub.URL, claudePort)()

	_, err := exchangeClaudeCode(context.Background(), "c", "v", "http://localhost/cb", "s")
	if err == nil || !strings.Contains(err.Error(), "renewed") {
		t.Fatalf("err = %v, want a refusal naming renewal", err)
	}
}

// Anthropic's own words survive. A refused sign-in that reports only a status
// code sends somebody to the wrong place.
func TestARefusedClaudeExchangeKeepsAnthropicsWords(t *testing.T) {
	stub := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"the code has already been used"}`)
	})
	defer stub.Close()
	defer swapClaudeEndpoints(t, stub.URL, claudePort)()

	_, err := exchangeClaudeCode(context.Background(), "c", "v", "http://localhost/cb", "s")
	if err == nil || !strings.Contains(err.Error(), "already been used") {
		t.Fatalf("err = %v, want Anthropic's own description", err)
	}
}

// The SHIPPING path takes a port the kernel chooses, so `claude` signing itself
// in cannot be holding it. Every other test here pins a port — the fake browser
// has to know where to knock — so without this one, the value that actually
// ships is the only one never exercised.
func TestTheLoopbackTakesAPortTheKernelChooses(t *testing.T) {
	defer swapClaudeEndpoints(t, "http://127.0.0.1:1/token", 0)()

	lns, port, err := claudeListen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, l := range lns {
			_ = l.Close()
		}
	}()
	if port == 0 {
		t.Fatal("claudeListen reported port 0, which cannot go in a redirect_uri")
	}
	// And a second sign-in can run beside the first, which is the whole point.
	second, other, err := claudeListen()
	if err != nil {
		t.Fatalf("a second sign-in could not bind: %v", err)
	}
	for _, l := range second {
		_ = l.Close()
	}
	if other == port {
		t.Errorf("both sign-ins were handed port %d", port)
	}
}

// Anthropic's token endpoint refuses in TWO shapes, and a probe against the
// live endpoint returned the second one — so the flat-string reading alone
// turned a perfectly clear "Rate limited" into a decode failure, and the
// refusal reached the terminal as "the answer could not be read".
func TestARefusalIsReadInEitherShape(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{
			"the OAuth shape from RFC 6749",
			`{"error":"invalid_grant","error_description":"the code has already been used"}`,
			"already been used",
		},
		{
			"the Anthropic API envelope",
			`{"error":{"type":"rate_limit_error","message":"Rate limited. Please try again later."}}`,
			"Rate limited",
		},
		{
			"an envelope with no message still names the type",
			`{"error":{"type":"authentication_error"}}`,
			"authentication_error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, tc.body)
			})
			defer stub.Close()
			defer swapClaudeEndpoints(t, stub.URL, claudePort)()

			_, err := exchangeClaudeCode(context.Background(), "c", "v", "http://localhost/cb", "s")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "could not be read") {
				t.Error("a readable refusal was reported as an unreadable answer")
			}
		})
	}
}

// ------------------------------------------------------------------ helpers

// swapClaudeEndpoints points the flow at a stub and returns the restore.
func swapClaudeEndpoints(t *testing.T, base string, port int) func() {
	t.Helper()
	oldToken, oldAuthz, oldPort := claudeTokenURLVar, claudeAuthzURLVar, claudePort
	claudeTokenURLVar, claudeAuthzURLVar, claudePort = base, base+"/authorize", port
	return func() { claudeTokenURLVar, claudeAuthzURLVar, claudePort = oldToken, oldAuthz, oldPort }
}

// lazyReader blocks until the browser has been "opened", because the state to
// paste back is only knowable from the URL the flow built.
func lazyReader(ch <-chan string) io.Reader { return &chanReader{ch: ch} }

type chanReader struct {
	ch   <-chan string
	rest string
	done bool
}

func (c *chanReader) Read(p []byte) (int, error) {
	if !c.done {
		c.rest, c.done = <-c.ch, true
	}
	if c.rest == "" {
		return 0, io.EOF
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}
