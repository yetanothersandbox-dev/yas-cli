package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/config"
)

// cmdLogin stores credentials in ~/.config/yas/config.json. The API key is
// read from the terminal with echo off — never argv, where every process on
// the machine can read it out of ps — and validated with one real List call
// before it is saved, so a typo fails here and not on the first `yas new`.
func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	anthropic := fs.Bool("anthropic", false, "sign in to Claude in a browser, so the credential renews itself; -key pastes a console key instead")
	github := fs.Bool("github", false, "store a GitHub API token for boxes: the whole REST API, taken from the `gh` CLI when it is signed in")
	attach := fs.String("attach", "", "with -github: where it applies (all, box:<name>, profile:<id>); default keeps the existing attachment, or `all` on a first run")
	openai := fs.Bool("openai", false, "sign in to ChatGPT in a browser, so the credential renews itself; -key pastes a platform key instead")
	paste := fs.Bool("key", false, "paste an existing yas_sk_ key instead of signing in with GitHub")
	device := fs.Bool("device", false, "use the GitHub device flow (for SSH sessions and browserless machines)")
	manual := fs.Bool("manual", false, "with -anthropic: bring the code back by hand instead of through a loopback port (for SSH sessions)")
	force := fs.Bool("force", false, "sign in again even if this machine already holds a working key")
	baseURL := fs.String("url", "", "gateway base URL (default "+api.DefaultBaseURL+")")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *baseURL != "" {
		cfg.BaseURL = strings.TrimRight(*baseURL, "/")
	}

	switch {
	case *anthropic:
		return loginAnthropic(cfg, *paste, *manual)
	case *github:
		// NOT the same credential the GitHub sign-in leaves in custody, and
		// this used to refuse on the grounds that it was. The sign-in token is
		// a GitHub APP token, reaching only what that App is installed on;
		// this is the account's own, and storing it REPLACES the sign-in token
		// on every box rather than sitting beside it. See
		// storeGitHubIntegration.
		return storeGitHubIntegration(cfg, *attach)
	case *openai:
		return loginOpenAI(cfg, *paste)
	default:
		// Already signed in? Then do nothing, and say so.
		//
		// Every sign-in MINTS a key server-side, and only the SHA-256 of the
		// secret is ever stored — so a key cannot be handed back a second time,
		// and "reuse the existing one" has to mean "do not ask for another".
		// Without this, running `yas login` twice left two live credentials on
		// the account with the same name and no way to tell them apart, which
		// is what three identical `signup:` keys on one account turned out to
		// be.
		//
		// The check is a real authenticated call rather than "is a key
		// present": a revoked or rotated key is exactly the case where somebody
		// SHOULD be sent through the flow, and a config file cannot know that.
		if !*force && cfg.APIKey != "" {
			if login, ok := whoami(cfg); ok {
				fmt.Fprintf(os.Stderr, "already signed in as %s — `yas login -force` to sign in again\n", login)
				return nil
			}
		}

		// The front door. Self-serve when the build knows its GitHub app;
		// paste is always available (-key, or when no app is configured).
		clientID := githubClientID
		if v := strings.TrimSpace(os.Getenv("YAS_GITHUB_CLIENT_ID")); v != "" {
			clientID = v
		}
		if *paste || clientID == "" {
			if err := loginPaste(&cfg); err != nil {
				return err
			}
			break
		}
		if err := loginGitHub(&cfg, clientID, *device); err != nil {
			return err
		}
	}

	return config.Save(cfg)
}

func promptSecret(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", errors.New("nothing entered")
	}
	return v, nil
}

// loginPaste is the hand-a-key path: prompt, validate, one real List call
// before anything is saved.
func loginPaste(cfg *config.Config) error {
	v, err := promptSecret("yas API key (yas_sk_...): ")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(v, "yas_sk_") {
		return errors.New("that does not look like a yas key; they start with yas_sk_")
	}
	base := cfg.BaseURLResolved()
	if base == "" {
		base = api.DefaultBaseURL
	}
	probe := &api.Client{BaseURL: base, Key: v}
	if _, err := probe.List(context.Background()); err != nil {
		return fmt.Errorf("the gateway at %s refused this key: %w", base, err)
	}
	cfg.APIKey = v
	fmt.Fprintln(os.Stderr, "✓ Key saved")
	return nil
}

// loginGitHub is the self-serve path, and its DEFAULT is one GitHub
// interaction: the install page doubles as the sign-in (the app requests
// user authorization during installation), the browser bounces back to a
// loopback port with a code, and the gateway exchanges it. The device flow
// remains behind -device — and as the automatic fallback when no loopback
// port is free — for terminals whose browser is on another machine.
//
// Either way this is the CUSTODY handover: the GitHub credential goes to
// the gateway and is discarded here. The yas key arrives once and goes
// straight into the config file; nothing is ever printed.
func loginGitHub(cfg *config.Config, clientID string, forceDevice bool) error {
	ctx := context.Background()
	slug := githubAppSlug
	if v := strings.TrimSpace(os.Getenv("YAS_GITHUB_APP_SLUG")); v != "" {
		slug = v
	}
	base := cfg.BaseURLResolved()
	if base == "" {
		base = api.DefaultBaseURL
	}
	cl := &api.Client{BaseURL: base}

	var res api.SignupResult
	webErr := errors.New("no app slug in this build")
	if !forceDevice && slug != "" {
		wl := &webLogin{ClientID: clientID, Slug: slug, Out: os.Stderr}
		if webErr = wl.Start(); webErr == nil {
			var code string
			if code, webErr = wl.Authorize(ctx); webErr == nil {
				if res, webErr = cl.SignupCode(ctx, code); webErr == nil && res.Installations == 0 {
					wl.PromptInstall(ctx)
				}
			}
			wl.Close()
		}
	}
	if forceDevice || webErr != nil {
		if !forceDevice {
			fmt.Fprintf(os.Stderr, "falling back to the device flow (%v)\n", webErr)
		}
		fmt.Fprintln(os.Stderr, "Signing in with GitHub (ctrl-c to abort; `yas login -key` to paste a key instead)")
		flow := &deviceFlow{ClientID: clientID, Out: os.Stderr}
		pair, err := flow.Run(ctx)
		if err != nil {
			return err
		}
		flow.promptInstall(ctx, pair.AccessToken, slug, "", os.Stdin)
		if res, err = cl.Signup(ctx, pair.AccessToken, pair.RefreshToken, pair.ExpiresIn); err != nil {
			return fmt.Errorf("the gateway refused the signup: %w", err)
		}
	}
	cfg.APIKey = res.Key
	if res.TenantCreated {
		fmt.Fprintf(os.Stderr, "✓ Account created — signed in as %s\n", res.Login)
	} else {
		fmt.Fprintf(os.Stderr, "✓ Signed in as %s\n", res.Login)
	}
	return nil
}

// storeProviderKey sends a provider key into the gateway's custody. It needs
// a working yas key first — custody hangs off the user account.
func storeProviderKey(cfg config.Config, prompt, which string) error {
	key := cfg.APIKeyResolved()
	if key == "" {
		return errors.New("no API key yet; run `yas login` first — provider keys are stored on your account")
	}
	v, err := promptSecret(prompt)
	if err != nil {
		return err
	}
	base := cfg.BaseURLResolved()
	if base == "" {
		base = api.DefaultBaseURL
	}
	cl := &api.Client{BaseURL: base, Key: key}
	var anthropic, openai *string
	if which == "anthropic" {
		anthropic = &v
	} else {
		openai = &v
	}
	if err := cl.PutUserCredentials(context.Background(), anthropic, openai); err != nil {
		return fmt.Errorf("the gateway refused to store it: %w", err)
	}
	fmt.Fprintln(os.Stderr, "stored server-side; new boxes get it automatically")
	return nil
}

// loginOpenAI stores an OpenAI credential, and prefers the one that keeps
// working.
//
// # Why the browser is the default here
//
// A ChatGPT plan is the credential most people with Codex actually have, and it
// is the one a paste cannot hold: the token it gives you lives hours, so a
// pasted string stops working the same afternoon and every schedule on it stops
// with it. The sign-in yields a REFRESH token too, which the gateway keeps and
// renews — so the credential survives the terminal that created it.
//
// A platform key (`sk-…`) has no such problem and no sign-in: it is pasted, as
// before, behind -key. Both land in the same field; the fleet tells them apart
// by shape, so nothing downstream has to be told which was used.
//
// The browser flow needs a loopback port OpenAI registered, so it cannot work
// over a bare SSH session. That falls back to the paste rather than failing,
// with the trade named — a pasted subscription token is better than no
// credential, for as long as it lasts.
func loginOpenAI(cfg config.Config, forcePaste bool) error {
	if cfg.APIKeyResolved() == "" {
		return errors.New("no API key yet; run `yas login` first — provider keys are stored on your account")
	}
	if forcePaste {
		return storeProviderKey(cfg, "OpenAI API key: ", "openai")
	}

	tok, err := signInToCodex(context.Background(), osOpenBrowser, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "the ChatGPT sign-in did not finish (%v)\n", err)
		fmt.Fprintln(os.Stderr, "falling back to a pasted key — a platform key (sk-…) never expires;")
		fmt.Fprintln(os.Stderr, "a pasted ChatGPT token will stop working in a few hours.")
		return storeProviderKey(cfg, "OpenAI API key: ", "openai")
	}

	base := cfg.BaseURLResolved()
	if base == "" {
		base = api.DefaultBaseURL
	}
	cl := &api.Client{BaseURL: base, Key: cfg.APIKeyResolved()}
	if err := cl.PutOpenAIOAuth(context.Background(), tok.AccessToken, tok.RefreshToken, tok.ExpiresIn); err != nil {
		return fmt.Errorf("the gateway refused to store the sign-in: %w", err)
	}
	fmt.Fprintln(os.Stderr, "✓ Signed in with ChatGPT — stored server-side and renewed for you")
	fmt.Fprintln(os.Stderr, "  new boxes get it automatically; nothing was written to this machine")
	return nil
}

// loginAnthropic stores an Anthropic credential, and prefers the one that keeps
// working.
//
// # Why the browser is the default here
//
// The twin of loginOpenAI below, arrived at the same way. A Claude subscription
// is the credential most people with Claude Code actually have, and it is the
// one a paste cannot hold: the token it gives you lives hours, so a pasted
// string stops working the same afternoon and every schedule on it stops with
// it. The sign-in yields a REFRESH token too, which the gateway keeps and
// renews — so the credential survives the terminal that created it.
//
// A console key (`sk-ant-api03-…`) has no such problem and no sign-in: it is
// pasted, as before, behind -key. Both land in the same field; the fleet tells
// them apart by shape (proxy.AnthropicAuthFor), so nothing downstream has to be
// told which was used.
//
// The loopback flow needs a port, so it cannot work over a bare SSH session.
// That falls back to Anthropic's print-the-code mode rather than to a paste of a
// credential — a one-time code is worthless without the verifier this process
// holds — and only a failure THERE falls back to the key.
func loginAnthropic(cfg config.Config, forcePaste, manual bool) error {
	if cfg.APIKeyResolved() == "" {
		return errors.New("no API key yet; run `yas login` first — provider keys are stored on your account")
	}
	if forcePaste {
		return storeProviderKey(cfg, "Anthropic API key: ", "anthropic")
	}

	ctx := context.Background()
	tok, err := claudeSignIn(ctx, manual)
	if err != nil {
		fmt.Fprintf(os.Stderr, "the Claude sign-in did not finish (%v)\n", err)
		fmt.Fprintln(os.Stderr, "falling back to a pasted key — a console key (sk-ant-api03-…) never expires;")
		fmt.Fprintln(os.Stderr, "a pasted subscription token will stop working in a few hours.")
		return storeProviderKey(cfg, "Anthropic API key: ", "anthropic")
	}

	base := cfg.BaseURLResolved()
	if base == "" {
		base = api.DefaultBaseURL
	}
	cl := &api.Client{BaseURL: base, Key: cfg.APIKeyResolved()}
	if err := cl.PutAnthropicOAuth(ctx, tok.AccessToken, tok.RefreshToken, tok.ExpiresIn); err != nil {
		return fmt.Errorf("the gateway refused to store the sign-in: %w", err)
	}
	fmt.Fprintln(os.Stderr, "\u2713 Signed in with Claude — stored server-side and renewed for you")
	fmt.Fprintln(os.Stderr, "  new boxes get it automatically; nothing was written to this machine")
	return nil
}

// claudeSignIn picks a way back from the browser.
//
// The switch to the printed code happens ONLY when the loopback was never
// viable — no port, so no redirect can reach this process. That distinction is
// the whole design of this function: a sign-in that timed out or was cancelled
// means somebody walked away or said no, and opening a second browser tab at
// them is not a recovery. Falling forward on a dead port is; falling forward on
// a human decision is not.
func claudeSignIn(ctx context.Context, manual bool) (claudeTokens, error) {
	if manual {
		return signInToClaudeManual(ctx, osOpenBrowser, os.Stderr, os.Stdin)
	}
	tok, err := signInToClaude(ctx, osOpenBrowser, os.Stderr)
	if !errors.Is(err, errNoLoopback) {
		return tok, err
	}
	fmt.Fprintf(os.Stderr, "no loopback port for the sign-in (%v)\n", err)
	fmt.Fprintln(os.Stderr, "using the code Claude prints for you instead")
	return signInToClaudeManual(ctx, osOpenBrowser, os.Stderr, os.Stdin)
}

// whoami reports the account a stored key belongs to, and whether it still
// works at all.
//
// Deliberately quiet about WHY it failed. A network blip and a revoked key both
// come back false, and both lead to the same place — the sign-in flow — so
// distinguishing them here would only add a message before an identical
// outcome. The one case that must not happen is being told "already signed in"
// while holding a key the server has revoked.
func whoami(cfg config.Config) (string, bool) {
	key := cfg.APIKeyResolved()
	base := cfg.BaseURLResolved()
	if key == "" {
		return "", false
	}
	if base == "" {
		base = api.DefaultBaseURL
	}
	cl := &api.Client{BaseURL: base, Key: key}

	// Bounded, because this runs BEFORE the sign-in a person asked for. A
	// gateway that hangs must cost a few seconds and then let them sign in, not
	// block the command they typed.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	who, err := cl.Whoami(ctx)
	if err != nil {
		return "", false
	}
	if who.Login != "" {
		return who.Login, true
	}
	return who.TenantID, true
}
