package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

// A parked box is not counting down its LIFETIME — it is not running. What
// ends it is retention, and showing the lifetime wall for one would name a
// time at which nothing happens.
func TestTheClockShownIsTheOneThatAppliesToTheState(t *testing.T) {
	dead := time.Now().Add(3 * time.Hour)
	kept := time.Now().Add(6 * 24 * time.Hour)
	for _, tc := range []struct {
		status string
		want   time.Time
		why    string
	}{
		{"idle", dead, "a running box is destroyed at its lifetime wall"},
		{"busy", dead, "a running box is destroyed at its lifetime wall"},
		{"suspended", kept, "a parked box ends at retention, not at a lifetime it is not spending"},
	} {
		got := goneAt(api.Sandbox{Status: tc.status, Deadline: dead, SuspendExpiresAt: kept})
		if !got.Equal(tc.want) {
			t.Errorf("%s: got %v, want %v — %s", tc.status, got, tc.want, tc.why)
		}
	}
}

// The picker must not invent a deadline for a box whose detail has not landed.
func TestNoClockYetShowsNothing(t *testing.T) {
	if got := untilText(time.Time{}); got != "" {
		t.Errorf("untilText(zero) = %q, want empty: the fetch has not landed yet", got)
	}
	if soon(time.Time{}) {
		t.Error("an unknown deadline was treated as imminent")
	}
}

// Both halves are there: how long is left, and when exactly.
func TestUntilTextSaysHowLongAndWhen(t *testing.T) {
	at := time.Now().Add(26 * time.Hour)
	got := untilText(at)
	if !strings.HasPrefix(got, "26h") {
		t.Errorf("got %q, want it to lead with the time remaining", got)
	}
	if !strings.Contains(got, at.Local().Format("2 Jan 15:04")) {
		t.Errorf("got %q, want the local wall-clock time too — a deadline is only useful "+
			"in the reader's own timezone", got)
	}
}

func TestUntilTextScales(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "<1m"},
		{20 * time.Minute, "20m"},
		{5 * time.Hour, "5h"},
		{47 * time.Hour, "47h"},
		{6 * 24 * time.Hour, "6d"},
	} {
		got := untilText(time.Now().Add(tc.in))
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("in %v: got %q, want it to start %q", tc.in, got, tc.want)
		}
	}
}

// A deadline already past must read as imminent rather than as a negative
// number or a stale future time.
func TestAPastDeadlineReadsAsImminent(t *testing.T) {
	got := untilText(time.Now().Add(-time.Minute))
	if got != "any moment now" {
		t.Errorf("got %q, want the reaper to sound close", got)
	}
}

func TestOnlyACloseDeadlineIsWarned(t *testing.T) {
	if soon(time.Now().Add(5 * time.Hour)) {
		t.Error("five hours away was coloured as urgent; everything urgent means nothing is")
	}
	if !soon(time.Now().Add(10 * time.Minute)) {
		t.Error("ten minutes away was not flagged")
	}
}
