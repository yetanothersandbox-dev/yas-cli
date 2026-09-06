package main

import (
	"testing"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
)

// The pool line has to survive a pool that is not a whole number of gigabytes.
//
// It rendered the limit with "%.0f", which is fine for the 10 GiB tier it was
// written against and prints the free tier's half a gigabyte as "0". `yas ls`
// then said "pool 0.5/0 GiB" — a full pool with no capacity at all, on the
// command people run most.
func TestGibShowsOnlyThePrecisionItNeeds(t *testing.T) {
	for _, tc := range []struct {
		mib  int
		want string
	}{
		// The bug, exactly: the free tier's whole pool.
		{512, "0.5"},
		// Whole sizes stay whole — "10", never "10.0".
		{10240, "10"},
		{1024, "1"},
		{65536, "64"},
		{0, "0"},
		// A quarter is 0.25 GiB and one decimal renders it "0.2" — lossy, but
		// present, which is the whole difference from the "0" this replaced. No
		// tier is this size; the case is here to pin the rounding rather than to
		// endorse it.
		{256, "0.2"},
		{2048 + 512, "2.5"},
	} {
		if got := gib(tc.mib); got != tc.want {
			t.Errorf("gib(%d) = %q, want %q", tc.mib, got, tc.want)
		}
	}
}

// The whole line, on the tier that broke it.
func TestThePoolLineOnTheFreeTier(t *testing.T) {
	var p api.Pool
	p.MemMiB.Limit, p.MemMiB.Used = 512, 512
	p.Boxes.Running = 1

	got := poolLine(&p)
	if want := "pool 0.5/0.5 GiB"; !contains(got, want) {
		t.Errorf("pool line = %q, want it to contain %q — a free account with one box is FULL, "+
			"not out of a pool of nothing", got, want)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
