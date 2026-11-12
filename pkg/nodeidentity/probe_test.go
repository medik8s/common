package nodeidentity_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// refusingStatusHandler delegates to the fake but answers every status patch
// with 403, modeling a platform without condition permission.
func refusingStatusHandler(fake *fakeNodeAPI) http.Handler {
	inner := fake.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/status") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","status":"Failure","code":403,"reason":"Forbidden"}`))
			return
		}
		inner.ServeHTTP(w, r)
	})
}

func newMatrixClient(t *testing.T, fake *fakeNodeAPI) *nodeidentity.Client {
	t.Helper()
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	srv := pki.newTestServer(t, fake.handler())
	return pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
}

func TestRunPermissionMatrixAllExpected(t *testing.T) {
	fake := newFakeNodeAPI("worker-1", "uid-1")
	c := newMatrixClient(t, fake)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	if !res.AllExpected() {
		t.Fatalf("expected all checks as expected, got deviations: %+v", res.Unexpected())
	}
	if res.NodeUID != "uid-1" {
		t.Fatalf("NodeUID = %q", res.NodeUID)
	}

	// The matrix must leave the node as it found it.
	if fake.unschedulable() {
		t.Fatal("node left cordoned")
	}
	if _, ok := fake.conditionsOf("Medik8sNodeIdentityProbe"); ok {
		t.Fatal("probe condition left behind")
	}
	if _, ok := fake.taintOf("medik8s.io/nodeidentity-probe"); ok {
		t.Fatal("probe taint left behind")
	}

	names := make(map[string]bool)
	for _, chk := range res.Checks {
		names[chk.Name] = true
	}
	for _, want := range []string{
		"get-own-node",
		"cordon (spec.unschedulable=true)",
		"condition-write (nodes/status)",
		"taint-write (expect refusal)",
	} {
		if !names[want] {
			t.Errorf("missing check %q; have %v", want, names)
		}
	}
}

func TestRunPermissionMatrixRestoresPriorCordon(t *testing.T) {
	// A node that was already cordoned before the matrix must be left
	// cordoned, not silently uncordoned.
	fake := newFakeNodeAPI("worker-1", "uid-1")
	fake.mu.Lock()
	fake.unsched = true
	fake.mu.Unlock()
	c := newMatrixClient(t, fake)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	if !res.AllExpected() {
		t.Fatalf("deviations: %+v", res.Unexpected())
	}
	if !fake.unschedulable() {
		t.Fatal("pre-existing cordon was cleared by the matrix")
	}
}

func TestRunPermissionMatrixTaintPermittedRollsBack(t *testing.T) {
	// A platform that unexpectedly permits the taint write: the matrix rolls
	// the probe taint back and reports the check as a deviation.
	fake := newFakeNodeAPI("worker-1", "uid-1")
	fake.mu.Lock()
	fake.refuseTaint = false
	fake.mu.Unlock()
	c := newMatrixClient(t, fake)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	deviations := res.Unexpected()
	if len(deviations) != 1 {
		t.Fatalf("expected exactly the taint deviation, got %+v", deviations)
	}
	if deviations[0].Name != "taint-write (expect refusal)" || deviations[0].Outcome != nodeidentity.OutcomePermitted {
		t.Fatalf("unexpected deviation: %+v", deviations[0])
	}
	if _, ok := fake.taintOf("medik8s.io/nodeidentity-probe"); ok {
		t.Fatal("probe taint was not rolled back")
	}
}

func TestRunPermissionMatrixOtherNodeRead(t *testing.T) {
	fake := newFakeNodeAPI("worker-1", "uid-1")
	c := newMatrixClient(t, fake)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{OtherNode: "worker-2"})
	if !res.AllExpected() {
		t.Fatalf("deviations: %+v", res.Unexpected())
	}
	found := false
	for _, chk := range res.Checks {
		if chk.Name == "get-other-node (read)" {
			found = true
		}
	}
	if !found {
		t.Fatal("other-node check missing")
	}
}

func TestRunPermissionMatrixTransportFailureReportsFailed(t *testing.T) {
	// A server that is gone: the matrix must report failure, not skip.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	srv.Close() // the client builds its transport lazily; every call now fails

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	if len(res.Checks) != 1 {
		t.Fatalf("expected only the anchoring GET, got %+v", res.Checks)
	}
	if res.Checks[0].Outcome != nodeidentity.OutcomeFailed {
		t.Fatalf("expected failed outcome, got %q", res.Checks[0].Outcome)
	}
	if res.NodeUID != "" {
		t.Fatalf("NodeUID should be empty on failure, got %q", res.NodeUID)
	}
}

func TestRunPermissionMatrixCustomNames(t *testing.T) {
	fake := newFakeNodeAPI("worker-1", "uid-1")
	c := newMatrixClient(t, fake)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{
		ConditionType:      "CustomProbe",
		TaintKey:           "example.com/custom",
		ConditionTimestamp: "2026-11-11T18:00:00Z",
	})
	if !res.AllExpected() {
		t.Fatalf("deviations: %+v", res.Unexpected())
	}
	if _, ok := fake.conditionsOf("CustomProbe"); ok {
		t.Fatal("custom probe condition left behind")
	}
	if _, ok := fake.conditionsOf("Medik8sNodeIdentityProbe"); ok {
		t.Fatal("default-named condition was written despite the custom type")
	}
	if _, ok := fake.taintOf("example.com/custom"); ok {
		t.Fatal("custom probe taint left behind")
	}
}

func TestRunPermissionMatrixRestoresWhenConditionWriteFails(t *testing.T) {
	// The condition write is refused (a platform without condition
	// permission): the cordon must still be restored and the refusal
	// reported as a deviation, never silently skipped.
	fake := newFakeNodeAPI("worker-1", "uid-1")
	c := newMatrixClient(t, fake)
	// Make every status patch forbidden by pointing the client at a fake
	// that refuses the subresource: reuse refuseTaint is wrong; instead use
	// a client whose expected UID mismatches only on the status path is not
	// expressible. Simplest honest model: a server that 403s status patches.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	srv := pki.newTestServer(t, refusingStatusHandler(fake))
	c = pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	deviations := res.Unexpected()
	if len(deviations) != 1 || deviations[0].Name != "condition-write (nodes/status)" {
		t.Fatalf("expected the condition-write deviation, got %+v", deviations)
	}
	if fake.unschedulable() {
		t.Fatal("node left cordoned when the condition write failed")
	}
	if _, ok := fake.conditionsOf("Medik8sNodeIdentityProbe"); ok {
		t.Fatal("probe condition left behind")
	}
}
