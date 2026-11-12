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
# A package path whose first element contains a dot is a module path, and so
# is external; everything else is standard library. Imports of this module's
# own packages are allowed but currently unused.

set -euo pipefail

# Evaluate the module as a consumer will get it, not as a local workspace
# resolves it: a go.work overlay can satisfy an import from a sibling
# directory, hiding a dependency external to the published module.
export GOWORK=off

PACKAGES=(./pkg/nodeidentity/...)

MODULE=$(go list -f '{{.Module.Path}}' ./pkg/nodeidentity)
if [ -z "$MODULE" ]; then
  echo "verify-nodeidentity-deps: cannot resolve the module for ./pkg/nodeidentity"
  exit 1
fi

STATUS=0
for pkg in $(go list "${PACKAGES[@]}"); do
  while read -r dep; do
    first="${dep%%/*}"
    if [[ "$first" == "$MODULE" ]]; then
      continue
    fi
    if [[ "$first" == *.* ]]; then
      echo "verify-nodeidentity-deps: ${pkg} imports external module path ${dep}"
      STATUS=1
    fi
  done < <(go list -f '{{range .Imports}}{{println .}}{{end}}' "$pkg")
done

if [ "$STATUS" -ne 0 ]; then
  echo "verify-nodeidentity-deps: pkg/nodeidentity must stay standard-library only."
  exit 1
fi
echo "verify-nodeidentity-deps: OK"
