#!/usr/bin/env bash
# Prepares the e2e environment on the kind cluster E2E_CLUSTER (default
# harbor-op-e2e): Harbor from its chart (test/harbor/up.sh), the operator image
# built for the host architecture and loaded into kind, and the operator
# installed from deploy/charts/harbor-operator. Prints the env file path; it
# holds HARBOR_URL, the admin credentials, the in-cluster Harbor URL and the
# kube context.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

cluster="${E2E_CLUSTER:-harbor-op-e2e}"
tag="e2e"
image="ghcr.io/soliddowant/harbor-operator:${tag}"
arch="$(go env GOARCH)"

log() { echo "e2e-up: $*" >&2; }

env_file="$(HARBOR_CLUSTER="$cluster" test/harbor/up.sh)"

log "building the operator image for linux/${arch}"
mkdir -p "build/images/${arch}"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -ldflags="-s -w -X main.version=${tag}" -o "build/images/${arch}/harbor-operator" ./cmd/harbor-operator
docker buildx build --platform "linux/${arch}" --tag "$image" --load . >&2

log "loading the image into kind"
kind load docker-image "$image" --name "$cluster" >&2

log "installing the operator chart"
helm upgrade --install harbor-operator deploy/charts/harbor-operator \
	--kube-context "kind-${cluster}" --namespace harbor-operator-system --create-namespace \
	--set "image.tag=${tag}" --set image.pullPolicy=Never \
	--wait --timeout 5m >&2

cat >>"$env_file" <<ENV
HARBOR_INTERNAL_URL=http://harbor.harbor.svc
E2E_KUBE_CONTEXT=kind-${cluster}
ENV
echo "$env_file"
