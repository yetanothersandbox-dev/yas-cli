package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// McpServer is one remote MCP tool server this account can attach to boxes.
//
// There is no Secret field, and there is not going to be one: the API never
// returns a credential, so a struct with somewhere to put it would be a place
// for somebody to conclude the API might.
//
// That rule now covers a GRANT as well as a pasted key. An OAuth row holds four
// secrets at the gateway — the access token, the refresh token, the client
// secret and the PKCE verifier — and not one of them has a field here, because
// not one of them is ever sent. McpOAuth carries the STATE of a sign-in and
// nothing that could complete, renew or replay one.
type McpServer struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	// URL is the endpoint as it was typed, path and all.
	URL       string          `json:"url"`
	Transport string          `json:"transport,omitempty"`
	Spec      json.RawMessage `json:"spec,omitempty"`
	// HasSecret means a credential is stored, whoever it came from — a key
	// somebody pasted or a token a sign-in minted. It goes FALSE on a row that
	// needs reauthorizing, which is correct rather than a rendering artefact:
	// the gateway really did clear the dead access token.
	HasSecret bool       `json:"hasSecret"`
	Attach    []string   `json:"attach,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Lapsed    bool       `json:"lapsed,omitempty"`
	// OAuth is nil for a server whose credential was pasted. That nil is the
	// only way to tell the two kinds apart, and every reader here leans on it.
	OAuth     *McpOAuth `json:"oauth,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// The three words a grant is ever in. Constants rather than three spellings,
// because they are read by the table renderer, by connect's poll and by the
// refusal that names the command which fixes it.
const (
	// McpOAuthPending is a server that offers a sign-in and holds no grant —
	// never connected, or disconnected on purpose.
	McpOAuthPending = "pending"
	// McpOAuthConnected is a live grant the gateway renews on its own.
	McpOAuthConnected = "connected"
	// McpOAuthNeedsReauth is a grant that died somewhere a refresh cannot
	// reach. The client registration survives, so reconnecting is one hop.
	McpOAuthNeedsReauth = "needs_reauth"
)

// McpOAuth is the state of a sign-in the gateway holds for this account, and
// never the credential that sign-in produced.
//
// # The two expiries are different things
//
// McpServer.ExpiresAt is when the tenant's ATTACHMENT lapses: a time-boxed
// grant stops being handed to new boxes. This one is when the ACCESS TOKEN dies
// and the gateway is due to renew it. Reading the second as the first reports a
// perfectly healthy server as expiring hourly, so the two never share a column.
type McpOAuth struct {
	Status string `json:"status"`
	// Detail is why, in the vendor's words or the gateway's. It is present on a
	// needs_reauth row, which is the one place it is worth reading aloud.
	Detail string `json:"detail,omitempty"`
	Issuer string `json:"issuer,omitempty"`
	// Scopes is what was GRANTED once a sign-in completed, which is narrower
	// than what was asked for whenever somebody unticked a box.
	Scopes   []string `json:"scopes,omitempty"`
	ClientID string   `json:"clientId,omitempty"`
	// ClientSource is "dcr" for a client the gateway registered itself.
	ClientSource string     `json:"clientSource,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	CheckedAt    *time.Time `json:"checkedAt,omitempty"`
}

// McpServerRequest is the write shape. Secret is write-only, matching the API:
// it goes up and never comes back.
type McpServerRequest struct {
	Description  string   `json:"description,omitempty"`
	URL          string   `json:"url,omitempty"`
	Transport    string   `json:"transport,omitempty"`
	Secret       *string  `json:"secret,omitempty"`
	Attach       []string `json:"attach,omitzero"`
	ExpiresInSec int      `json:"expiresInSec,omitempty"`
}

func (c *Client) McpServers(ctx context.Context) ([]McpServer, error) {
	var out struct {
		McpServers []McpServer `json:"mcpServers"`
	}
	if err := c.do(ctx, "GET", "/v1/mcp-servers", nil, &out); err != nil {
		return nil, err
	}
	return out.McpServers, nil
}

func (c *Client) McpServer(ctx context.Context, id string) (McpServer, error) {
	var out McpServer
	err := c.do(ctx, "GET", "/v1/mcp-servers/"+url.PathEscape(id), nil, &out)
	return out, err
}

func (c *Client) PutMcpServer(ctx context.Context, id string, req McpServerRequest) (McpServer, error) {
	var out McpServer
	err := c.do(ctx, "PUT", "/v1/mcp-servers/"+url.PathEscape(id), req, &out)
	return out, err
}

func (c *Client) DeleteMcpServer(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/v1/mcp-servers/"+url.PathEscape(id), nil, nil)
}

// AttachMcpTo adds or removes one attachment spec, leaving the rest alone.
//
// Read-modify-write, for the reason AttachTo is: the API's PUT replaces the
// list, and doing it here keeps the server's surface at the four verbs a named
// object needs.
func (c *Client) AttachMcpTo(ctx context.Context, id, spec string, add bool) (McpServer, error) {
	cur, err := c.McpServer(ctx, id)
	if err != nil {
		return McpServer{}, err
	}
	next := make([]string, 0, len(cur.Attach)+1)
	for _, a := range cur.Attach {
		if a != spec {
			next = append(next, a)
		}
	}
	if add {
		next = append(next, spec)
	} else if len(next) == len(cur.Attach) {
		return McpServer{}, fmt.Errorf("%s is not attached to %s", id, spec)
	}
	// next is non-nil, so the last detach sends an empty list instead of
	// omitting the field.
	return c.PutMcpServer(ctx, id, McpServerRequest{Attach: next})
}

// McpServerTest is what the handshake reported.
//
// It carries no Inconclusive, unlike IntegrationTest, and that is the honest
// difference: `initialize` is defined by the protocol rather than the vendor
// and its reply NAMES the server, so a pass here means a session actually
// opened. There is no case where the probe cannot tell.
type McpServerTest struct {
	Tested          bool   `json:"tested"`
	Ok              bool   `json:"ok"`
	Status          int    `json:"status"`
	ServerName      string `json:"serverName,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

func (c *Client) TestMcpServer(ctx context.Context, id string) (McpServerTest, error) {
	var out McpServerTest
	err := c.do(ctx, "POST", "/v1/mcp-servers/"+url.PathEscape(id)+"/test", struct{}{}, &out)
	return out, err
}

// McpConnectStart is a sign-in waiting for a browser.
//
// AuthorizeURL is the vendor's consent page with this flow's state and PKCE
// challenge already on it. It is single use and it expires, which is why
// nothing here caches it.
type McpConnectStart struct {
	FlowID       string    `json:"flowId"`
	AuthorizeURL string    `json:"authorizeUrl"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Scopes       []string  `json:"scopes,omitempty"`
	Issuer       string    `json:"issuer,omitempty"`
	// ClientSource tells a client we registered from one the tenant brought.
	ClientSource string `json:"clientSource,omitempty"`
}

// The four words a flow's poll ever answers with.
const (
	McpFlowPending   = "pending"
	McpFlowConnected = "connected"
	// McpFlowFailed is a vendor that refused, or somebody who said no. Detail
	// carries which.
	McpFlowFailed = "failed"
	// McpFlowExpired is nobody ever coming back. Held apart from failed
	// because the answer differs: this one is fixed by connecting again, and
	// there is no vendor refusal to report because no vendor was heard from.
	McpFlowExpired = "expired"
)

// McpConnectState is where one flow has got to.
type McpConnectState struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ConnectMcpServer starts the authorize leg and answers with the URL a browser
// has to visit.
//
// next is where the gateway sends the browser once the grant lands. It must be
// a LOCAL path on the site — the gateway refuses a scheme or a host, because an
// open redirect immediately after an authorization is the most valuable one
// there is. An empty next gets the gateway's own "go back to your terminal"
// page, which is the CLI's case.
func (c *Client) ConnectMcpServer(ctx context.Context, id, next string) (McpConnectStart, error) {
	var out McpConnectStart
	body := struct {
		Next string `json:"next,omitempty"`
	}{Next: next}
	err := c.do(ctx, "POST", "/v1/mcp-servers/"+url.PathEscape(id)+"/connect", body, &out)
	return out, err
}

// McpConnectStatus asks how one flow ended, or whether it has.
func (c *Client) McpConnectStatus(ctx context.Context, id, flow string) (McpConnectState, error) {
	var out McpConnectState
	err := c.do(ctx, "GET", "/v1/mcp-servers/"+url.PathEscape(id)+"/connect/"+url.PathEscape(flow), nil, &out)
	return out, err
}

// DisconnectMcpServer drops the grant and keeps the client registration, so
// connecting again is one hop rather than the whole discovery chain.
func (c *Client) DisconnectMcpServer(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/v1/mcp-servers/"+url.PathEscape(id)+"/disconnect", struct{}{}, nil)
}

// McpServerInline is one server named on a create rather than stored.
//
// For a caller that composes a prompt and the tools it needs in one request.
// The secret is carried and never persisted — it stops at the host's proxy,
// exactly as the GitHub token does.
type McpServerInline struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	Secret      string `json:"secret,omitempty"`
}
