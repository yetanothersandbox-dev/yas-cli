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

// The two pages a browser ever sees. Self-contained, styled inline, and
// served with an explicit charset — without one the em dash renders as
// mojibake and the last thing a user sees of the login is a glitch.
const callbackPage = `<!doctype html><html><head><meta charset="utf-8"><title>yas</title><style>
:root{color-scheme:light dark}
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#fafafa;color:#1a1a1a}
.card{text-align:center;padding:3rem 4rem;border-radius:12px;background:#fff;box-shadow:0 1px 3px rgba(0,0,0,.08),0 8px 24px rgba(0,0,0,.06)}
.mark{font-size:2.4rem}
h1{font-size:1.15rem;margin:.8rem 0 .35rem;font-weight:600}
p{margin:0;color:#666;font-size:.9rem}
@media (prefers-color-scheme:dark){body{background:#111;color:#eee}.card{background:#1c1c1e;box-shadow:none;border:1px solid #333}p{color:#999}}
</style></head><body><div class="card"><div class="mark">%s</div><h1>%s</h1><p>%s</p></div></body></html>`

func writeCallbackPage(w http.ResponseWriter, mark, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, callbackPage, mark, title, detail)
}

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
			writeCallbackPage(w, "✕", "Sign-in was not completed", "You can close this tab and try again from your terminal.")
			wl.fail <- fmt.Errorf("github reported %q", e)
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "no code in the callback", http.StatusBadRequest)
			return
		}
		writeCallbackPage(w, "✓", "You're all set", "You can close this tab and return to your terminal.")
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
// redirected straight back; a first-time user clicks Authorize once. The URL
// itself is machine noise and stays off the terminal — a machine that cannot
// open a browser is what `yas login -device` is for, and the timeout says so.
func (wl *webLogin) Authorize(ctx context.Context) (string, error) {
	u := "https://github.com/login/oauth/authorize?client_id=" + wl.ClientID +
		"&redirect_uri=http%3A%2F%2F127.0.0.1%3A" + strconv.Itoa(wl.port) + "%2Fcallback" +
		"&state=" + wl.state
	fmt.Fprintln(wl.Out, "Opening GitHub in your browser to sign in…")
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
	fmt.Fprintln(wl.Out, "First sign-in: choose which repositories your boxes may access.")
	fmt.Fprintf(wl.Out, "Opening github.com/apps/%s in your browser…\n", wl.Slug)
	wl.open(u)
	if _, err := wl.await(ctx, "the install page"); err != nil {
		fmt.Fprintf(wl.Out, "(repository access not confirmed: %v — scratch boxes work either way)\n", err)
	}
}
