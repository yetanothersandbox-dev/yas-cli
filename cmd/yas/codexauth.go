package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Signing in to ChatGPT, on this machine, because it cannot be done anywhere
// else.
//
// # Why the CLI owns this and the gateway does not
//
// OpenAI publishes no third-party OAuth for ChatGPT-subscription inference. The
// only client whose tokens the Codex backend accepts is OpenAI's own Codex CLI
// client, and its registered redirect is the fixed loopback address below — so
// the authorize leg can only run on a machine that can BE localhost:1455. A
// hosted gateway never can.
//
// What the gateway does own is the REFRESH: a subscription access token lives
// hours, a schedule fires at 03:00 against one minted yesterday, and the guest
// cannot renew for itself because it never sees the credential at all. So this
// hands over the whole pair and keeps nothing.
//
// The PKCE verifier never leaves this process. That is what makes an
// authorization code delivered to the wrong listener useless to whoever got it.

const (
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexScope    = "openid profile email offline_access"
	codexCallback = "/auth/callback"
	// codexOriginator names us to OpenAI, the way other coding tools do.
	codexOriginator = "yas"
	// Long enough for a real sign-in, including a password manager and a
	// second factor; short enough that a closed tab does not hold the port
	// forever.
	codexSignInTimeout = 5 * time.Minute
)

// Overridable so the flow can be tested end to end against a stub, and so a
// test never binds the REAL port — which on a developer's machine is quite
// likely to be held by `codex login` itself. Variables rather than parameters
// because they are constants to every caller: nothing outside a test has any
// business choosing an OpenAI endpoint, and the port is not ours to choose at
// all (OpenAI registered this exact redirect against the Codex client, so a
// different one is simply refused).
var (
	codexAuthzURLVar = "https://auth.openai.com/oauth/authorize"
	codexTokenURLVar = "https://auth.openai.com/oauth/token"
	codexPort        = 1455
)

// codexTokens is what a completed sign-in yields.
type codexTokens struct {
	AccessToken  string
	RefreshToken string
	// ExpiresIn is seconds, as OpenAI reports it. The gateway turns it into an
	// instant on its own clock rather than trusting this machine's.
	ExpiresIn int64
}

// signInToCodex runs the whole flow: bind the callback, open a browser, wait
// for the redirect, exchange the code.
func signInToCodex(ctx context.Context, open func(string), out io.Writer) (codexTokens, error) {
	verifier, err := randomURLSafe(32)
	if err != nil {
		return codexTokens{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state, err := randomURLSafe(32)
	if err != nil {
		return codexTokens{}, err
	}

	listeners, err := listenLoopback(codexPort)
	if err != nil {
		return codexTokens{}, err
	}
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	redirect := fmt.Sprintf("http://localhost:%d%s", codexPort, codexCallback)
	codes := make(chan string, 1)
	fails := make(chan error, 1)
	srv := &http.Server{Handler: codexCallbackHandler(state, codes, fails)}
	for _, ln := range listeners {
		go func(l net.Listener) { _ = srv.Serve(l) }(ln)
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	q := url.Values{
		"response_type":              {"code"},
		"client_id":                  {codexClientID},
		"redirect_uri":               {redirect},
		"scope":                      {codexScope},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
		"originator":                 {codexOriginator},
	}
	authorize := codexAuthzURLVar + "?" + q.Encode()
	fmt.Fprintln(out, "Opening ChatGPT to sign in…")
	fmt.Fprintln(out, "If no browser opens, visit:\n  "+authorize)
	open(authorize)

	var code string
	select {
	case code = <-codes:
	case err := <-fails:
		return codexTokens{}, err
	case <-time.After(codexSignInTimeout):
		return codexTokens{}, errors.New("timed out waiting for the ChatGPT sign-in to finish")
	case <-ctx.Done():
		return codexTokens{}, ctx.Err()
	}
	return exchangeCodexCode(ctx, code, verifier, redirect)
}

// listenLoopback binds the callback port on BOTH address families.
//
// # Why this is not just Listen("tcp", "127.0.0.1:1455")
//
// The redirect OpenAI registered is `http://localhost:1455/...`, and on macOS
// `localhost` resolves to ::1 before 127.0.0.1. Binding IPv4 alone means a
// different process holding IPv6 *:1455 receives the callback instead — and
// because those are different address families, that does NOT raise
// EADDRINUSE, so nothing here would notice.
//
// That is not hypothetical; talyn shipped the IPv4-only version and another
// tool using the same Codex OAuth client answered its callback. Anything built
// on that client collides the same way: `codex login`, the Codex CLI, OpenCode.
//
// A SPECIFIC bind beats a wildcard one for routing, which is what takes the
// callback back from a process holding *:1455. A host with no IPv6 stack is
// not an error — carry on with IPv4. Failing to bind IPv4 is, because that is
// the family we can always be reached on.
func listenLoopback(port int) ([]net.Listener, error) {
	var out []net.Listener
	// IPv6 first: if this one is going to fail for a reason worth reporting,
	// report it before taking the IPv4 socket.
	if ln, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port)); err == nil {
		out = append(out, ln)
	} else if isAddrInUse(err) {
		return nil, fmt.Errorf("something is already listening on [::1]:%d — "+
			"close `codex login`, the Codex CLI or another tool using the same sign-in, then try again", port)
	}
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		for _, l := range out {
			_ = l.Close()
		}
		if isAddrInUse(err) {
			return nil, fmt.Errorf("something is already listening on 127.0.0.1:%d — "+
				"close `codex login`, the Codex CLI or another tool using the same sign-in, then try again", port)
		}
		return nil, fmt.Errorf("could not open the sign-in callback port %d: %w", port, err)
	}
	return append(out, ln), nil
}

func isAddrInUse(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "address already in use")
}

// codexCallbackHandler answers the redirect and hands the code back.
func codexCallbackHandler(state string, codes chan<- string, fails chan<- error) http.Handler {
	var once bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != codexCallback {
			http.NotFound(w, r)
			return
		}
		if once {
			return
		}
		once = true
		q := r.URL.Query()
		// The state check is the CSRF guard and it runs before anything else is
		// believed: without it, any page that can reach this loopback could
		// feed us an authorization code minted for a different account.
		if q.Get("state") != state {
			codexPage(w, http.StatusBadRequest, "That sign-in did not come from this terminal.",
				"Close this tab and run `yas login -openai` again.")
			fails <- errors.New("the sign-in came back with a state this terminal did not issue")
			return
		}
		if e := q.Get("error"); e != "" {
			detail := q.Get("error_description")
			if detail == "" {
				detail = e
			}
			codexPage(w, http.StatusOK, "Sign-in cancelled.", detail)
			fails <- fmt.Errorf("openai ended the sign-in: %s", detail)
			return
		}
		code := q.Get("code")
		if code == "" {
			codexPage(w, http.StatusBadRequest, "That sign-in came back empty.",
				"Close this tab and run `yas login -openai` again.")
			fails <- errors.New("openai returned no authorization code")
			return
		}
		codexPage(w, http.StatusOK, "Signed in.", "You can close this tab and go back to your terminal.")
		codes <- code
	})
}

// codexPage is the one thing the browser ever sees from us. Plain, self-closing
// text: this window belongs to OpenAI's flow, not to a product surface.
func codexPage(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>yas</title>`+
		`<body style="background:#0d0e10;color:#e8e9ec;font:15px/1.7 ui-monospace,Menlo,monospace;padding:48px">`+
		`<p><strong>%s</strong></p><p style="color:#9aa0a6">%s</p>`, title, detail)
}

// exchangeCodexCode turns the authorization code into a token pair. The
// verifier proves this is the same process that started the flow, which is what
// makes a stolen code useless.
func exchangeCodexCode(ctx context.Context, code, verifier, redirect string) (codexTokens, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {codexClientID},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURLVar,
		strings.NewReader(form.Encode()))
	if err != nil {
		return codexTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return codexTokens{}, err
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return codexTokens{}, fmt.Errorf("openai's answer could not be read: %w", err)
	}
	if resp.StatusCode/100 != 2 || body.Error != "" {
		detail := body.Description
		if detail == "" {
			detail = body.Error
		}
		return codexTokens{}, fmt.Errorf("openai refused the sign-in (%d): %s", resp.StatusCode, detail)
	}
	if body.AccessToken == "" {
		return codexTokens{}, errors.New("openai returned no access token")
	}
	// A refresh token is not optional for this fleet, and saying so here beats
	// discovering it at 03:00: without one the gateway cannot renew, and every
	// schedule on this credential would stop within hours.
	if body.RefreshToken == "" {
		return codexTokens{}, errors.New("openai returned no refresh token, so this sign-in " +
			"could not be renewed and would stop working within hours")
	}
	return codexTokens{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		ExpiresIn:    body.ExpiresIn,
	}, nil
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
