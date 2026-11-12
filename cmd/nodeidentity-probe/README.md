# nodeidentity-probe

Measures what a node's own identity may do to its Node object — the
permission matrix the pre-kubelet self-cordon design depends on:

| check | expected |
|---|---|
| `get-own-node` | permitted |
| `cordon (spec.unschedulable=true)` | permitted |
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
2. starts an `oc debug` pod on the node and copies the binary onto the host,
3. runs it **on the node**, with the node's own kubelet client certificate,
   the kubelet CA, and the direct API endpoint from the node's kubeconfig —
   no values passed by hand, no admin credentials involved,
4. deletes the debug pod and prints the matrix.

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

all checks match the measured platforms
```

Optional: `NODEIDENTITY_PROBE_OTHER_NODE=<name>` adds the other-node read
check.

Exit codes: `0` all checks as expected, `1` at least one deviation, `2` the
probe could not run. Through `make`, `0` and `1` are propagated; any other
non-zero code — including `2` — may also be a failure of the make harness
itself (build, `oc`, debug pod), since GNU make uses `2` for its own errors.
When the distinction matters, run the binary directly.

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
| `--timeout` | overall timeout, default 60s |
| `--json` | print the result as JSON instead of the table (for CI) |

Where to run it: anywhere that can read the credential files and reach the
API endpoint — on the node itself, or from an admin host that has copies of
the node's credentials. The endpoint must be the **direct** one from the
node's kubeconfig, never kubelet's local proxy.

To run it by hand on a node, you first need the binary on that node. The
make target does all of this for you; by hand it is four steps (the same
ones the make target automates):

```bash
# 1. Build for the node's architecture (oc get node worker-1 -o
#    jsonpath='{.status.nodeInfo.architecture}' tells you which), and make
#    it executable — oc cp preserves the mode.
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
  -o /tmp/nodeidentity-probe ./cmd/nodeidentity-probe
chmod 0755 /tmp/nodeidentity-probe

# 2. Start a debug pod on the node and keep it alive, then note its name.
oc debug node/worker-1 -- bash -c 'sleep 600' &
oc get pods -o name | grep worker-1-debug    # e.g. pod/worker-1-debug-abc12

# 3. Copy the binary onto the host filesystem through the debug pod.
#    Use /var/tmp, not /tmp — RHCOS mounts /tmp noexec.
oc cp /tmp/nodeidentity-probe worker-1-debug-abc12:/host/var/tmp/nodeidentity-probe

# 4. Run it on the host, inside the debug pod's chroot.
oc exec worker-1-debug-abc12 -- chroot /host /var/tmp/nodeidentity-probe \
  --node-name worker-1 \
  --uid "$(oc get node worker-1 -o jsonpath='{.metadata.uid}')" \
  --api-server "$(oc debug node/worker-1 -- chroot /host /bin/bash -c \
    "sed -n 's/.*server: \(https:[^ ]*\).*/\1/p' /etc/kubernetes/kubeconfig | head -1")" \
  --ca   /etc/kubernetes/kubelet-ca.crt \
  --cert /var/lib/kubelet/pki/kubelet-client-current.pem \
  --key  /var/lib/kubelet/pki/kubelet-client-current.pem

# Clean up: delete the debug pod (oc delete pod worker-1-debug-abc12) and
# remove the binary (oc exec ... -- chroot /host rm /var/tmp/nodeidentity-probe).
```

Or, interactively inside one debug session — `oc debug` does not forward
stdin for file transfers, so the copy still needs the pod step above; the
session is only for the final invocation:

```bash
oc debug node/worker-1 -- chroot /host /bin/bash
/var/tmp/nodeidentity-probe --node-name worker-1 --uid <uid> \
  --api-server "$(sed -n 's/.*server: \(https:[^ ]*\).*/\1/p' /etc/kubernetes/kubeconfig | head -1)" \
  --ca /etc/kubernetes/kubelet-ca.crt \
  --cert /var/lib/kubelet/pki/kubelet-client-current.pem \
  --key /var/lib/kubelet/pki/kubelet-client-current.pem
```

### The conformance test

`pkg/nodeidentity` also ships the same measurement as an opt-in Go test,
`TestNodeIdentityConformance`, enabled with `NODEIDENTITY_CONFORMANCE=1` and
the same identity values as environment variables — see
`pkg/nodeidentity/conformance_test.go` for the full contract.

## What it does to the node

It leaves the node as it found it:

- briefly cordons it (`spec.unschedulable`) and restores the **prior** value,
- writes one probe condition (`Medik8sNodeIdentityProbe`) and removes it,
- attempts one taint write that the measured platforms refuse with 403; if a
  platform accepts it, the probe taint is rolled back and the check is
  reported as a deviation,
- cleanup failures are reported as deviations — never hidden.

## When to run it — and when not

- **Run it** for qualification, once per supported OpenShift version (the
  permission matrix is the self-cordon design's single external dependency),
  and after upgrades when you suspect the node identity's permissions
  changed.
- **Do not run it** as a monitoring probe or a readiness gate: it mutates
  node state briefly by design, which is right for a qualification run and
  wrong for a periodic health check.
