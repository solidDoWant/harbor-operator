#!/usr/bin/env bash
# Regenerates the generated reference pages under docs/admin/ (`make generate`
# runs it; `make docs-check` fails when it changes them):
#   - the CRD field reference, from the CRDs in config/crd/bases;
#   - the chart values reference (docs/admin/chart-values.md), from the
#     chart's values.yaml.
#
# TODO: neither generator exists yet. The issues that add the first CRD kind
# and the chart add them here (with the tool in flake.nix), and list the pages
# in docs/README.md. Until then this generates nothing.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

generated=0
shopt -s nullglob
crds=(config/crd/bases/*.yaml)
if ((${#crds[@]} > 0)); then
	echo "generate-reference: TODO: CRDs exist but no CRD reference generator is wired in yet" >&2
fi
if [[ -f deploy/charts/harbor-operator/values.yaml ]]; then
	echo "generate-reference: TODO: the chart exists but no chart values reference generator is wired in yet" >&2
fi
[[ "$generated" == 1 ]] || echo "generate-reference: nothing to generate yet"
