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
