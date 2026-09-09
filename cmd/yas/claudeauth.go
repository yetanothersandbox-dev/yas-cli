package main

import (
	"bufio"
	"context"
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

// Signing in to Claude, on this machine, for the same reason the ChatGPT
// sign-in beside it runs here: the authorize leg needs a browser and a listener
// on the machine that opened it, and a hosted gateway is neither.
//
// # What this is for
//
// A Claude subscription is the credential most people with Claude Code actually
// hold, and until now `yas login -anthropic` could only take a console API key.
// A subscription token could be PASTED into that field and it worked — the
// proxy has classified `sk-ant-oat…` as OAuth and sent it as a Bearer token for
// a while (internal/proxy/proxy.go authAnthropic) — but it died within hours,
// because a paste carries no refresh token and nothing could renew it.
//
// So the point of this file is not the access token. It is the REFRESH token,
// which goes to the gateway with it and is what keeps a 03:00 schedule working
// against a credential minted yesterday. The guest never sees either.
//
// # Two ways back
//
// A loopback redirect is the better one: the browser returns to a port this
// process is holding and nobody types anything. It needs a port, so it cannot
// work over a bare SSH session, and a provider only accepts a redirect it has
// registered.
//
// The other is Anthropic's `code=true` mode, which redirects to a page that
// PRINTS the code for you to bring back. Slower, and it is a paste — but it is
// a paste of a one-time code that is worthless without the verifier this
// process is holding, not a paste of a credential. It works anywhere a browser
// works, including on another machine entirely, so it is the fallback and it is
// what -manual asks for.
//
// The PKCE verifier never leaves this process. That is what makes an
// authorization code delivered to the wrong listener useless to whoever got it.

const (
	// claudeClientID is Claude Code's own OAuth client. Public by construction:
	// it is in the URL the browser is sent to, which is the only reason it can
	// be written down here.
	claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeScope    = "org:create_api_key user:profile user:inference " +
		"user:sessions:claude_code user:mcp_servers user:file_upload"
	claudeCallback = "/callback"
	// Long enough for a real sign-in, including a password manager and a
	// second factor; short enough that a closed tab does not hold the port
	// forever.
	claudeSignInTimeout = 5 * time.Minute
)

// Variables rather than constants so the flow can be tested end to end against
// a stub, and so a test never binds the REAL port — which on a developer's
// machine is quite likely to be held by `claude` signing itself in. Nothing
// outside a test has any business choosing an Anthropic endpoint, and the port
// is not ours to choose at all: a redirect the provider has not registered is
// simply refused.
var (
	claudeAuthzURLVar = "https://claude.com/cai/oauth/authorize"
	claudeTokenURLVar = "https://platform.claude.com/v1/oauth/token"
	// claudeCodeRedirect is the page that prints the code instead of
	// redirecting back to this machine. Reached by adding `code=true`.
	claudeCodeRedirect = "https://platform.claude.com/oauth/code/callback"
	// claudePort is the loopback port. ZERO is the shipping value and means
	// "let the kernel choose" — see claudeListen for why that is allowed here
	// and not for Codex. A test pins it, because a callback answered from
	// outside this process has to know where to knock.
	claudePort = 0
)

// errNoLoopback marks the one failure that is worth trying another way: this
// machine could not hold the callback port at all.
var errNoLoopback = errors.New("no loopback port")

// claudeListen opens the callback, and reports which port it landed on.
//
// # Why this port is not fixed, and the Codex one is
//
// It depends on what each provider registered. OpenAI registered ONE redirect
// for the Codex client, so its port is not ours to choose and a collision can
// only be reported. Anthropic registered the loopback the way RFC 8252 §7.3
// asks for, accepting any port — its own client picks a fresh one on every
// sign-in — and where that is allowed it is simply better. The process most
// likely to be holding a fixed port here is `claude` signing itself in, which
// is exactly the collision a fixed port cannot survive.
//
// IPv4 is bound FIRST, the reverse of listenLoopback, because the kernel has to
// choose the number before the second family can be asked for it. The IPv6 bind
// is best-effort for the reason written there: on macOS `localhost` resolves to
// ::1 before 127.0.0.1, so holding only IPv4 can lose the callback to whoever
// holds the other family. A port drawn at random is not going to be one of
// those.
func claudeListen() ([]net.Listener, int, error) {
	if claudePort != 0 {
		lns, err := listenLoopback(claudePort, "close `claude` if it is signing in")
		return lns, claudePort, err
	}
	v4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	port := v4.Addr().(*net.TCPAddr).Port
	out := []net.Listener{v4}
	if v6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port)); err == nil {
		out = append(out, v6)
	}
	return out, port, nil
}

// claudeTokens is what a completed sign-in yields.
type claudeTokens struct {
	AccessToken  string
	RefreshToken string
	// ExpiresIn is seconds, as Anthropic reports it. The gateway turns it into
	// an instant on its own clock rather than trusting this machine's.
	ExpiresIn int64
}

// signInToClaude runs the loopback flow: bind the callback, open a browser,
// wait for the redirect, exchange the code. Nobody types anything.
func signInToClaude(ctx context.Context, open func(string), out io.Writer) (claudeTokens, error) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		return claudeTokens{}, err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return claudeTokens{}, err
	}

	listeners, port, err := claudeListen()
	if err != nil {
		// Wrapped, because the CALLER decides differently on this than on any
		// other failure here: a port that cannot be had means the loopback was
		// never viable and the printed-code flow should take over, where a
		// timeout or a refusal means a person answered and should be believed.
		return claudeTokens{}, fmt.Errorf("%w: %w", errNoLoopback, err)
	}
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	redirect := fmt.Sprintf("http://localhost:%d%s", port, claudeCallback)
	codes := make(chan string, 1)
	fails := make(chan error, 1)
	srv := &http.Server{Handler: callbackHandler(claudeCallback, state, "yas login -anthropic", codes, fails)}
	for _, ln := range listeners {
		go func(l net.Listener) { _ = srv.Serve(l) }(ln)
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	authorize := claudeAuthorizeURL(challenge, state, redirect)
	fmt.Fprintln(out, "Opening Claude to sign in…")
	fmt.Fprintln(out, "If no browser opens, visit:\n  "+authorize)
	open(authorize)

	var code string
	select {
	case code = <-codes:
	case err := <-fails:
		return claudeTokens{}, err
	case <-time.After(claudeSignInTimeout):
		return claudeTokens{}, errors.New("timed out waiting for the Claude sign-in to finish")
	case <-ctx.Done():
		return claudeTokens{}, ctx.Err()
	}
	return exchangeClaudeCode(ctx, code, verifier, redirect, state)
}

// signInToClaudeManual runs the flow that comes back through the person rather
// than through a port: Anthropic prints `<code>#<state>` and they bring it here.
//
// Reads with echo ON, deliberately. This is a one-time code and not a
// credential, and a hidden field is where a paste goes wrong silently — the
// screen has to show that something arrived.
func signInToClaudeManual(ctx context.Context, open func(string), out io.Writer, in io.Reader) (claudeTokens, error) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		return claudeTokens{}, err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return claudeTokens{}, err
	}

	authorize := claudeAuthorizeURL(challenge, state, claudeCodeRedirect)
	fmt.Fprintln(out, "Opening Claude to sign in…")
	fmt.Fprintln(out, "If no browser opens, visit:\n  "+authorize)
	open(authorize)
	fmt.Fprint(out, "Paste the code Claude shows you: ")

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return claudeTokens{}, fmt.Errorf("could not read the code: %w", err)
	}
	code, err := splitClaudeCode(strings.TrimSpace(line), state)
	if err != nil {
		return claudeTokens{}, err
	}
	return exchangeClaudeCode(ctx, code, verifier, claudeCodeRedirect, state)
}

// splitClaudeCode takes apart what the page prints and checks the half this
// terminal issued.
//
// The state check is the same CSRF guard the loopback handler makes, and it
// matters MORE here: a pasted string came through a person, who cannot be
// expected to notice that they copied it from the wrong tab. A code with no
// state at all is refused rather than tried, because a code minted for another
// account is exactly what that would look like.
func splitClaudeCode(pasted, state string) (string, error) {
	if pasted == "" {
		return "", errors.New("nothing entered")
	}
	code, gotState, ok := strings.Cut(pasted, "#")
	if !ok || strings.TrimSpace(gotState) == "" {
		return "", errors.New("that code is missing the part after the `#`; copy the whole string Claude shows")
	}
	if strings.TrimSpace(gotState) != state {
		return "", errors.New("that code did not come from this terminal; run `yas login -anthropic` again " +
			"and use the tab it opens")
	}
	if strings.TrimSpace(code) == "" {
		return "", errors.New("that code is empty before the `#`")
	}
	return strings.TrimSpace(code), nil
}

// claudeAuthorizeURL builds the authorize leg. One function for both modes, so
// the two cannot drift in a way that only one of them notices.
func claudeAuthorizeURL(challenge, state, redirect string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {claudeClientID},
		"redirect_uri":          {redirect},
		"scope":                 {claudeScope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	// Set on BOTH modes, which is easy to get wrong: it reads like "print the
	// code for me" and it is not. Anthropic's own client sets it
	// unconditionally and picks the mode with redirect_uri alone.
	q.Set("code", "true")
	return claudeAuthzURLVar + "?" + q.Encode()
}

// oauthError reads a failure out of `error`, whichever of TWO shapes it came
// in, and returns the machine-readable code and the human-readable detail.
//
// This is not defensive coding for its own sake. Anthropic's token endpoint
// answers in both shapes depending on what went wrong: the OAuth one from
// RFC 6749 §5.2, `{"error":"invalid_grant","error_description":"…"}`, and the
// Anthropic API envelope, `{"error":{"type":"rate_limit_error","message":"…"}}`.
// A rate-limited probe returned the second.
//
// Declaring `error` a string and meeting the object is not a wrong message —
// it is a DECODE failure, so the whole response is discarded and the caller
// reports "the answer could not be read" while holding an answer that said
// exactly what was wrong.
func oauthError(raw json.RawMessage, description string) (code, detail string) {
	if len(raw) == 0 {
		return "", description
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, description
	}
	var envelope struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		if description == "" {
			description = envelope.Message
		}
		return envelope.Type, description
	}
	return string(raw), description
}

// exchangeClaudeCode turns the authorization code into a token pair. The
// verifier proves this is the same process that started the flow, which is what
// makes a stolen code useless.
//
// JSON rather than a form, which is the one place this differs in shape from
// the Codex exchange beside it. Anthropic's token endpoint takes a JSON body
// for this client; sending a form buys an `invalid_request` that says nothing
// about why.
func exchangeClaudeCode(ctx context.Context, code, verifier, redirect, state string) (claudeTokens, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"client_id":     claudeClientID,
		"redirect_uri":  redirect,
		"code_verifier": verifier,
		"state":         state,
	})
	if err != nil {
		return claudeTokens{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeTokenURLVar, strings.NewReader(string(body)))
	if err != nil {
		return claudeTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return claudeTokens{}, err
	}
	defer resp.Body.Close()

	var out struct {
		AccessToken  string          `json:"access_token"`
		RefreshToken string          `json:"refresh_token"`
		ExpiresIn    int64           `json:"expires_in"`
		Error        json.RawMessage `json:"error"`
		Description  string          `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return claudeTokens{}, fmt.Errorf("anthropic's answer could not be read: %w", err)
	}
	code2, detail := oauthError(out.Error, out.Description)
	if resp.StatusCode/100 != 2 || code2 != "" {
		if detail == "" {
			detail = code2
		}
		return claudeTokens{}, fmt.Errorf("anthropic refused the sign-in (%d): %s", resp.StatusCode, detail)
	}
	if out.AccessToken == "" {
		return claudeTokens{}, errors.New("anthropic returned no access token")
	}
	// A refresh token is not optional for this fleet, and saying so here beats
	// discovering it at 03:00: without one the gateway cannot renew, and every
	// schedule on this credential would stop within hours.
	if out.RefreshToken == "" {
		return claudeTokens{}, errors.New("anthropic returned no refresh token, so this sign-in " +
			"could not be renewed and would stop working within hours")
	}
	return claudeTokens{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		ExpiresIn:    out.ExpiresIn,
	}, nil
}
