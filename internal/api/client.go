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
	// OnRetry, when set, is called just before Create waits out a 503
	// no_capacity, with the attempt about to be made (1-based), how long the
	// wait is, and the refusal that caused it.
	//
	// It exists because the caller cannot otherwise tell WHY a create is slow.
	// From outside this method, "queueing behind fleet capacity" and "this one
	// is just taking a while" look identical, and the CLI used to guess — it
	// told everyone waiting more than eight seconds that "the fleet is finding
	// room", which is a cause it had no way of knowing. This is the one moment
	// the cause IS known, so it is reported instead of inferred.
	OnRetry func(attempt int, wait time.Duration, err error)
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
		if c.OnRetry != nil {
			c.OnRetry(attempt+2, wait, err)
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

// Pool is what this account is holding against what it may hold.
//
// The CLI went without it for a long time and was poorer for it: the product is
// sold as a POOL — that is the whole pricing model and the whole reason
// suspending is worth doing — and the command people run most could not say how
// full theirs was. One call, on the one screen where the answer is useful.
//
// Zero limits mean unbounded, matching the server: an operator-raised account
// has no ceiling and must not be drawn as if it were at 0% of nothing.
type Pool struct {
	Plan struct {
		Name string `json:"name"`
	} `json:"plan"`
	MemMiB struct {
		Limit int `json:"limit"`
		Used  int `json:"used"`
		Free  int `json:"free"`
	} `json:"memMib"`
	MilliVcpu struct {
		Limit int `json:"limit"`
		Used  int `json:"used"`
	} `json:"milliVcpu"`
	Boxes struct {
		Running   int `json:"running"`
		Suspended int `json:"suspended"`
		Total     int `json:"total"`
	} `json:"boxes"`
}

// Pool reads the caller's pool. Errors are the caller's to ignore: a header
// line is worth having and never worth failing a command over.
func (c *Client) Pool(ctx context.Context) (*Pool, error) {
	var p Pool
	if err := c.do(ctx, http.MethodGet, "/v1/pool", nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Get reads one sandbox's full record.
//
// The response is an ENVELOPE — `{"sandbox": {...}, "terminal": bool,
// "retired": bool}` — and not the record at the top level. Decoding it flat
// silently produced a zero-valued Sandbox on every call: `yas ls` printed empty
// statuses, the picker showed every box as blank, and sshutil's
// wait-until-ready loop compared against a status that was always "". Nothing
// errored, because a JSON object with no matching fields decodes cleanly into a
// struct and leaves it untouched.
//
// `retired` is lifted onto the record rather than returned separately: every
// caller that cares wants to know "is this a live box or a remembered one?",
// and a second return value would be dropped at three of the four call sites.
func (c *Client) Get(ctx context.Context, id string) (Sandbox, error) {
	var env struct {
		Sandbox  Sandbox `json:"sandbox"`
		Terminal bool    `json:"terminal"`
		Retired  bool    `json:"retired"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/sandboxes/"+id, nil, &env); err != nil {
		return Sandbox{}, err
	}
	out := env.Sandbox
	if env.Retired {
		out.Retired = true
	}
	return out, nil
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
	// Installations is how many app installations the account has; -1 means
	// the gateway could not tell. Zero steers login's install prompt.
	Installations int `json:"installations"`
}

// Signup exchanges a GitHub token pair for a tenant key, handing the pair
// into the gateway's custody as it does. The only client method that sends
// no Authorization header: it is how the first credential comes to exist.
func (c *Client) Signup(ctx context.Context, githubToken, refreshToken string, expiresIn int64) (SignupResult, error) {
	return c.signupBody(ctx, map[string]any{
		"githubToken": githubToken, "refreshToken": refreshToken, "expiresIn": expiresIn,
	})
}

func (c *Client) signupBody(ctx context.Context, body map[string]any) (SignupResult, error) {
	b, err := json.Marshal(body)
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

// SignupCode is the install-time flow's half of Signup: the CLI hands over
// the authorization code and the gateway does the exchange — the client
// secret never travels.
func (c *Client) SignupCode(ctx context.Context, code string) (SignupResult, error) {
	return c.signupBody(ctx, map[string]any{"code": code})
}

// Whoami is GET /v1/user: the account, and which credentials the gateway
// holds — presence only.
type Whoami struct {
	TenantID string `json:"tenantId"`
	Login    string `json:"login"`
	Name     string `json:"name"`
	GitHub   struct {
		Connected bool   `json:"connected"`
		Login     string `json:"login"`
	} `json:"github"`
	AnthropicKey bool `json:"anthropicKey"`
	OpenAIKey    bool `json:"openaiKey"`
}

func (c *Client) Whoami(ctx context.Context) (Whoami, error) {
	var w Whoami
	err := c.do(ctx, http.MethodGet, "/v1/user", nil, &w)
	return w, err
}

// PutUserCredentials stores provider keys server-side. Nil = leave alone;
// pointer-to-empty = clear.
func (c *Client) PutUserCredentials(ctx context.Context, anthropic, openai *string) error {
	body := map[string]any{}
	if anthropic != nil {
		body["anthropicKey"] = *anthropic
	}
	if openai != nil {
		body["openaiKey"] = *openai
	}
	return c.do(ctx, http.MethodPut, "/v1/user/credentials", body, nil)
}

// KeyRow is one listed key. No secret: it is not stored anywhere.
type KeyRow struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"createdAt"`
	Live      bool       `json:"live"`
	Current   bool       `json:"current"`
	RevokedAt *time.Time `json:"revokedAt"`
}

func (c *Client) Keys(ctx context.Context) ([]KeyRow, error) {
	var out struct {
		Keys []KeyRow `json:"keys"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/keys", nil, &out)
	return out.Keys, err
}

// CreateKey mints a named service key on the caller's own tenant and returns
// the one copy of its secret that will ever exist.
func (c *Client) CreateKey(ctx context.Context, name string) (id, secret string, err error) {
	var out struct {
		KeyID string `json:"keyId"`
		Key   string `json:"key"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/keys", map[string]string{"name": name}, &out); err != nil {
		return "", "", err
	}
	return out.KeyID, out.Key, nil
}

func (c *Client) RevokeKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/keys/"+id, nil, nil)
}

// BoxDefaults is the size a box of this account's gets when a create names none.
//
// Both the override and what is actually in force, because they are different
// questions and a caller that only knew one would have to guess: MemMiB is zero
// for almost everybody, and Effective is the number a box will really get.
type BoxDefaults struct {
	MemMiB             int `json:"memMib"`
	MilliVcpu          int `json:"milliVcpu"`
	EffectiveMemMiB    int `json:"effectiveMemMib"`
	EffectiveMilliVcpu int `json:"effectiveMilliVcpu"`
	MaxMemMiB          int `json:"maxMemMib"`
	MaxMilliVcpu       int `json:"maxMilliVcpu"`
}

// BoxDefaults reads the current setting.
func (c *Client) BoxDefaults(ctx context.Context) (BoxDefaults, error) {
	var out BoxDefaults
	if err := c.do(ctx, http.MethodGet, "/v1/user/box-defaults", nil, &out); err != nil {
		return BoxDefaults{}, err
	}
	return out, nil
}

// SetBoxDefaults writes it. Zero on a field CLEARS it, returning that dimension
// to the plan's own default — there is no separate delete verb, so an emptied
// value has to mean "stop overriding".
func (c *Client) SetBoxDefaults(ctx context.Context, memMiB, milliVcpu int) (BoxDefaults, error) {
	body := struct {
		MemMiB    int `json:"memMib"`
		MilliVcpu int `json:"milliVcpu"`
	}{memMiB, milliVcpu}
	var out BoxDefaults
	if err := c.do(ctx, http.MethodPut, "/v1/user/box-defaults", body, &out); err != nil {
		return BoxDefaults{}, err
	}
	return out, nil
}

// RegionChoice is one entry in the catalogue.
type RegionChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Live is false for a region that is announced and not yet running. It
	// takes no work, and is listed anyway so the next one does not appear one
	// day with no warning.
	Live bool `json:"live"`
}

// RegionSettings is where an account's boxes are placed.
//
// Region is what was chosen and may be empty; EffectiveRegion is what placement
// will actually prefer, because an empty or not-yet-live choice resolves to the
// default and a caller should not have to know that rule.
type RegionSettings struct {
	Region          string         `json:"region"`
	EffectiveRegion string         `json:"effectiveRegion"`
	Regions         []RegionChoice `json:"regions"`
}

// Region reads where this account's boxes go.
func (c *Client) Region(ctx context.Context) (RegionSettings, error) {
	var out RegionSettings
	if err := c.do(ctx, http.MethodGet, "/v1/user/region", nil, &out); err != nil {
		return RegionSettings{}, err
	}
	return out, nil
}

// SetRegion moves them. Either a region id, or an IANA time zone to work one
// out from — the CLI sends the zone when the caller has expressed no preference,
// which is how a machine that has never been placed places itself.
func (c *Client) SetRegion(ctx context.Context, id, timeZone string) (RegionSettings, error) {
	body := struct {
		Region   string `json:"region,omitempty"`
		TimeZone string `json:"timeZone,omitempty"`
	}{id, timeZone}
	var out RegionSettings
	if err := c.do(ctx, http.MethodPut, "/v1/user/region", body, &out); err != nil {
		return RegionSettings{}, err
	}
	return out, nil
}
