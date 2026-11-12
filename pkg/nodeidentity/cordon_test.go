package nodeidentity_test

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

func TestPatchUnschedulableSetsAndClears(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	n, err := c.Cordon(context.Background())
	if err != nil {
		t.Fatalf("Cordon: %v", err)
	}
	if !n.Spec.Unschedulable {
		t.Fatal("cordon response does not report spec.unschedulable=true")
	}
	if !fake.unschedulable() {
		t.Fatal("fake did not store the cordon")
	}

	n, err = c.Uncordon(context.Background())
	if err != nil {
		t.Fatalf("Uncordon: %v", err)
	}
	if n.Spec.Unschedulable {
		t.Fatal("uncordon response reports spec.unschedulable=true")
	}
	if fake.unschedulable() {
		t.Fatal("fake did not store the uncordon")
	}
}

func TestPatchUnschedulableRecordsResourceVersion(t *testing.T) {
	// The cordon response's resourceVersion must become the precondition for
	// a subsequent condition patch, exactly like a GET would.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	if _, err := c.Cordon(context.Background()); err != nil {
		t.Fatalf("Cordon: %v", err)
	}
	// No GetOwnNode in between: the condition patch must reuse the cordon's
	// resourceVersion or fail with a conflict if it does not.
	_, err := c.PatchOwnNodeCondition(context.Background(), nodeidentity.Condition{
		Type: "Probe", Status: "True", Reason: "Test",
	})
	if err != nil {
		t.Fatalf("condition patch after cordon: %v", err)
	}
	if _, ok := fake.conditionsOf("Probe"); !ok {
		t.Fatal("condition was not stored")
	}
}

func TestPatchUnschedulableValidatesIdentity(t *testing.T) {
	// A replaced Node object must never be acted on with a stale identity.
	// The patch carries the expected UID as an atomic server-side
	// precondition (verified against a live API server): a mismatching UID
	// fails the whole write with 422 before any spec change is applied, so
	// the cordon never lands on the replacement.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-replaced")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-original", cert, key)

	_, err := c.Cordon(context.Background())
	if err == nil {
		t.Fatal("expected the UID precondition to fail the write")
	}
	if errors.Is(err, nodeidentity.ErrForbidden) {
		t.Fatalf("unexpected refusal class: %v", err)
	}
	if fake.unschedulable() {
		t.Fatal("the cordon was applied despite the identity mismatch")
	}
}

func TestGetNodeOtherNode(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	n, err := c.GetNode(context.Background(), "worker-2")
	if err != nil {
		t.Fatalf("GetNode other: %v", err)
	}
	if n.Metadata.Name != "worker-2" || n.Metadata.UID == "" {
		t.Fatalf("unexpected other node: %+v", n.Metadata)
	}

	// Reads of the own node through GetNode still validate the own identity.
	if _, err := c.GetNode(context.Background(), "worker-1"); err != nil {
		t.Fatalf("GetNode own: %v", err)
	}

	if _, err := c.GetNode(context.Background(), ""); !errors.Is(err, nodeidentity.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for empty name, got %v", err)
	}
}

func TestGetNodeReturnsNameMismatch(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	// Serve the own node under any name: the fake does this for other names,
	// so request the own name from a client configured for a different node.
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)

	// The fake answers GET /api/v1/nodes/worker-9 with a node named worker-9.
	n, err := c.GetNode(context.Background(), "worker-9")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if n.Metadata.Name != "worker-9" {
		t.Fatalf("expected worker-9, got %q", n.Metadata.Name)
	}
}

func TestRemoveOwnNodeCondition(t *testing.T) {
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	ctx := context.Background()

	// Seed a foreign condition and the probe condition.
	for _, ct := range []string{"Foreign", "Probe"} {
		if _, err := c.PatchOwnNodeCondition(ctx, nodeidentity.Condition{Type: ct, Status: "True", Reason: "Seed"}); err != nil {
			t.Fatalf("seed %s: %v", ct, err)
		}
	}

	if err := c.RemoveOwnNodeCondition(ctx, "Probe"); err != nil {
		t.Fatalf("RemoveOwnNodeCondition: %v", err)
	}
	if _, ok := fake.conditionsOf("Probe"); ok {
		t.Fatal("probe condition still present")
	}
	if _, ok := fake.conditionsOf("Foreign"); !ok {
		t.Fatal("foreign condition was removed")
	}

	// Removing an absent condition is a success.
	if err := c.RemoveOwnNodeCondition(ctx, "Absent"); err != nil {
		t.Fatalf("remove absent: %v", err)
	}

	// Empty type is invalid config.
	if err := c.RemoveOwnNodeCondition(ctx, ""); !errors.Is(err, nodeidentity.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestRemoveOwnNodeConditionSurvivesIndexShift(t *testing.T) {
	// Another writer adds a condition between the client's GET and its
	// remove patch; the guarded test op fails (or the rv precondition trips)
	// and the retry removes the right condition, never a foreign one.
	pki := newTestPKI(t)
	cert, key := pki.writeClientPair(t, "system:node:worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", cert, key)
	ctx := context.Background()

	if _, err := c.PatchOwnNodeCondition(ctx, nodeidentity.Condition{Type: "Probe", Status: "True", Reason: "Seed"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Shift the indexes out from under the next GET by prepending a
	// condition the client has not observed.
	fake.mu.Lock()
	fake.conditions = append([]nodeidentity.Condition{{Type: "Foreign0", Status: "True"}}, fake.conditions...)
	fake.mu.Unlock()

	if err := c.RemoveOwnNodeCondition(ctx, "Probe"); err != nil {
		t.Fatalf("RemoveOwnNodeCondition under index shift: %v", err)
	}
	if _, ok := fake.conditionsOf("Probe"); ok {
		t.Fatal("probe condition still present")
	}
	if _, ok := fake.conditionsOf("Foreign0"); !ok {
		t.Fatal("foreign condition was removed")
	}
}

func TestCombinedKubeletPEM(t *testing.T) {
	// kubelet's client material on OpenShift is ONE file with the
	// certificate followed by the key; Config points both paths at it. The
	// loader must scan for the right block instead of assuming the first.
	pki := newTestPKI(t)
	certPEM, keyPEM := pki.newLeaf(t, "system:node:worker-1",
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour), x509.ExtKeyUsageClientAuth, nil, nil)
	combined := append(append([]byte{}, certPEM...), keyPEM...)
	combinedFile := filepath.Join(t.TempDir(), "kubelet-client-current.pem")
	if err := os.WriteFile(combinedFile, combined, 0600); err != nil {
		t.Fatal(err)
	}

	fake := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, fake.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", combinedFile, combinedFile)

	node, err := c.GetOwnNode(context.Background())
	if err != nil {
		t.Fatalf("GET with combined PEM: %v", err)
	}
	if node.Metadata.Name != "worker-1" {
		t.Fatalf("unexpected node %q", node.Metadata.Name)
	}
	// The server required and verified a client certificate, so the pair
	// was presented and matched the CA.
}
