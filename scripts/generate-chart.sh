#!/usr/bin/env bash
# Writes the chart's generated templates from the files controller-gen produced
# (run by `make generate`; never edit the outputs by hand):
#   - templates/crds/*.yaml from config/crd/bases, each behind the crds.install value;
#   - templates/_manager-rules.tpl: the rules in config/rbac/role.yaml as the named
#     template harbor-operator.managerRules, which the ClusterRole and the
#     per-namespace Roles render.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

chart=deploy/charts/harbor-operator
rm -rf "$chart/templates/crds"
mkdir -p "$chart/templates/crds"
for crd in config/crd/bases/*.yaml; do
	out="$chart/templates/crds/$(basename "$crd")"
	{
		echo '{{- if .Values.crds.install }}'
		cat "$crd"
		echo '{{- end }}'
	} >"$out"
done

{
	echo '{{/* Generated from config/rbac/role.yaml by make generate; do not edit. */}}'
	echo '{{- define "harbor-operator.managerRules" -}}'
	sed -n '/^rules:/,$p' config/rbac/role.yaml
	echo '{{- end }}'
} >"$chart/templates/_manager-rules.tpl"
