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
type McpServer struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	// URL is the endpoint as it was typed, path and all.
	URL       string          `json:"url"`
	Transport string          `json:"transport,omitempty"`
	Spec      json.RawMessage `json:"spec,omitempty"`
	HasSecret bool            `json:"hasSecret"`
	Attach    []string        `json:"attach,omitempty"`
	ExpiresAt *time.Time      `json:"expiresAt,omitempty"`
	Lapsed    bool            `json:"lapsed,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
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
