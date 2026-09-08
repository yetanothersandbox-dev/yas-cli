package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

func whoamiServer(t *testing.T, status int, body string) *api.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/user" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &api.Client{BaseURL: srv.URL, Key: "yas_sk_test"}
}

// Only the agents are gated. `yas bash` needs no model, and refusing it because
// nobody stored an LLM key would be a worse bug than the one this prevents.
func TestOnlyTheAgentsNeedACredential(t *testing.T) {
	for cmd, want := range map[string]string{
		"claude": "anthropic",
		"codex":  "openai",
		"bash":   "",
		"htop":   "",
		"vim":    "",
		"":       "",
	} {
		if got := credentialFor(cmd); got != want {
			t.Errorf("credentialFor(%q) = %q, want %q", cmd, got, want)
		}
	}
}

// The point of the whole thing: refuse BEFORE a box exists, and say what to run.
func TestAnAgentWithNoCredentialIsRefusedBeforeABoxIsMade(t *testing.T) {
	for _, tc := range []struct {
		cmd, body, wants string
	}{
		{"claude", `{"anthropicKey":false,"openaiKey":true}`, "yas login -anthropic"},
		{"codex", `{"anthropicKey":true,"openaiKey":false}`, "yas login -openai"},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			cl := whoamiServer(t, http.StatusOK, tc.body)
			err := requireProviderCredential(context.Background(), cl, tc.cmd)
			if err == nil {
				t.Fatalf("%s was allowed to build a box with no credential", tc.cmd)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the refusal does not name the fix: %v", err)
			}
			// The other question somebody asks is whether they are paying for
			// something that did not work.
			if !strings.Contains(err.Error(), "no box was created") {
				t.Errorf("the refusal does not say nothing was created: %v", err)
			}
		})
	}
}

func TestAnAgentWithItsCredentialIsAllowed(t *testing.T) {
	cl := whoamiServer(t, http.StatusOK, `{"anthropicKey":true,"openaiKey":true}`)
	for _, cmd := range []string{"claude", "codex", "bash"} {
		if err := requireProviderCredential(context.Background(), cl, cmd); err != nil {
			t.Errorf("%s was refused despite a stored credential: %v", cmd, err)
		}
	}
}

// It FAILS OPEN. An operator tenant has no user row (409), an older gateway may
// not serve the route, and a network blip is a network blip — none of those are
// evidence that a credential is missing, and blocking on a failed probe would
// turn a working setup into a broken one.
func TestAFailedProbeDoesNotBlockAnything(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"an operator tenant with no user row", http.StatusConflict, `{"error":"no_user"}`},
		{"a gateway that does not serve the route", http.StatusNotFound, `{"error":"not_found"}`},
		{"an answer that is not JSON", http.StatusOK, `<html>`},
		{"a server error", http.StatusInternalServerError, `{"error":"boom"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl := whoamiServer(t, tc.status, tc.body)
			if err := requireProviderCredential(context.Background(), cl, "claude"); err != nil {
				t.Errorf("a failed probe blocked the command: %v", err)
			}
		})
	}
	// And an unreachable gateway.
	dead := &api.Client{BaseURL: "http://127.0.0.1:1", Key: "k"}
	if err := requireProviderCredential(context.Background(), dead, "claude"); err != nil {
		t.Errorf("an unreachable gateway blocked the command: %v", err)
	}
}

// The check must not be the thing that tells somebody their key leaked.
func TestTheRefusalNamesNoSecret(t *testing.T) {
	cl := whoamiServer(t, http.StatusOK, `{"anthropicKey":false}`)
	err := requireProviderCredential(context.Background(), cl, "claude")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	var probe map[string]any
	_ = json.Unmarshal([]byte(`{}`), &probe)
	for _, bad := range []string{"sk-", "yas_sk_"} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("the refusal contains something key-shaped (%q): %v", bad, err)
		}
	}
}
