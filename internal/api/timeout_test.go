package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The bug this file exists for: a resume of a hibernated box has to wait for a
// 16 GiB pull out of the bucket before the host says anything, and the CLI cut
// every call off at sixty seconds from its own end. `yas ssh` reported a
// failure for a resume that then completed.
func TestResumeWaitsLongerThanAnOrdinaryCall(t *testing.T) {
	if ResumeTimeout <= DefaultRequestTimeout {
		t.Fatalf("ResumeTimeout %v does not exceed the ordinary %v, so a hibernated "+
			"resume is cut off exactly as before", ResumeTimeout, DefaultRequestTimeout)
	}
	if ResumeTimeout > time.Hour {
		t.Errorf("ResumeTimeout %v is long enough to be indistinguishable from a hang; a "+
			"resume that slow is broken and the caller should be told", ResumeTimeout)
	}
}

// The timeout must live on the context, not on http.Client. A client-wide
// Timeout is a ceiling no caller can raise — it was why a longer deadline on
// the resume call would have changed nothing.
func TestTheDefaultClientImposesNoCeilingOfItsOwn(t *testing.T) {
	if to := (&Client{}).http().Timeout; to != 0 {
		t.Fatalf("http().Timeout = %v, want 0: a client-wide timeout caps every call "+
			"including the ones that are meant to take minutes", to)
	}
}

// A caller who sets their own deadline keeps it, in both directions: `bound`
// must not extend a short one or shorten a long one.
func TestACallersOwnDeadlineIsLeftAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	got, cancel2 := bound(ctx, time.Hour)
	defer cancel2()
	d, ok := got.Deadline()
	if !ok || time.Until(d) > time.Second {
		t.Fatalf("bound replaced the caller's 5ms deadline (deadline in %v)", time.Until(d))
	}
}

func TestAContextWithNoDeadlineGetsTheDefault(t *testing.T) {
	got, cancel := bound(context.Background(), 30*time.Second)
	defer cancel()
	if _, ok := got.Deadline(); !ok {
		t.Fatal("an unbounded context stayed unbounded, so a dead host hangs the CLI for ever")
	}
}

// End to end against a real server: a slow resume that beats ResumeTimeout
// succeeds, where the old fixed ceiling would have failed it.
func TestASlowResumeStillSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond) // stands in for the pull
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Key: "k"}
	if err := c.Resume(context.Background(), "box"); err != nil {
		t.Fatalf("a resume that took 150ms failed: %v", err)
	}
}

// ctrl-C must still work on a long exec. An earlier version of this fix used
// context.WithoutCancel to escape the default bound, which also threw away the
// caller's cancellation — a followed exec would have ignored ctrl-C entirely.
func TestAFollowedExecStillHonoursTheCallersCancellation(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done() // never finishes on its own
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{BaseURL: srv.URL, Key: "k"}
	done := make(chan error, 1)
	go func() { done <- c.ExecFollow(ctx, "box", ExecRequest{}, func(EventsPage) {}) }()

	<-started
	cancel() // the ctrl-C

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelling the context did not stop a followed exec; ctrl-C would hang")
	}
}
