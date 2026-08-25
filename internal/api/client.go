package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the product path. There is exactly one hostname a yas
// client ever talks to; the fleet behind it is not addressable and not named.
const DefaultBaseURL = "https://api.yetanothersandbox.dev"

// Client is one authenticated caller of the gateway.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
	// Sleep is the retry pause seam; tests replace it. Nil means time.Sleep.
	Sleep func(time.Duration)
}

// apiError is the {error, message} body every non-2xx carries, plus what the
// caller needs to act on it.
type apiError struct {
	Kind       string
	Message    string
	Status     int
	RetryAfter time.Duration
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Kind, e.Status)
}

// IsQuota reports the caller's own cap: retrying anywhere is pointless and the
// error must say so rather than invite it.
func IsQuota(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests
}

// IsNoCapacity reports fleet pressure, the one refusal worth a bounded retry.
func IsNoCapacity(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusServiceUnavailable
}

// IsNotFound reports an id that does not exist for THIS tenant — the API
// deliberately answers a foreign tenant's id identically.
func IsNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// ErrorKind exposes the wire `error` field ("suspended", "quota_exceeded", …)
// for the few places that need finer grain than the predicates above.
func ErrorKind(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Kind
	}
	return ""
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

// do runs one request and decodes into out (which may be nil). Every request
// this client ever makes carries the bearer key; nothing else authenticates.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.raw(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// raw runs one request and returns the response with a 2xx status; anything
// else is decoded into the shared error taxonomy and the body is closed.
func (c *Client) raw(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	ae := &apiError{Status: resp.StatusCode}
	var wire struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if json.Unmarshal(raw, &wire) == nil && wire.Error != "" {
		ae.Kind, ae.Message = wire.Error, wire.Message
	} else {
		ae.Message = strings.TrimSpace(string(raw))
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, perr := strconv.Atoi(s); perr == nil {
			ae.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	return nil, ae
}

// Create places one bare sandbox. The 202 is SYNCHRONOUS for a bare create:
// when it returns, the guest is up and req.SSHKeys are installed, so the
// caller may connect immediately.
//
// The retry policy lives here, once: 503 no_capacity is retried up to twice,
// honouring Retry-After, because it means "pressure right now"; 429 is the
// caller's own cap and is NEVER retried — hammering it converts a quota
// message into a rate-limit ban; everything else is terminal.
func (c *Client) Create(ctx context.Context, req CreateRequest) error {
	for attempt := 0; ; attempt++ {
		err := c.do(ctx, http.MethodPost, "/v1/sandboxes", req, nil)
		if err == nil || !IsNoCapacity(err) || attempt >= 2 {
			return err
		}
		var ae *apiError
		wait := 5 * time.Second
		if errors.As(err, &ae) && ae.RetryAfter > 0 {
			wait = ae.RetryAfter
		}
		c.sleep(wait)
	}
}

// List returns the index rows. Summaries only — status needs Get per id,
// because the index cannot answer what changes every second.
func (c *Client) List(ctx context.Context) ([]SandboxSummary, error) {
	var out struct {
		Sandboxes []SandboxSummary `json:"sandboxes"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/sandboxes", nil, &out); err != nil {
		return nil, err
	}
	return out.Sandboxes, nil
}

func (c *Client) Get(ctx context.Context, id string) (Sandbox, error) {
	var s Sandbox
	err := c.do(ctx, http.MethodGet, "/v1/sandboxes/"+id, nil, &s)
	return s, err
}

func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/sandboxes/"+id, nil, nil)
}

func (c *Client) Suspend(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/sandboxes/"+id+"/suspend", map[string]any{}, nil)
}

func (c *Client) Resume(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/sandboxes/"+id+"/resume", map[string]any{}, nil)
}

// SSH installs the COMPLETE public-key set — replace, not add — and returns
// what a client pins before its first connection.
func (c *Client) SSH(ctx context.Context, id string, publicKeys []string) (SSHAccess, error) {
	var a SSHAccess
	err := c.do(ctx, http.MethodPost, "/v1/sandboxes/"+id+"/ssh",
		map[string]any{"publicKeys": publicKeys}, &a)
	return a, err
}

// Events polls the transcript from a cursor.
func (c *Client) Events(ctx context.Context, id string, after int64) (EventsPage, error) {
	var p EventsPage
	err := c.do(ctx, http.MethodGet, "/v1/sandboxes/"+id+"/events?after="+strconv.FormatInt(after, 10), nil, &p)
	return p, err
}

// ExecFollow starts one command and streams its frames. The stream ends at
// the frame with Terminal=true; a drop before that is NOT the command dying —
// the caller resumes with Events from the last cursor it saw.
func (c *Client) ExecFollow(ctx context.Context, id string, req ExecRequest, onPage func(EventsPage)) error {
	resp, err := c.raw(ctx, http.MethodPost, "/v1/sandboxes/"+id+"/exec?follow=1", req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return readSSE(resp.Body, onPage)
}

// SignupResult is what /v1/signup hands back — including the ONE copy of the
// secret that will ever exist.
type SignupResult struct {
	TenantID      string `json:"tenantId"`
	Login         string `json:"login"`
	KeyID         string `json:"keyId"`
	Key           string `json:"key"`
	TenantCreated bool   `json:"tenantCreated"`
}

// Signup exchanges a GitHub access token for a tenant key. The only client
// method that sends no Authorization header: it is how the first credential
// comes to exist.
func (c *Client) Signup(ctx context.Context, githubToken string) (SignupResult, error) {
	b, err := json.Marshal(map[string]string{"githubToken": githubToken})
	if err != nil {
		return SignupResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/v1/signup", bytes.NewReader(b))
	if err != nil {
		return SignupResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http().Do(req)
	if err != nil {
		return SignupResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		ae := &apiError{Status: resp.StatusCode}
		var wire struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if json.Unmarshal(raw, &wire) == nil && wire.Error != "" {
			ae.Kind, ae.Message = wire.Error, wire.Message
		} else {
			ae.Message = strings.TrimSpace(string(raw))
		}
		return SignupResult{}, ae
	}
	var out SignupResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SignupResult{}, err
	}
	if !strings.HasPrefix(out.Key, "yas_sk_") {
		return SignupResult{}, errors.New("the gateway's signup answer carried no key")
	}
	return out, nil
}
