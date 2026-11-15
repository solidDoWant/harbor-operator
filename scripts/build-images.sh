#!/usr/bin/env bash
# Builds the operator image ghcr.io/soliddowant/harbor-operator:$VERSION for
# linux/amd64 and linux/arm64 (`make build-images`). Without PUSH_ALL=true the
# image for the host architecture is loaded into the local Docker daemon;
# PUSH_ALL=true pushes the multi-arch image (releases only).
# Each binary is scanned with govulncheck -mode=binary. Findings are reported
# but do not stop the build (VULNCHECK_STRICT=true makes them fail it): the
# release gate decides whether known vulnerabilities block a release.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

version="${VERSION:-0.0.0-dev}"
image="ghcr.io/soliddowant/harbor-operator:${version}"
ldflags="-s -w -X main.version=${version}"

for arch in amd64 arm64; do
	mkdir -p "build/images/$arch"
	CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -ldflags="$ldflags" -o "build/images/$arch/harbor-operator" ./cmd/harbor-operator
	if ! govulncheck -mode=binary "build/images/$arch/harbor-operator"; then
		echo "build-images: govulncheck reported vulnerabilities in the $arch binary" >&2
		[[ "${VULNCHECK_STRICT:-false}" == "true" ]] && exit 1
	fi
done

if [[ "${PUSH_ALL:-false}" == "true" ]]; then
	docker buildx build --platform linux/amd64,linux/arm64 --tag "$image" --push .
	exit 0
fi

# A multi-arch image cannot be loaded into the Docker daemon, so each
# architecture is built on its own and the host's one gets the plain tag.
host=$(go env GOARCH)
for arch in amd64 arm64; do
	tag="${image}-${arch}"
	docker buildx build --platform "linux/$arch" --tag "$tag" --load .
	if [[ "$arch" == "$host" ]]; then
		docker tag "$tag" "$image"
	fi
done
echo "build-images: built $image (linux/amd64 and linux/arm64 built; $host loaded as $image)"
