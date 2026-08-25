package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// The install-time login: ONE GitHub interaction instead of two.
//
// The app is configured to request user authorization DURING installation,
// so the install page is also the sign-in: the user picks repositories,
// clicks install (or saves a change to an existing installation), and GitHub
// redirects the browser to 127.0.0.1 with an authorization code. This
// listener catches it, and the gateway — which holds the client secret —
// does the code-for-token exchange inside /v1/signup.
//
// The device flow stays (`yas login -device`) for the terminals this cannot
// serve: SSH sessions and machines whose browser is elsewhere, where nothing
// can reach 127.0.0.1 of the machine running yas.

// loopbackPorts are the ports registered as the app's callback URLs. Both
// must stay in the app's settings; a port not registered there is a GitHub
// error page, not a fallback.
var loopbackPorts = []int{8976, 8977}

type webLogin struct {
	Slug        string
	Ports       []int
	Out         io.Writer
	OpenBrowser func(string)
	Timeout     time.Duration
}

// Run opens the install page and waits for the browser to come back with a
// code. The state parameter rides the whole round trip, so a stray or
// malicious request to the loopback port cannot complete someone's login.
func (wl *webLogin) Run(ctx context.Context) (string, error) {
	ports := wl.Ports
	if len(ports) == 0 {
		ports = loopbackPorts
	}
	var ln net.Listener
	var port int
	for _, p := range ports {
		var err error
		ln, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			port = p
			break
		}
	}
	if ln == nil {
		return "", errors.New("no loopback port is free")
	}
	defer ln.Close()

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	state := hex.EncodeToString(nonce)

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			// Not our round trip. Refuse without completing anything: this is
			// the whole reason state exists.
			http.Error(w, "state mismatch; run yas login again", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<h3>Sign-in was not completed.</h3><p>You can close this tab.</p>")
			done <- result{err: fmt.Errorf("github reported %q", e)}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "no code in the callback", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<h3>Signed in — return to your terminal.</h3><p>You can close this tab.</p>")
		done <- result{code: code}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	installURL := "https://github.com/apps/" + wl.Slug + "/installations/new?state=" + state
	fmt.Fprintf(wl.Out, "\n  Opening GitHub to install the app and sign in — pick the repositories\n"+
		"  your boxes may touch (the callback returns to 127.0.0.1:%d):\n\n    %s\n\n", port, installURL)
	open := wl.OpenBrowser
	if open == nil {
		open = osOpenBrowser
	}
	open(installURL)

	timeout := wl.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	select {
	case res := <-done:
		return res.code, res.err
	case <-time.After(timeout):
		return "", errors.New("the browser never came back; `yas login -device` works without one")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
