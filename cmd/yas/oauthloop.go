package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// The parts of a browser sign-in that are the same whoever is signing you in.
//
// Two providers now send a browser back to this machine — OpenAI, for a ChatGPT
// subscription, and Anthropic, for a Claude one — and the pieces below are the
// ones where a second copy would be a liability rather than a duplication. The
// state check is the CSRF guard; the double bind is the fix for a callback that
// silently goes to another process. Both were learned once and neither should
// have to be learned again in a second file.
//
// What is NOT here is anything provider-shaped: client ids, endpoints, scopes
// and the token exchange all live beside the provider they belong to.

// pkcePair mints a verifier and the challenge derived from it.
//
// The verifier never leaves the process that made it. That is the whole
// property PKCE buys: an authorization code delivered to the wrong listener is
// useless to whoever received it, because they cannot produce the verifier the
// token endpoint will ask for.
func pkcePair() (verifier, challenge string, err error) {
	verifier, err = randomURLSafe(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// listenLoopback binds the callback port on BOTH address families.
//
// # Why this is not just Listen("tcp", "127.0.0.1:<port>")
//
// The redirects these providers register are `http://localhost:<port>/...`, and
// on macOS `localhost` resolves to ::1 before 127.0.0.1. Binding IPv4 alone
// means a different process holding IPv6 *:<port> receives the callback instead
// — and because those are different address families, that does NOT raise
// EADDRINUSE, so nothing here would notice.
//
// That is not hypothetical; talyn shipped the IPv4-only version and another tool
// using the same Codex OAuth client answered its callback. Anything built on a
// shared client collides the same way: `codex login`, the Codex CLI, OpenCode,
// and on the Anthropic side `claude` signing itself in.
//
// A SPECIFIC bind beats a wildcard one for routing, which is what takes the
// callback back from a process holding *:<port>. A host with no IPv6 stack is
// not an error — carry on with IPv4. Failing to bind IPv4 is, because that is
// the family we can always be reached on.
//
// conflict names what to close when the port is held, because the answer
// differs per provider and a message that says only "in use" leaves somebody
// hunting.
func listenLoopback(port int, conflict string) ([]net.Listener, error) {
	var out []net.Listener
	// IPv6 first: if this one is going to fail for a reason worth reporting,
	// report it before taking the IPv4 socket.
	if ln, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port)); err == nil {
		out = append(out, ln)
	} else if isAddrInUse(err) {
		return nil, fmt.Errorf("something is already listening on [::1]:%d — %s, then try again", port, conflict)
	}
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		for _, l := range out {
			_ = l.Close()
		}
		if isAddrInUse(err) {
			return nil, fmt.Errorf("something is already listening on 127.0.0.1:%d — %s, then try again", port, conflict)
		}
		return nil, fmt.Errorf("could not open the sign-in callback port %d: %w", port, err)
	}
	return append(out, ln), nil
}

func isAddrInUse(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "address already in use")
}

// callbackHandler answers the redirect and hands the code back.
//
// retry is the command to run again, named in the browser when the sign-in is
// refused — the tab is where somebody is looking when it fails, so it is where
// the next step has to be written.
func callbackHandler(path, state, retry string, codes chan<- string, fails chan<- error) http.Handler {
	var once bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if once {
			return
		}
		once = true
		q := r.URL.Query()
		// The state check is the CSRF guard and it runs before anything else is
		// believed: without it, any page that can reach this loopback could
		// feed us an authorization code minted for a different account.
		if q.Get("state") != state {
			signInPage(w, http.StatusBadRequest, "That sign-in did not come from this terminal.",
				"Close this tab and run `"+retry+"` again.")
			fails <- errors.New("the sign-in came back with a state this terminal did not issue")
			return
		}
		if e := q.Get("error"); e != "" {
			detail := q.Get("error_description")
			if detail == "" {
				detail = e
			}
			signInPage(w, http.StatusOK, "Sign-in cancelled.", detail)
			fails <- fmt.Errorf("the sign-in was ended: %s", detail)
			return
		}
		code := q.Get("code")
		if code == "" {
			signInPage(w, http.StatusBadRequest, "That sign-in came back empty.",
				"Close this tab and run `"+retry+"` again.")
			fails <- errors.New("the sign-in returned no authorization code")
			return
		}
		signInPage(w, http.StatusOK, "Signed in.", "You can close this tab and go back to your terminal.")
		codes <- code
	})
}

// signInPage is the one thing the browser ever sees from us. Plain,
// self-closing text: this window belongs to the provider's flow, not to a
// product surface.
func signInPage(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>yas</title>`+
		`<body style="background:#0d0e10;color:#e8e9ec;font:15px/1.7 ui-monospace,Menlo,monospace;padding:48px">`+
		`<p><strong>%s</strong></p><p style="color:#9aa0a6">%s</p>`, title, detail)
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
