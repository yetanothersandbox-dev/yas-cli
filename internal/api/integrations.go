package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// Integration is one named upstream this account can attach to boxes.
//
// There is no Secret field, and there is not going to be one: the API never
// returns a credential, so a struct with somewhere to put it would be a place
// for somebody to conclude the API might.
type Integration struct {
	ID          string          `json:"id"`
	Description string          `json:"description,omitempty"`
	Service     string          `json:"service,omitempty"`
	Spec        json.RawMessage `json:"spec,omitempty"`
	HasSecret   bool            `json:"hasSecret"`
	Attach      []string        `json:"attach,omitempty"`
	ExpiresAt   *time.Time      `json:"expiresAt,omitempty"`
	Lapsed      bool            `json:"lapsed,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// Upstream digs the upstream out of the stored spec, for display.
func (in Integration) Upstream() string {
	var spec struct {
		Upstream string `json:"upstream"`
	}
	_ = json.Unmarshal(in.Spec, &spec)
	return spec.Upstream
}

// IntegrationRequest is the write shape. Secret is write-only, matching the
// API: it goes up and never comes back.
type IntegrationRequest struct {
	Description  string   `json:"description,omitempty"`
	Service      string   `json:"service,omitempty"`
	Upstream     string   `json:"upstream,omitempty"`
	Inject       any      `json:"inject,omitempty"`
	Secret       *string  `json:"secret,omitempty"`
	Attach       []string `json:"attach,omitzero"`
	ExpiresInSec int      `json:"expiresInSec,omitempty"`
	StripPrefix  string   `json:"stripPrefix,omitempty"`
	Methods      []string `json:"methods,omitempty"`
	PathPrefix   string   `json:"pathPrefix,omitempty"`
}

// CatalogEntry is one service in the catalogue.
type CatalogEntry struct {
	Handle              string `json:"handle"`
	Title               string `json:"title"`
	Summary             string `json:"summary"`
	Upstream            string `json:"upstream"`
	CredentialLabel     string `json:"credentialLabel"`
	Notes               string `json:"notes,omitempty"`
	OverridableUpstream bool   `json:"overridableUpstream,omitempty"`
}

func (c *Client) Integrations(ctx context.Context) ([]Integration, error) {
	var out struct {
		Integrations []Integration `json:"integrations"`
	}
	if err := c.do(ctx, "GET", "/v1/integrations", nil, &out); err != nil {
		return nil, err
	}
	return out.Integrations, nil
}

func (c *Client) Integration(ctx context.Context, id string) (Integration, error) {
	var out Integration
	err := c.do(ctx, "GET", "/v1/integrations/"+url.PathEscape(id), nil, &out)
	return out, err
}

func (c *Client) PutIntegration(ctx context.Context, id string, req IntegrationRequest) (Integration, error) {
	var out Integration
	err := c.do(ctx, "PUT", "/v1/integrations/"+url.PathEscape(id), req, &out)
	return out, err
}

func (c *Client) DeleteIntegration(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/v1/integrations/"+url.PathEscape(id), nil, nil)
}

func (c *Client) IntegrationCatalog(ctx context.Context) ([]CatalogEntry, error) {
	var out struct {
		Catalog []CatalogEntry `json:"catalog"`
	}
	if err := c.do(ctx, "GET", "/v1/integrations/catalog", nil, &out); err != nil {
		return nil, err
	}
	return out.Catalog, nil
}

// AttachTo adds a spec to an integration's attachment list, leaving the rest of
// it alone.
//
// Read-modify-write, because the API's PUT replaces the list. Doing it here
// rather than adding an /attach route keeps the server's surface at the four
// verbs a named object needs.
func (c *Client) AttachTo(ctx context.Context, id, spec string, add bool) (Integration, error) {
	cur, err := c.Integration(ctx, id)
	if err != nil {
		return Integration{}, err
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
		return Integration{}, fmt.Errorf("%s is not attached to %s", id, spec)
	}
	// next is non-nil, so the last detach sends an empty list instead of omitting it.
	return c.PutIntegration(ctx, id, IntegrationRequest{Attach: next})
}

// IntegrationTest is what a credential probe reports.
//
// Inconclusive is carried rather than folded into Ok, because several vendors
// cannot be probed honestly — they answer 200 for any well-formed key. A green
// tick that means nothing is worse than no tick, since it is the one somebody
// stops checking behind.
type IntegrationTest struct {
	Tested       bool   `json:"tested"`
	Ok           bool   `json:"ok"`
	Status       int    `json:"status"`
	Detail       string `json:"detail,omitempty"`
	Inconclusive string `json:"inconclusive,omitempty"`
}

func (c *Client) TestIntegration(ctx context.Context, id string) (IntegrationTest, error) {
	var out IntegrationTest
	err := c.do(ctx, "POST", "/v1/integrations/"+url.PathEscape(id)+"/test", struct{}{}, &out)
	return out, err
}
