package sshutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

// hostErr builds the error the gateway actually returns while a host is down,
// by making the client parse a real response rather than by hand-rolling the
// type — which is the point: the client owns that shape, and a test that
// invented it would keep passing if the wire format changed.
func hostErr(t *testing.T, status int, kind string, retryAfter string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": kind, "message": "could not reach the host holding this sandbox",
		})
	}))
	t.Cleanup(srv.Close)

	cl := &api.Client{BaseURL: srv.URL, Key: "yas_sk_test", Sleep: func(time.Duration) {}}
	_, err := cl.Get(t.Context(), "some-box")
	if err == nil {
		t.Fatal("the client accepted an error response")
	}
	return err
}

// A reconnect must survive the host being briefly unreachable, because that is
// the outage it exists for.
//
// # WATCHED FAIL
//
// Restore `sshTransportFailed` as the loop's gate and the two host cases here
// return false: the loop then treats the FIRST retry as fatal and prints
// "could not reach the host holding this sandbox". Which is what happened live
// on 2026-09-08, twice, during a fleet deploy — the reconnect announced itself
// and then gave up in the same breath, because an attempt that fails before ssh
// starts arrives as an API error, not as ssh exiting 255.
func TestAReconnectRidesOutAHostThatIsBrieflyUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"ssh's own transport failure", exitCode(t, 255), true},
		{"the host is not listening (fleetd restarting)",
			hostErr(t, http.StatusServiceUnavailable, "host_unreachable", "5"), true},
		{"the host answered and then stopped",
			hostErr(t, http.StatusGatewayTimeout, "host_silent", ""), true},

		// The other side of the line: things a second attempt cannot fix.
		{"the box's own command failed", exitCode(t, 1), false},
		{"the fleet has no capacity", hostErr(t, http.StatusServiceUnavailable, "no_capacity", "30"), false},
		{"the box does not exist", hostErr(t, http.StatusNotFound, "not_found", ""), false},
		{"the key was refused", hostErr(t, http.StatusUnauthorized, "unauthorized", ""), false},
		{"nothing went wrong", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := worthReconnecting(tc.err); got != tc.want {
				t.Errorf("worthReconnecting(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// no_capacity is a 503 too, and it must NOT be swept in with the host being
// down: that one is the fleet declining to place a box, and re-dialling it for
// 90 seconds would sit on a refusal the caller should see.
func TestFleetPressureIsNotMistakenForAHostOutage(t *testing.T) {
	err := hostErr(t, http.StatusServiceUnavailable, "no_capacity", "30")
	if api.IsHostTransient(err) {
		t.Error("a no-capacity refusal was read as a host outage; they share a status code and differ in kind")
	}
	if !api.IsNoCapacity(err) {
		t.Error("a no-capacity refusal stopped being one")
	}
}

// The server's own Retry-After is honoured when it is longer than the backoff.
// It says 5 seconds while fleetd restarts, and dialling at 1 second spends
// attempts from a 90-second window on a socket that is not open yet.
func TestTheServersRetryAfterIsRead(t *testing.T) {
	if got := api.RetryAfter(hostErr(t, http.StatusServiceUnavailable, "host_unreachable", "5")); got.Seconds() != 5 {
		t.Errorf("RetryAfter = %v, want 5s", got)
	}
	if got := api.RetryAfter(hostErr(t, http.StatusGatewayTimeout, "host_silent", "")); got != 0 {
		t.Errorf("RetryAfter = %v with no header, want 0", got)
	}
	if got := api.RetryAfter(exitCode(t, 255)); got != 0 {
		t.Errorf("RetryAfter on a non-API error = %v, want 0", got)
	}
}

// exitCode produces a real *exec.ExitError with the given code.
func exitCode(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+itoa(code)).Run()
	if err == nil {
		t.Fatalf("exit %d produced no error", code)
	}
	return err
}
