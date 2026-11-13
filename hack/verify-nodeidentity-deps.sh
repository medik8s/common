#!/bin/bash
#
# pkg/nodeidentity must import nothing outside the Go standard library.
#
# Two operators on different Kubernetes versions consume medik8s/common, and
# nodeidentity is on the safety-critical path of node-local remediation. A
# dependency here — even k8s.io/apimachinery — would have to be reconciled
# across every consumer before any of them could take a fix. The package
# talks plain JSON over HTTPS to the API server and needs none of it.
#
# This script checks ALL dependencies (including transitive and test
# dependencies) and fails on any `go list` error, so a broken build cannot
# silently pass the check. Aligned with hack/verify-nodestate-deps.sh.

set -euo pipefail

# Evaluate the module as a consumer will get it, not as a local workspace
# resolves it: a go.work overlay can satisfy an import from a sibling
# directory, hiding a dependency external to the published module.
export GOWORK=off

PACKAGES=(./pkg/nodeidentity/...)

if ! MODULE=$(go list -f '{{.Module.Path}}' ./pkg/nodeidentity 2>/dev/null); then
  echo "verify-nodeidentity-deps: cannot resolve the module for ./pkg/nodeidentity"
  exit 1
fi
if [ -z "$MODULE" ]; then
  echo "verify-nodeidentity-deps: empty module path for ./pkg/nodeidentity"
  exit 1
fi

# -deps includes transitive dependencies; -test includes _test.go imports.
# Capturing output in a variable (rather than process substitution) lets
# `set -e` catch a `go list` failure.
if ! DEPS=$(go list -deps -test "${PACKAGES[@]}" 2>/dev/null); then
  echo "verify-nodeidentity-deps: go list failed"
  exit 1
fi

# Filter to external module paths (contain a dot in the first segment),
# then remove this package's own test variants. What remains must be empty.
VIOLATIONS=$(echo "$DEPS" | awk -F/ '$1 ~ /\./ { print }' | grep -v "^${MODULE}/pkg/nodeidentity" | grep -v "^${MODULE}/pkg/nodeidentity[./]" || true)

if [ -n "$VIOLATIONS" ]; then
  echo "verify-nodeidentity-deps: pkg/nodeidentity has non-stdlib dependencies:"
  echo "$VIOLATIONS"
  exit 1
fi
echo "verify-nodeidentity-deps: OK"
