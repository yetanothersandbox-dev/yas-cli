package api_test

import (
	"context"
	"encoding/json"
	"io"
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

// The grant's shape is decoded off a real response body, because a JSON tag
// that never matched is invisible from a round trip through the struct.
func TestMcpServerDecodesTheGrant(t *testing.T) {
	body := `{
		"id":"strava","url":"https://mcp.strava.com/mcp","transport":"http",
		"hasSecret":true,"attach":["all"],
		"expiresAt":"2026-10-01T00:00:00Z","lapsed":false,
		"createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-14T09:00:00Z",
		"oauth":{"status":"connected","issuer":"https://www.strava.com",
			"scopes":["read","activity:read"],"clientId":"abc123","clientSource":"dcr",
			"expiresAt":"2026-09-14T15:00:00Z","checkedAt":"2026-09-14T09:00:00Z"}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := (&api.Client{BaseURL: srv.URL}).McpServer(context.Background(), "strava")
	if err != nil {
		t.Fatal(err)
	}
	if got.OAuth == nil {
		t.Fatal("the grant did not decode at all")
	}
	if got.OAuth.Status != api.McpOAuthConnected || got.OAuth.Issuer != "https://www.strava.com" {
		t.Errorf("grant = %+v", *got.OAuth)
	}
	if !reflect.DeepEqual(got.OAuth.Scopes, []string{"read", "activity:read"}) {
		t.Errorf("scopes = %v", got.OAuth.Scopes)
	}
	if got.OAuth.ClientID != "abc123" || got.OAuth.ClientSource != "dcr" {
		t.Errorf("client = %+v", *got.OAuth)
	}
	// THE TWO EXPIRIES ARE DIFFERENT THINGS. The row's is the attachment
	// lapsing; the grant's is the access token running out. Reading one as the
	// other reports a healthy server as expiring hourly.
	if got.ExpiresAt == nil || got.OAuth.ExpiresAt == nil || got.ExpiresAt.Equal(*got.OAuth.ExpiresAt) {
		t.Errorf("the row's lapse and the token's life were conflated: %v vs %v", got.ExpiresAt, got.OAuth.ExpiresAt)
	}
	// A server whose credential was pasted has NO grant, and that nil is how
	// every reader tells the two kinds apart.
	if pasted := (api.McpServer{HasSecret: true}); pasted.OAuth != nil {
		t.Error("a row with no oauth key decoded a non-nil grant")
	}
}

// One thin method per route, and the route is the part a test can pin: a path
// that is wrong by one segment is a 404 nobody can read.
func TestMcpConnectRoutes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		call       func(*api.Client) error
		wantMethod string
		wantPath   string
		wantBody   string
	}{
		{
			name: "connect, with no next",
			call: func(c *api.Client) error {
				_, err := c.ConnectMcpServer(context.Background(), "my server", "")
				return err
			},
			wantMethod: http.MethodPost,
			// The id is path-escaped, so a name with a space reaches the route
			// it was meant for rather than splitting the path.
			wantPath: "/v1/mcp-servers/my%20server/connect",
			// An absent next is OMITTED rather than sent empty, so the gateway
			// reads "the CLI's case" and serves its own landing page.
			wantBody: `{}`,
		},
		{
			name: "connect, with a next",
			call: func(c *api.Client) error {
				_, err := c.ConnectMcpServer(context.Background(), "strava", "/app/mcp")
				return err
			},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/mcp-servers/strava/connect",
			wantBody:   `{"next":"/app/mcp"}`,
		},
		{
			name: "the poll",
			call: func(c *api.Client) error {
				_, err := c.McpConnectStatus(context.Background(), "strava", "flow/1")
				return err
			},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/mcp-servers/strava/connect/flow%2F1",
		},
		{
			name:       "disconnect",
			call:       func(c *api.Client) error { return c.DisconnectMcpServer(context.Background(), "strava") },
			wantMethod: http.MethodPost,
			wantPath:   "/v1/mcp-servers/strava/disconnect",
			wantBody:   `{}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var method, path, body string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method, path = r.Method, r.URL.EscapedPath()
				b, _ := io.ReadAll(r.Body)
				body = string(b)
				_, _ = w.Write([]byte(`{"status":"pending"}`))
			}))
			defer srv.Close()

			if err := tc.call(&api.Client{BaseURL: srv.URL}); err != nil {
				t.Fatal(err)
			}
			if method != tc.wantMethod {
				t.Errorf("method = %s, want %s", method, tc.wantMethod)
			}
			if path != tc.wantPath {
				t.Errorf("path = %s, want %s", path, tc.wantPath)
			}
			if body != tc.wantBody {
				t.Errorf("body = %s, want %s", body, tc.wantBody)
			}
		})
	}
}

// The start of a flow decodes whole. flowId is the one field without which the
// poll cannot ask anything at all.
func TestConnectMcpServerDecodesTheStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"flowId":"f1","authorizeUrl":"https://www.strava.com/oauth/authorize?x=1",
			"expiresAt":"2026-09-14T09:10:00Z","scopes":["read"],"issuer":"https://www.strava.com",
			"clientSource":"dcr"}`))
	}))
	defer srv.Close()

	got, err := (&api.Client{BaseURL: srv.URL}).ConnectMcpServer(context.Background(), "strava", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.FlowID != "f1" || got.AuthorizeURL != "https://www.strava.com/oauth/authorize?x=1" {
		t.Errorf("start = %+v", got)
	}
	if got.ExpiresAt.IsZero() || got.Issuer != "https://www.strava.com" || got.ClientSource != "dcr" {
		t.Errorf("start = %+v", got)
	}
}

// Neither the grant nor the flow has anywhere to put a token. The access token,
// the refresh token, the client secret and the PKCE verifier are all held by
// the gateway, and a field here would be a place for somebody to conclude one
// of them travels.
func TestNoConnectTypeCanCarryACredential(t *testing.T) {
	for _, v := range []any{
		api.McpOAuth{Status: api.McpOAuthConnected, ClientID: "abc"},
		api.McpConnectStart{FlowID: "f1"},
		api.McpConnectState{Status: api.McpFlowPending},
	} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			t.Fatal(err)
		}
		for name := range fields {
			switch strings.ToLower(name) {
			case "secret", "clientsecret", "token", "accesstoken", "refreshtoken",
				"codeverifier", "verifier", "state", "code":
				t.Errorf("%T has a %q field; none of those ever leaves the gateway", v, name)
			}
		}
	}
}
