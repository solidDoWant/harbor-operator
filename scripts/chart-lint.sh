#!/usr/bin/env bash
# Lints the Helm chart without a cluster (`make chart-lint`, part of
# `make lint`): helm lint, then a render with the default values.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

chart=deploy/charts/harbor-operator
helm lint --strict "$chart"
helm template harbor-operator "$chart" --namespace harbor-operator >/dev/null
helm template harbor-operator "$chart" --namespace harbor-operator \
  --set grafanaDashboard.configMap.enabled=true --set grafanaDashboard.grafanaOperator.enabled=true >/dev/null
echo "chart-lint: passed"
