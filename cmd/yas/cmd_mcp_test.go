package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

// Only the default and error branches are driven from cmd/: the real
// subcommands call loadClient() and would reach the network. What is asserted
// here is the dispatch and the pure helpers.
func TestMcpRejectsAnUnknownSubcommand(t *testing.T) {
	err := cmdMcp([]string{"frobnicate"})
	if err == nil {
		t.Fatal("an unknown subcommand was accepted")
	}
	// The refusal names every subcommand there is, so somebody who typed the
	// wrong one does not have to go and look.
	for _, want := range []string{"list", "add", "rm", "test", "attach", "detach"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestMcpAttachNeedsBothArguments(t *testing.T) {
	for _, args := range [][]string{{}, {"linear"}, {"linear", "all", "extra"}} {
		if err := cmdMcp(append([]string{"attach"}, args...)); err == nil {
			t.Errorf("attach %v was accepted", args)
		}
	}
}

func TestMcpRmNeedsExactlyOneName(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}} {
		if err := cmdMcp(append([]string{"rm"}, args...)); err == nil {
			t.Errorf("rm %v was accepted", args)
		}
	}
}

func TestParseInlineMcp(t *testing.T) {
	env := map[string]string{
		"YAS_MCP_SECRET_LINEAR":     "lin_api_x",
		"YAS_MCP_SECRET_MY_TRACKER": "tok_y",
	}
	lookup := func(k string) string { return env[k] }

	for _, tc := range []struct {
		name  string
		in    []string
		want  []api.McpServerInline
		error string
	}{
		{
			name: "one server, key from the environment",
			in:   []string{"linear=https://mcp.linear.app/mcp"},
			want: []api.McpServerInline{
				{Name: "linear", URL: "https://mcp.linear.app/mcp", Secret: "lin_api_x"},
			},
		},
		{
			// A hyphen in the name becomes an underscore in the variable, which
			// is the only spelling a shell will accept.
			name: "a hyphenated name",
			in:   []string{"my-tracker=https://mcp.example.dev/mcp"},
			want: []api.McpServerInline{
				{Name: "my-tracker", URL: "https://mcp.example.dev/mcp", Secret: "tok_y"},
			},
		},
		{
			// A server that needs no key is legitimate.
			name: "no key stored",
			in:   []string{"open=https://mcp.example.dev/mcp"},
			want: []api.McpServerInline{{Name: "open", URL: "https://mcp.example.dev/mcp"}},
		},
		{
			// The URL may contain an `=`, so only the FIRST one splits.
			name: "an equals in the url",
			in:   []string{"x=https://mcp.example.dev/mcp"},
			want: []api.McpServerInline{{Name: "x", URL: "https://mcp.example.dev/mcp"}},
		},
		{name: "no equals", in: []string{"linear"}, error: "not name=url"},
		{name: "no name", in: []string{"=https://x/mcp"}, error: "not name=url"},
		{name: "no url", in: []string{"linear="}, error: "not name=url"},
		{
			name:  "the same name twice",
			in:    []string{"linear=https://a/mcp", "linear=https://b/mcp"},
			error: "twice",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseInlineMcp(tc.in, lookup)
			if tc.error != "" {
				if err == nil {
					t.Fatalf("accepted %v", tc.in)
				}
				if !strings.Contains(err.Error(), tc.error) {
					t.Fatalf("error %q does not contain %q", err, tc.error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The key is read from the environment and never from argv, because a
// credential on the command line is a credential in the shell history and in
// the process table.
func TestInlineMcpSecretVar(t *testing.T) {
	for in, want := range map[string]string{
		"linear":     "YAS_MCP_SECRET_LINEAR",
		"my-tracker": "YAS_MCP_SECRET_MY_TRACKER",
		"a-b-c":      "YAS_MCP_SECRET_A_B_C",
	} {
		if got := inlineMcpSecretVar(in); got != want {
			t.Errorf("inlineMcpSecretVar(%q) = %q, want %q", in, got, want)
		}
	}
}

// `mcp` shadows any program of that name inside a box, so it being in the
// reserved set is a compatibility decision worth pinning.
func TestMcpIsAReservedVerb(t *testing.T) {
	if _, ok := verbs["mcp"]; !ok {
		t.Fatal("mcp is not a verb, so `yas mcp list` would try to run `mcp` inside a box")
	}
}
