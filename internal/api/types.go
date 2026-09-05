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
	// No IdleTtlSec. The idle timeout is not a tenant setting: it is 600s on
	// every tier, it is the ceiling as well as the default, and a create asking
	// for longer was already refused. Offering a field that can only be set to
	// what it already is, or to something worse for the person setting it, is a
	// question with no good answer.
	// SSHKeys are public halves only; the create installs them before the 202,
	// so create-then-connect does not race.
	SSHKeys []string `json:"sshKeys,omitempty"`
	// The credentials below go to the HOST-SIDE proxy, never into the guest.
	AnthropicKey string `json:"anthropicKey,omitempty"`
	GitHubToken  string `json:"githubToken,omitempty"`
	OpenAIKey    string `json:"openaiKey,omitempty"`
	// Provider is the LLM upstream this box is wired to: "openai", or empty for
	// Anthropic. Fixed at create and unchangeable afterwards — the credential
	// proxy's route table is built from it before the guest boots — so it is
	// here rather than on a later call.
	//
	// It is what makes a bare box usable for anything but Claude. `yas new`
	// sends no task, so there was nowhere at all to say which provider a box
	// was for, and every box the CLI made was an Anthropic box regardless of
	// which key the account held.
	Provider string `json:"provider,omitempty"`
	// Policy is the box's egress posture. Absent = proxy mode (no route off the
	// link, everything through the credential proxy) — exactly what every create
	// was before policies. Credentials go through the proxy in every mode.
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
	// Version is omitted — meaning 1 — unless the policy uses grammar that only
	// version 2 defines. That omission is what keeps an unchanged command line
	// producing an unchanged fence on an upgraded CLI: under version 1 a bare
	// allow entry still covers the name and everything under it.
	Version     int               `json:"version,omitempty"`
	Egress      *EgressPolicy     `json:"egress,omitempty"`
	Credentials *CredentialPolicy `json:"credentials,omitempty"`
}

type EgressPolicy struct {
	Mode string `json:"mode,omitempty"` // proxy | filtered | open
	// Allow is a suffix list under version 1. Under version 2 a bare entry is
	// EXACT, "*.host" is every name under it, and either may carry ":443".
	Allow []string `json:"allow,omitempty"`
	// Deny, AllowNets and DenyNets need version 2. Deny and DenyNets beat every
	// allow and apply to filtered and open; AllowNets grants addresses with no
	// DNS involved and is filtered-only.
	Deny      []string       `json:"deny,omitempty"`
	AllowNets []string       `json:"allowNets,omitempty"`
	DenyNets  []string       `json:"denyNets,omitempty"`
	Connect   []ConnectEntry `json:"connect,omitempty"`
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
	// Deadline and SuspendExpiresAt are the two ways a box stops existing, and
	// only one applies at a time: a running box is destroyed at its lifetime
	// wall, a parked one when its suspension passes retention. Both are on the
	// per-id record and NEITHER is on the list, which is why the picker only
	// knows them once the detail fetch lands.
	Deadline         time.Time `json:"deadline"`
	SuspendExpiresAt time.Time `json:"suspendExpiresAt"`
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
