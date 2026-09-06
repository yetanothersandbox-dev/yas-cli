package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/api/apitest"
)

// The device flow against a scripted GitHub: a pending poll, a slow_down
// penalty, then the token. What matters is that the client survives the
// normal churn of a human finding their browser — not just the happy path.
func TestDeviceFlowPollsThroughPendingAndSlowDown(t *testing.T) {
	var polls atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/device":
			_, _ = io.WriteString(w, `{"device_code":"dc1","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`)
		case "/token":
			switch polls.Add(1) {
			case 1:
				_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
			case 2:
				_, _ = io.WriteString(w, `{"error":"slow_down"}`)
			default:
				_, _ = io.WriteString(w, `{"access_token":"gho_good","refresh_token":"ghr_1","expires_in":28800}`)
			}
		}
	}))
	defer gh.Close()

	var out strings.Builder
	var slept []time.Duration
	flow := &deviceFlow{
		ClientID:      "Iv1.test",
		DeviceCodeURL: gh.URL + "/device",
		TokenURL:      gh.URL + "/token",
		Out:           &out,
		Sleep:         func(d time.Duration) { slept = append(slept, d) },
		OpenBrowser:   func(string) {},
	}
	pair, err := flow.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pair.AccessToken != "gho_good" || pair.RefreshToken != "ghr_1" || pair.ExpiresIn != 28800 {
		t.Fatalf("pair = %+v; the refresh half is what custody runs on", pair)
	}
	// The human-facing prompt must carry both halves of the instruction.
	for _, want := range []string{"ABCD-1234", "github.com/login/device"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the prompt does not show %q:\n%s", want, out.String())
		}
	}
	// slow_down added five seconds, observably: the third sleep is longer.
	if len(slept) != 3 || slept[2] != slept[0]+5*time.Second {
		t.Fatalf("sleeps = %v; the slow_down penalty was not applied", slept)
	}
}

func TestDeviceFlowSurfacesADecline(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/device" {
			_, _ = io.WriteString(w, `{"device_code":"dc1","user_code":"X","verification_uri":"u","expires_in":900,"interval":1}`)
			return
		}
		_, _ = io.WriteString(w, `{"error":"access_denied"}`)
	}))
	defer gh.Close()
	flow := &deviceFlow{
		ClientID: "Iv1.test", DeviceCodeURL: gh.URL + "/device", TokenURL: gh.URL + "/token",
		Out: io.Discard, Sleep: func(time.Duration) {}, OpenBrowser: func(string) {},
	}
	if _, err := flow.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("err = %v, want the decline surfaced", err)
	}
}

func TestDeviceFlowWithNoClientIDFailsUsefully(t *testing.T) {
	flow := &deviceFlow{Out: io.Discard}
	_, err := flow.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "YAS_GITHUB_CLIENT_ID") {
		t.Fatalf("err = %v, want a pointer at the fix", err)
	}
}

// Signup on the client: the one unauthenticated call, and the refusal must
// come back typed so login can explain it.
func TestClientSignup(t *testing.T) {
	gw := apitest.New()
	gw.SignupKey = "yas_sk_fresh"
	srv := httptest.NewServer(gw)
	defer srv.Close()

	cl := &api.Client{BaseURL: srv.URL}
	res, err := cl.Signup(context.Background(), "gho_good", "ghr_1", 28800)
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "yas_sk_fresh" || res.TenantID != "gh-583231" || res.Login != "octocat" {
		t.Fatalf("res = %+v", res)
	}

	if _, err := cl.Signup(context.Background(), "gho_stolen", "", 0); err == nil || api.ErrorKind(err) != "github_refused" {
		t.Fatalf("err = %v, want the github_refused kind", err)
	}
}

// The custody handover: the whole pair — access, refresh, expiry — must
// reach signup, because the gateway can only refresh what it was given.
func TestSignupCarriesTheWholePair(t *testing.T) {
	gw := apitest.New()
	gw.SignupKey = "yas_sk_fresh"
	srv := httptest.NewServer(gw)
	defer srv.Close()
	cl := &api.Client{BaseURL: srv.URL}
	if _, err := cl.Signup(context.Background(), "gho_good", "ghr_refresh", 28800); err != nil {
		t.Fatal(err)
	}
	if gw.SignupSaw["refreshToken"] != "ghr_refresh" || gw.SignupSaw["expiresIn"] != float64(28800) {
		t.Fatalf("signup saw %v; the refresh half was dropped", gw.SignupSaw)
	}
}

// The install prompt is non-lazy: zero installations means the install page
// is opened and named NOW, at sign-in, not discovered as a git failure in a
// box later. And a user who already installed is told so, not prompted.
func TestPromptInstall(t *testing.T) {
	installs := 0
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"total_count":`+strconv.Itoa(installs)+`}`)
	}))
	defer gh.Close()

	var out strings.Builder
	opened := ""
	flow := &deviceFlow{Out: &out, OpenBrowser: func(u string) { opened = u }}
	flow.promptInstall(context.Background(), "gho_x", "yas-app", gh.URL, strings.NewReader("\n"))
	if !strings.Contains(out.String(), "github.com/apps/yas-app/installations/new") {
		t.Fatalf("the prompt does not name the install page:\n%s", out.String())
	}
	if !strings.Contains(opened, "installations/new") {
		t.Fatalf("the browser was not pointed at the install page (opened %q)", opened)
	}

	installs = 2
	out.Reset()
	opened = ""
	flow.promptInstall(context.Background(), "gho_x", "yas-app", gh.URL, strings.NewReader("\n"))
	if opened != "" || !strings.Contains(out.String(), "2 installation") {
		t.Fatalf("an installed user was re-prompted (opened %q, out %q)", opened, out.String())
	}
}

// The service-key lifecycle through the client: mint (secret once), list
// (no secrets), revoke.
func TestClientKeys(t *testing.T) {
	gw := apitest.New()
	srv := httptest.NewServer(gw)
	defer srv.Close()
	cl := &api.Client{BaseURL: srv.URL, Key: "yas_sk_test"}
	ctx := context.Background()

	id, secret, err := cl.CreateKey(ctx, "talyn-prod")
	if err != nil || secret == "" {
		t.Fatalf("mint: %v %q", err, secret)
	}
	keys, err := cl.Keys(ctx)
	if err != nil || len(keys) != 1 || keys[0].Name != "talyn-prod" {
		t.Fatalf("list: %v %+v", err, keys)
	}
	if err := cl.RevokeKey(ctx, id); err != nil {
		t.Fatal(err)
	}
	keys, _ = cl.Keys(ctx)
	if keys[0].Live {
		t.Fatal("the revoked key still lists live")
	}
	if err := cl.RevokeKey(ctx, "ak_absent"); err == nil || !api.IsNotFound(err) {
		t.Fatalf("revoking an absent id: %v, want typed not-found", err)
	}
}

// Provider keys go to the gateway's custody, pointer semantics intact:
// absent field untouched, present field set.
func TestClientPutUserCredentials(t *testing.T) {
	gw := apitest.New()
	srv := httptest.NewServer(gw)
	defer srv.Close()
	cl := &api.Client{BaseURL: srv.URL, Key: "yas_sk_test"}
	v := "sk-ant-x"
	if err := cl.PutUserCredentials(context.Background(), &v, nil); err != nil {
		t.Fatal(err)
	}
	if gw.UserCreds["anthropicKey"] != "sk-ant-x" {
		t.Fatalf("gateway saw %v", gw.UserCreds)
	}
	if _, ok := gw.UserCreds["openaiKey"]; ok {
		t.Fatal("an absent field travelled; it would clear a stored key")
	}
	w, err := cl.Whoami(context.Background())
	if err != nil || !w.GitHub.Connected || w.Login != "octocat" {
		t.Fatalf("whoami = %+v, %v", w, err)
	}
}

// The install-time client half: a code goes up, a key comes back.
func TestClientSignupCode(t *testing.T) {
	gw := apitest.New()
	gw.SignupKey = "yas_sk_fresh"
	srv := httptest.NewServer(gw)
	defer srv.Close()
	cl := &api.Client{BaseURL: srv.URL}
	res, err := cl.SignupCode(context.Background(), "good-code")
	if err != nil || res.Key != "yas_sk_fresh" {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if _, err := cl.SignupCode(context.Background(), "forged"); err == nil || api.ErrorKind(err) != "github_refused" {
		t.Fatalf("err = %v, want github_refused", err)
	}
}
