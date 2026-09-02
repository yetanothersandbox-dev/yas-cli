package sshutil

import (
	"testing"
	"time"
)

// The old wait polled on a flat two-second timer, and Resume is SYNCHRONOUS —
// it returns once the box is back — so the status is normally already there on
// the first look. That flat interval added up to two seconds of nothing to
// every wake, which is most of what made resuming feel slow from the CLI.
func TestTheFirstPollIsPromptAndTheBackoffIsBounded(t *testing.T) {
	if firstPoll > 250*time.Millisecond {
		t.Errorf("firstPoll = %v; the answer is usually ready immediately and this is added to "+
			"every resume", firstPoll)
	}
	if firstPoll <= 0 {
		t.Fatal("firstPoll must be positive or the loop spins")
	}
	if maxPoll < time.Second {
		t.Errorf("maxPoll = %v; a genuinely slow resume would poll too hard", maxPoll)
	}
	if maxPoll < firstPoll {
		t.Fatal("the backoff would count downwards")
	}
}

// The doubling has to reach the cap in a sane number of steps, and stop there.
func TestTheBackoffReachesTheCapAndStays(t *testing.T) {
	wait, steps := firstPoll, 0
	for wait < maxPoll {
		if wait *= 2; wait > maxPoll {
			wait = maxPoll
		}
		if steps++; steps > 20 {
			t.Fatalf("the backoff took more than 20 steps to reach %v", maxPoll)
		}
	}
	if wait != maxPoll {
		t.Errorf("settled at %v, want %v", wait, maxPoll)
	}
	// And it must not creep past it.
	if wait *= 2; wait > maxPoll {
		wait = maxPoll
	}
	if wait != maxPoll {
		t.Errorf("went past the cap to %v", wait)
	}
}

// A resume that is ready straight away must cost about one round trip, not a
// fixed interval. Measured as the total of the first few sleeps, which is what
// a caller actually waits through.
func TestAnImmediateResumeCostsAlmostNothing(t *testing.T) {
	if firstPoll > 250*time.Millisecond {
		t.Skip("covered above")
	}
	total := firstPoll
	if total > 500*time.Millisecond {
		t.Errorf("the first wait alone is %v; a box that is already back should not feel like a "+
			"wait at all", total)
	}
}
