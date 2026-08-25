package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
	"github.com/Gilbert09/yas/clients/yas/internal/config"
)

// cmdLogin stores credentials in ~/.config/yas/config.json. The API key is
// read from the terminal with echo off — never argv, where every process on
// the machine can read it out of ps — and validated with one real List call
// before it is saved, so a typo fails here and not on the first `yas new`.
func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	anthropic := fs.Bool("anthropic", false, "store an Anthropic API key for new boxes (host-side proxy only; never enters a guest)")
	github := fs.Bool("github", false, "store a GitHub token for new boxes")
	openai := fs.Bool("openai", false, "store an OpenAI key for new boxes")
	paste := fs.Bool("key", false, "paste an existing yas_sk_ key instead of signing in with GitHub")
	device := fs.Bool("device", false, "use the GitHub device flow (for SSH sessions and browserless machines)")
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
		return storeProviderKey(cfg, "Anthropic API key: ", "anthropic")
	case *github:
		// The github token is no longer pasted at all: it comes from the
		// GitHub sign-in and lives server-side.
		return errors.New("github access now comes from `yas login` itself (the GitHub sign-in); there is nothing to paste")
	case *openai:
		return storeProviderKey(cfg, "OpenAI API key: ", "openai")
	default:
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

	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "saved")
	return nil
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
		var code string
		wl := &webLogin{Slug: slug, Out: os.Stderr}
		code, webErr = wl.Run(ctx)
		if webErr == nil {
			res, webErr = cl.SignupCode(ctx, code)
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
	what := "signed in"
	if res.TenantCreated {
		what = "account created"
	}
	fmt.Fprintf(os.Stderr, "%s as %s (tenant %s, key %s)\n", what, res.Login, res.TenantID, res.KeyID)
	fmt.Fprintln(os.Stderr, "your github access rides server-side from here; the laptop keeps only the yas key")
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
