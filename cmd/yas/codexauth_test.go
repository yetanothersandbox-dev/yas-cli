package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The callback must be reachable on BOTH loopback families.
//
// `localhost` resolves to ::1 before 127.0.0.1 on macOS, so an IPv4-only bind
// hands the callback to whatever holds IPv6 *:1455 — and because they are
// different families, that raises no EADDRINUSE and nothing notices. talyn
// shipped exactly that and another tool on the same OAuth client answered its
// callback.
func TestTheCallbackBindsBothLoopbackFamilies(t *testing.T) {
	port := freePort(t)
	lns, err := listenLoopback(port, "close the other tool")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, l := range lns {
			_ = l.Close()
		}
	}()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "here")
	})}
	for _, ln := range lns {
		go func(l net.Listener) { _ = srv.Serve(l) }(ln)
	}
	defer srv.Close()

	for _, host := range []string{"127.0.0.1", "[::1]"} {
		resp, err := http.Get(fmt.Sprintf("http://%s:%d/", host, port))
		if err != nil {
			// A machine with no IPv6 stack is not a failure; IPv4 must work.
			if host == "[::1]" {
				t.Logf("no IPv6 loopback on this host: %v", err)
				continue
			}
			t.Fatalf("%s did not answer: %v", host, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "here" {
			t.Errorf("%s answered %q", host, body)
		}
	}
}

// Two sign-ins at once must not both think they own the port. The second has
// to say so rather than silently never receiving its callback.
func TestASecondSignInRefusesTheHeldPort(t *testing.T) {
	port := freePort(t)
	first, err := listenLoopback(port, "close the other tool")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, l := range first {
			_ = l.Close()
		}
	}()
	if _, err := listenLoopback(port, "close the other tool"); err == nil {
		t.Fatal("a second bind of the same port succeeded; one of the two would never get its callback")
	} else if !strings.Contains(err.Error(), "already listening") {
		t.Errorf("the refusal does not say what to do about it: %v", err)
	}
}

// The state check is the CSRF guard. Without it, any page that can reach this
// loopback could feed us an authorization code minted for another account.
func TestACallbackWithTheWrongStateIsRefused(t *testing.T) {
	codes := make(chan string, 1)
	fails := make(chan error, 1)
	h := callbackHandler(codexCallback, "the-real-state", "yas login -openai", codes, fails)

	rec := doCallback(t, h, url.Values{"state": {"forged"}, "code": {"stolen"}})
	if rec.code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.code)
	}
	select {
	case c := <-codes:
		t.Fatalf("a forged callback delivered a code: %q", c)
	case err := <-fails:
		if !strings.Contains(err.Error(), "state") {
			t.Errorf("the failure does not name the cause: %v", err)
		}
	default:
		t.Fatal("a forged callback was neither accepted nor reported")
	}
}

func TestAGoodCallbackDeliversItsCode(t *testing.T) {
	codes := make(chan string, 1)
	fails := make(chan error, 1)
	h := callbackHandler(codexCallback, "s1", "yas login -openai", codes, fails)
	rec := doCallback(t, h, url.Values{"state": {"s1"}, "code": {"ok-code"}})
	if rec.code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.code)
	}
	select {
	case c := <-codes:
		if c != "ok-code" {
			t.Errorf("code = %q", c)
		}
	case err := <-fails:
		t.Fatalf("a good callback failed: %v", err)
	}
}

// A cancelled sign-in is reported as one, not as a timeout five minutes later.
func TestACancelledSignInIsReportedAtOnce(t *testing.T) {
	codes := make(chan string, 1)
	fails := make(chan error, 1)
	h := callbackHandler(codexCallback, "s1", "yas login -openai", codes, fails)
	doCallback(t, h, url.Values{
		"state": {"s1"}, "error": {"access_denied"},
		"error_description": {"The user declined"},
	})
	select {
	case err := <-fails:
		if !strings.Contains(err.Error(), "declined") {
			t.Errorf("the refusal lost OpenAI's own words: %v", err)
		}
	default:
		t.Fatal("a declined sign-in was not reported")
	}
}

// The whole flow, against a stub standing in for OpenAI: the browser is opened
// at a URL carrying PKCE and our state, the callback is answered, and the code
// is exchanged for a pair.
func TestSignInExchangesTheCodeForAPair(t *testing.T) {
	var gotForm url.Values
	stub := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","refresh_token":"rt","expires_in":3600}`)
	})
	defer stub.Close()

	oldToken, oldAuthz, oldPort := codexTokenURLVar, codexAuthzURLVar, codexPort
	// A free port, not 1455: on a developer's machine the real one is quite
	// likely to be held by `codex login`, and a test that fails for that reason
	// is a test nobody trusts.
	codexTokenURLVar, codexAuthzURLVar, codexPort = stub.URL, stub.URL+"/authorize", freePort(t)
	defer func() { codexTokenURLVar, codexAuthzURLVar, codexPort = oldToken, oldAuthz, oldPort }()

	// The browser is "opened" by answering our own callback, the way OpenAI's
	// redirect would.
	open := func(authorize string) {
		u, err := url.Parse(authorize)
		if err != nil {
			t.Error(err)
			return
		}
		q := u.Query()
		for _, want := range []string{"code_challenge", "state", "code_challenge_method"} {
			if q.Get(want) == "" {
				t.Errorf("the authorize URL carries no %s", want)
			}
		}
		if q.Get("code_challenge_method") != "S256" {
			t.Errorf("PKCE method = %q, want S256", q.Get("code_challenge_method"))
		}
		if q.Get("redirect_uri") != fmt.Sprintf("http://localhost:%d%s", codexPort, codexCallback) {
			t.Errorf("redirect_uri = %q; OpenAI registered a fixed one", q.Get("redirect_uri"))
		}
		go func() {
			cb := fmt.Sprintf("http://127.0.0.1:%d%s?state=%s&code=the-code",
				codexPort, codexCallback, url.QueryEscape(q.Get("state")))
			for i := 0; i < 50; i++ {
				if resp, err := http.Get(cb); err == nil {
					resp.Body.Close()
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	tok, err := signInToCodex(context.Background(), open, io.Discard)
	if err != nil {
		t.Fatalf("sign-in failed: %v", err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" || tok.ExpiresIn != 3600 {
		t.Fatalf("pair = %+v", tok)
	}
	// The verifier proves this is the process that started the flow. Without it
	// a stolen code is usable by whoever stole it.
	if gotForm.Get("code_verifier") == "" {
		t.Error("the exchange sent no PKCE verifier")
	}
	if gotForm.Get("code") != "the-code" {
		t.Errorf("exchanged %q", gotForm.Get("code"))
	}
}

// A sign-in with no refresh token cannot be renewed, so every schedule on it
// would stop within hours. Better to fail here, where the message can say so.
func TestASignInWithoutARefreshTokenIsRefused(t *testing.T) {
	stub := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"at","expires_in":3600}`)
	})
	defer stub.Close()
	old := codexTokenURLVar
	codexTokenURLVar = stub.URL
	defer func() { codexTokenURLVar = old }()

	_, err := exchangeCodexCode(context.Background(), "c", "v", "http://localhost/cb")
	if err == nil || !strings.Contains(err.Error(), "renewed") {
		t.Fatalf("err = %v, want a refusal naming renewal", err)
	}
}

// ------------------------------------------------------------------ helpers

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type recorded struct {
	code int
	body string
}

func doCallback(t *testing.T, h http.Handler, q url.Values) recorded {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, codexCallback+"?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	w := &capture{code: http.StatusOK}
	h.ServeHTTP(w, req)
	return recorded{code: w.code, body: w.body.String()}
}

type capture struct {
	code int
	body strings.Builder
	hdr  http.Header
}

func (c *capture) Header() http.Header {
	if c.hdr == nil {
		c.hdr = http.Header{}
	}
	return c.hdr
}
func (c *capture) Write(b []byte) (int, error) { return c.body.Write(b) }
func (c *capture) WriteHeader(code int)        { c.code = code }

func newStubServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(h)
}
