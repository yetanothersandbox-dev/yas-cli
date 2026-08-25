package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
	"github.com/Gilbert09/yas/clients/yas/internal/api/apitest"
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
				_, _ = io.WriteString(w, `{"access_token":"gho_good"}`)
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
	token, err := flow.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if token != "gho_good" {
		t.Fatalf("token = %q", token)
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
	res, err := cl.Signup(context.Background(), "gho_good")
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "yas_sk_fresh" || res.TenantID != "gh-583231" || res.Login != "octocat" {
		t.Fatalf("res = %+v", res)
	}

	if _, err := cl.Signup(context.Background(), "gho_stolen"); err == nil || api.ErrorKind(err) != "github_refused" {
		t.Fatalf("err = %v, want the github_refused kind", err)
	}
}
