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

// The browser login, in two phases with one listener.
//
// Phase one is the AUTHORIZE url, not the install page: for a returning user
// GitHub answers it with an immediate redirect — no screens, no questions —
// which is what a login should feel like the second time. The gateway then
// reports how many installations the user's token can see, and only a user
// with NONE is sent to the install page as phase two: the first-run moment
// to pick repositories, never a page a returning user has to escape from.
//
// The device flow stays (`yas login -device`) for the terminals this cannot
// serve: SSH sessions and machines whose browser is elsewhere, where nothing
// can reach 127.0.0.1 of the machine running yas.

// loopbackPorts are the ports registered as the app's callback URLs. Both
// must stay in the app's settings; a port not registered there is a GitHub
// error page, not a fallback.
var loopbackPorts = []int{8976, 8977}

type webLogin struct {
	ClientID    string
	Slug        string
	Ports       []int
	Out         io.Writer
	OpenBrowser func(string)
	Timeout     time.Duration

	ln    net.Listener
	srv   *http.Server
	port  int
	state string
	codes chan string
	fail  chan error
}

// Start binds the loopback listener and generates the state that rides every
// round trip, so a stray request to the port cannot complete anyone's login.
func (wl *webLogin) Start() error {
	ports := wl.Ports
	if len(ports) == 0 {
		ports = loopbackPorts
	}
	for _, p := range ports {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			wl.ln, wl.port = ln, p
			break
		}
	}
	if wl.ln == nil {
		return errors.New("no loopback port is free")
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		wl.ln.Close()
		return err
	}
	wl.state = hex.EncodeToString(nonce)
	wl.codes = make(chan string, 2)
	wl.fail = make(chan error, 2)
	wl.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != wl.state {
			http.Error(w, "state mismatch; run yas login again", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<h3>Sign-in was not completed.</h3><p>You can close this tab.</p>")
			wl.fail <- fmt.Errorf("github reported %q", e)
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "no code in the callback", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<h3>Signed in — return to your terminal.</h3><p>You can close this tab.</p>")
		wl.codes <- code
	})}
	go func() { _ = wl.srv.Serve(wl.ln) }()
	return nil
}

func (wl *webLogin) Close() {
	if wl.srv != nil {
		wl.srv.Close()
	}
}

func (wl *webLogin) open(u string) {
	fn := wl.OpenBrowser
	if fn == nil {
		fn = osOpenBrowser
	}
	fn(u)
}

func (wl *webLogin) await(ctx context.Context, what string) (string, error) {
	timeout := wl.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	select {
	case code := <-wl.codes:
		return code, nil
	case err := <-wl.fail:
		return "", err
	case <-time.After(timeout):
		return "", fmt.Errorf("the browser never came back from %s; `yas login -device` works without one", what)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Authorize is phase one: the plain OAuth authorize URL. A returning user is
// redirected straight back; a first-time user clicks Authorize once.
func (wl *webLogin) Authorize(ctx context.Context) (string, error) {
	u := "https://github.com/login/oauth/authorize?client_id=" + wl.ClientID +
		"&redirect_uri=http%3A%2F%2F127.0.0.1%3A" + strconv.Itoa(wl.port) + "%2Fcallback" +
		"&state=" + wl.state
	fmt.Fprintf(wl.Out, "\n  Opening GitHub to sign in (the callback returns to 127.0.0.1:%d):\n\n    %s\n\n", wl.port, u)
	wl.open(u)
	return wl.await(ctx, "the sign-in page")
}

// PromptInstall is phase two, reached only when the account has no
// installations: pick repositories now, at sign-in, instead of meeting a git
// failure inside a box later. The redirect lands on the same listener; its
// code is spent on arrival and deliberately ignored — identity was settled
// in phase one.
func (wl *webLogin) PromptInstall(ctx context.Context) {
	u := "https://github.com/apps/" + wl.Slug + "/installations/new?state=" + wl.state
	fmt.Fprintf(wl.Out, "\n  The app is not installed on any of your repositories yet, so boxes\n"+
		"  cannot reach your code. Pick the repositories your boxes may touch:\n\n    %s\n\n", u)
	wl.open(u)
	if _, err := wl.await(ctx, "the install page"); err != nil {
		fmt.Fprintf(wl.Out, "  (install not confirmed: %v — scratch boxes work either way)\n", err)
	}
}
