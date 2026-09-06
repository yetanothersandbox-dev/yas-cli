package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yetanothersandbox-dev/yas-cli/internal/config"
)

// Signing in twice must not leave two credentials behind.
//
// Every sign-in MINTS a key server-side, and only the SHA-256 of the secret is
// stored — so a key can never be handed back a second time, and "reuse the one I
// have" has to mean "do not ask for another". Without this, `yas login` run
// twice left two live keys with the same name and no way to tell them apart;
// one account had three that way.
func TestWhoamiRecognisesAWorkingKey(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/v1/user" {
			t.Errorf("asked for %s, want /v1/user — the check must be a real authenticated "+
				"call, because a config file cannot know a key has been revoked", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Error("the check went out unauthenticated, so it proves nothing about the key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tenantId":"gh-1","login":"octocat"}`))
	}))
	defer srv.Close()

	login, ok := whoami(config.Config{APIKey: "yas_sk_test", BaseURL: srv.URL})
	if !ok {
		t.Fatal("a working key was not recognised, so login would mint another")
	}
	if login != "octocat" {
		t.Errorf("login = %q, want octocat", login)
	}
	if hits != 1 {
		t.Errorf("made %d calls, want exactly 1", hits)
	}
}

// A key the server no longer accepts must send somebody through the flow.
//
// This is the case that makes the check a real request rather than "is a key
// present in the file": being told "already signed in" while holding a revoked
// key is worse than being asked to sign in again, because nothing then explains
// why every later command fails.
func TestWhoamiRejectsAKeyTheServerRefuses(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		}))
		if _, ok := whoami(config.Config{APIKey: "yas_sk_dead", BaseURL: srv.URL}); ok {
			t.Errorf("a key refused with %d was treated as working", code)
		}
		srv.Close()
	}
}

// No key at all is not "signed in", and must not cost a network call.
func TestWhoamiWithNoKeyDoesNotCallOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("called the gateway with no key to send")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, ok := whoami(config.Config{BaseURL: srv.URL}); ok {
		t.Error("an empty key reported as signed in")
	}
}
