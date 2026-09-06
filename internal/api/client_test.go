package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetanothersandbox-dev/yas-cli/internal/api"
	"github.com/yetanothersandbox-dev/yas-cli/internal/api/apitest"
)

func newClient(t *testing.T) (*api.Client, *apitest.Gateway) {
	t.Helper()
	gw := apitest.New()
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	return &api.Client{BaseURL: srv.URL, Key: "yas_sk_test", Sleep: func(time.Duration) {}}, gw
}

func TestEveryRequestCarriesTheBearerKey(t *testing.T) {
	cl, gw := newClient(t)
	gw.Add(apitest.Box{ID: "b1"})
	if _, err := cl.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The fake 401s a missing Authorization header, so a passing List already
	// proves the header went out; the empty-key case proves the refusal maps.
	bad := &api.Client{BaseURL: cl.BaseURL, Key: ""}
	if _, err := bad.List(context.Background()); err == nil {
		t.Fatal("a keyless client was served")
	}
}

func TestCreateRetriesNoCapacityAndHonoursRetryAfter(t *testing.T) {
	cl, gw := newClient(t)
	var waits []time.Duration
	cl.Sleep = func(d time.Duration) { waits = append(waits, d) }
	gw.CreateRefusals = []apitest.Refusal{
		{Status: 503, Kind: "no_capacity", Message: "full", RetryAfter: "7"},
	}
	if err := cl.Create(context.Background(), api.CreateRequest{ID: "b-new"}); err != nil {
		t.Fatalf("one 503 then room should succeed: %v", err)
	}
	if len(waits) != 1 || waits[0] != 7*time.Second {
		t.Fatalf("waits = %v, want exactly the Retry-After", waits)
	}
	if _, ok := gw.Box("b-new"); !ok {
		t.Fatal("the box was never created")
	}
}

// OnRetry fires once per wait, before it, with the attempt it is about to
// make and the wait it honoured.
//
// It is the only way a caller learns WHY a create is slow. Without it the CLI
// guessed from a stopwatch and told everybody past eight seconds that the
// fleet was finding room — true here, and a fabrication on every create that
// was simply taking a while.
func TestCreateReportsEachCapacityRetry(t *testing.T) {
	cl, gw := newClient(t)
	type call struct {
		attempt int
		wait    time.Duration
		kind    string
	}
	var got []call
	cl.OnRetry = func(attempt int, wait time.Duration, err error) {
		got = append(got, call{attempt, wait, api.ErrorKind(err)})
	}
	gw.CreateRefusals = []apitest.Refusal{
		{Status: 503, Kind: "no_capacity", RetryAfter: "7"},
		{Status: 503, Kind: "no_capacity"},
	}
	if err := cl.Create(context.Background(), api.CreateRequest{ID: "b-new"}); err != nil {
		t.Fatalf("two 503s then room should succeed: %v", err)
	}
	want := []call{{2, 7 * time.Second, "no_capacity"}, {3, 5 * time.Second, "no_capacity"}}
	if len(got) != len(want) {
		t.Fatalf("OnRetry fired %d times (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("retry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A create that never sees a 503 never reports one. The hook is a report of
// something that happened, not a heartbeat — a caller that showed "the fleet
// is full" on a create which was only ever slow would be back to guessing.
func TestCreateReportsNoRetryWhenItSucceeds(t *testing.T) {
	cl, _ := newClient(t)
	fired := 0
	cl.OnRetry = func(int, time.Duration, error) { fired++ }
	if err := cl.Create(context.Background(), api.CreateRequest{ID: "b-new"}); err != nil {
		t.Fatal(err)
	}
	if fired != 0 {
		t.Errorf("OnRetry fired %d times on a clean create, want 0", fired)
	}
}

// A quota refusal is never retried, so it never reports a retry either.
func TestCreateReportsNoRetryOnQuota(t *testing.T) {
	cl, gw := newClient(t)
	fired := 0
	cl.OnRetry = func(int, time.Duration, error) { fired++ }
	gw.CreateRefusals = []apitest.Refusal{{Status: 429, Kind: "quota_exceeded"}}
	if err := cl.Create(context.Background(), api.CreateRequest{ID: "b"}); !api.IsQuota(err) {
		t.Fatalf("err = %v, want a quota refusal", err)
	}
	if fired != 0 {
		t.Errorf("OnRetry fired %d times on a quota refusal, want 0", fired)
	}
}

func TestCreateGivesUpAfterThreeNoCapacityAnswers(t *testing.T) {
	cl, gw := newClient(t)
	gw.CreateRefusals = []apitest.Refusal{
		{Status: 503, Kind: "no_capacity"}, {Status: 503, Kind: "no_capacity"}, {Status: 503, Kind: "no_capacity"},
	}
	err := cl.Create(context.Background(), api.CreateRequest{ID: "b"})
	if !api.IsNoCapacity(err) {
		t.Fatalf("err = %v, want a no-capacity refusal surfaced", err)
	}
	creates := 0
	for _, r := range gw.Requests {
		if r == "POST /v1/sandboxes" {
			creates++
		}
	}
	if creates != 3 {
		t.Fatalf("the client tried %d times, want exactly 3 (one call, two retries)", creates)
	}
}

func TestCreateNeverRetriesQuota(t *testing.T) {
	cl, gw := newClient(t)
	gw.CreateRefusals = []apitest.Refusal{{Status: 429, Kind: "quota_exceeded", Message: "workspace_cap"}}
	err := cl.Create(context.Background(), api.CreateRequest{ID: "b"})
	if !api.IsQuota(err) {
		t.Fatalf("err = %v, want the quota refusal", err)
	}
	creates := 0
	for _, r := range gw.Requests {
		if strings.HasPrefix(r, "POST /v1/sandboxes") {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("a 429 was retried (%d creates); hammering one's own cap is never right", creates)
	}
}

func TestNotFoundIsTyped(t *testing.T) {
	cl, _ := newClient(t)
	_, err := cl.Get(context.Background(), "never")
	if !api.IsNotFound(err) {
		t.Fatalf("err = %v, want typed not-found", err)
	}
}

func TestSSHInstallsKeysAndReturnsThePin(t *testing.T) {
	cl, gw := newClient(t)
	gw.Add(apitest.Box{ID: "b1"})
	access, err := cl.SSH(context.Background(), "b1", []string{"ssh-ed25519 AAA me"})
	if err != nil {
		t.Fatal(err)
	}
	if access.Host != "sandbox-b1" || access.User != "fleet" {
		t.Fatalf("access = %+v", access)
	}
	if !strings.HasPrefix(access.KnownHosts, "sandbox-b1 ") {
		t.Fatalf("knownHosts %q is not keyed on the pin label", access.KnownHosts)
	}
	b, _ := gw.Box("b1")
	if len(b.Keys) != 1 {
		t.Fatalf("keys installed = %v", b.Keys)
	}
}

func TestExecFollowParsesSplitFramesAndPings(t *testing.T) {
	cl, gw := newClient(t)
	gw.Add(apitest.Box{ID: "b1"})
	frame1 := `data: {"events":[{"seq":1,"event":{"type":"exec_output","subtype":"stdout","raw":{"stream":"stdout","data":"hi"}}}],"cursor":1,"terminal":false}` + "\n\n"
	frame2 := `data: {"events":[{"seq":2,"event":{"type":"exec_exit","raw":{"exitCode":0}}}],"cursor":2,"terminal":true}` + "\n\n"
	gw.Frames = []string{
		frame1[:37], // a frame split mid-JSON across two writes
		frame1[37:],
		": ping\n\n", // the 20s heartbeat, which is a comment and not data
		frame2,
	}
	var pages []api.EventsPage
	err := cl.ExecFollow(context.Background(), "b1", api.ExecRequest{Cmd: []string{"true"}}, func(p api.EventsPage) {
		pages = append(pages, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2 (the ping is not a page)", len(pages))
	}
	if !pages[1].Terminal || pages[1].Cursor != 2 {
		t.Fatalf("last page = %+v", pages[1])
	}
	if pages[0].Events[0].Event.Type != "exec_output" {
		t.Fatalf("first event = %+v", pages[0].Events[0])
	}
}

func TestEventsPollSharesTheFrameShape(t *testing.T) {
	cl, gw := newClient(t)
	gw.Add(apitest.Box{ID: "b1"})
	gw.PollPages[5] = `{"events":[{"seq":6,"event":{"type":"exec_exit","raw":{"exitCode":3}}}],"cursor":6,"terminal":true}`
	page, err := cl.Events(context.Background(), "b1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Terminal || page.Cursor != 6 || len(page.Events) != 1 {
		t.Fatalf("page = %+v", page)
	}
}

func TestErrorKindSurfacesTheWireField(t *testing.T) {
	cl, gw := newClient(t)
	gw.CreateRefusals = []apitest.Refusal{{Status: http.StatusConflict, Kind: "ssh_unavailable", Message: "rebake"}}
	err := cl.Create(context.Background(), api.CreateRequest{ID: "b"})
	if api.ErrorKind(err) != "ssh_unavailable" {
		t.Fatalf("kind = %q", api.ErrorKind(err))
	}
}

// Get must read the ENVELOPE fleetd actually sends.
//
// This is a regression test for a bug that survived because the fake agreed
// with the client and both disagreed with the server: fleetd answers
// `{"sandbox": {...}, "terminal": bool}` and the client decoded the record at
// the top level, so every Get returned a ZERO Sandbox. Nothing errored — a JSON
// object with no matching fields decodes cleanly and leaves the struct alone —
// so `yas ls` printed empty statuses and the readiness wait compared against a
// status that was always "".
//
// The assertion that matters is not "no error" but "the fields arrived".
func TestGetReadsTheSandboxEnvelope(t *testing.T) {
	cl, gw := newClient(t)
	gw.Add(apitest.Box{ID: "dev", Status: "idle", MemMiB: 4096})

	sb, err := cl.Get(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if sb.ID != "dev" {
		t.Fatalf("id = %q, want dev — the record did not survive the envelope", sb.ID)
	}
	if sb.Status != "idle" {
		t.Fatalf("status = %q, want idle", sb.Status)
	}
	if sb.MemMiB != 4096 {
		t.Fatalf("memMib = %d, want 4096", sb.MemMiB)
	}
}
