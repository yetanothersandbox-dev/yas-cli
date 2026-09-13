package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

func TestMcpServerAttachmentUpdates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial []string
		spec    string
		add     bool
		want    []string
		body    string
	}{
		{"detach last", []string{"all"}, "all", false, []string{}, `{"attach":[]}`},
		{"detach one", []string{"all", "profile:review"}, "all", false, []string{"profile:review"}, `{"attach":["profile:review"]}`},
		{"attach first", nil, "all", true, []string{"all"}, `{"attach":["all"]}`},
		{"attach another", []string{"all"}, "profile:review", true, []string{"all", "profile:review"}, `{"attach":["all","profile:review"]}`},
		{"attach existing", []string{"all"}, "all", true, []string{"all"}, `{"attach":["all"]}`},
		{"unrelated update", []string{"all"}, "", false, []string{"all"}, `{"description":"new"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := api.McpServer{
				ID: "linear", Description: "old", HasSecret: true,
				URL: "https://mcp.linear.app/mcp", Attach: tc.initial,
			}
			var body json.RawMessage
			var methods []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/mcp-servers/linear" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				methods = append(methods, r.Method)
				if r.Method == http.MethodPut {
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					var req api.McpServerRequest
					if err := json.Unmarshal(body, &req); err != nil {
						t.Error(err)
					}
					if req.Attach != nil {
						stored.Attach = req.Attach
					}
					if req.Description != "" {
						stored.Description = req.Description
					}
				}
				_ = json.NewEncoder(w).Encode(stored)
			}))
			defer srv.Close()
			cl := &api.Client{BaseURL: srv.URL}
			var err error
			if tc.spec == "" {
				_, err = cl.PutMcpServer(context.Background(), "linear", api.McpServerRequest{Description: "new"})
			} else {
				_, err = cl.AttachMcpTo(context.Background(), "linear", tc.spec, tc.add)
			}
			if err != nil {
				t.Fatal(err)
			}
			srv.Close()
			if string(body) != tc.body {
				t.Errorf("PUT body = %s, want %s", body, tc.body)
			}
			wantMethods := []string{http.MethodPut}
			if tc.spec != "" {
				wantMethods = append([]string{http.MethodGet}, wantMethods...)
			}
			if !reflect.DeepEqual(methods, wantMethods) {
				t.Errorf("methods = %v, want %v", methods, wantMethods)
			}
			if !reflect.DeepEqual(stored.Attach, tc.want) {
				t.Errorf("attachments = %v, want %v", stored.Attach, tc.want)
			}
			// A read-modify-write that changed the endpoint or disarmed the key
			// would be the worst possible bug in this function.
			if !stored.HasSecret || stored.URL != "https://mcp.linear.app/mcp" {
				t.Errorf("unrelated settings changed: %+v", stored)
			}
		})
	}
}

// The URL is what a human typed, and it must reach the API unaltered — a
// normalised one is a URL the dashboard cannot echo back to whoever wrote it.
func TestPutMcpServerSendsTheURLVerbatim(t *testing.T) {
	var got api.McpServerRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(api.McpServer{ID: "linear", URL: got.URL})
	}))
	defer srv.Close()
	secret := "lin_api_x"
	cl := &api.Client{BaseURL: srv.URL}
	if _, err := cl.PutMcpServer(context.Background(), "linear", api.McpServerRequest{
		URL: "https://api.example.dev/v1/mcp", Secret: &secret,
	}); err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://api.example.dev/v1/mcp" {
		t.Fatalf("url = %q", got.URL)
	}
}

// There is no Secret field on the read type and there must not be one: the API
// returns no credential, so somewhere to put one would suggest it might.
func TestMcpServerHasNoPlaceToPutACredential(t *testing.T) {
	b, err := json.Marshal(api.McpServer{ID: "linear", URL: "https://x/mcp", HasSecret: true})
	if err != nil {
		t.Fatal(err)
	}
	// The KEYS, not the substrings: `hasSecret` legitimately contains "secret"
	// and is the whole point of the type.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		switch strings.ToLower(name) {
		case "secret", "token", "key", "apikey", "authorization", "headers":
			t.Errorf("McpServer has a %q field; the API returns no credential", name)
		}
	}
	if string(fields["hasSecret"]) != "true" {
		t.Errorf("presence is not reported: %s", b)
	}
}
