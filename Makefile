# SHELL defines bash so all the inline scripts here will work as expected.
SHELL := /bin/bash

# GO_VERSION refers to the version of Golang to be downloaded when running dockerized version
GO_VERSION = 1.20
GOIMPORTS_VERSION = v0.11.0
SORT_IMPORTS_VERSION = v0.2.1

# Run go in a container
# --rm                                                          = remove container when stopped
# -v $$(pwd):/home/go/src/github.com/medik8s/node-maintenance-operator = bind mount current dir in container
# -u $$(id -u)                                                  = use current user (else new / modified files will be owned by root)
# -w /home/go/src/github.com/medik8s/node-maintenance-operator         = working dir
# -e ...                                                        = some env vars, especially set cache to a user writable dir
# --entrypoint /bin bash ... -c                                 = run bash -c on start; that means the actual command(s) need be wrapped in double quotes, see e.g. check target which will run: bash -c "make test"
export DOCKER_GO=docker run --rm -v $$(pwd):/home/go/src/github.com/medik8s/common \
	-u $$(id -u) -w /home/go/src/github.com/medik8s/common \
	-e "GOPATH=/go" -e "XDG_CACHE_HOME=/tmp/.cache" \
	--entrypoint /bin/bash golang:$(GO_VERSION) -c

# go-install-tool will 'go install' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-install-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin GOFLAGS='' go install $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef

.PHONY: build
build: ## Build.
	go build ./...

.PHONY: test
test: ## Run unit tests.
	go test ./... -coverprofile cover.out -v

.PHONY: lint
lint: tidy goimports fix-imports vet verify-nodestate-deps verify-nodeidentity-deps verify-no-changes ## Run linters etc.

.PHONY: verify-nodestate-deps
verify-nodestate-deps: ## Verify pkg/nodestate imports only the standard library.
	./hack/verify-nodestate-deps.sh

.PHONY: verify-nodeidentity-deps
verify-nodeidentity-deps: ## Verify pkg/nodeidentity imports only the standard library.
	./hack/verify-nodeidentity-deps.sh

# Run the node-identity permission matrix on a node of the currently logged-in
# cluster. Requires `oc` logged in; no container engine needed: the probe
# binary is built for the target GOARCH, copied onto the node through an
# `oc debug` pod, and executed on the host (chroot /host), so it uses the
# node's own kubelet certificate, kubelet CA, and the direct API endpoint
# from the node's kubeconfig. Use an exclusive qualification node. Cleanup
# failures and ambiguous state are reported; taint writes are dry-run only.
#
# Usage:
#   make nodeidentity-probe NODEIDENTITY_PROBE_NODE=<node-name>
#
# The probe is built for the target node's architecture automatically (read
# from the Node's status.nodeInfo.architecture, whose values match Go's
# GOARCH). Override with NODEIDENTITY_PROBE_GOARCH=<goarch> when the Node
# object does not report it or a specific build is wanted.
#
# Exit codes: make returns 0 on success and 2 for a failed recipe, including
# probe deviations. The recipe prints the probe's own exit code. Invoke the
# binary directly when its 0/1/2 distinction is required (see the README).
NODEIDENTITY_PROBE_NODE ?=
NODEIDENTITY_PROBE_GOARCH ?= auto
NODEIDENTITY_PROBE_OTHER_NODE ?=

.PHONY: nodeidentity-probe
nodeidentity-probe: ## Run the node-identity permission matrix on NODEIDENTITY_PROBE_NODE via oc debug.
	@set -eu; \
	node="$(NODEIDENTITY_PROBE_NODE)"; \
	if [ -z "$$node" ]; then \
	  echo "usage: make nodeidentity-probe NODEIDENTITY_PROBE_NODE=<node-name> [NODEIDENTITY_PROBE_GOARCH=<goarch>] [NODEIDENTITY_PROBE_OTHER_NODE=<other-node>]"; \
	  exit 2; \
	fi; \
	command -v oc >/dev/null || { echo "error: oc is not in PATH (oc login first)"; exit 2; }; \
	ns="$$(oc project -q 2>/dev/null || echo default)"; \
	goarch="$(NODEIDENTITY_PROBE_GOARCH)"; \
	if [ "$$goarch" = "auto" ]; then \
	  goarch=$$(oc get node "$$node" -o jsonpath='{.status.nodeInfo.architecture}'); \
	  if [ -z "$$goarch" ]; then echo "error: Node $$node does not report status.nodeInfo.architecture; set NODEIDENTITY_PROBE_GOARCH explicitly"; exit 2; fi; \
	fi; \
	tmpdir=$$(mktemp -d); \
	bin="$$tmpdir/nodeidentity-probe"; \
	hostdir=""; \
	cleanup() { if [ -n "$${pod:-}" ] && [ -n "$$hostdir" ]; then \
	    oc exec -n "$$ns" "$$pod" -- chroot /host /bin/rm -rf -- "$$hostdir" >/dev/null 2>&1 || echo "warning: remove $$hostdir on $$node manually" >&2; \
	  fi; \
	  [ -n "$${pod:-}" ] && oc delete pod "$$pod" -n "$$ns" --wait=false >/dev/null 2>&1 || true; \
	  [ -n "$${dbg:-}" ] && kill "$$dbg" 2>/dev/null || true; rm -rf "$$tmpdir"; }; \
	trap cleanup EXIT; \
	echo ">> building the probe for linux/$$goarch"; \
	CGO_ENABLED=0 GOOS=linux GOARCH=$$goarch go build -trimpath -ldflags="-s -w" -o "$$bin" ./cmd/nodeidentity-probe; \
	chmod 0755 "$$bin"; \
	uid=$$(oc get node "$$node" -o jsonpath='{.metadata.uid}'); \
	echo ">> starting a debug pod on $$node in namespace $$ns (held open for the binary transfer)"; \
	oc debug node/"$$node" -n "$$ns" -- bash -c 'sleep 600' >"$$tmpdir/debug.out" 2>&1 & \
	dbg=$$!; \
	pod=""; \
	for i in $$(seq 1 40); do \
	  pod=$$(sed -n 's/^Starting pod\/\([^ ]*\).*/\1/p' "$$tmpdir/debug.out" | head -1); \
	  if [ -n "$$pod" ]; then break; fi; sleep 3; \
	done; \
	if [ -z "$$pod" ]; then echo "error: this invocation's debug pod did not start (oc debug output below)"; cat "$$tmpdir/debug.out" >&2; exit 2; fi; \
	for i in $$(seq 1 30); do \
	  phase=$$(oc get pod "$$pod" -n "$$ns" -o jsonpath='{.status.phase}' 2>/dev/null || true); \
	  if [ "$$phase" = "Running" ]; then break; fi; sleep 2; \
	done; \
	if [ "$${phase:-}" != "Running" ]; then echo "error: debug pod $$pod is not Running (phase=$${phase:-unknown})"; exit 2; fi; \
	echo ">> creating private staging on $$node"; \
	hostdir=$$(oc exec -n "$$ns" "$$pod" -- chroot /host /bin/bash -c 'test "$$(id -u)" -eq 0 && umask 077 && mktemp -d /var/tmp/nodeidentity-probe.XXXXXXXXXX'); \
	if [[ ! "$$hostdir" =~ ^/var/tmp/nodeidentity-probe\.[a-zA-Z0-9]{10}$$ ]]; then echo "error: unexpected staging path" >&2; hostdir=""; exit 2; fi; \
	echo ">> copying the probe onto $$node"; \
	oc cp -n "$$ns" "$$bin" "$$pod:/host$$hostdir/nodeidentity-probe" >/dev/null; \
	echo ">> running the matrix on $$node"; \
	set +e; \
	oc exec -n "$$ns" "$$pod" -- chroot /host /bin/bash -c 'work=$$4; server=$$(sed -n "s/.*server: \(https:[^ ]*\).*/\1/p" /etc/kubernetes/kubeconfig | head -1); chmod 0755 "$$work/nodeidentity-probe" || exit 2; "$$work/nodeidentity-probe" --node-name "$$1" --uid "$$2" --api-server "$$server" --ca /etc/kubernetes/kubelet-ca.crt --cert /var/lib/kubelet/pki/kubelet-client-current.pem --key /var/lib/kubelet/pki/kubelet-client-current.pem $${3:+--other-node "$$3"}' _ "$$node" "$$uid" "$(NODEIDENTITY_PROBE_OTHER_NODE)" "$$hostdir"; \
	rc=$$?; \
	set -e; \
	if [ $$rc -ne 0 ]; then echo ">> probe exited with $$rc"; fi; \
	exit $$rc

.PHONY: check
check: ## Dockerized version of make test with additional verifications
	$(DOCKER_GO) "make lint test"

.PHONY: goimports
goimports: install-goimports ## Run go fmt against code.
	$(GOIMPORTS) -w ./pkg

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: verify-no-changes
verify-no-changes: ## verify there are no un-staged changes
	./hack/verify-diff.sh

.PHONY: tidy
tidy: ## Runs go mod tidy
	go mod tidy

SORT_IMPORTS = $(shell pwd)/bin/sort-imports
.PHONY: sort-imports
sort-imports: ## Download sort-imports locally if necessary.
	$(call go-install-tool,$(SORT_IMPORTS),github.com/slintes/sort-imports@$(SORT_IMPORTS_VERSION))

.PHONY: test-imports
test-imports: sort-imports ## Check for sorted imports
	$(SORT_IMPORTS) .

.PHONY: fix-imports
fix-imports: sort-imports ## Sort imports
	$(SORT_IMPORTS) -w .

GOIMPORTS = $(shell pwd)/bin/goimports
.PHONY: install-goimports
install-goimports: ## updates goimports.
	$(call go-install-tool,$(GOIMPORTS),golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION))
