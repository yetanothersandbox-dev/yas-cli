package tui

import "testing"

// The size rows must show this account's real numbers, and must mark the one it
// would get anyway. "server default (MiB)" told you a default existed and not
// what it was, so the only way to learn it was to make a box and look.
func TestTheLadderMarksTheAccountsOwnDefault(t *testing.T) {
	d := Defaults{MemMiB: 512, MilliVcpu: 500, MaxMemMiB: 10240, MaxMilliVcpu: 3000}

	mem, at := memLadder(d)
	if at < 0 {
		t.Fatal("the account's own default is not on the ladder, so nothing can be marked")
	}
	if mem[at].value != 512 || mem[at].label != "0.5 GiB" {
		t.Errorf("marked %d (%q), want 512 (0.5 GiB)", mem[at].value, mem[at].label)
	}
	for _, c := range mem {
		if c.value > d.MaxMemMiB {
			t.Errorf("offers %s, which is past this plan's ceiling of %d MiB", c.label, d.MaxMemMiB)
		}
	}

	cpu, at := vcpuLadder(d)
	if at < 0 || cpu[at].value != 500 {
		t.Fatalf("vcpu default not marked (index %d)", at)
	}
	if cpu[at].label != "0.5 vCPU" {
		t.Errorf("label = %q, want 0.5 vCPU — 500 milli is half a vCPU, not 500 of them", cpu[at].label)
	}
}

// A default that is not a round number must still appear, and must not be
// silently rounded to a neighbour: the box it describes is the one that
// actually gets made.
func TestAnUnusualDefaultIsOfferedRatherThanRounded(t *testing.T) {
	got, at := memLadder(Defaults{MemMiB: 1536, MaxMemMiB: 8192})
	if at < 0 {
		t.Fatal("an off-ladder default was dropped")
	}
	if got[at].value != 1536 {
		t.Errorf("marked %d, want the account's actual 1536", got[at].value)
	}
	// And it must land in order, or the arrow keys walk the sizes backwards.
	for i := 1; i < len(got); i++ {
		if got[i].value <= got[i-1].value {
			t.Fatalf("ladder is not ascending at %d: %v", i, values(got))
		}
	}
}

// A plan with a small ceiling must not offer sizes it would refuse.
func TestTheLadderStopsAtThePlanCeiling(t *testing.T) {
	got, _ := memLadder(Defaults{MemMiB: 512, MaxMemMiB: 512})
	if len(got) != 1 || got[0].value != 512 {
		t.Fatalf("ladder = %v, want just the one size this plan allows", values(got))
	}
}

// No ceiling reported is not the same as a ceiling of zero. A form that offered
// nothing would make the account unable to create a box at all.
func TestNoCeilingStillOffersSizes(t *testing.T) {
	got, _ := memLadder(Defaults{MemMiB: 2048})
	if len(got) < 2 {
		t.Fatalf("ladder = %v, want the full set when no ceiling is known", values(got))
	}
}

// And nothing known at all still has to produce a usable form.
func TestAnEmptyDefaultsStillProducesALadder(t *testing.T) {
	got, at := memLadder(Defaults{})
	if len(got) == 0 {
		t.Fatal("no sizes at all; the form could not create a box")
	}
	if at != -1 {
		t.Errorf("marked index %d as the default when none is known", at)
	}
}

func TestSizeLabelsReadLikeTheDashboard(t *testing.T) {
	for _, tc := range []struct {
		mib  int
		want string
	}{{512, "0.5 GiB"}, {1024, "1 GiB"}, {1536, "1.5 GiB"}, {16384, "16 GiB"}} {
		if got := gib(tc.mib); got != tc.want {
			t.Errorf("gib(%d) = %q, want %q", tc.mib, got, tc.want)
		}
	}
	for _, tc := range []struct {
		milli int
		want  string
	}{{500, "0.5 vCPU"}, {1000, "1 vCPU"}, {3000, "3 vCPU"}} {
		if got := vcpuLabel(tc.milli); got != tc.want {
			t.Errorf("vcpuLabel(%d) = %q, want %q", tc.milli, got, tc.want)
		}
	}
}

func values(cs []choice) []int {
	out := make([]int, len(cs))
	for i, c := range cs {
		out[i] = c.value
	}
	return out
}
