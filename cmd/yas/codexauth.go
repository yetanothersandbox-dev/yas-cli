package main

import (
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
	verifier, challenge, err := pkcePair()
	if err != nil {
		return codexTokens{}, err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return codexTokens{}, err
	}

	listeners, err := listenLoopback(codexPort, "close `codex login`, the Codex CLI or "+
		"another tool using the same sign-in")
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
	srv := &http.Server{Handler: callbackHandler(codexCallback, state, "yas login -openai", codes, fails)}
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
