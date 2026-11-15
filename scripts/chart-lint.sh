#!/usr/bin/env bash
# Lints the Helm chart without a cluster (`make chart-lint`, part of
# `make lint`): helm lint, then a render with the default values and one with
# watchNamespaces set, then one with the scrape options on.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

chart=deploy/charts/harbor-operator
helm lint --strict "$chart"
helm template harbor-operator "$chart" --namespace harbor-operator >/dev/null
helm template harbor-operator "$chart" --namespace harbor-operator --set 'watchNamespaces={team-a,team-b}' >/dev/null
helm template harbor-operator "$chart" --namespace harbor-operator \
	--set metrics.reader.enabled=true --set metrics.reader.subjects[0].namespace=monitoring --set metrics.reader.subjects[0].name=scraper \
	--set metrics.vmServiceScrape.enabled=true --set metrics.serviceMonitor.enabled=true >/dev/null
echo "chart-lint: passed"
