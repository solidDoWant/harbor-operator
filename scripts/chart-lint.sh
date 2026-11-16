#!/usr/bin/env bash
# Lints the Helm chart without a cluster (`make chart-lint`, part of
# `make lint`): helm lint, then a render with the default values and one with
# watchNamespaces set, then one with the scrape options on.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

chart=deploy/charts/harbor-operator
helm lint --strict "$chart"
helm template harbor-operator "$chart" --namespace harbor-operator >/dev/null
helm template harbor-operator "$chart" --namespace harbor-operator \
  --set grafanaDashboard.configMap.enabled=true --set grafanaDashboard.grafanaOperator.enabled=true >/dev/null
helm template harbor-operator "$chart" --namespace harbor-operator --set 'watchNamespaces={team-a,team-b}' >/dev/null
helm template harbor-operator "$chart" --namespace harbor-operator \
	--set metrics.reader.enabled=true --set metrics.reader.subjects[0].namespace=monitoring --set metrics.reader.subjects[0].name=scraper \
	--set metrics.vmServiceScrape.enabled=true --set metrics.serviceMonitor.enabled=true >/dev/null

# Assert the pod labels and the cross-namespace import render, and stay off by default.
out=$(helm template harbor-operator "$chart" --namespace harbor-operator --set podLabels.a=b)
grep -q '^        a: b$' <<<"$out" || { echo "chart-lint: podLabels not rendered on the pod template" >&2; exit 1; }
out=$(helm template harbor-operator "$chart" --namespace harbor-operator --set grafanaDashboard.grafanaOperator.enabled=true)
! grep -q allowCrossNamespaceImport <<<"$out" || { echo "chart-lint: allowCrossNamespaceImport rendered by default" >&2; exit 1; }
out=$(helm template harbor-operator "$chart" --namespace harbor-operator \
  --set grafanaDashboard.grafanaOperator.enabled=true --set grafanaDashboard.grafanaOperator.allowCrossNamespaceImport=true)
grep -q '^  allowCrossNamespaceImport: true$' <<<"$out" || { echo "chart-lint: allowCrossNamespaceImport not rendered" >&2; exit 1; }
echo "chart-lint: passed"
