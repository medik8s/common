package nodeidentity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ProbeOutcome classifies the result of one permission-matrix check.
type ProbeOutcome string

const (
	// OutcomePermitted means the API server accepted the write (or served
	// the read).
	OutcomePermitted ProbeOutcome = "permitted"
	// OutcomeRefused means the API server rejected the request with 403 —
	// for taint writes this is the expected NodeRestriction behavior.
	OutcomeRefused ProbeOutcome = "refused"
	// OutcomeFailed means the check could not be measured: a transport
	// failure, an identity mismatch, or an unexpected error class.
	OutcomeFailed ProbeOutcome = "failed"
)

// ProbeCheck is one measured permission.
type ProbeCheck struct {
	// Name identifies the check, for example "cordon (spec.unschedulable)".
	Name string
	// Expected is the outcome the measured platforms produce. A check whose
	// Outcome differs from Expected is a deviation the caller must see.
	Expected ProbeOutcome
	// Outcome is what this run measured.
	Outcome ProbeOutcome
	// Detail carries the deviation or failure reason, never the success path.
	Detail string
}

// AsExpected reports whether the check matched its expected outcome.
func (pc ProbeCheck) AsExpected() bool { return pc.Outcome == pc.Expected }

// ProbeResult is the measured permission matrix of one node identity.
type ProbeResult struct {
	// NodeName and NodeUID identify the measured node.
	NodeName string
	NodeUID  string
	// APIServer is the endpoint the matrix ran against.
	APIServer string
	// Checks are the measured checks, in execution order.
	Checks []ProbeCheck
}

// Unexpected returns the checks whose outcome deviates from the measured
// platforms. An empty slice means the matrix matches.
func (r *ProbeResult) Unexpected() []ProbeCheck {
	var out []ProbeCheck
	for _, c := range r.Checks {
		if !c.AsExpected() {
			out = append(out, c)
		}
	}
	return out
}

// AllExpected reports whether every check matched.
func (r *ProbeResult) AllExpected() bool { return len(r.Unexpected()) == 0 }

// ProbeOptions configures RunPermissionMatrix.
type ProbeOptions struct {
	// ConditionType is the type of the probe condition written and removed
	// during the matrix. Default "Medik8sNodeIdentityProbe".
	ConditionType string
	// TaintKey is the key of the taint whose write the matrix attempts (and
	// expects to be refused). Default "medik8s.io/nodeidentity-probe".
	TaintKey string
	// OtherNode, when non-empty, enables the other-node read check against
	// that node. Reads of other nodes are permitted to the node identity;
	// writes never are.
	OtherNode string
	// ConditionTimestamp is the RFC3339 timestamp written into the probe
	// condition's lastTransitionTime. The package sets no hidden clock; an
	// empty value omits the field.
	ConditionTimestamp string
}

func (o ProbeOptions) conditionType() string {
	if o.ConditionType != "" {
		return o.ConditionType
	}
	return "Medik8sNodeIdentityProbe"
}

func (o ProbeOptions) taintKey() string {
	if o.TaintKey != "" {
		return o.TaintKey
	}
	return "medik8s.io/nodeidentity-probe"
}

// RunPermissionMatrix measures what this node identity may do to its own
// Node: the permission matrix the pre-kubelet self-cordon design depends on
// (cordon and uncordon permitted, taint writes refused, own status conditions
// permitted, reads of other nodes permitted). It is the executable form of
// the per-platform verification that design requires, intended for
// qualification runs and conformance tests — one call per supported
// OpenShift version.
//
// The matrix leaves the node as it found it. The cordon is restored to its
// prior value, the probe condition is removed, and the taint write is
// expected to be refused — if a platform unexpectedly permits it, the probe
// taint is rolled back and the check is reported as a deviation. A check
// that cannot be measured (transport failure, identity mismatch) is
// reported as failed, never skipped: the matrix either measures or says why
// it could not.
func (c *Client) RunPermissionMatrix(ctx context.Context, opts ProbeOptions) *ProbeResult {
	res := &ProbeResult{
		NodeName:  c.cfg.NodeName,
		APIServer: c.cfg.APIServer,
	}
	record := func(name string, expected ProbeOutcome, err error) {
		switch {
		case err == nil:
			res.Checks = append(res.Checks, ProbeCheck{Name: name, Expected: expected, Outcome: OutcomePermitted})
		case errors.Is(err, ErrForbidden):
			res.Checks = append(res.Checks, ProbeCheck{Name: name, Expected: expected, Outcome: OutcomeRefused})
		default:
			res.Checks = append(res.Checks, ProbeCheck{Name: name, Expected: expected, Outcome: OutcomeFailed, Detail: err.Error()})
		}
	}

	// 1. Read the own node; it anchors the UID and the prior cordon state.
	node, err := c.GetOwnNode(ctx)
	record("get-own-node", OutcomePermitted, err)
	if err != nil {
		return res
	}
	res.NodeUID = node.Metadata.UID
	priorUnschedulable := node.Spec.Unschedulable
	cordonedByUs := false

	// Cleanup never reuses the matrix context, which may be expired by the
	// time it runs: each cleanup derives its own bounded context.
	cleanupCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 30*time.Second)
	}

	// Restore the cordon to its prior value no matter how the remaining
	// checks go — but only if the node still carries the cordon WE set. If
	// another writer changed it in the meantime, that change stands and the
	// conflict is reported instead of silently reverted.
	defer func() {
		if !cordonedByUs {
			return
		}
		cctx, cancel := cleanupCtx()
		defer cancel()
		current, getErr := c.GetOwnNode(cctx)
		if getErr != nil {
			// Cannot verify ownership: attempt the restore anyway, and
			// report the failure without claiming to know the current state.
			if _, err := c.PatchUnschedulable(cctx, priorUnschedulable); err != nil {
				res.Checks = append(res.Checks, ProbeCheck{
					Name: "restore cordon (cleanup)", Expected: OutcomePermitted,
					Outcome: OutcomeFailed, Detail: fmt.Sprintf("restoring the cordon to its prior value failed: %v; the node's current state is unknown (a check read failed too: %v)", err, getErr),
				})
			}
			return
		}
		if !current.Spec.Unschedulable {
			// Someone already moved the cordon off what we set. Their change
			// stands; restoring would clobber it.
			res.Checks = append(res.Checks, ProbeCheck{
				Name: "restore cordon (cleanup)", Expected: OutcomePermitted,
				Outcome: OutcomeFailed, Detail: "intervening cordon update detected; the administrator's change was left intact and the restore was skipped",
			})
			return
		}
		if _, err := c.PatchUnschedulable(cctx, priorUnschedulable); err != nil {
			state := "unknown (a follow-up read failed)"
			if again, err2 := c.GetOwnNode(cctx); err2 == nil {
				state = cordonWord(again.Spec.Unschedulable)
			}
			res.Checks = append(res.Checks, ProbeCheck{
				Name: "restore cordon (cleanup)", Expected: OutcomePermitted,
				Outcome: OutcomeFailed, Detail: fmt.Sprintf("restoring the cordon to its prior value failed: %v; the node is currently %s", err, state),
			})
		}
	}()

	// 2. Cordon and read the flag back from the patch response.
	cordoned, err := c.Cordon(ctx)
	if err == nil && !cordoned.Spec.Unschedulable {
		err = fmt.Errorf("%w: patch accepted but spec.unschedulable is not set in the response", ErrAPIServer)
	}
	record("cordon (spec.unschedulable=true)", OutcomePermitted, err)
	cordonedByUs = err == nil && cordoned.Spec.Unschedulable

	// 3. Write the probe condition; remove it on the way out.
	condType := opts.conditionType()
	if existing := findCondition(node, condType); existing {
		// Another run's condition is already there; overwriting it would
		// destroy its data and our cleanup would remove it. Refuse.
		res.Checks = append(res.Checks, ProbeCheck{
			Name: "condition-write (nodes/status)", Expected: OutcomePermitted,
			Outcome: OutcomeFailed,
			Detail:  fmt.Sprintf("a %q condition already exists (likely a previous run that did not clean up); refusing to overwrite it - remove it or set ProbeOptions.ConditionType", condType),
		})
	} else {
		cond := Condition{
			Type:    condType,
			Status:  "True",
			Reason:  "PermissionMatrix",
			Message: "pkg/nodeidentity permission matrix probe; removed by the same run",
		}
		if opts.ConditionTimestamp != "" {
			cond.LastTransitionTime = opts.ConditionTimestamp
		}
		err = c.patchConditionWithRetry(ctx, cond, 5)
		record("condition-write (nodes/status)", OutcomePermitted, err)
		// The removal is deferred regardless of the write's reported result:
		// a failed response does not prove the write was not applied (a
		// timeout after the server processed it, for example), and removal
		// of an absent condition is a success.
		defer func() {
			cctx, cancel := cleanupCtx()
			defer cancel()
			if err := c.RemoveOwnNodeCondition(cctx, condType); err != nil {
				res.Checks = append(res.Checks, ProbeCheck{
					Name: "remove probe condition (cleanup)", Expected: OutcomePermitted,
					Outcome: OutcomeFailed, Detail: err.Error(),
				})
			}
		}()
	}

	// 4. Attempt a taint write and expect the NodeRestriction refusal. The
	// request asks for dry-run processing, so a platform that unexpectedly
	// permits it stores nothing; the rollback below stays as defense against
	// servers that ignore the dry-run parameter.
	if err := c.attemptProbeTaint(ctx, opts.taintKey()); err != nil {
		if errors.Is(err, ErrForbidden) {
			res.Checks = append(res.Checks, ProbeCheck{Name: "taint-write (expect refusal)", Expected: OutcomeRefused, Outcome: OutcomeRefused})
		} else {
			res.Checks = append(res.Checks, ProbeCheck{Name: "taint-write (expect refusal)", Expected: OutcomeRefused, Outcome: OutcomeFailed, Detail: err.Error()})
		}
	} else {
		// The write was accepted, which the measured platforms do not do.
		// Roll the probe taint back and report the deviation.
		detail := "taint write was PERMITTED; the measured platforms refuse it (NodeRestriction); the request was dry-run and the probe taint was rolled back"
		cctx, cancel := cleanupCtx()
		defer cancel()
		if rbErr := c.removeProbeTaint(cctx, opts.taintKey()); rbErr != nil {
			detail = fmt.Sprintf("taint write was PERMITTED and the rollback failed: %v", rbErr)
		}
		res.Checks = append(res.Checks, ProbeCheck{Name: "taint-write (expect refusal)", Expected: OutcomeRefused, Outcome: OutcomePermitted, Detail: detail})
	}

	// 5. Optionally read another node.
	if opts.OtherNode != "" {
		if opts.OtherNode == c.cfg.NodeName {
			res.Checks = append(res.Checks, ProbeCheck{
				Name: "get-other-node (read)", Expected: OutcomePermitted,
				Outcome: OutcomeFailed,
				Detail:  "OtherNode is the identity's own node; an own-node read is measured by get-own-node and cannot measure other-node permissions",
			})
		} else {
			_, err = c.GetNode(ctx, opts.OtherNode)
			record("get-other-node (read)", OutcomePermitted, err)
		}
	}

	return res
}

// findCondition reports whether a condition of the given type is present.
func findCondition(node *Node, condType string) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == condType {
			return true
		}
	}
	return false
}

// patchConditionWithRetry applies the probe condition following the
// package's documented conflict protocol: on ErrConflict, re-GET (which
// re-validates the identity and refreshes the resourceVersion) and re-patch,
// bounded. The conflict is expected here in practice: cordoning a node makes
// the node controller add the node.kubernetes.io/unschedulable taint, and
// that concurrent spec write bumps the resourceVersion between the probe's
// cordon response and its condition patch.
func (c *Client) patchConditionWithRetry(ctx context.Context, cond Condition, attempts int) error {
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			// Refresh the precondition; the GET also re-validates the UID.
			if _, gerr := c.GetOwnNode(ctx); gerr != nil {
				return gerr
			}
		}
		_, err = c.PatchOwnNodeCondition(ctx, cond)
		if !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return err
}

// attemptProbeTaint strategic-merges one probe taint onto the own node,
// requesting dry-run processing: authorization and admission still run — on
// the measured platforms NodeRestriction refuses with 403 — but a platform
// that accepts the write stores nothing. On the measured platforms this is
// refused with 403 before anything is stored.
func (c *Client) attemptProbeTaint(ctx context.Context, key string) error {
	body := []byte(fmt.Sprintf(`{"spec":{"taints":[{"key":%q,"value":"probe","effect":"NoSchedule"}]}}`, key))
	resp, err := c.do(ctx, http.MethodPatch,
		"/api/v1/nodes/"+url.PathEscape(c.cfg.NodeName)+"?dryRun=All", PatchTypeStrategicMerge, body)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)
	return classify(resp)
}

// removeProbeTaint removes the probe taint with a strategic-merge delete
// directive, used only on platforms that unexpectedly permit the write.
func (c *Client) removeProbeTaint(ctx context.Context, key string) error {
	body := []byte(fmt.Sprintf(`{"spec":{"taints":[{"key":%q,"effect":"NoSchedule","$patch":"delete"}]}}`, key))
	resp, err := c.do(ctx, http.MethodPatch,
		"/api/v1/nodes/"+url.PathEscape(c.cfg.NodeName), PatchTypeStrategicMerge, body)
	if err != nil {
		return err
	}
	defer drainAndClose(resp)
	return classify(resp)
}

func cordonWord(unschedulable bool) string {
	if unschedulable {
		return "cordoned"
	}
	return "uncordoned"
}
