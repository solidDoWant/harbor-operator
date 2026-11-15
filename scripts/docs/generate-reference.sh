#!/usr/bin/env bash
# Regenerates the generated reference pages under docs/admin/ (`make generate`
# runs it; `make docs-check` fails when it changes them):
#   - the chart values reference (docs/admin/chart-values.md), from the
#     chart's values.yaml.
#
# TODO: the CRD field reference generator does not exist yet. The issue that
# adds it wires it in here (with the tool in flake.nix) and lists the page in
# docs/README.md.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

./scripts/docs/chart_values.py deploy/charts/harbor-operator/values.yaml >docs/admin/chart-values.md
echo "generate-reference: wrote docs/admin/chart-values.md"
