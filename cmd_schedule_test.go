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

// An edit must not clear the parts of the task it cannot name. The stored task
// can carry fields this client has never heard of, and the server's PUT
// replaces the whole row — so a prompt extracted, edited and sent back alone
// would silently delete the rest.
//
// It must also never send `task` AND the shorthand together: the server refuses
// a body carrying both, and every stored task now names a model, so the merge
// path is the ordinary one rather than the exception.
func TestScheduleTaskPayload(t *testing.T) {
	rich := json.RawMessage(`{"prompt":"old","model":"claude-opus-5","maxTurns":40}`)
	cases := []struct {
		name                    string
		existing                json.RawMessage
		prompt, model, provider string
		wantPrompt              string
		wantModel               string
		wantProvider            string
		wantTask                map[string]any
	}{
		{
			name:     "nothing new round-trips the task verbatim",
			existing: rich,
			wantTask: map[string]any{"prompt": "old", "model": "claude-opus-5", "maxTurns": float64(40)},
		},
		{
			name:     "a new prompt replaces only the prompt",
			existing: rich, prompt: "new",
			wantTask: map[string]any{"prompt": "new", "model": "claude-opus-5", "maxTurns": float64(40)},
		},
		{
			name:     "a new model replaces only the model",
			existing: rich, model: "claude-haiku-4-5",
			wantTask: map[string]any{"prompt": "old", "model": "claude-haiku-4-5", "maxTurns": float64(40)},
		},
		{
			name:     "both at once, and the field neither names survives",
			existing: rich, prompt: "new", model: "claude-sonnet-5",
			wantTask: map[string]any{"prompt": "new", "model": "claude-sonnet-5", "maxTurns": float64(40)},
		},
		{
			// A create. There is no stored task to merge into, so the
			// shorthand is all there is.
			name:     "no stored task takes the shorthand",
			existing: nil,
			prompt:   "new", model: "claude-sonnet-5",
			wantPrompt: "new", wantModel: "claude-sonnet-5",
		},
		{
			// The provider is fixed for the life of every box a schedule
			// fires, so an edit that never mentions it must not clear it —
			// that would quietly move a running OpenAI schedule onto the
			// Anthropic table, where it has no route to its own model.
			name:     "an edit that names no provider leaves the stored one alone",
			existing: json.RawMessage(`{"prompt":"old","model":"gpt-5-codex","provider":"openai"}`),
			prompt:   "new",
			wantTask: map[string]any{"prompt": "new", "model": "gpt-5-codex", "provider": "openai"},
		},
		{
			name:     "a new provider replaces only the provider",
			existing: json.RawMessage(`{"prompt":"old","model":"gpt-5","provider":"openai","maxTurns":40}`),
			provider: "anthropic",
			wantTask: map[string]any{"prompt": "old", "model": "gpt-5", "provider": "anthropic", "maxTurns": float64(40)},
		},
		{
			name:     "a create carries the provider as shorthand",
			existing: nil,
			prompt:   "new", model: "gpt-5-codex", provider: "openai",
			wantPrompt: "new", wantModel: "gpt-5-codex", wantProvider: "openai",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			task, prompt, model, provider := scheduleTaskPayload(c.existing, c.prompt, c.model, c.provider)
			if prompt != c.wantPrompt {
				t.Errorf("prompt = %q, want %q", prompt, c.wantPrompt)
			}
			if model != c.wantModel {
				t.Errorf("model = %q, want %q", model, c.wantModel)
			}
			if provider != c.wantProvider {
				t.Errorf("provider = %q, want %q", provider, c.wantProvider)
			}
			if len(task) != 0 && (prompt != "" || model != "" || provider != "") {
				t.Fatalf("sent task AND shorthand together; the server refuses that body")
			}
			if c.wantTask == nil {
				if len(task) != 0 {
					t.Fatalf("task = %s, want none", task)
				}
				return
			}
			var got map[string]any
			if err := json.Unmarshal(task, &got); err != nil {
				t.Fatalf("task %s: %v", task, err)
			}
			for k, want := range c.wantTask {
				if got[k] != want {
					t.Errorf("task[%q] = %v, want %v", k, got[k], want)
				}
			}
			if len(got) != len(c.wantTask) {
				t.Errorf("task = %v, want exactly %v", got, c.wantTask)
			}
		})
	}
}
