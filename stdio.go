package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// cmdStdio is the ProxyCommand half of every connection: ssh invokes it as
// `yas stdio %h`, it performs the ssh-stream upgrade against the gateway and
// splices stdin/stdout onto the connection. Invoked by ssh, never by hand —
// which is why every diagnostic goes to stderr: stdout IS the ssh byte
// stream, and one stray print corrupts the protocol.
//
// No reconnect logic on purpose. ssh owns retry; a ProxyCommand that
// reconnected underneath it would hand ssh a brand-new TCP stream mid-session
// and the transport layer would (rightly) refuse to trust it.
func cmdStdio(args []string) error {
	fs := flag.NewFlagSet("stdio", flag.ExitOnError)
	timeout := fs.Duration("timeout", 10*time.Second, "budget for the upgrade handshake")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: yas stdio <sandbox-id> (ssh passes %h here)")
	}
	// ssh hands over the HOST it was told to connect to, which is the pin
	// label `sandbox-<id>`; accept the bare id too so a human poking at it by
	// hand gets the same behaviour.
	id := strings.TrimPrefix(fs.Arg(0), "sandbox-")

	_, cl, err := loadClient()
	if err != nil {
		return err
	}

	conn, br, err := dialStream(cl.BaseURL, cl.Key, id, *timeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Both directions, first-one-wins — the same reading fleetctl's ssh-proxy
	// makes: ssh's own teardown is the session ending, and waiting for the
	// second copy would leave this process behind on every disconnect.
	//
	// conn->stdout goes THROUGH the bufio.Reader: any guest bytes that arrived
	// behind the 101 headers are already buffered in it, and reading the raw
	// conn would silently drop them — which stalls ssh on its first byte.
	done := make(chan error, 2)
	go func() { _, err := io.Copy(conn, os.Stdin); done <- err }()
	go func() { _, err := io.Copy(os.Stdout, br); done <- err }()
	if err := <-done; err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// dialStream opens the raw connection and performs the websocket-shaped
// upgrade by hand. By hand because net/http's client owns its connections and
// will not give one back after a 101; the handshake is ~40 lines and this
// program depends on every byte of it.
func dialStream(base, token, id string, timeout time.Duration) (net.Conn, *bufio.Reader, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, nil, fmt.Errorf("base URL: %w", err)
	}
	addr := u.Host
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	var conn net.Conn
	switch u.Scheme {
	case "https":
		if !strings.Contains(addr, ":") {
			addr += ":443"
		}
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: u.Hostname()})
	case "http":
		// Plain http exists for tests and local gateways; the product default
		// is TLS.
		if !strings.Contains(addr, ":") {
			addr += ":80"
		}
		conn, err = dialer.Dial("tcp", addr)
	default:
		return nil, nil, fmt.Errorf("base URL scheme %q is not http(s)", u.Scheme)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("dialing the gateway: %w", err)
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		conn.Close()
		return nil, nil, err
	}
	wsKey := base64.StdEncoding.EncodeToString(nonce)

	_ = conn.SetDeadline(time.Now().Add(timeout))
	req := "GET " + strings.TrimRight(u.Path, "/") + "/v1/sandboxes/" + id + "/ssh-stream HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Authorization: Bearer " + token + "\r\n" +
		"Connection: Upgrade\r\n" +
		"Upgrade: websocket\r\n" +
		"Sec-WebSocket-Key: " + wsKey + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("writing the upgrade request: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("reading the upgrade response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer conn.Close()
		return nil, nil, errors.New(streamRefusal(resp, id))
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != websocketAccept(wsKey) {
		conn.Close()
		return nil, nil, fmt.Errorf("the gateway's Sec-WebSocket-Accept is wrong (got %q); refusing the stream", got)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, br, nil
}

// streamRefusal turns a non-101 into the one actionable line ssh will show.
func streamRefusal(resp *http.Response, id string) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var wire struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &wire)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "no sandbox " + id + " — `yas list` shows yours"
	case wire.Error == "sandbox_not_live":
		return "sandbox " + id + " is not running; if it is suspended, `yas resume " + id + "` first"
	case resp.StatusCode == http.StatusUnauthorized:
		return "the gateway refused this API key; `yas login` to replace it"
	case wire.Message != "":
		return "the gateway answered " + resp.Status + ": " + wire.Message
	default:
		return "the gateway answered " + resp.Status + ": " + strings.TrimSpace(string(body))
	}
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}
