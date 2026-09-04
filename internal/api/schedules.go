package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// Schedule is one recurring job: a prompt, a posture and a cadence.
//
// Task is raw for the reason Integration.Spec is: the server does not own that
// schema either, so a struct here would be a third definition of the host's
// task object and would silently drop every field added to it.
type Schedule struct {
	ID          string          `json:"id"`
	Description string          `json:"description,omitempty"`
	Cron        string          `json:"cron"`
	Timezone    string          `json:"timezone,omitempty"`
	Profile     string          `json:"profile,omitempty"`
	Task        json.RawMessage `json:"task,omitempty"`
	Enabled     bool            `json:"enabled"`
	Overlap     string          `json:"overlap,omitempty"`
	// NextRunAt is absent when nothing will fire this schedule — it is paused,
	// or its expression has no further match. A pointer so "no next firing" and
	// "the year 1" are not the same value.
	NextRunAt *time.Time `json:"nextRunAt,omitempty"`
	// NextRunIn is the server's own countdown in seconds. Used in preference to
	// subtracting NextRunAt from the local clock, because a laptop whose clock
	// is wrong would otherwise quietly report the wrong answer.
	NextRunIn int64     `json:"nextRunIn,omitempty"`
	LastRun   *LastRun  `json:"lastRun,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// LastRun is the newest firing, summarised.
type LastRun struct {
	FiredAt   time.Time `json:"firedAt"`
	Outcome   string    `json:"outcome"`
	SandboxID string    `json:"sandboxId,omitempty"`
}

// Prompt digs the prompt out of the stored task, for display.
func (s Schedule) Prompt() string {
	var t struct {
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(s.Task, &t)
	return t.Prompt
}

// Model digs the model out of the stored task. It is what a recurring job
// spends, so a listing shows it.
func (s Schedule) Model() string {
	var t struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(s.Task, &t)
	return t.Model
}

// Firing is one occurrence: what was due, what happened, and which box it
// became.
type Firing struct {
	ID        string    `json:"id"`
	DueAt     time.Time `json:"dueAt"`
	FiredAt   time.Time `json:"firedAt"`
	SandboxID string    `json:"sandboxId,omitempty"`
	Outcome   string    `json:"outcome"`
	Detail    string    `json:"detail,omitempty"`
}

// The outcomes a firing reports. Mirrors the server's set; a value outside it
// is rendered verbatim rather than mapped to "unknown", so a server that grows
// an outcome does not need a client release to be readable.
const (
	FiringDispatched = "dispatched"
	FiringSkipped    = "skipped"
	FiringMissed     = "missed"
	FiringRefused    = "refused"
)

// ScheduleRequest is the write shape.
//
// Prompt+Model and Task are the two ways to say what the work is, and the
// server takes one or the other. Enabled is a pointer because the server treats an
// absent field as "leave it as it is" — sending false by omission would pause
// a schedule on a request that only meant to change the prompt.
type ScheduleRequest struct {
	Description string          `json:"description,omitempty"`
	Cron        string          `json:"cron"`
	Timezone    string          `json:"timezone,omitempty"`
	Profile     string          `json:"profile,omitempty"`
	Prompt      string          `json:"prompt,omitempty"`
	Model       string          `json:"model,omitempty"`
	Task        json.RawMessage `json:"task,omitempty"`
	Enabled     *bool           `json:"enabled,omitempty"`
	Overlap     string          `json:"overlap,omitempty"`
}

func (c *Client) Schedules(ctx context.Context) ([]Schedule, error) {
	var out struct {
		Schedules []Schedule `json:"schedules"`
	}
	if err := c.do(ctx, "GET", "/v1/schedules", nil, &out); err != nil {
		return nil, err
	}
	return out.Schedules, nil
}

func (c *Client) Schedule(ctx context.Context, id string) (Schedule, error) {
	var out Schedule
	err := c.do(ctx, "GET", "/v1/schedules/"+url.PathEscape(id), nil, &out)
	return out, err
}

func (c *Client) PutSchedule(ctx context.Context, id string, req ScheduleRequest) (Schedule, error) {
	var out Schedule
	err := c.do(ctx, "PUT", "/v1/schedules/"+url.PathEscape(id), req, &out)
	return out, err
}

func (c *Client) DeleteSchedule(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/v1/schedules/"+url.PathEscape(id), nil, nil)
}

func (c *Client) Firings(ctx context.Context, id string, limit int) ([]Firing, error) {
	path := "/v1/schedules/" + url.PathEscape(id) + "/firings"
	if limit > 0 {
		path += "?limit=" + fmt.Sprint(limit)
	}
	var out struct {
		Firings []Firing `json:"firings"`
	}
	if err := c.do(ctx, "GET", path, nil, &out); err != nil {
		return nil, err
	}
	return out.Firings, nil
}

// RunSchedule fires a schedule now, beside its cadence rather than instead of
// it — the next scheduled firing is untouched.
//
// A firing that was not dispatched comes back as an ERROR from c.do (the server
// answers 409), so the Firing returned is only ever a successful one. The
// refusal's own sentence is in the error, which is where a caller will look.
func (c *Client) RunSchedule(ctx context.Context, id string) (Firing, error) {
	var out Firing
	err := c.do(ctx, "POST", "/v1/schedules/"+url.PathEscape(id)+"/run", nil, &out)
	return out, err
}
