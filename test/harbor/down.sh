#!/usr/bin/env bash
# Deletes the Harbor test cluster (HARBOR_CLUSTER, default harbor-op-int).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
cluster="${HARBOR_CLUSTER:-harbor-op-int}"
kind delete cluster --name "$cluster"
rm -f "build/harbor/${cluster}.env"
