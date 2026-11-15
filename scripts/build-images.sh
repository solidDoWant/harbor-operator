#!/usr/bin/env bash
# Builds the operator image ghcr.io/soliddowant/harbor-operator:$VERSION for
# linux/amd64 and linux/arm64 (`make build-images`); PUSH_ALL=true also pushes.
#
# TODO: not implemented yet. The image build (ko, or a distroless Dockerfile
# from gcr.io) comes with the first image issue. Until then this only says so,
# and the release workflow skips the image.
set -euo pipefail
echo "build-images: TODO: no image build yet (VERSION=${VERSION:-unset}, PUSH_ALL=${PUSH_ALL:-false}); nothing built"
