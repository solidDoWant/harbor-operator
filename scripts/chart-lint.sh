#!/usr/bin/env bash
# Lints the Helm chart without a cluster (`make chart-lint`, part of
# `make lint`). Skipped, with a message, until the chart exists.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

chart=deploy/charts/harbor-operator
if [[ ! -f "$chart/Chart.yaml" ]]; then
	echo "chart-lint: no chart at $chart yet; skipped"
	exit 0
fi
helm lint --strict "$chart"
