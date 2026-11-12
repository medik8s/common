package nodeidentity_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// withMainPatchStatus answers every main-resource patch on the own node with
// the given status code, delegating everything else.
func withMainPatchStatus(fake *fakeNodeAPI, code int) http.Handler {
	inner := fake.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/"+fake.name {
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"kind":"Status","status":"Failure","code":%d}`, code)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func TestCordonRefused(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, withMainPatchStatus(fake, http.StatusForbidden))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	for _, fn := range []struct {
		name string
		call func() error
	}{
		{"Cordon", func() error { _, err := c.Cordon(context.Background()); return err }},
		{"Uncordon", func() error { _, err := c.Uncordon(context.Background()); return err }},
	} {
		if err := fn.call(); !errors.Is(err, nodeidentity.ErrForbidden) {
			t.Errorf("%s: expected ErrForbidden, got %v", fn.name, err)
		}
	}
}

func TestCordonTransportError(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	srv.Close()

	if _, err := c.Cordon(context.Background()); !errors.Is(err, nodeidentity.ErrAPIServer) {
		t.Fatalf("expected ErrAPIServer, got %v", err)
	}
}

// nodeJSONHandler serves a fixed JSON document for every node GET.
func nodeJSONHandler(doc string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, doc)
	})
}

func TestGetNodeServerReturnsWrongName(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	srv := pki.newTestServer(t, nodeJSONHandler(`{"metadata":{"name":"worker-1","uid":"uid-1","resourceVersion":"1"}}`))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	if _, err := c.GetNode(context.Background(), "worker-2"); !errors.Is(err, nodeidentity.ErrNodeNameMismatch) {
		t.Fatalf("expected ErrNodeNameMismatch, got %v", err)
	}
}

func TestGetNodeMissingIdentityFields(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	srv := pki.newTestServer(t, nodeJSONHandler(`{"metadata":{"name":"worker-2"}}`))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	if _, err := c.GetNode(context.Background(), "worker-2"); !errors.Is(err, nodeidentity.ErrAPIServer) {
		t.Fatalf("expected ErrAPIServer for missing uid, got %v", err)
	}
}

// withJSONPatchStatus answers every JSON-patch on the status subresource with
// the given code, delegating everything else.
func withJSONPatchStatus(fake *fakeNodeAPI, code int) http.Handler {
	inner := fake.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/"+fake.name+"/status" &&
			r.Header.Get("Content-Type") == "application/json-patch+json" {
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"kind":"Status","status":"Failure","code":%d}`, code)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func TestRemoveOwnNodeConditionNonRetryableRefusal(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, withJSONPatchStatus(fake, http.StatusForbidden))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	ctx := context.Background()

	if _, err := c.PatchOwnNodeCondition(ctx, nodeidentity.Condition{Type: "Probe", Status: "True"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := c.RemoveOwnNodeCondition(ctx, "Probe"); !errors.Is(err, nodeidentity.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestRemoveOwnNodeConditionRetriesExhausted(t *testing.T) {
	// Every guarded remove conflicts (index shifts under the client on every
	// attempt): the bounded retry must give up with an error, not loop.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, withJSONPatchStatus(fake, http.StatusConflict))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	ctx := context.Background()

	if _, err := c.PatchOwnNodeCondition(ctx, nodeidentity.Condition{Type: "Probe", Status: "True"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := c.RemoveOwnNodeCondition(ctx, "Probe"); !errors.Is(err, nodeidentity.ErrAPIServer) {
		t.Fatalf("expected ErrAPIServer after exhausted retries, got %v", err)
	}
}

func TestRemoveOwnNodeConditionGetFails(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	srv.Close()

	if err := c.RemoveOwnNodeCondition(context.Background(), "Probe"); !errors.Is(err, nodeidentity.ErrAPIServer) {
		t.Fatalf("expected ErrAPIServer, got %v", err)
	}
}

// mainPatchEchoHandler accepts cordon patches but never reports the flag in
// the response, modeling a server that drops the field. Other main-resource
// patches (the taint probe) are delegated.
func mainPatchEchoHandler(fake *fakeNodeAPI) http.Handler {
	inner := fake.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/"+fake.name {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte("unschedulable")) {
				// Answer with the fake's current state (correct name, uid and
				// resourceVersion) but without applying the cordon, modeling
				// a server that accepts the write and drops the field.
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(fake.nodeJSON())
				return
			}
		}
		inner.ServeHTTP(w, r)
	})
}

func TestRunPermissionMatrixCordonNotReflected(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, mainPatchEchoHandler(fake))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	deviations := res.Unexpected()
	if len(deviations) != 1 || deviations[0].Name != "cordon (spec.unschedulable=true)" {
		t.Fatalf("expected the cordon deviation, got %+v", deviations)
	}
	if deviations[0].Outcome != nodeidentity.OutcomeFailed {
		t.Fatalf("expected failed outcome, got %q", deviations[0].Outcome)
	}
}

// taintAsymmetricHandler permits taint writes but refuses the delete
// directive, so the unexpected-permitted rollback fails.
func taintAsymmetricHandler(fake *fakeNodeAPI) http.Handler {
	inner := fake.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/"+fake.name {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			r.Body = io.NopCloser(bytes.NewReader(body))
			if bytes.Contains(body, []byte("$patch")) {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"kind":"Status","status":"Failure","code":403}`)
				return
			}
		}
		inner.ServeHTTP(w, r)
	})
}

func TestRunPermissionMatrixRollbackFailure(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	fake.mu.Lock()
	fake.refuseTaint = false
	fake.mu.Unlock()
	srv := pki.newTestServer(t, taintAsymmetricHandler(fake))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	deviations := res.Unexpected()
	if len(deviations) != 1 || deviations[0].Name != "taint-write (expect refusal)" {
		t.Fatalf("expected the taint deviation, got %+v", deviations)
	}
	if deviations[0].Detail == "" || !strings.Contains(deviations[0].Detail, "rollback failed") {
		t.Fatalf("expected rollback-failure detail, got %q", deviations[0].Detail)
	}
}

func TestRunPermissionMatrixConditionRemoveCleanupFails(t *testing.T) {
	// The condition write succeeds; its removal is refused. The matrix must
	// report the cleanup as a deviation and still restore the cordon.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, withJSONPatchStatus(fake, http.StatusForbidden))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	deviations := res.Unexpected()
	if len(deviations) != 1 || deviations[0].Name != "remove probe condition (cleanup)" {
		t.Fatalf("expected the cleanup deviation, got %+v", deviations)
	}
	if fake.unschedulable() {
		t.Fatal("node left cordoned")
	}
}

func TestRunPermissionMatrixCordonRestoreCleanupFails(t *testing.T) {
	// The API server refuses main-resource patches after the cordon: the
	// taint check fails, the restore fails, and the cleanup deviation
	// reports the node's CURRENT state from a follow-up read — never a
	// guess about the direction the restore was heading.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := &countingFake{fakeAPI: newFakeNodeAPI("worker-1", "uid-1"), closeAfter: 1}
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	var cleanup *nodeidentity.ProbeCheck
	for i := range res.Checks {
		if res.Checks[i].Name == "restore cordon (cleanup)" {
			cleanup = &res.Checks[i]
		}
	}
	if cleanup == nil {
		t.Fatalf("restore-cordon cleanup check missing: %+v", res.Checks)
	}
	if cleanup.Outcome != nodeidentity.OutcomeFailed {
		t.Fatalf("unexpected cleanup outcome: %+v", cleanup)
	}
	if !strings.Contains(cleanup.Detail, "currently cordoned") {
		t.Fatalf("restore detail must state the confirmed current state, got %q", cleanup.Detail)
	}
}

func TestRunPermissionMatrixNoRestoreWhenCordonFailed(t *testing.T) {
	// A node that was cordoned before the matrix, whose API then refuses
	// every main-resource patch: the cordon check itself fails, so nothing
	// was changed and no restore is attempted — a failed cordon must not
	// produce a phantom restore that could clobber the pre-existing state.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	inner := newFakeNodeAPI("worker-1", "uid-1")
	inner.mu.Lock()
	inner.unsched = true
	inner.mu.Unlock()
	fake := &countingFake{fakeAPI: inner, closeAfter: 0}
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	for _, chk := range res.Checks {
		if chk.Name == "restore cordon (cleanup)" {
			t.Fatalf("no restore may be attempted when the cordon never landed: %+v", chk)
		}
		if chk.Name == "cordon (spec.unschedulable=true)" && chk.Outcome != nodeidentity.OutcomeFailed {
			t.Fatalf("expected the cordon check to fail, got %+v", chk)
		}
	}
	if !inner.unschedulable() {
		t.Fatal("the pre-existing cordon was disturbed")
	}
}

func TestRunPermissionMatrixInterveningCordonLeftIntact(t *testing.T) {
	// An administrator uncordons the node between the matrix's cordon and
	// its cleanup: the intervening change stands, the restore is skipped,
	// and the conflict is reported instead of silently reverted.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, interveningHandler(fake))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	var cleanup *nodeidentity.ProbeCheck
	for i := range res.Checks {
		if res.Checks[i].Name == "restore cordon (cleanup)" {
			cleanup = &res.Checks[i]
		}
	}
	if cleanup == nil {
		t.Fatalf("restore-cordon cleanup check missing: %+v", res.Checks)
	}
	if !strings.Contains(cleanup.Detail, "intervening cordon update") {
		t.Fatalf("expected the intervening-update report, got %q", cleanup.Detail)
	}
	if fake.unschedulable() {
		t.Fatal("the matrix re-cordoned a node an administrator had uncordoned")
	}
}

// interveningHandler uncordons the node once, right after the matrix's
// cordon patch lands, simulating a concurrent administrator.
func interveningHandler(fake *fakeNodeAPI) http.Handler {
	inner := fake.handler()
	var intervened bool
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		mu.Lock()
		defer mu.Unlock()
		if !intervened && r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/"+fake.name {
			intervened = true
			fake.mu.Lock()
			fake.unsched = false // the administrator's uncordon
			fake.mu.Unlock()
		}
	})
}

// countingFake answers main-resource patches with 503 after a fixed number
// of them, simulating the API disappearing mid-matrix.
type countingFake struct {
	fakeAPI    *fakeNodeAPI
	closeAfter int
	mains      int
}

func (f *countingFake) handler() http.Handler {
	inner := f.fakeAPI.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/"+f.fakeAPI.name {
			f.mains++
			if f.mains > f.closeAfter {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"kind":"Status","status":"Failure","code":503}`)
				return
			}
		}
		inner.ServeHTTP(w, r)
	})
}

func TestConditionPatchClearsReasonAndMessage(t *testing.T) {
	// The patch wire form always serializes Reason and Message, so an empty
	// value CLEARS the stored field — an omitted key would leave it. The
	// fake models strategic-merge presence semantics, so this test proves
	// both sides of the contract.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	ctx := context.Background()

	if _, err := c.PatchOwnNodeCondition(ctx, nodeidentity.Condition{
		Type: "Probe", Status: "True", Reason: "Original", Message: "original message",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := c.PatchOwnNodeCondition(ctx, nodeidentity.Condition{
		Type: "Probe", Status: "False",
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, ok := fake.conditionsOf("Probe")
	if !ok {
		t.Fatal("condition disappeared")
	}
	if got.Reason != "" || got.Message != "" {
		t.Fatalf("empty Reason/Message must clear the stored fields, got %+v", got)
	}
	if got.Status != "False" {
		t.Fatalf("status must update, got %q", got.Status)
	}
}

func TestStatusPatchUIDPreconditionRejectsReplacedNode(t *testing.T) {
	// A condition patch through a stale identity fails the UID precondition
	// server-side with 422: the status of a replaced Node is never written.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-replaced")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-original", cert, key)

	_, err := c.PatchOwnNodeCondition(context.Background(), nodeidentity.Condition{Type: "Probe", Status: "True"})
	if err == nil {
		t.Fatal("expected the UID precondition to fail the status write")
	}
	if _, ok := fake.conditionsOf("Probe"); ok {
		t.Fatal("condition was stored despite the identity mismatch")
	}
}

func TestRunPermissionMatrixPreExistingConditionNotOverwritten(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	ctx := context.Background()

	// Seed a leftover condition from a previous run.
	if _, err := c.PatchOwnNodeCondition(ctx, nodeidentity.Condition{
		Type: "Medik8sNodeIdentityProbe", Status: "True", Reason: "PreviousRun",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := c.RunPermissionMatrix(ctx, nodeidentity.ProbeOptions{})
	deviations := res.Unexpected()
	if len(deviations) != 1 || deviations[0].Name != "condition-write (nodes/status)" {
		t.Fatalf("expected the condition-write deviation, got %+v", deviations)
	}
	if !strings.Contains(deviations[0].Detail, "already exists") {
		t.Fatalf("deviation must explain the pre-existing condition, got %q", deviations[0].Detail)
	}
	// The previous run's condition is untouched, and no cleanup removed it.
	got, ok := fake.conditionsOf("Medik8sNodeIdentityProbe")
	if !ok || got.Reason != "PreviousRun" {
		t.Fatalf("the pre-existing condition must be left intact, got %+v", got)
	}
}

func TestRunPermissionMatrixOtherNodeIsOwnNode(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{OtherNode: "worker-1"})
	deviations := res.Unexpected()
	if len(deviations) != 1 || deviations[0].Name != "get-other-node (read)" {
		t.Fatalf("expected the other-node deviation, got %+v", deviations)
	}
	if !strings.Contains(deviations[0].Detail, "own node") {
		t.Fatalf("deviation must explain the own-node refusal, got %q", deviations[0].Detail)
	}
}

func TestRunPermissionMatrixTaintAttemptIsDryRun(t *testing.T) {
	// The taint probe must request dry-run processing, so a platform that
	// unexpectedly permits the write stores nothing. The fake models a
	// non-compliant server (it stores anyway), which also keeps the
	// rollback path exercised.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	if !res.AllExpected() {
		t.Fatalf("deviations: %+v", res.Unexpected())
	}
	fake.mu.Lock()
	dryRun := fake.taintDryRun
	fake.mu.Unlock()
	if dryRun != "All" {
		t.Fatalf("the taint attempt must request dryRun=All, got %q", dryRun)
	}
}

func TestFakeConditionMergePreservesOmittedFields(t *testing.T) {
	// The fake models strategic-merge presence semantics: a patch carrying
	// only status must leave the stored Reason untouched. The package's own
	// patches always carry reason and message, so drive the fake directly
	// with a hand-built patch.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	ctx := context.Background()

	if _, err := c.PatchOwnNodeCondition(ctx, nodeidentity.Condition{
		Type: "Probe", Status: "True", Reason: "Original", Message: "original",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Hand-built status-only patch: no reason or message keys at all.
	node, err := c.GetOwnNode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	patch := fmt.Sprintf(`{"metadata":{"resourceVersion":%q,"uid":%q},"status":{"conditions":[{"type":"Probe","status":"False"}]}}`,
		node.Metadata.ResourceVersion, node.Metadata.UID)
	if _, err := c.PatchOwnNodeStatus(ctx, nodeidentity.PatchTypeStrategicMerge, []byte(patch)); err != nil {
		t.Fatalf("status-only patch: %v", err)
	}
	got, _ := fake.conditionsOf("Probe")
	if got.Status != "False" {
		t.Fatalf("status must update, got %q", got.Status)
	}
	if got.Reason != "Original" || got.Message != "original" {
		t.Fatalf("omitted fields must be preserved, got %+v", got)
	}
}

func TestRunPermissionMatrixConditionWriteRetriesOnConflict(t *testing.T) {
	// The node controller adds the unschedulable taint after our cordon,
	// bumping the resourceVersion between the cordon response and the
	// condition patch: the matrix must follow the documented conflict
	// protocol (re-GET, re-patch) instead of reporting a deviation.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, taintRacingHandler(fake))
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	if !res.AllExpected() {
		t.Fatalf("deviations: %+v", res.Unexpected())
	}
	if _, ok := fake.conditionsOf("Medik8sNodeIdentityProbe"); ok {
		t.Fatal("probe condition left behind")
	}
}

// taintRacingHandler models the node controller: after every cordon patch
// lands, a concurrent spec write (the unschedulable taint) bumps the
// resourceVersion, invalidating the client's observed precondition.
func taintRacingHandler(fake *fakeNodeAPI) http.Handler {
	inner := fake.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		r.Body = io.NopCloser(bytes.NewReader(body))
		inner.ServeHTTP(w, r)
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/"+fake.name &&
			bytes.Contains(body, []byte("unschedulable")) {
			fake.bumpRV() // the node controller's taint write lands
		}
	})
}
