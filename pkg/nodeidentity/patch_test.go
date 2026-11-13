package nodeidentity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

func TestPatchOwnNodeStatusGoldenRequest(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	var gotMethod, gotPath, gotContentType string
	var gotBody []byte
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotContentType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"metadata":{"name":"worker-1","uid":"uid-1","resourceVersion":"9"}}`))
	})
	srv := pki.newTestServer(t, h)
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	patch := []byte(`{"status":{"conditions":[{"type":"X","status":"True"}]}}`)
	if _, err := c.PatchOwnNodeStatus(context.Background(), nodeidentity.PatchTypeStrategicMerge, patch); err != nil {
		t.Fatalf("patch: %v", err)
	}
	if gotMethod != http.MethodPatch || gotPath != "/api/v1/nodes/worker-1/status" {
		t.Fatalf("unexpected request: %s %s", gotMethod, gotPath)
	}
	if gotContentType != "application/strategic-merge-patch+json" {
		t.Fatalf("unexpected content type: %q", gotContentType)
	}
	if string(gotBody) != string(patch) {
		t.Fatalf("body not passed through verbatim: %s", gotBody)
	}
}

func TestPatchOwnNodeConditionValidatesFields(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	srv := pki.newTestServer(t, newFakeNodeAPI("worker-1", "uid-1").handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	if _, err := c.PatchOwnNodeCondition(context.Background(), nodeidentity.Condition{Status: "True"}); !errors.Is(err, nodeidentity.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for missing Type, got %v", err)
	}
	if _, err := c.PatchOwnNodeCondition(context.Background(), nodeidentity.Condition{Type: "T"}); !errors.Is(err, nodeidentity.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for missing Status, got %v", err)
	}
}

func TestPatchOwnNodeConditionAutoGetAndGoldenBody(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	var patchBodies [][]byte
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			b, _ := io.ReadAll(r.Body)
			patchBodies = append(patchBodies, b)
			r.Body = io.NopCloser(bytes.NewReader(b)) // restore for the inner handler
		}
		api.handler().ServeHTTP(w, r)
	})
	srv := pki.newTestServer(t, h)
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	cond := nodeidentity.Condition{
		Type: "SBRBootHeld", Status: "True", Reason: "FencingBootHoldActive",
		Message: "held", LastTransitionTime: "2026-11-11T10:00:00Z",
	}
	if _, err := c.PatchOwnNodeCondition(context.Background(), cond); err != nil {
		t.Fatalf("patch: %v", err)
	}

	if len(patchBodies) != 1 {
		t.Fatalf("expected one patch, got %d", len(patchBodies))
	}
	var decoded struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Status struct {
			Conditions []nodeidentity.Condition `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal(patchBodies[0], &decoded); err != nil {
		t.Fatalf("patch body not valid JSON: %v", err)
	}
	if decoded.Metadata.ResourceVersion != "100" {
		t.Fatalf("precondition not taken from the auto-GET: %q", decoded.Metadata.ResourceVersion)
	}
	if len(decoded.Status.Conditions) != 1 || decoded.Status.Conditions[0].Type != "SBRBootHeld" {
		t.Fatalf("patch must carry exactly the caller's condition: %+v", decoded.Status.Conditions)
	}
	if decoded.Status.Conditions[0].LastTransitionTime != "2026-11-11T10:00:00Z" {
		t.Fatal("LastTransitionTime must be passed through, never injected")
	}
}

func TestPatchOwnNodeConditionConflict(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	api := newFakeNodeAPI("worker-1", "uid-1")
	srv := pki.newTestServer(t, api.handler())
	c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)

	if _, err := c.GetOwnNode(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.bumpRV() // another writer wins the race

	_, err := c.PatchOwnNodeCondition(context.Background(), nodeidentity.Condition{Type: "T", Status: "True"})
	if !errors.Is(err, nodeidentity.ErrConflict) {
		t.Fatalf("expected ErrConflict on stale precondition, got %v", err)
	}

	api.mu.Lock()
	conflicts := api.conflicts
	api.mu.Unlock()
	if conflicts != 1 {
		t.Fatalf("expected the server to see one conflict, got %d", conflicts)
	}
}

func TestPatchOwnNodeStatusErrorMatrix(t *testing.T) {
	pki := newTestPKI(t)
	certFile, keyFile := pki.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusInternalServerError} {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		})
		srv := pki.newTestServer(t, h)
		c := pki.newTestClient(t, srv.URL, "worker-1", "uid-1", certFile, keyFile)
		if _, err := c.PatchOwnNodeStatus(context.Background(), nodeidentity.PatchTypeStrategicMerge, []byte(`{}`)); err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
	}
}
