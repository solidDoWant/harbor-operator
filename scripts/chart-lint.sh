#!/usr/bin/env bash
# Lints the Helm chart without a cluster (`make chart-lint`, part of
# `make lint`): helm lint, then a render with the default values and one with
# watchNamespaces set.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

chart=deploy/charts/harbor-operator
helm lint --strict "$chart"
helm template harbor-operator "$chart" --namespace harbor-operator >/dev/null
helm template harbor-operator "$chart" --namespace harbor-operator --set 'watchNamespaces={team-a,team-b}' >/dev/null
echo "chart-lint: passed"
