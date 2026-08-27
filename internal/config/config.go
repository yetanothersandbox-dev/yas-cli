// Package config is the CLI's one file on disk: ~/.config/yas/config.json.
//
// Environment always beats the file (YAS_API_KEY, YAS_BASE_URL, and the
// provider keys at create time), so a script can override without editing
// anything, and the file never has to exist at all for a fully env-configured
// caller. Secrets live in the file or the environment and never in argv,
// where every process on the machine can read them out of ps.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Defaults struct {
	MemMiB int `json:"memMib,omitempty"`
	// VcpuCount is the old default, in WHOLE vCPUs; MilliVcpu is thousandths
	// of one. Both are read so an existing config file keeps meaning what it
	// meant, and the milli one wins when both are set.
	VcpuCount      int `json:"vcpuCount,omitempty"`
	MilliVcpu      int `json:"milliVcpu,omitempty"`
	DiskMiB        int `json:"diskMib,omitempty"`
	IdleTtlSec     int `json:"idleTtlSec,omitempty"`
	MaxLifetimeSec int `json:"maxLifetimeSec,omitempty"`
}

type Config struct {
	APIKey  string `json:"apiKey,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
	// Provider credentials, passed to the CREATE body only. They configure the
	// host-side credential proxy and never enter a guest — which is what makes
	// them safe to send at all.
	AnthropicKey string `json:"anthropicKey,omitempty"`
	GitHubToken  string `json:"githubToken,omitempty"`
	OpenAIKey    string `json:"openaiKey,omitempty"`
	// SSHIdentity overrides the dedicated key path; empty means the managed
	// ~/.config/yas/id_ed25519.
	SSHIdentity string   `json:"sshIdentity,omitempty"`
	Defaults    Defaults `json:"defaults,omitempty"`
	LastBox     string   `json:"lastBox,omitempty"`
}

// Dir is where the config and the dedicated ssh key live. XDG_CONFIG_HOME is
// honoured because the people who set it mean it.
func Dir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "yas"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "yas"), nil
}

func path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// Load reads the file; a missing file is an empty config, not an error —
// `yas login` is what creates it, and env-only callers never do.
func Load() (Config, error) {
	var c Config
	p, err := path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s is not valid JSON: %w", p, err)
	}
	return c, nil
}

// Save writes the file 0600 in a 0700 directory: it holds keys.
func Save(c Config) error {
	d, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "config.json"), append(b, '\n'), 0o600)
}

// APIKeyResolved is the key requests carry: env first, then the file.
func (c Config) APIKeyResolved() string {
	if v := os.Getenv("YAS_API_KEY"); v != "" {
		return v
	}
	return c.APIKey
}

// BaseURLResolved is the gateway: env, then file, then the product default.
func (c Config) BaseURLResolved() string {
	if v := os.Getenv("YAS_BASE_URL"); v != "" {
		return v
	}
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return ""
}

// AnthropicKeyResolved (and the two below) feed the create body: the ambient
// env var a developer already has wins over the stored one.
func (c Config) AnthropicKeyResolved() string {
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		return v
	}
	return c.AnthropicKey
}

func (c Config) GitHubTokenResolved() string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return c.GitHubToken
}

func (c Config) OpenAIKeyResolved() string {
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		return v
	}
	return c.OpenAIKey
}
