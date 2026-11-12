package nodeidentity_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// integration_test.go exercises the full ordered protocol the boot-hold
// reporter uses, against the stateful fake API server that enforces the
// resourceVersion precondition exactly like the real one.

func TestOrderedFlowHappyPath(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	// GET -> validate -> PATCH, the reporter's ordered flow.
	node, err := c.GetOwnNode(context.Background())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	firstRV := node.Metadata.ResourceVersion

	cond := nodeidentity.Condition{Type: "SBRBootHeld", Status: "True", Reason: "FencingBootHoldActive", Message: `{"version":1}`}
	patched, err := c.PatchOwnNodeCondition(context.Background(), cond)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	if patched.Metadata.ResourceVersion == firstRV {
		t.Fatal("resourceVersion must advance on a successful patch")
	}

	got, ok := api.conditionsOf("SBRBootHeld")
	if !ok || got.Reason != "FencingBootHoldActive" || got.Message != `{"version":1}` {
		t.Fatalf("condition not applied as sent: %+v", got)
	}
}

func TestConflictThenSuccess(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	if _, err := c.GetOwnNode(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.bumpRV() // stale precondition on the first attempt

	cond := nodeidentity.Condition{Type: "SBRBootHeld", Status: "True"}
	if _, err := c.PatchOwnNodeCondition(context.Background(), cond); !errors.Is(err, nodeidentity.ErrConflict) {
		t.Fatalf("first attempt must conflict, got %v", err)
	}

	// The caller's contract: re-GET, re-validate, re-patch.
	if _, err := c.GetOwnNode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PatchOwnNodeCondition(context.Background(), cond); err != nil {
		t.Fatalf("retry after re-GET must succeed, got %v", err)
	}
	if _, ok := api.conditionsOf("SBRBootHeld"); !ok {
		t.Fatal("condition missing after successful retry")
	}
}

func TestConditionsMergeByType(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	// Two foreign conditions pre-exist (other writers).
	pre := newFakeNodeAPI("worker-1", "uid-1")
	_ = pre
	api.mu.Lock()
	api.conditions = []nodeidentity.Condition{
		{Type: "Ready", Status: "Unknown"},
		{Type: "MemoryPressure", Status: "False"},
	}
	api.mu.Unlock()

	for _, cond := range []nodeidentity.Condition{
		{Type: "SBRBootHeld", Status: "True", Reason: "R1"},
		{Type: "SBRBootProtection", Status: "True", Reason: "R2"},
	} {
		if _, err := c.GetOwnNode(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := c.PatchOwnNodeCondition(context.Background(), cond); err != nil {
			t.Fatalf("patch %s: %v", cond.Type, err)
		}
	}

	// The patch must coexist with the pre-existing conditions (strategic
	// merge by type) — our patch bodies never re-send foreign conditions.
	api.mu.Lock()
	types := map[string]bool{}
	for _, cond := range api.conditions {
		types[cond.Type] = true
	}
	api.mu.Unlock()
	for _, want := range []string{"Ready", "MemoryPressure", "SBRBootHeld", "SBRBootProtection"} {
		if !types[want] {
			t.Fatalf("condition %s missing after patches; present: %v", want, types)
		}
	}
}

func TestUIDFailsafeBlocksWrite(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-live-replaced")
	srv := pki.newTestServer(t, api.handler())
	// The descriptor recorded uid-expected; the live object has a different
	// UID — every operation must fail before any write is attempted.
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-expected", certFile, keyFile)

	if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrNodeNameMismatch) {
		t.Fatalf("expected ErrNodeNameMismatch, got %v", err)
	}
	if _, err := c.PatchOwnNodeCondition(context.Background(), nodeidentity.Condition{Type: "T", Status: "True"}); !errors.Is(err, nodeidentity.ErrNodeNameMismatch) {
		t.Fatalf("patch must be blocked by the UID failsafe, got %v", err)
	}

	api.mu.Lock()
	patches := api.patch
	api.mu.Unlock()
	if patches != 0 {
		t.Fatalf("no write may reach the server on UID mismatch, got %d patches", patches)
	}
}

func TestInjectedHTTPClientUsedAsIs(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())

	// A deliberately broken injected client: with valid certificate files on
	// disk (the fail-fast check passes), the request must fail through the
	// injected client, proving it was used as-is and no transport was built.
	broken := &http.Client{Transport: brokenTransport{}}
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile,
		func(cfg *nodeidentity.Config) { cfg.HTTPClient = broken })

	if _, err := c.GetOwnNode(context.Background()); !errors.Is(err, nodeidentity.ErrAPIServer) {
		t.Fatalf("expected ErrAPIServer through the injected client, got %v", err)
	}

	api.mu.Lock()
	gets := api.gets
	api.mu.Unlock()
	if gets != 0 {
		t.Fatalf("injected client must be used as-is; %d requests reached the server", gets)
	}
}

// brokenTransport fails every round trip.
type brokenTransport struct{}

func (brokenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("injected transport failure")
}
