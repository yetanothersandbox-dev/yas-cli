// Package api is the yas CLI's client for the control-plane gateway.
//
// The wire shapes mirror openapi.yaml and cmd/fleetd/sandbox_api.go by hand.
// Hand-written rather than generated, matching how the rest of this repo
// treats the contract: the openapi file is the source of truth and a human
// keeps the two in agreement.
package api

import (
	"encoding/json"
	"time"
)

// CreateRequest is the body of POST /v1/sandboxes, bare-sandbox subset: the
// CLI never sends `task` — a box you ssh into has no work order.
type CreateRequest struct {
	// ID is required and caller-chosen; the create is idempotent on it.
	ID     string `json:"id"`
	MemMiB int    `json:"memMib,omitempty"`
	// MilliVcpu is thousandths of one vCPU (1000 = one vCPU) — how the API
	// sells CPU since fractional plans. The old `vcpuCount` field (whole
	// vCPUs) is still accepted server-side; this client always speaks milli.
	MilliVcpu      int `json:"milliVcpu,omitempty"`
	DiskMiB        int `json:"diskMib,omitempty"`
	MaxLifetimeSec int `json:"maxLifetimeSec,omitempty"`
	IdleTtlSec     int `json:"idleTtlSec,omitempty"`
	// SSHKeys are public halves only; the create installs them before the 202,
	// so create-then-connect does not race.
	SSHKeys []string `json:"sshKeys,omitempty"`
	// The credentials below go to the HOST-SIDE proxy, never into the guest.
	AnthropicKey string `json:"anthropicKey,omitempty"`
	GitHubToken  string `json:"githubToken,omitempty"`
	OpenAIKey    string `json:"openaiKey,omitempty"`
	// Policy is the box's privacy posture. Absent = sealed (proxy egress,
	// credentials attached) — exactly what every create was before policies.
	Policy *Policy `json:"policy,omitempty"`
	// Profile names a saved profile the GATEWAY expands, at the edge, into the
	// fields above before the body reaches a host. It never travels further: a
	// host has no concept of a profile, and a field it ignored would leave a
	// caller who mistyped a name with a box that has none of the posture they
	// asked for. An unknown name is refused with 404 no_such_profile.
	Profile string `json:"profile,omitempty"`
}

// Policy mirrors the server's SandboxPolicy.
type Policy struct {
	Egress      *EgressPolicy     `json:"egress,omitempty"`
	Credentials *CredentialPolicy `json:"credentials,omitempty"`
}

type EgressPolicy struct {
	Mode    string         `json:"mode,omitempty"` // proxy | filtered | open
	Allow   []string       `json:"allow,omitempty"`
	Connect []ConnectEntry `json:"connect,omitempty"`
}

type ConnectEntry struct {
	Host  string `json:"host"`
	Ports []int  `json:"ports,omitempty"`
}

type CredentialPolicy struct {
	GitHub    string `json:"github,omitempty"`
	Anthropic string `json:"anthropic,omitempty"`
	OpenAI    string `json:"openai,omitempty"`
}

// SandboxSummary is one row of the gateway's GET /v1/sandboxes. The index
// cannot answer status or cost; Get carries those.
type SandboxSummary struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	Placed     bool      `json:"placed"`
	TemplateID string    `json:"templateId"`
	Agentic    bool      `json:"agentic"`
}

// Sandbox is the full record from GET /v1/sandboxes/{id} — the subset of the
// Run schema the CLI reads.
type Sandbox struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"` // queued|starting|idle|busy|suspended|stopped|failed|cancelled
	Phase      string    `json:"phase"`
	CreatedAt  time.Time `json:"createdAt"`
	MemMiB     int       `json:"memMib"`
	MemUsedMiB int       `json:"memUsedMib"`
	// VcpuCount is the guest's whole-vCPU topology; MilliVcpu is the CPU-time
	// allowance (1000 = one vCPU). MilliVcpu is 0 on records from hosts that
	// predate the milli unit — read that as VcpuCount whole vCPUs.
	VcpuCount int     `json:"vcpuCount"`
	MilliVcpu int     `json:"milliVcpu"`
	CostUSD   float64 `json:"costUsd"`
	Retired   bool    `json:"retired"`
}

// SSHAccess is the response of POST /v1/sandboxes/{id}/ssh.
//
// ProxyCommand is present on the wire but deliberately unexported here: it is
// the HOST-side relay, meaningless off the host, and a laptop client that
// read it would try to run root tooling it does not have. The CLI substitutes
// its own `yas stdio` ProxyCommand.
type SSHAccess struct {
	User          string `json:"user"`
	HostPublicKey string `json:"hostPublicKey"`
	Fingerprint   string `json:"fingerprint"`
	KnownHosts    string `json:"knownHosts"`
	Host          string `json:"host"`
}

// ExecRequest mirrors guestagent.ExecRequest: argv, never a shell string,
// unless Shell asks the guest to wrap it.
type ExecRequest struct {
	Cmd       []string          `json:"cmd"`
	Shell     bool              `json:"shell,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TimeoutMs int64             `json:"timeoutMs,omitempty"`
}

// Event is one transcript entry. Raw carries the typed payload (ExecChunk,
// ExecExit) which the caller decodes by Type.
type Event struct {
	Seq   int64      `json:"seq"`
	Event EventInner `json:"event"`
}

type EventInner struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype"`
	Raw     json.RawMessage `json:"raw"`
}

// EventsPage is the shared {events, cursor, terminal} object — identical
// between the poll and every SSE frame, which is what lets a dropped stream
// degrade to polling from the cursor it already holds.
type EventsPage struct {
	Events   []Event `json:"events"`
	Cursor   int64   `json:"cursor"`
	Terminal bool    `json:"terminal"`
}

// ExecChunk is the Raw payload of an exec_output event.
type ExecChunk struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

// ExecExit is the Raw payload of an exec_exit event.
type ExecExit struct {
	ExitCode  int  `json:"exitCode"`
	TimedOut  bool `json:"timedOut"`
	PipesHeld bool `json:"pipesHeld"`
}
