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

func startWL(t *testing.T, ports []int) (*webLogin, chan string) {
	t.Helper()
	opened := make(chan string, 2)
	wl := &webLogin{
		ClientID: "Iv23.test", Slug: "yetanothersandbox-app", Out: io.Discard,
		OpenBrowser: func(u string) { opened <- u },
		Timeout:     5 * time.Second,
		Ports:       ports,
	}
	if err := wl.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wl.Close)
	return wl, opened
}

// Phase one is the AUTHORIZE url — the returning-user path — and the state
// must gate the callback: a stray request with the wrong state cannot
// complete anyone's login.
func TestWebLoginAuthorizeRoundTrip(t *testing.T) {
	wl, opened := startWL(t, []int{18976, 18977})
	type res struct {
		code string
		err  error
	}
	done := make(chan res, 1)
	go func() {
		c, err := wl.Authorize(context.Background())
		done <- res{c, err}
	}()

	u := <-opened
	if !strings.HasPrefix(u, "https://github.com/login/oauth/authorize?client_id=Iv23.test") {
		t.Fatalf("phase one opened %q; the install page is NOT the login page", u)
	}
	if !strings.Contains(u, "redirect_uri=http%3A%2F%2F127.0.0.1%3A18976%2Fcallback") {
		t.Fatalf("no loopback redirect_uri in %q", u)
	}
	parsed, _ := url.Parse(u)
	state := parsed.Query().Get("state")

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
		t.Fatalf("Authorize = %q, %v", got.code, got.err)
	}
}

// Phase two reuses the SAME listener and state, and swallows the install
// redirect's code — identity was settled in phase one.
func TestWebLoginInstallPromptSecondPhase(t *testing.T) {
	wl, opened := startWL(t, []int{18978})
	go func() {
		u := <-opened // authorize
		p, _ := url.Parse(u)
		_, _ = http.Get("http://127.0.0.1:18978/callback?code=c1&state=" + p.Query().Get("state"))
		u2 := <-opened // install page
		if !strings.Contains(u2, "github.com/apps/yetanothersandbox-app/installations/new") {
			panic("phase two opened " + u2)
		}
		p2, _ := url.Parse(u2)
		_, _ = http.Get("http://127.0.0.1:18978/callback?code=c2&installation_id=1&setup_action=install&state=" + p2.Query().Get("state"))
	}()
	if code, err := wl.Authorize(context.Background()); err != nil || code != "c1" {
		t.Fatalf("authorize: %q %v", code, err)
	}
	doneBy := time.Now().Add(5 * time.Second)
	wl.PromptInstall(context.Background())
	if time.Now().After(doneBy) {
		t.Fatal("PromptInstall did not return promptly after the install redirect")
	}
}

// A denial in phase one surfaces as an error, not a hang.
func TestWebLoginSurfacesADenial(t *testing.T) {
	wl, opened := startWL(t, []int{18979})
	errs := make(chan error, 1)
	go func() {
		_, err := wl.Authorize(context.Background())
		errs <- err
	}()
	u := <-opened
	p, _ := url.Parse(u)
	resp, err := http.Get("http://127.0.0.1:18979/callback?error=access_denied&state=" + p.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want the denial surfaced", err)
	}
}
