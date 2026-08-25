package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// The GitHub half of self-serve login: the OAuth device flow, RFC 8628.
//
// The CLI asks GitHub for a one-time code, the human approves it in a
// browser, and the CLI polls until GitHub hands over an access token. That
// token goes to the gateway's /v1/signup, which verifies it against GitHub
// and mints the yas key. No client secret exists anywhere in this flow — a
// device-flow app has only a PUBLIC client id, which is why it is safe to
// bake into this binary.

// githubClientID is the OAuth app this build fronts. Stamped by the Makefile;
// YAS_GITHUB_CLIENT_ID overrides for development against another app. Empty
// means this build cannot self-serve and login says so.
var githubClientID = ""

// deviceFlow carries the endpoints so tests can point them at a fake GitHub.
type deviceFlow struct {
	ClientID      string
	DeviceCodeURL string // default https://github.com/login/device/code
	TokenURL      string // default https://github.com/login/oauth/access_token
	HTTP          *http.Client
	Out           io.Writer // the human-facing prompt; stderr in production
	Sleep         func(time.Duration)
	OpenBrowser   func(url string) // best-effort; nil means the OS default
}

func (d *deviceFlow) http() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (d *deviceFlow) sleep(t time.Duration) {
	if d.Sleep != nil {
		d.Sleep(t)
		return
	}
	time.Sleep(t)
}

// post sends one form-encoded request and decodes GitHub's JSON answer.
func (d *deviceFlow) post(ctx context.Context, rawURL string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := d.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// Run performs the whole flow and returns the GitHub access token.
func (d *deviceFlow) Run(ctx context.Context) (string, error) {
	if d.ClientID == "" {
		return "", errors.New("this build has no GitHub app configured; set YAS_GITHUB_CLIENT_ID or paste a key instead")
	}
	if d.DeviceCodeURL == "" {
		d.DeviceCodeURL = "https://github.com/login/device/code"
	}
	if d.TokenURL == "" {
		d.TokenURL = "https://github.com/login/oauth/access_token"
	}

	var code struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
	}
	// No scopes requested, on purpose: /user answers for a scopeless token,
	// and a signup that asked for repo access would be asking for something
	// it has no use for and the user has every reason to refuse.
	if err := d.post(ctx, d.DeviceCodeURL, url.Values{"client_id": {d.ClientID}}, &code); err != nil {
		return "", fmt.Errorf("asking github for a device code: %w", err)
	}
	if code.Error != "" || code.DeviceCode == "" {
		return "", fmt.Errorf("github refused to start the sign-in (%s); is the app's device flow enabled?", code.Error)
	}

	fmt.Fprintf(d.Out, "\n  Visit  %s\n  Enter  %s\n\n", code.VerificationURI, code.UserCode)
	openBrowser := d.OpenBrowser
	if openBrowser == nil {
		openBrowser = osOpenBrowser
	}
	openBrowser(code.VerificationURI)

	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	for {
		if code.ExpiresIn > 0 && time.Now().After(deadline) {
			return "", errors.New("the sign-in code expired before it was approved; run `yas login` again")
		}
		d.sleep(interval)
		var tok struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		err := d.post(ctx, d.TokenURL, url.Values{
			"client_id":   {d.ClientID},
			"device_code": {code.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &tok)
		if err != nil {
			return "", fmt.Errorf("polling github: %w", err)
		}
		switch tok.Error {
		case "":
			if tok.AccessToken == "" {
				return "", errors.New("github answered without a token or an error")
			}
			return tok.AccessToken, nil
		case "authorization_pending":
			// The human has not clicked yet. The normal case; poll on.
		case "slow_down":
			// GitHub names the penalty in RFC 8628: add five seconds.
			interval += 5 * time.Second
		case "expired_token":
			return "", errors.New("the sign-in code expired before it was approved; run `yas login` again")
		case "access_denied":
			return "", errors.New("the sign-in was declined")
		default:
			return "", fmt.Errorf("github ended the sign-in: %s", tok.Error)
		}
	}
}

// osOpenBrowser is a courtesy, never a dependency: the URL is already printed.
func osOpenBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	default:
		return
	}
	_ = cmd.Start()
}
