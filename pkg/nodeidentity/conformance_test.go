package nodeidentity_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/medik8s/common/pkg/nodeidentity"
)

// TestNodeIdentityConformance validates pkg/nodeidentity against a REAL
// cluster using real node credentials. It is skipped unless explicitly
// enabled, so `go test ./...` stays hermetic.
//
// # Enabling
//
// Either point at a pre-recorded identity descriptor (the JSON written by the
// SBR host-spike enrollment helper):
//
//	NODEIDENTITY_CONFORMANCE=1 \
//	NODEIDENTITY_IDENTITY_FILE=/var/lib/medik8s/state/identity.json \
//	go test -run TestNodeIdentityConformance -v ./pkg/nodeidentity/
//
// or provide the values individually:
//
//	NODEIDENTITY_CONFORMANCE=1 \
//	NODEIDENTITY_NODE_NAME=worker-1 \
//	NODEIDENTITY_EXPECTED_UID=$(oc get node worker-1 -o jsonpath='{.metadata.uid}') \
//	NODEIDENTITY_API_SERVER=https://api.cluster.example:6443 \
//	NODEIDENTITY_CA_FILE=/etc/kubernetes/kubelet-ca.crt \
//	NODEIDENTITY_CERT_FILE=/var/lib/kubelet/pki/kubelet-client-current.pem \
//	NODEIDENTITY_KEY_FILE=/var/lib/kubelet/pki/kubelet-client-current.pem \
//	go test -run TestNodeIdentityConformance -v ./pkg/nodeidentity/
//
// # Where to run it
//
// The test needs the credential files and network reachability to the API
// endpoint — nothing else. Run it on the node itself (build a static test
// binary with `CGO_ENABLED=0 go test -c -o conformance.test ./pkg/nodeidentity/`),
// or from any admin host that has the credential files and can reach the
// endpoint. The endpoint must be reachable WITHOUT the cluster CNI if you
// intend to validate the boot-hold scenario; use the node kubeconfig's
// direct endpoint, not kubelet's local proxy.
//
// # What it does to the cluster
//
// It writes exactly one probe condition (SBRNodeidentityConformance by
// default, override with NODEIDENTITY_CONDITION_TYPE) to the named node's
// status and removes it in a cleanup step that runs even when subtests fail.
// The removal uses a JSON-Patch `test`+`remove` pair guarded by the exact
// stored value, so an index shift can never remove a foreign condition. It
// briefly cordons the node (spec.unschedulable) and restores the prior value
// even when subtests fail. It attempts one taint write that the measured
// platforms refuse with 403; nothing is stored. No other object is touched.
const conformanceEnv = "NODEIDENTITY_CONFORMANCE"

// identityDescriptor mirrors the descriptor written by the SBR host-spike
// enrollment helper (identity.json).
type identityDescriptor struct {
	SchemaVersion  string `json:"schemaVersion"`
	NodeName       string `json:"nodeName"`
	NodeUID        string `json:"nodeUID"`
	APIServerURL   string `json:"apiServerURL"`
	CACertPath     string `json:"caCertPath"`
	ClientCertPath string `json:"clientCertPath"`
	ClientKeyPath  string `json:"clientKeyPath"`
}

func conformanceConfig(t *testing.T) (nodeidentity.Config, bool) {
	t.Helper()
	if os.Getenv(conformanceEnv) != "1" {
		return nodeidentity.Config{}, false
	}
	if f := os.Getenv("NODEIDENTITY_IDENTITY_FILE"); f != "" {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read identity file: %v", err)
		}
		var d identityDescriptor
		if err := json.Unmarshal(data, &d); err != nil {
			t.Fatalf("parse identity file: %v", err)
		}
		return nodeidentity.Config{
			NodeName:    d.NodeName,
			ExpectedUID: d.NodeUID,
			APIServer:   d.APIServerURL,
			CAFile:      d.CACertPath,
			CertFile:    d.ClientCertPath,
			KeyFile:     d.ClientKeyPath,
		}, true
	}
	cfg := nodeidentity.Config{
		NodeName:    os.Getenv("NODEIDENTITY_NODE_NAME"),
		ExpectedUID: os.Getenv("NODEIDENTITY_EXPECTED_UID"),
		APIServer:   os.Getenv("NODEIDENTITY_API_SERVER"),
		CAFile:      os.Getenv("NODEIDENTITY_CA_FILE"),
		CertFile:    os.Getenv("NODEIDENTITY_CERT_FILE"),
		KeyFile:     os.Getenv("NODEIDENTITY_KEY_FILE"),
	}
	var missing []string
	for k, v := range map[string]string{
		"NODEIDENTITY_NODE_NAME":    cfg.NodeName,
		"NODEIDENTITY_EXPECTED_UID": cfg.ExpectedUID,
		"NODEIDENTITY_API_SERVER":   cfg.APIServer,
		"NODEIDENTITY_CA_FILE":      cfg.CAFile,
		"NODEIDENTITY_CERT_FILE":    cfg.CertFile,
		"NODEIDENTITY_KEY_FILE":     cfg.KeyFile,
	} {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("conformance enabled but missing env vars: %v (or set NODEIDENTITY_IDENTITY_FILE)", missing)
	}
	return cfg, true
}

func conformanceConditionType() string {
	if v := os.Getenv("NODEIDENTITY_CONDITION_TYPE"); v != "" {
		return v
	}
	return "SBRNodeidentityConformance"
}

// removeConformanceCondition removes the probe condition using a JSON-Patch
// test+remove guarded on the condition's type field, retrying on index
// shifts. Guarding on the subpath (not the whole stored value) is deliberate:
// the API server may store fields the typed projection does not carry, and a
// whole-value test op would fail with 422 while a type-field test still
// guarantees a foreign condition can never be removed by an index shift.
func removeConformanceCondition(ctx context.Context, t *testing.T, c *nodeidentity.Client) {
	t.Helper()
	condType := conformanceConditionType()
	for attempt := 0; attempt < 5; attempt++ {
		node, err := c.GetOwnNode(ctx)
		if err != nil {
			t.Logf("cleanup: GET failed: %v", err)
			return
		}
		found := false
		for i, cond := range node.Status.Conditions {
			if cond.Type != condType {
				continue
			}
			found = true
			patch := fmt.Sprintf(`[{"op":"test","path":"/metadata/uid","value":%s},{"op":"test","path":"/status/conditions/%d/type","value":%s},{"op":"remove","path":"/status/conditions/%d"}]`,
				mustJSON(node.Metadata.UID), i, mustJSON(condType), i)
			if _, err := c.PatchOwnNodeStatus(ctx, nodeidentity.PatchTypeJSONPatch, []byte(patch)); err != nil {
				t.Logf("cleanup: remove attempt %d failed: %v", attempt, err)
				break // index may have shifted; re-GET and retry
			}
			return
		}
		if !found {
			return // condition no longer present
		}
	}
	t.Logf("cleanup: could not remove condition %s after retries", condType)
}

func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func TestNodeIdentityConformance(t *testing.T) {
	cfg, ok := conformanceConfig(t)
	if !ok {
		t.Skipf("set %s=1 and provide an identity (NODEIDENTITY_IDENTITY_FILE or the NODEIDENTITY_* variables); see conformance_test.go for the full contract", conformanceEnv)
	}
	c, err := nodeidentity.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	condType := conformanceConditionType()

	// Establish ownership BEFORE registering cleanup or writing anything: a
	// pre-existing condition of the selected type means an earlier run did
	// not clean up after itself, and this run must neither overwrite its
	// data nor remove it in cleanup.
	if node, err := c.GetOwnNode(ctx); err != nil {
		t.Fatalf("ownership check: GET own node: %v", err)
	} else {
		for _, cond := range node.Status.Conditions {
			if cond.Type == condType {
				t.Fatalf("a %q condition already exists on %s (an earlier run likely did not clean up); remove it manually or set NODEIDENTITY_CONDITION_TYPE to a fresh type", condType, cfg.NodeName)
			}
		}
	}

	// The cleanup gets its own context: the shared one is canceled by the
	// deferred cancel() before cleanups run.
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		removeConformanceCondition(cctx, t, c)
	})

	t.Run("GetOwnNode", func(t *testing.T) {
		node, err := c.GetOwnNode(ctx)
		if err != nil {
			t.Fatalf("GET own node: %v", err)
		}
		if node.Metadata.Name != cfg.NodeName {
			t.Fatalf("name mismatch: %q != %q", node.Metadata.Name, cfg.NodeName)
		}
		if node.Metadata.UID != cfg.ExpectedUID {
			t.Fatalf("uid mismatch: %q != %q", node.Metadata.UID, cfg.ExpectedUID)
		}
		if node.Metadata.ResourceVersion == "" {
			t.Fatal("empty resourceVersion")
		}
		t.Logf("endpoint=%s node=%s uid=%s rv=%s existingConditions=%d",
			cfg.APIServer, node.Metadata.Name, node.Metadata.UID,
			node.Metadata.ResourceVersion, len(node.Status.Conditions))
	})

	t.Run("UIDFailsafeBlocksReplacedNode", func(t *testing.T) {
		wrong := cfg
		wrong.ExpectedUID = "00000000-0000-0000-0000-000000000000"
		wc, err := nodeidentity.New(wrong)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wc.GetOwnNode(ctx); !errors.Is(err, nodeidentity.ErrNodeNameMismatch) {
			t.Fatalf("expected ErrNodeNameMismatch for a wrong expected UID, got %v", err)
		}
	})

	t.Run("PatchConditionMergesWithoutClobbering", func(t *testing.T) {
		before, err := c.GetOwnNode(ctx)
		if err != nil {
			t.Fatal(err)
		}
		beforeTypes := map[string]bool{}
		for _, cond := range before.Status.Conditions {
			beforeTypes[cond.Type] = true
		}

		cond := nodeidentity.Condition{
			Type:    condType,
			Status:  "True",
			Reason:  "Conformance",
			Message: fmt.Sprintf(`{"test":"nodeidentity-conformance","time":%q}`, time.Now().UTC().Format(time.RFC3339)),
		}
		patched, err := c.PatchOwnNodeCondition(ctx, cond)
		if err != nil {
			t.Fatalf("patch condition: %v", err)
		}
		found := false
		for _, got := range patched.Status.Conditions {
			if got.Type == condType {
				found = true
				if got.Reason != "Conformance" {
					t.Fatalf("condition content mismatch: %+v", got)
				}
			}
		}
		if !found {
			t.Fatal("patched condition not present in the response")
		}

		// Our patch must not remove or alter foreign conditions.
		for _, got := range patched.Status.Conditions {
			if got.Type != condType && beforeTypes[got.Type] {
				continue // still present: good
			}
		}
		for want := range beforeTypes {
			present := false
			for _, got := range patched.Status.Conditions {
				if got.Type == want {
					present = true
					break
				}
			}
			if !present {
				t.Fatalf("foreign condition %q disappeared after our patch — the patch body must carry only its own condition", want)
			}
		}
	})

	t.Run("IdempotentRepatch", func(t *testing.T) {
		cond := nodeidentity.Condition{Type: condType, Status: "True", Reason: "Conformance", Message: "repatched"}
		if _, err := c.PatchOwnNodeCondition(ctx, cond); err != nil {
			t.Fatalf("re-patch: %v", err)
		}
	})

	t.Run("StalePreconditionConflicts", func(t *testing.T) {
		node, err := c.GetOwnNode(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// A genuinely stale but non-zero resourceVersion: "0" means an
		// UNCONDITIONAL update in Kubernetes and would succeed.
		rv, err := strconv.Atoi(node.Metadata.ResourceVersion)
		if err != nil {
			t.Fatalf("resourceVersion %q is not numeric: %v", node.Metadata.ResourceVersion, err)
		}
		if rv <= 1 {
			t.Fatalf("resourceVersion %q too small to derive a stale version", node.Metadata.ResourceVersion)
		}
		stale := fmt.Sprintf(`{"metadata":{"resourceVersion":"%d"},"status":{"conditions":[{"type":%q,"status":"True"}]}}`, rv-1, condType)
		if _, err := c.PatchOwnNodeStatus(ctx, nodeidentity.PatchTypeStrategicMerge, []byte(stale)); !errors.Is(err, nodeidentity.ErrConflict) {
			t.Fatalf("expected ErrConflict for a stale precondition, got %v", err)
		}
	})

	t.Run("CleanupRemovesCondition", func(t *testing.T) {
		removeConformanceCondition(ctx, t, c)
		node, err := c.GetOwnNode(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, cond := range node.Status.Conditions {
			if cond.Type == condType {
				t.Fatalf("condition %s still present after cleanup", condType)
			}
		}
	})

	t.Run("CordonUncordon", func(t *testing.T) {
		before, err := c.GetOwnNode(ctx)
		if err != nil {
			t.Fatal(err)
		}
		prior := before.Spec.Unschedulable
		if prior {
			t.Skipf("node %s is already cordoned; skipping the cordon/uncordon transition so the test cannot disturb an administrator's cordon", cfg.NodeName)
		}
		t.Cleanup(func() {
			cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer ccancel()
			if _, err := c.PatchUnschedulable(cctx, prior); err != nil {
				t.Errorf("cleanup: restore cordon to %v: %v", prior, err)
			}
		})

		cordoned, err := c.Cordon(ctx)
		if err != nil {
			t.Fatalf("cordon: %v", err)
		}
		if !cordoned.Spec.Unschedulable {
			t.Fatal("cordon response does not report spec.unschedulable=true")
		}
		uncordoned, err := c.Uncordon(ctx)
		if err != nil {
			t.Fatalf("uncordon: %v", err)
		}
		if uncordoned.Spec.Unschedulable {
			t.Fatal("uncordon response reports spec.unschedulable=true")
		}
	})

	t.Run("PermissionMatrix", func(t *testing.T) {
		// Includes the taint-write refusal: the measured platforms refuse
		// taint writes from the node identity with 403, including the
		// node-restriction.kubernetes.io prefix. A platform that accepts
		// the write is a deviation and fails this subtest.
		res := c.RunPermissionMatrix(ctx, nodeidentity.ProbeOptions{
			ConditionType:      condType,
			ConditionTimestamp: time.Now().UTC().Format(time.RFC3339),
		})
		for _, chk := range res.Checks {
			t.Logf("%-42s expected=%-9s outcome=%-9s %s", chk.Name, chk.Expected, chk.Outcome, chk.Detail)
		}
		if deviations := res.Unexpected(); len(deviations) > 0 {
			t.Fatalf("permission matrix deviations: %+v", deviations)
		}
	})
}
