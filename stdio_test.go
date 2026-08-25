package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Gilbert09/yas/clients/yas/internal/api/apitest"
)

// dialStream against the hijacking fake: the 101 must verify, and — the bug
// this test exists for — the guest banner the fake writes IMMEDIATELY after
// its 101 must come out of the returned reader, not be lost in the bufio
// buffer behind the response headers.
func TestDialStreamKeepsBytesBufferedPastThe101(t *testing.T) {
	gw := apitest.New()
	gw.Add(apitest.Box{ID: "b1"})
	gw.EchoStream = true
	srv := httptest.NewServer(gw)
	defer srv.Close()

	conn, br, err := dialStream(srv.URL, "yas_sk_test", "b1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	banner := make([]byte, len("SSH-2.0-fake\r\n"))
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(br, banner); err != nil {
		t.Fatalf("the banner behind the 101 never arrived: %v", err)
	}
	if string(banner) != "SSH-2.0-fake\r\n" {
		t.Fatalf("read %q, want the fake's banner", banner)
	}

	// And the echo path proves the write half.
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(br, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("echo = %q, err %v", echo, err)
	}
}

// A refusal must come back as ONE actionable line naming the remedy, because
// ssh shows the user nothing but our stderr.
func TestDialStreamRefusalsAreActionable(t *testing.T) {
	gw := apitest.New()
	gw.Add(apitest.Box{ID: "asleep"}) // EchoStream false → scripted 409 sandbox_not_live
	srv := httptest.NewServer(gw)
	defer srv.Close()

	_, _, err := dialStream(srv.URL, "yas_sk_test", "asleep", 5*time.Second)
	if err == nil {
		t.Fatal("a 409 dialed as a stream")
	}
	if !strings.Contains(err.Error(), "yas resume asleep") {
		t.Fatalf("the refusal does not name the remedy: %v", err)
	}

	_, _, err = dialStream(srv.URL, "yas_sk_test", "never-existed", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "yas list") {
		t.Fatalf("the 404 refusal does not point at `yas list`: %v", err)
	}
}
