#!/usr/bin/env bash
# Writes the chart's generated templates from the files controller-gen produced
# (run by `make generate`; never edit the outputs by hand):
#   - templates/crds/*.yaml from config/crd/bases, each behind the crds.install value;
#   - templates/clusterrole.yaml from the rules in config/rbac/role.yaml.
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
	echo '# Generated from config/rbac/role.yaml by make generate; do not edit.'
	cat <<'HEADER'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "harbor-operator.fullname" . }}
  labels:
    {{- include "harbor-operator.labels" . | nindent 4 }}
HEADER
	sed -n '/^rules:/,$p' config/rbac/role.yaml
} >"$chart/templates/clusterrole.yaml"
