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
		v, err := promptSecret("Anthropic API key: ")
		if err != nil {
			return err
		}
		cfg.AnthropicKey = v
	case *github:
		v, err := promptSecret("GitHub token: ")
		if err != nil {
			return err
		}
		cfg.GitHubToken = v
	case *openai:
		v, err := promptSecret("OpenAI API key: ")
		if err != nil {
			return err
		}
		cfg.OpenAIKey = v
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
		if err := loginGitHub(&cfg, clientID); err != nil {
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

// loginGitHub is the self-serve path: device flow, then /v1/signup. The
// secret arrives once and goes straight into the config file; it is never
// printed.
func loginGitHub(cfg *config.Config, clientID string) error {
	ctx := context.Background()
	fmt.Fprintln(os.Stderr, "Signing in with GitHub (ctrl-c to abort; `yas login -key` to paste a key instead)")
	flow := &deviceFlow{ClientID: clientID, Out: os.Stderr}
	token, err := flow.Run(ctx)
	if err != nil {
		return err
	}
	base := cfg.BaseURLResolved()
	if base == "" {
		base = api.DefaultBaseURL
	}
	cl := &api.Client{BaseURL: base}
	res, err := cl.Signup(ctx, token)
	if err != nil {
		return fmt.Errorf("the gateway refused the signup: %w", err)
	}
	cfg.APIKey = res.Key
	what := "signed in"
	if res.TenantCreated {
		what = "account created"
	}
	fmt.Fprintf(os.Stderr, "%s as %s (tenant %s, key %s)\n", what, res.Login, res.TenantID, res.KeyID)
	return nil
}
