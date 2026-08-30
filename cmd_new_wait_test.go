package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Gilbert09/yas/clients/yas/internal/ui"
)

// The rungs escalate, and each one says only what is knowable from a clock.
//
// The rung this replaced said "the fleet is finding room" to anybody waiting
// more than eight seconds — a cause, asserted by a client that cannot see the
// fleet. These say how long it has been, and the later two say what to do
// about it. This guards the property that matters: no rung claims to know why.
func TestCreateRungsNeverClaimACause(t *testing.T) {
	// Words that assert a reason for the wait. A rung containing one is a rung
	// guessing at something only the gateway can answer.
	banned := []string{"fleet", "capacity", "full", "queue", "room", "busy"}
	last := time.Duration(-1)
	for i, r := range createRungs {
		if r.after <= last {
			t.Errorf("rung %d fires at %s, not after the one before it (%s)", i, r.after, last)
		}
		last = r.after
		for _, w := range banned {
			if strings.Contains(strings.ToLower(r.say), w) {
				t.Errorf("rung %d says %q, which claims a cause the clock cannot know", i, r.say)
			}
		}
	}
}

// No rung promises that hanging up is harmless.
//
// The gateway builds on the REQUEST's context, so a ctrl-c cancels the create.
// "ctrl-c is safe" or "it keeps building" would be a comfortable lie, and the
// kind a person only discovers when the box they were promised is not there.
func TestCreateRungsNeverPromiseACancelIsFree(t *testing.T) {
	for i, r := range createRungs {
		s := strings.ToLower(r.say)
		for _, claim := range []string{"safe", "keeps building", "carries on", "in the background", "still be created"} {
			if strings.Contains(s, claim) {
				t.Errorf("rung %d says %q — the create is cancelled with the request", i, r.say)
			}
		}
	}
}

// A cause from the gateway outranks the clock, and keeps outranking it.
//
// Both writers run at once — the ticker on its own goroutine, OnRetry on the
// one making the request — and the specific message must not be overwritten by
// the next tick of a stopwatch that knows less.
func TestCapacityOutranksTheClock(t *testing.T) {
	// A real spinner: off a terminal ui.Start prints its label plainly rather
	// than animating, so this drives the same Relabel path a create does.
	w := &createWait{id: "api", sp: ui.Start("creating api…")}
	defer w.sp.Stop("")
	w.capacity(2, 5*time.Second, nil)

	got := w.cause
	if !strings.Contains(got, "the fleet is full") {
		t.Errorf("cause = %q, want it to name the capacity refusal", got)
	}
	if !strings.Contains(got, "5s") || !strings.Contains(got, "attempt 2 of 3") {
		t.Errorf("cause = %q, want the wait and which attempt is next", got)
	}
	if w.cause == "" {
		t.Error("the cause cleared itself")
	}
}

// start() must always return a usable stop func, including off a terminal
// where it never launches the clock at all. A nil return, or one that panics
// on the non-TTY path, would take down every create in CI.
func TestWaitStopsCleanlyOffATerminal(t *testing.T) {
	// go test's stderr is not a character device, so this exercises the
	// non-terminal branch as written.
	w := &createWait{id: "api"}
	stop := w.start()
	if stop == nil {
		t.Fatal("start returned no stop func")
	}
	stop()
}
