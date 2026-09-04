package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Gilbert09/yas/clients/yas/internal/api"
)

// `yas schedule` reserves a verb, which shadows any program of that name. The
// list of what it dispatches is worth pinning: adding a subcommand is a
// one-line change, and removing one silently is too.
//
// Only the DEFAULT branch is driven here. Every real subcommand calls
// loadClient and would reach the network, and a unit test that talks to the
// production gateway is a test that fails on an aeroplane.
func TestScheduleRejectsAnUnknownSubcommand(t *testing.T) {
	err := cmdSchedule([]string{"frobnicate"})
	if err == nil || !strings.Contains(err.Error(), "unknown schedule command") {
		t.Fatalf("err = %v, want the unknown-command refusal", err)
	}
	// The refusal lists what IS available, because the commonest cause is a
	// half-remembered name rather than a typo.
	for _, sub := range []string{"list", "add", "show", "rm", "pause", "resume", "runs", "now"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("the refusal does not mention %q", sub)
		}
	}
}

// The listing columns, which are the whole interface for "is this working?".
func TestScheduleColumns(t *testing.T) {
	at := time.Date(2026, 9, 5, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		sched    api.Schedule
		wantNext string
		wantWhen string
		wantLast string
	}{
		{
			name: "an enabled schedule shows the server's own countdown",
			sched: api.Schedule{
				Cron: "0 3 * * *", Timezone: "Europe/London",
				Enabled: true, NextRunAt: &at, NextRunIn: 4*3600 + 12*60,
			},
			wantNext: "in 4h12m",
			wantWhen: "0 3 * * * Europe/London",
			wantLast: "-",
		},
		{
			// A zone is not optional in this column: "0 3 * * *" alone is three
			// o'clock somewhere, and the somewhere is the point.
			name:     "an empty timezone reads as UTC rather than as blank",
			sched:    api.Schedule{Cron: "0 3 * * *", Enabled: true, NextRunAt: &at, NextRunIn: 30},
			wantNext: "in 30s",
			wantWhen: "0 3 * * * UTC",
			wantLast: "-",
		},
		{
			name:     "a paused schedule says so instead of showing a time",
			sched:    api.Schedule{Cron: "0 3 * * *", Enabled: false, NextRunAt: &at, NextRunIn: 100},
			wantNext: "paused",
			wantWhen: "0 3 * * * UTC",
			wantLast: "-",
		},
		{
			// Enabled with nothing that will ever fire it. Blank would read as
			// a rendering bug; "never" is the actual answer.
			name:     "an enabled schedule with no next firing says never",
			sched:    api.Schedule{Cron: "0 0 30 2 *", Enabled: true},
			wantNext: "never",
			wantWhen: "0 0 30 2 * UTC",
			wantLast: "-",
		},
		{
			name: "a schedule that has run shows its last outcome",
			sched: api.Schedule{
				Cron: "0 3 * * *", Enabled: true, NextRunAt: &at, NextRunIn: 60,
				LastRun: &api.LastRun{Outcome: api.FiringRefused, FiredAt: at},
			},
			wantNext: "in 1m",
			wantWhen: "0 3 * * * UTC",
			wantLast: "refused " + at.Local().Format("Jan 2 15:04"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextColumn(c.sched); got != c.wantNext {
				t.Errorf("next = %q, want %q", got, c.wantNext)
			}
			if got := cronColumn(c.sched); got != c.wantWhen {
				t.Errorf("when = %q, want %q", got, c.wantWhen)
			}
			if got := lastColumn(c.sched); got != c.wantLast {
				t.Errorf("last = %q, want %q", got, c.wantLast)
			}
		})
	}
}

func TestShortDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{59 * time.Minute, "59m"},
		{time.Hour + 5*time.Minute, "1h05m"},
		{25 * time.Hour, "25h00m"},
		{72 * time.Hour, "3d"},
	}
	for _, c := range cases {
		if got := shortDuration(c.in); got != c.want {
			t.Errorf("shortDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A prompt in a table column must be one line. A newline breaks the alignment
// of every row below it, so the table stops being a table at the first
// paragraph somebody wrote.
func TestTruncateOneLine(t *testing.T) {
	got := truncateOneLine("triage the\nnew issues\n\n  and label them", 60)
	if got != "triage the new issues and label them" {
		t.Fatalf("got %q, want the newlines collapsed", got)
	}
	if got := truncateOneLine("abcdefghij", 5); got != "abcd…" {
		t.Fatalf("got %q, want it cut to 5 columns", got)
	}
}

// The prompt is dug out of the raw task, because the client does not own that
// schema either and a struct here would be a third definition of it.
func TestSchedulePromptFromRawTask(t *testing.T) {
	s := api.Schedule{Task: json.RawMessage(`{"prompt":"triage","model":"claude-opus-5","maxTurns":40}`)}
	if got := s.Prompt(); got != "triage" {
		t.Fatalf("prompt = %q", got)
	}
	// A task with no prompt, and a task that is not JSON at all, must both come
	// back empty rather than panicking a listing.
	if got := (api.Schedule{Task: json.RawMessage(`{"model":"x"}`)}).Prompt(); got != "" {
		t.Fatalf("prompt = %q, want empty", got)
	}
	if got := (api.Schedule{}).Prompt(); got != "" {
		t.Fatalf("prompt = %q, want empty", got)
	}
}
