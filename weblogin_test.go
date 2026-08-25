package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The loopback round trip, driven as the browser would drive it: Run opens
// the install page (captured), the "browser" comes back to the callback with
// the code and the SAME state, and Run returns the code.
func TestWebLoginRoundTrip(t *testing.T) {
	opened := make(chan string, 1)
	wl := &webLogin{
		Slug: "yetanothersandbox-app", Out: io.Discard,
		OpenBrowser: func(u string) { opened <- u },
		Timeout:     5 * time.Second,
		Ports:       []int{18976, 18977},
	}
	type res struct {
		code string
		err  error
	}
	done := make(chan res, 1)
	go func() {
		c, err := wl.Run(context.Background())
		done <- res{c, err}
	}()

	u := <-opened
	if !strings.Contains(u, "github.com/apps/yetanothersandbox-app/installations/new?state=") {
		t.Fatalf("opened %q, want the install page with state", u)
	}
	parsed, _ := url.Parse(u)
	state := parsed.Query().Get("state")

	// A stray request with the WRONG state must not complete the login.
	resp, err := http.Get("http://127.0.0.1:18976/callback?code=evil&state=wrong")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong state answered %d, want 400", resp.StatusCode)
	}

	resp, err = http.Get("http://127.0.0.1:18976/callback?code=good-code&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "return to your terminal") {
		t.Fatalf("the browser page says %q", body)
	}
	got := <-done
	if got.err != nil || got.code != "good-code" {
		t.Fatalf("Run = %q, %v", got.code, got.err)
	}
}

// GitHub reporting an error (user cancelled) surfaces as an error, not a
// hang and not a bogus code.
func TestWebLoginSurfacesADenial(t *testing.T) {
	opened := make(chan string, 1)
	wl := &webLogin{
		Slug: "x", Out: io.Discard,
		OpenBrowser: func(u string) { opened <- u },
		Timeout:     5 * time.Second,
		Ports:       []int{18978},
	}
	errs := make(chan error, 1)
	go func() {
		_, err := wl.Run(context.Background())
		errs <- err
	}()
	u := <-opened
	state := ""
	if p, err := url.Parse(u); err == nil {
		state = p.Query().Get("state")
	}
	resp, err := http.Get("http://127.0.0.1:18978/callback?error=access_denied&state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want the denial surfaced", err)
	}
}
