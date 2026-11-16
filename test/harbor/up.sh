#!/usr/bin/env bash
# Starts (or reuses) a kind cluster with Harbor installed from its Helm chart
# and writes the connection settings to build/harbor/<cluster>.env.
#
#   test/harbor/up.sh            # prints the env file path
#   HARBOR_CLUSTER=harbor-op-6 test/harbor/up.sh
#
# Harbor is exposed on a NodePort that kind maps to a free host port, and
# externalURL matches it so the registry token service works for pushes.
#
# The cluster also runs a plain registry (namespace upstream) that proxy-cache
# tests use as their upstream. Harbor reaches it inside the cluster at
# UPSTREAM_URL; tests push to it from the host at UPSTREAM_REGISTRY.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

cluster="${HARBOR_CLUSTER:-harbor-op-int}"
node_image="mirror.gcr.io/kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0"
# HARBOR_VERSION picks a Harbor minor from test/harbor/versions.yaml (default:
# the first entry).
versions=scripts/ci/harbor-versions.py
harbor_minor="$($versions get "${HARBOR_VERSION:-}" minor)"
chart="$($versions get "$harbor_minor" chart)"
chart_repo="$($versions get "$harbor_minor" repo)"
chart_version="$($versions get "$harbor_minor" chartVersion)"
extra_values="$($versions get "$harbor_minor" values)"
node_port=30002
upstream_node_port=30005
upstream_image="ghcr.io/distribution/distribution:3.1.2@sha256:d106962e6fe3fa69c178cec77eeeee5edb626b8df6535b7bd6b95e658a0a5ca9"
admin_password="Harbor12345"
env_file="build/harbor/${cluster}.env"
mkdir -p build/harbor

log() { echo "harbor-up: $*" >&2; }

free_port() {
	python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

# kind picks host ports before Docker binds them, so another process can take
# one in between. Only that error is retried, with new ports.
create_cluster() {
	local port upstream_port out
	port="${HARBOR_HOST_PORT:-$(free_port)}"
	upstream_port="$(free_port)"
	log "creating kind cluster $cluster (host port $port)"
	if out="$(kind create cluster --name "$cluster" --image "$node_image" --wait 120s --config <(
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
      - containerPort: ${upstream_node_port}
        hostPort: ${upstream_port}
        listenAddress: 127.0.0.1
        protocol: TCP
YAML
	) 2>&1)"; then
		echo "$out" >&2
		return 0
	fi
	echo "$out" >&2
	kind delete cluster --name "$cluster" >&2 || true
	if grep -q 'port is already allocated' <<<"$out"; then
		return 75
	fi
	return 1
}

if ! kind get clusters 2>/dev/null | grep -qx "$cluster"; then
	for attempt in 1 2 3; do
		rc=0
		create_cluster || rc=$?
		if [[ $rc -eq 0 ]]; then break; fi
		if [[ $rc -ne 75 || $attempt -eq 3 ]]; then
			log "creating kind cluster failed"
			exit 1
		fi
		log "host port already allocated; retrying (attempt $((attempt + 1)) of 3)"
	done
fi

kubectl_ctx="kind-${cluster}"
port="$(docker port "${cluster}-control-plane" "${node_port}/tcp" | head -1 | sed 's/.*://')"
url="http://127.0.0.1:${port}"

log "installing Harbor ${harbor_minor} (chart ${chart_version})"
helm_args=(--version "$chart_version" -f test/harbor/values.yaml)
if [[ -n "$chart_repo" ]]; then helm_args+=(--repo "$chart_repo"); fi
if [[ -n "$extra_values" ]]; then helm_args+=(-f "$extra_values"); fi
helm upgrade --install harbor "$chart" "${helm_args[@]}" \
	--kube-context "$kubectl_ctx" --namespace harbor --create-namespace \
	--set "externalURL=${url}" \
	--wait --timeout 10m >&2

log "waiting for ${url}"
for _ in $(seq 1 120); do
	if curl -fsS -o /dev/null "${url}/api/v2.0/ping"; then
		break
	fi
	sleep 2
done
curl -fsS -o /dev/null "${url}/api/v2.0/ping"

log "starting the upstream registry"
kubectl --context "$kubectl_ctx" apply -f - >&2 <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: upstream
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: upstream
  namespace: upstream
spec:
  replicas: 1
  selector:
    matchLabels: {app: upstream}
  template:
    metadata:
      labels: {app: upstream}
    spec:
      containers:
        - name: registry
          image: ${upstream_image}
          ports:
            - containerPort: 5000
          readinessProbe:
            httpGet: {path: /v2/, port: 5000}
---
apiVersion: v1
kind: Service
metadata:
  name: upstream
  namespace: upstream
spec:
  type: NodePort
  selector: {app: upstream}
  ports:
    - port: 5000
      targetPort: 5000
      nodePort: ${upstream_node_port}
YAML
kubectl --context "$kubectl_ctx" -n upstream rollout status deployment/upstream --timeout 5m >&2

# A cluster made by an older version of this script has no host port for the upstream.
upstream_host="$(docker port "${cluster}-control-plane" "${upstream_node_port}/tcp" 2>/dev/null | head -1 | sed 's/.*://' || true)"

cat >"$env_file" <<ENV
HARBOR_URL=${url}
HARBOR_ADMIN_USER=admin
HARBOR_ADMIN_PASSWORD=${admin_password}
HARBOR_CLUSTER=${cluster}
HARBOR_MINOR=${harbor_minor}
UPSTREAM_URL=http://upstream.upstream.svc.cluster.local:5000
UPSTREAM_REGISTRY=${upstream_host:+127.0.0.1:${upstream_host}}
ENV
log "ready: ${env_file}"
echo "$env_file"
