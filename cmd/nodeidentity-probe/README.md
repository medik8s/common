# nodeidentity-probe

Measures what a node's own identity may do to its Node object — the
permission matrix the pre-kubelet self-cordon design depends on:

| check | expected |
|---|---|
| `get-own-node` | permitted |
| `cordon (spec.unschedulable=true)` | permitted |
| `uncordon (spec.unschedulable=false)` | permitted (measured during cleanup) |
| `condition-write (nodes/status)` | permitted |
| `taint-write (expect refusal)` | **refused** (403, NodeRestriction) |
| `get-other-node (read)` (optional) | permitted |

**This binary is a diagnostic tool for humans and CI, not a runtime
component.** The production consumers (the SBR boot unit, the agent, the
conformance test) import [`pkg/nodeidentity`](../../pkg/nodeidentity) and
call the `Client` directly. Nothing ships this tool, nothing parses its
output, and its exit codes are for shells and pipelines, not an API.

## Usage

### The easy way — the make target

From a checkout of this repo, logged in with `oc` to any cluster where you
can run `oc debug node`:

```bash
make nodeidentity-probe NODEIDENTITY_PROBE_NODE=<node-name>
```

That one command:

1. reads the node's architecture from the Node object and builds the probe
   for it (`NODEIDENTITY_PROBE_GOARCH=<goarch>` overrides),
2. starts an `oc debug` pod and copies the binary into a unique root-owned
   mode-0700 staging directory on the host,
3. runs it **on the node**, with the node's own kubelet client certificate,
   the kubelet CA, and the direct API endpoint from the node's kubeconfig —
   no values passed by hand, no admin credentials involved,
4. removes that invocation's staging directory, deletes the debug pod, and prints the matrix.

Example output:

```
node identity permission matrix
node:    worker-1 (uid e12c0ff5-9097-405a-bbd7-be3a37e82756)
server:  https://api.cluster.example:6443

check                                        expected   outcome    detail
get-own-node                                 permitted  permitted
cordon (spec.unschedulable=true)             permitted  permitted
condition-write (nodes/status)               permitted  permitted
taint-write (expect refusal)                 refused    refused
uncordon (spec.unschedulable=false)           permitted  permitted

all checks match the measured platforms
```

Optional: `NODEIDENTITY_PROBE_OTHER_NODE=<name>` adds the other-node read
check.

The binary returns `0` when all checks matched, `1` for a deviation or an
unmeasured check, and `2` when it cannot start. **GNU make returns `2` for any
failed recipe**, including a probe that returned `1`; it does not propagate
the probe's `1`. The make recipe prints the probe's code. Run the binary
directly when that distinction matters.

### The binary directly

The make target is a wrapper; the binary itself takes an explicit identity —
the same values an enrollment routine records in `identity.json`:

```bash
./nodeidentity-probe \
  --node-name worker-1 \
  --uid        e12c0ff5-9097-405a-bbd7-be3a37e82756 \
  --api-server https://api.cluster.example:6443 \
  --ca         /etc/kubernetes/kubelet-ca.crt \
  --cert       /var/lib/kubelet/pki/kubelet-client-current.pem \
  --key        /var/lib/kubelet/pki/kubelet-client-current.pem
```

Or point at a descriptor file (individual flags override the file's values):

```bash
./nodeidentity-probe --identity-file /var/lib/medik8s/state/identity.json
```

with the descriptor shaped like:

```json
{
  "schemaVersion": "1",
  "nodeName": "worker-1",
  "nodeUID": "e12c0ff5-9097-405a-bbd7-be3a37e82756",
  "apiServerURL": "https://api.cluster.example:6443",
  "caCertPath": "/etc/kubernetes/kubelet-ca.crt",
  "clientCertPath": "/var/lib/kubelet/pki/kubelet-client-current.pem",
  "clientKeyPath": "/var/lib/kubelet/pki/kubelet-client-current.pem"
}
```

Flags:

| flag | meaning |
|---|---|
| `--node-name` | name of the Node this identity belongs to (required) |
| `--uid` | expected Node UID, recorded while the node was healthy (required) |
| `--api-server` | direct host-routable API endpoint, `https` only (required) |
| `--ca` | CA bundle path verifying the API server (required) |
| `--cert`, `--key` | kubelet client certificate and key paths; may be the SAME combined PEM file, which is the OpenShift layout (required) |
| `--identity-file` | descriptor JSON as above; individual flags override it |
| `--other-node` | also measure the read of this node (optional) |
| `--timeout` | timeout for checks, default 60s; condition and cordon cleanup each have a separate 30s limit |
| `--json` | print the result as JSON instead of the table (for CI) |

Where to run it: anywhere that can read the credential files and reach the
API endpoint — on the node itself, or from an admin host that has copies of
the node's credentials. The endpoint must be the **direct** one from the
node's kubeconfig, never kubelet's local proxy.

To run it by hand, use a private directory on both the build host and the
target node. The target directory must be created as root with mode 0700;
copying directly to a shared `/var/tmp/nodeidentity-probe` path is unsafe.
Use an exclusive qualification node without other scheduling-state writers.

```bash
# Build locally for the target architecture.
builddir=$(mktemp -d)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
  -o "$builddir/nodeidentity-probe" ./cmd/nodeidentity-probe
chmod 0755 "$builddir/nodeidentity-probe"

# Start a debug pod, then set pod to the exact name printed by this command.
oc debug node/worker-1 -- bash -c 'sleep 600' &
pod=<name-printed-by-oc-debug>
hostdir=$(oc exec "$pod" -- chroot /host /bin/bash -c \
  'test "$(id -u)" -eq 0 && umask 077 && mktemp -d /var/tmp/nodeidentity-probe.XXXXXXXXXX')
# Verify the returned path before using it; use the make target for automated cleanup.
[[ "$hostdir" =~ ^/var/tmp/nodeidentity-probe\.[a-zA-Z0-9]{10}$ ]] || exit 2
oc cp "$builddir/nodeidentity-probe" "$pod:/host$hostdir/nodeidentity-probe"
server=$(oc exec "$pod" -- chroot /host /bin/bash -c \
  "sed -n 's/.*server: \(https:[^ ]*\).*/\1/p' /etc/kubernetes/kubeconfig | head -1")
oc exec "$pod" -- chroot /host "$hostdir/nodeidentity-probe" \
  --node-name worker-1 \
  --uid "$(oc get node worker-1 -o jsonpath='{.metadata.uid}')" \
  --api-server "$server" \
  --ca /etc/kubernetes/kubelet-ca.crt \
  --cert /var/lib/kubelet/pki/kubelet-client-current.pem \
  --key /var/lib/kubelet/pki/kubelet-client-current.pem

# Run cleanup even if the probe fails. If oc cannot reach the node, record
# hostdir and remove that directory once access is restored.
oc exec "$pod" -- chroot /host /bin/rm -rf -- "$hostdir"
oc delete pod "$pod"
rm -rf -- "$builddir"
```

### The conformance test

`pkg/nodeidentity` also ships the same measurement as an opt-in Go test,
`TestNodeIdentityConformance`, enabled with `NODEIDENTITY_CONFORMANCE=1` and
the same identity values as environment variables — see
`pkg/nodeidentity/conformance_test.go` for the full contract.

## What it does to the node

- It cordons an initially schedulable node, then attempts a UID- and
  resourceVersion-guarded uncordon and verifies the response.
- It preserves a pre-existing cordon and reports uncordon as **not measured**;
  that run cannot return a full-success result.
- It writes a condition with a per-run ownership marker, rechecks absence on
  conflict, and removes it only while the marker still belongs to the run.
- It tests taint permissions with `dryRun=All` and a unique value. Even when
  permitted, no live rollback is sent and existing taints are preserved.
- Cleanup has its own bounded contexts. If the cordon response was lost or
  current state cannot be read, it reports residual/unknown state rather than
  blindly uncordoning. Inspect ownership before manual recovery.

Run the matrix on an exclusive qualification node: no concurrent maintenance,
remediation or other diagnostic run should change its scheduling state. A
boolean cannot detect another actor reasserting the same cordon. There is no
claim of transactional restoration across process termination, API loss or
concurrent administrators. A cordon is scheduling isolation, not fencing.

The live conformance test uses a fresh random condition-name suffix on each
invocation; `NODEIDENTITY_CONDITION_TYPE` configures its prefix. Cleanup
failures fail the test. Unit tests use local TLS servers and shell mocks;
they do not replace qualification on the supported OpenShift versions.

## When to run it — and when not

- **Run it** for qualification, once per supported OpenShift version (the
  permission matrix is the self-cordon design's single external dependency),
  and after upgrades when you suspect the node identity's permissions
  changed.
- **Do not run it** as a monitoring probe or a readiness gate: it mutates
  node state briefly by design, which is right for a qualification run and
  wrong for a periodic health check.
