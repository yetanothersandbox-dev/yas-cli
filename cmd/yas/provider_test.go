package main

import "testing"

// The provider is fixed when a box is created and can never be changed, so a
// typo here is a box that has to be thrown away. These pin the two rules that
// decide it.

func TestParseProvider(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		// Empty is the wire spelling of Anthropic, and always has been. Both
		// forms must produce it, or an explicit `-provider anthropic` would
		// send a field every older gateway ignores.
		{in: "", want: ""},
		{in: "anthropic", want: ""},
		{in: "Anthropic", want: ""},
		{in: "  anthropic  ", want: ""},
		{in: "openai", want: "openai"},
		{in: "OpenAI", want: "openai"},
		{in: " openai ", want: "openai"},
		// Products and model ids are not providers. Refused here rather than at
		// the server, so the message arrives before a create does.
		{in: "codex", wantErr: true},
		{in: "claude", wantErr: true},
		{in: "gpt-5", wantErr: true},
		{in: "gemini", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseProvider(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseProvider(%q) = %q, want an error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProvider(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("parseProvider(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// `yas <anything>` runs <anything> in a box, and the box is wired to one
// provider before it boots — so for the agent CLIs the word typed is the only
// statement of intent there is. Without this, `yas codex` made an Anthropic box
// and codex failed in it as a network timeout.
func TestProviderForCommand(t *testing.T) {
	cases := map[string]string{
		"codex": "openai",
		// Anthropic is the default, so `yas claude` sends nothing — the same
		// body it has always sent.
		"claude": "",
		"bash":   "",
		"vim":    "",
		"":       "",
	}
	for cmd, want := range cases {
		t.Run(cmd, func(t *testing.T) {
			if got := providerForCommand(cmd); got != want {
				t.Errorf("providerForCommand(%q) = %q, want %q", cmd, got, want)
			}
		})
	}
}

// Whatever providerForCommand returns has to be a value parseProvider accepts,
// or `yas codex` would build a create body the server refuses.
func TestEveryCommandProviderIsOneTheServerTakes(t *testing.T) {
	for _, cmd := range []string{"codex", "claude", "bash"} {
		got := providerForCommand(cmd)
		parsed, err := parseProvider(got)
		if err != nil {
			t.Fatalf("providerForCommand(%q) = %q, which parseProvider refuses: %v", cmd, got, err)
		}
		if parsed != got {
			t.Errorf("providerForCommand(%q) = %q but parseProvider normalises it to %q", cmd, got, parsed)
		}
	}
}
