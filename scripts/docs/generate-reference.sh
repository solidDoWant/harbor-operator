#!/usr/bin/env bash
# Regenerates the generated reference pages under docs/admin/ (`make generate`
# runs it; `make docs-check` fails when it changes them):
#   - the chart values reference (docs/admin/chart-values.md), from the
#     chart's values.yaml;
#   - the CRD field reference (docs/user/crd-reference.md), from the
#     generated CRDs in config/crd/bases (scripts/docs/crdref);
#   - the required-permission table (internal/harbor) and its admin page,
#     from the annotated client methods (scripts/docs/permissions).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

./scripts/docs/chart_values.py deploy/charts/harbor-operator/values.yaml >docs/admin/chart-values.md
echo "generate-reference: wrote docs/admin/chart-values.md"
go run ./scripts/docs/crdref config/crd/bases >docs/user/crd-reference.md
echo "generate-reference: wrote docs/user/crd-reference.md"
./scripts/docs/include-examples.py
go run ./scripts/docs/permissions
echo "generate-reference: wrote internal/harbor/required_permissions_generated.go and docs/admin/required-permissions.md"
