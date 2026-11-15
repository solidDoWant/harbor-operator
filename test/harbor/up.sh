#!/usr/bin/env bash
# Starts (or reuses) a kind cluster with Harbor installed from its Helm chart
# and writes the connection settings to build/harbor/<cluster>.env.
#
#   test/harbor/up.sh            # prints the env file path
#   HARBOR_CLUSTER=harbor-op-6 test/harbor/up.sh
#
# Harbor is exposed on a NodePort that kind maps to a free host port, and
# externalURL matches it so the registry token service works for pushes.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

cluster="${HARBOR_CLUSTER:-harbor-op-int}"
node_image="mirror.gcr.io/kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0"
chart="oci://ghcr.io/home-operations/charts-mirror/harbor"
chart_version="1.19.4"
node_port=30002
admin_password="Harbor12345"
env_file="build/harbor/${cluster}.env"
mkdir -p build/harbor

log() { echo "harbor-up: $*" >&2; }

if ! kind get clusters 2>/dev/null | grep -qx "$cluster"; then
	port="${HARBOR_HOST_PORT:-$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')}"
	log "creating kind cluster $cluster (host port $port)"
	kind create cluster --name "$cluster" --image "$node_image" --wait 120s --config <(
		cat <<YAML
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - containerPort: ${node_port}
        hostPort: ${port}
        listenAddress: 127.0.0.1
        protocol: TCP
YAML
	) >&2
fi

kubectl_ctx="kind-${cluster}"
port="$(docker port "${cluster}-control-plane" "${node_port}/tcp" | head -1 | sed 's/.*://')"
url="http://127.0.0.1:${port}"

log "installing Harbor chart ${chart_version}"
helm upgrade --install harbor "$chart" --version "$chart_version" \
	--kube-context "$kubectl_ctx" --namespace harbor --create-namespace \
	-f test/harbor/values.yaml --set "externalURL=${url}" \
	--wait --timeout 10m >&2

log "waiting for ${url}"
for _ in $(seq 1 120); do
	if curl -fsS -o /dev/null "${url}/api/v2.0/ping"; then
		break
	fi
	sleep 2
done
curl -fsS -o /dev/null "${url}/api/v2.0/ping"

cat >"$env_file" <<ENV
HARBOR_URL=${url}
HARBOR_ADMIN_USER=admin
HARBOR_ADMIN_PASSWORD=${admin_password}
HARBOR_CLUSTER=${cluster}
ENV
log "ready: ${env_file}"
echo "$env_file"
