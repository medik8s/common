package nodeidentity_test

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

func regressionClient(t *testing.T, f *fakeNodeAPI, wrap func(http.Handler) http.Handler) *nodeidentity.Client {
	t.Helper()
	p := newTestPKI(t)
	cf, kf := p.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	h := f.handler()
	if wrap != nil {
		h = wrap(h)
	}
	s := p.newTestServer(t, h)
	return p.newTestClient(t, s.URL, "worker-1", "uid-1", cf, kf)
}

// Regression coverage for partial failures, ownership and snapshot preconditions.
func TestRegressionLostCordonResponse(t *testing.T) {
	f := newFakeNodeAPI("worker-1", "uid-1")
	dropped := false
	c := regressionClient(t, f, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !dropped && r.Method == "PATCH" && r.URL.Path == "/api/v1/nodes/worker-1" && r.URL.RawQuery == "" {
				dropped = true
				next.ServeHTTP(httptest.NewRecorder(), r) // persist, then lose the response
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	f.mu.Lock()
	held := f.unsched
	f.mu.Unlock()
	if !held {
		t.Fatal("must not blindly uncordon after a lost response")
	}
	reported := false
	for _, check := range res.Checks {
		if check.Name == "restore cordon (cleanup)" && check.Outcome == nodeidentity.OutcomeFailed && strings.Contains(check.Detail, "currently cordoned") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("ambiguous applied cordon must report residual state and recovery: %+v", res.Checks)
	}
}

func TestRegressionDryRunPreservesExistingTaint(t *testing.T) {
	f := newFakeNodeAPI("worker-1", "uid-1")
	const key = "medik8s.io/nodeidentity-probe"
	f.taints = []nodeidentity.Taint{{Key: key, Value: "administrator", Effect: "NoExecute"}}
	c := regressionClient(t, f, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "PATCH" && r.URL.Query().Get("dryRun") == "All" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(f.nodeJSON())
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.taints) != 1 || f.taints[0].Value != "administrator" {
		t.Fatalf("dry-run caused existing taint deletion: taints=%+v checks=%+v", f.taints, res.Checks)
	}
}

func TestRegressionConditionAppearsDuringProbe(t *testing.T) {
	f := newFakeNodeAPI("worker-1", "uid-1")
	const typ = "Medik8sNodeIdentityProbe"
	inserted := false
	c := regressionClient(t, f, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			if !inserted && r.Method == "PATCH" && r.URL.Path == "/api/v1/nodes/worker-1" && r.URL.RawQuery == "" {
				inserted = true
				f.mu.Lock()
				f.conditions = append(f.conditions, nodeidentity.Condition{Type: typ, Status: "True", Reason: "OtherOwner"})
				f.rv++
				f.mu.Unlock()
			}
		})
	})
	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	got, ok := f.conditionsOf(typ)
	if !ok || got.Reason != "OtherOwner" {
		t.Fatalf("concurrent condition overwritten/removed: got=%+v exists=%v allExpected=%v", got, ok, res.AllExpected())
	}
}

func TestRegressionPreCordonedDoesNotQualifyUncordon(t *testing.T) {
	f := newFakeNodeAPI("worker-1", "uid-1")
	f.unsched = true
	c := regressionClient(t, f, nil)
	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	if res.AllExpected() {
		t.Fatalf("claims full qualification without measuring uncordon: %+v", res.Checks)
	}
}

func TestRegressionUncordonResponseMustBeVerified(t *testing.T) {
	f := newFakeNodeAPI("worker-1", "uid-1")
	c := regressionClient(t, f, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "PATCH" && r.URL.Path == "/api/v1/nodes/worker-1" && r.URL.RawQuery == "" {
				b, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(b))
				if bytes.Contains(b, []byte(`"unschedulable":false`)) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(f.nodeJSON())
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	if res.AllExpected() {
		t.Fatalf("uncordon returned a still-cordoned Node but matrix passed: %+v", res.Checks)
	}
}

func TestRegressionSnapshotPreconditionNotReplacedByOtherReader(t *testing.T) {
	f := newFakeNodeAPI("worker-1", "uid-1")
	c := regressionClient(t, f, nil)
	before, err := c.GetOwnNode(context.Background())
	if err != nil {
		t.Fatal(err)
	} // reader A
	f.mu.Lock()
	f.rv++
	f.conditions = []nodeidentity.Condition{{Type: "Gate", Status: "False"}}
	f.mu.Unlock()
	if _, err := c.GetOwnNode(context.Background()); err != nil {
		t.Fatal(err)
	} // reader B
	_, err = c.PatchOwnNodeConditionAtVersion(context.Background(), before.Metadata.ResourceVersion, nodeidentity.Condition{Type: "Gate", Status: "True"}) // A's stale decision
	if !errors.Is(err, nodeidentity.ErrConflict) {
		t.Fatalf("decision from RV %s accepted against another reader's newer RV: err=%v", before.Metadata.ResourceVersion, err)
	}
}

func TestRegressionTransportCauseSurvives(t *testing.T) {
	p := newTestPKI(t)
	cf, kf := p.writeClientPair(t, "worker-1", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	// Real TLS server, a different trusted CA in the client.
	s := p.newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	wrong := newTestPKI(t)
	c := p.newTestClient(t, s.URL, "worker-1", "uid-1", cf, kf, func(cfg *nodeidentity.Config) { cfg.CAFile = wrong.writeCA(t) })
	_, err := c.GetOwnNode(context.Background())
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Errorf("TLS verification cause lost (ErrAPIServer=%v): %v", errors.Is(err, nodeidentity.ErrAPIServer), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.GetOwnNode(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
}

func TestRegressionRawStatusPatchExplicitUIDGuard(t *testing.T) {
	// Fake requires an RV; a real server additionally accepts no-RV updates.
	f := newFakeNodeAPI("worker-1", "uid-1")
	c := regressionClient(t, f, nil)
	f.mu.Lock()
	f.uid = "replacement-uid"
	f.mu.Unlock()
	_, err := c.PatchOwnNodeStatus(context.Background(), nodeidentity.PatchTypeStrategicMerge, []byte(`{"metadata":{"uid":"uid-1","resourceVersion":"100"},"status":{"conditions":[{"type":"StaleIdentity","status":"True"}]}}`))
	if !errors.Is(err, nodeidentity.ErrNodeNameMismatch) {
		t.Fatalf("expected atomic UID rejection, got %v", err)
	}
	if _, ok := f.conditionsOf("StaleIdentity"); ok {
		t.Fatal("UID-guarded raw patch must not mutate a replacement Node")
	}
}

func TestRegressionConditionReplacedAtCleanup(t *testing.T) {
	f := newFakeNodeAPI("worker-1", "uid-1")
	replaced := false
	c := regressionClient(t, f, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !replaced && r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/status") && r.Header.Get("Content-Type") == nodeidentity.PatchTypeJSONPatch {
				replaced = true
				f.mu.Lock()
				f.conditions = []nodeidentity.Condition{{Type: "Medik8sNodeIdentityProbe", Status: "True", Message: "another owner's evidence"}}
				f.rv++
				f.mu.Unlock()
			}
			next.ServeHTTP(w, r)
		})
	})
	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	got, ok := f.conditionsOf("Medik8sNodeIdentityProbe")
	if !ok || got.Message != "another owner's evidence" {
		t.Fatalf("cleanup removed a replacement condition: %+v", got)
	}
	if res.AllExpected() {
		t.Fatal("ownership conflict must be visible")
	}
}

func TestRegressionCordonChangesAfterCleanupRead(t *testing.T) {
	f := newFakeNodeAPI("worker-1", "uid-1")
	c := regressionClient(t, f, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/nodes/worker-1" && r.URL.RawQuery == "" {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				if bytes.Contains(body, []byte(`"unschedulable":false`)) {
					f.mu.Lock()
					f.rv++
					f.unsched = true
					f.mu.Unlock()
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	res := c.RunPermissionMatrix(context.Background(), nodeidentity.ProbeOptions{})
	if !f.unschedulable() {
		t.Fatal("cleanup overwrote a concurrent cordon")
	}
	if res.AllExpected() {
		t.Fatal("cleanup conflict must be reported")
	}
}

func TestRegressionExplicitVersionRequired(t *testing.T) {
	f := newFakeNodeAPI("worker-1", "uid-1")
	c := regressionClient(t, f, nil)
	for _, rv := range []string{"", "0"} {
		_, err := c.PatchOwnNodeConditionAtVersion(context.Background(), rv, nodeidentity.Condition{Type: "T", Status: "True"})
		if !errors.Is(err, nodeidentity.ErrInvalidConfig) {
			t.Fatalf("version %q: %v", rv, err)
		}
	}
	if f.gets != 0 || f.patch != 0 {
		t.Fatal("invalid versions must fail before API access")
	}
}
