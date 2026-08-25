package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Env beats file beats built-in, for every resolved field. The table is the
// test: a resolver added without a row here is a resolver whose precedence
// nobody decided.
func TestPrecedence(t *testing.T) {
	cfg := Config{
		APIKey: "yas_sk_file", BaseURL: "https://file.example",
		AnthropicKey: "sk-ant-file", GitHubToken: "ghp_file", OpenAIKey: "sk-file",
	}
	cases := []struct {
		name    string
		env     map[string]string
		resolve func() string
		want    string
	}{
		{"api key env wins", map[string]string{"YAS_API_KEY": "yas_sk_env"}, cfg.APIKeyResolved, "yas_sk_env"},
		{"api key file", nil, cfg.APIKeyResolved, "yas_sk_file"},
		{"base url env wins", map[string]string{"YAS_BASE_URL": "https://env.example"}, cfg.BaseURLResolved, "https://env.example"},
		{"base url file", nil, cfg.BaseURLResolved, "https://file.example"},
		{"anthropic env wins", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-env"}, cfg.AnthropicKeyResolved, "sk-ant-env"},
		{"anthropic file", nil, cfg.AnthropicKeyResolved, "sk-ant-file"},
		{"github GITHUB_TOKEN wins", map[string]string{"GITHUB_TOKEN": "ghp_env"}, cfg.GitHubTokenResolved, "ghp_env"},
		{"github GH_TOKEN second", map[string]string{"GH_TOKEN": "gho_env"}, cfg.GitHubTokenResolved, "gho_env"},
		{"github file", nil, cfg.GitHubTokenResolved, "ghp_file"},
		{"openai env wins", map[string]string{"OPENAI_API_KEY": "sk-env"}, cfg.OpenAIKeyResolved, "sk-env"},
	}
	envKeys := []string{"YAS_API_KEY", "YAS_BASE_URL", "ANTHROPIC_API_KEY", "GITHUB_TOKEN", "GH_TOKEN", "OPENAI_API_KEY"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range envKeys {
				t.Setenv(k, "")
				os.Unsetenv(k)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := tc.resolve(); got != tc.want {
				t.Fatalf("resolved %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSaveIsPrivateAndRoundTrips(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	in := Config{APIKey: "yas_sk_x", LastBox: "brisk-otter-4f2a", Defaults: Defaults{MemMiB: 2048}}
	if err := Save(in); err != nil {
		t.Fatal(err)
	}
	dir, _ := Dir()
	st, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config perms = %o; it holds keys and must be 0600", st.Mode().Perm())
	}
	out, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip lost data: %+v != %+v", out, in)
	}
}

func TestMissingFileIsAnEmptyConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c != (Config{}) {
		t.Fatalf("missing file resolved to %+v", c)
	}
}
