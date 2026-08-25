// Package apitest is a fake gateway: the tenant route family over an
// in-memory store, plus scripted refusals. The client's tests drive the real
// client against this over a real httptest server, so what they assert is
// what a caller receives, not what a method intended.
package apitest

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// wsAccept mirrors the RFC 6455 accept digest the client verifies.
func wsAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

type Box struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	MemMiB    int       `json:"memMib"`
	Keys      []string  `json:"-"`
}

// Gateway is one fake control plane. Script* fields, when set, replace the
// happy path for the next matching request.
type Gateway struct {
	mu    sync.Mutex
	boxes map[string]*Box
	// CreateRefusals is a queue of canned refusals the next creates answer
	// with; empty means accept.
	CreateRefusals []Refusal
	// EchoStream makes /ssh-stream a hijacking echo server.
	EchoStream bool
	// Frames scripts /exec?follow=1: each string is one SSE line group
	// written verbatim, so a test can split frames and inject pings.
	Frames []string
	// PollPages scripts /events by cursor.
	PollPages map[int64]string
	// SignupKey, when set, makes POST /v1/signup answer 201 with this key for
	// the token "gho_good" and 401 for anything else.
	SignupKey string
	// SignupInstallations is the installations count signup reports.
	SignupInstallations int
	// SignupSaw records the last signup body, so a test can assert the
	// refresh half of the pair actually travelled.
	SignupSaw map[string]any
	// UserCreds records the last PUT /v1/user/credentials body.
	UserCreds map[string]any
	// KeyRows backs /v1/keys; Add rows or let POST create them.
	KeyRows []map[string]any

	Requests []string // method+path, in order, for assertions
}

type Refusal struct {
	Status     int
	Kind       string
	Message    string
	RetryAfter string
}

func New() *Gateway {
	return &Gateway{boxes: map[string]*Box{}, PollPages: map[int64]string{}}
}

func (g *Gateway) Add(b Box) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if b.Status == "" {
		b.Status = "idle"
	}
	cp := b
	g.boxes[b.ID] = &cp
}

func (g *Gateway) Box(id string) (Box, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, ok := g.boxes[id]
	if !ok {
		return Box{}, false
	}
	return *b, true
}

func (g *Gateway) record(r *http.Request) {
	g.mu.Lock()
	g.Requests = append(g.Requests, r.Method+" "+r.URL.Path)
	g.mu.Unlock()
}

func writeErr(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": kind, "message": msg})
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.record(r)
	// Signup is the one public route: it is how the first credential is born.
	if r.URL.Path == "/v1/signup" && r.Method == http.MethodPost {
		g.signup(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/user") || strings.HasPrefix(r.URL.Path, "/v1/keys") {
		g.userAPI(w, r)
		return
	}
	// textproto trims trailing whitespace, so an empty key arrives as a bare "Bearer".
	if auth := r.Header.Get("Authorization"); auth == "" || strings.TrimSpace(strings.TrimPrefix(auth, "Bearer")) == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no bearer token")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/sandboxes")
	switch {
	case path == "" && r.Method == http.MethodPost:
		g.create(w, r)
	case path == "" && r.Method == http.MethodGet:
		g.list(w)
	default:
		id, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		g.byID(w, r, id, rest)
	}
}

func (g *Gateway) signup(w http.ResponseWriter, r *http.Request) {
	if g.SignupKey == "" {
		writeErr(w, http.StatusForbidden, "signup_disabled", "not scripted")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil ||
		(body["githubToken"] != "gho_good" && body["code"] != "good-code") {
		writeErr(w, http.StatusUnauthorized, "github_refused", "bad token")
		return
	}
	g.mu.Lock()
	g.SignupSaw = body
	g.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tenantId": "gh-583231", "login": "octocat", "keyId": "ak_test",
		"key": g.SignupKey, "tenantCreated": true, "installations": g.SignupInstallations,
	})
}

// userAPI is the account surface, authenticated like everything else.
func (g *Gateway) userAPI(w http.ResponseWriter, r *http.Request) {
	if auth := r.Header.Get("Authorization"); auth == "" || strings.TrimSpace(strings.TrimPrefix(auth, "Bearer")) == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "no bearer token")
		return
	}
	switch {
	case r.URL.Path == "/v1/user" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenantId": "gh-583231", "login": "octocat",
			"github": map[string]any{"connected": true, "login": "octocat"},
		})
	case r.URL.Path == "/v1/user/credentials" && r.Method == http.MethodPut:
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		g.UserCreds = body
		g.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/v1/keys" && r.Method == http.MethodGet:
		g.mu.Lock()
		rows := append([]map[string]any(nil), g.KeyRows...)
		g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": rows})
	case r.URL.Path == "/v1/keys" && r.Method == http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Name == "" {
			writeErr(w, http.StatusBadRequest, "bad_request", "name is required")
			return
		}
		g.mu.Lock()
		id := "ak_" + strconv.Itoa(len(g.KeyRows)+1)
		g.KeyRows = append(g.KeyRows, map[string]any{
			"id": id, "name": body.Name, "createdAt": time.Now().UTC(), "live": true,
		})
		g.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"keyId": id, "name": body.Name, "key": "yas_sk_service"})
	case strings.HasPrefix(r.URL.Path, "/v1/keys/") && r.Method == http.MethodDelete:
		id := strings.TrimPrefix(r.URL.Path, "/v1/keys/")
		g.mu.Lock()
		found := false
		for _, row := range g.KeyRows {
			if row["id"] == id {
				row["live"] = false
				found = true
			}
		}
		g.mu.Unlock()
		if !found {
			writeErr(w, http.StatusNotFound, "not_found", "no such key")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeErr(w, http.StatusNotFound, "not_found", "no such route in the fake")
	}
}

func (g *Gateway) create(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	if len(g.CreateRefusals) > 0 {
		ref := g.CreateRefusals[0]
		g.CreateRefusals = g.CreateRefusals[1:]
		g.mu.Unlock()
		if ref.RetryAfter != "" {
			w.Header().Set("Retry-After", ref.RetryAfter)
		}
		writeErr(w, ref.Status, ref.Kind, ref.Message)
		return
	}
	g.mu.Unlock()

	var req struct {
		ID      string   `json:"id"`
		MemMiB  int      `json:"memMib"`
		SSHKeys []string `json:"sshKeys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	g.Add(Box{ID: req.ID, Status: "idle", MemMiB: req.MemMiB, CreatedAt: time.Now().UTC(), Keys: req.SSHKeys})
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": req.ID})
}

func (g *Gateway) list(w http.ResponseWriter) {
	g.mu.Lock()
	out := make([]map[string]any, 0, len(g.boxes))
	for _, b := range g.boxes {
		// Like the real gateway: summaries carry NO status and NO host.
		out = append(out, map[string]any{"id": b.ID, "createdAt": b.CreatedAt, "placed": true})
	}
	g.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"sandboxes": out})
}

func (g *Gateway) byID(w http.ResponseWriter, r *http.Request, id, rest string) {
	g.mu.Lock()
	b, ok := g.boxes[id]
	g.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such sandbox")
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(b)
	case rest == "" && r.Method == http.MethodDelete:
		g.mu.Lock()
		delete(g.boxes, id)
		g.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case rest == "suspend":
		g.setStatus(id, "suspended")
		w.WriteHeader(http.StatusOK)
	case rest == "resume":
		g.setStatus(id, "idle")
		w.WriteHeader(http.StatusOK)
	case rest == "ssh":
		var req struct {
			PublicKeys []string `json:"publicKeys"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		b.Keys = req.PublicKeys
		g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"user":          "fleet",
			"hostPublicKey": "ssh-ed25519 AAAATESTKEY",
			"fingerprint":   "SHA256:test",
			"knownHosts":    "sandbox-" + id + " ssh-ed25519 AAAATESTKEY",
			"host":          "sandbox-" + id,
			"proxyCommand":  "fleetctl ssh-proxy -root /var/lib/fleet -id " + id,
		})
	case rest == "ssh-stream":
		g.stream(w, r)
	case rest == "exec":
		g.exec(w, r)
	case rest == "events":
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		g.mu.Lock()
		page, ok := g.PollPages[after]
		g.mu.Unlock()
		if !ok {
			page = `{"events":[],"cursor":` + strconv.FormatInt(after, 10) + `,"terminal":true}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(page))
	default:
		writeErr(w, http.StatusNotFound, "not_found", "no such route in the fake")
	}
}

func (g *Gateway) setStatus(id, status string) {
	g.mu.Lock()
	if b, ok := g.boxes[id]; ok {
		b.Status = status
	}
	g.mu.Unlock()
}

// stream answers the websocket-shaped upgrade with a 101 and echoes bytes,
// hijacking exactly as fleetd does.
func (g *Gateway) stream(w http.ResponseWriter, r *http.Request) {
	if !g.EchoStream {
		writeErr(w, http.StatusConflict, "sandbox_not_live", "the fake has no stream scripted")
		return
	}
	if r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Sec-WebSocket-Key") == "" {
		writeErr(w, http.StatusUpgradeRequired, "upgrade_required", "send the websocket handshake")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "not_hijackable", "recorder in use?")
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	accept := wsAccept(r.Header.Get("Sec-WebSocket-Key"))
	_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n"))
	// A banner FIRST, like a real sshd — the client must not lose bytes that
	// arrive behind its own 101 read.
	_, _ = conn.Write([]byte("SSH-2.0-fake\r\n"))
	echo(conn, brw)
}

func echo(conn net.Conn, brw *bufio.ReadWriter) {
	buf := make([]byte, 4096)
	for {
		n, err := brw.Reader.Read(buf)
		if n > 0 {
			if _, werr := conn.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (g *Gateway) exec(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("follow") != "1" {
		writeErr(w, http.StatusBadRequest, "bad_request", "the fake only serves follow=1")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no_flusher", "")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	g.mu.Lock()
	frames := append([]string(nil), g.Frames...)
	g.mu.Unlock()
	for _, f := range frames {
		_, _ = fmt.Fprint(w, f)
		fl.Flush()
	}
}
