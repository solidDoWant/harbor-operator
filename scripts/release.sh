#!/usr/bin/env bash
# Dry run of a release (`make release VERSION=x.y.z`): checks the version, that
# CHANGELOG.md has its section (the release notes), and that the binary, image
# and chart build. It never tags or pushes. Publishing is pushing the tag
# v$VERSION: the release workflow (.github/workflows/release.yml) then builds
# and pushes the image and chart and creates the GitHub release.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

version="${VERSION:-}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$ ]]; then
	echo "release: VERSION must look like 0.1.0 or 0.1.0-rc.1, got '${version}'" >&2
	exit 2
fi
if git rev-parse -q --verify "refs/tags/v$version" >/dev/null; then
	echo "release: tag v$version already exists" >&2
	exit 1
fi

echo "release: notes for v$version:"
./scripts/changelog-section.sh "$version"
make --no-print-directory build VERSION="$version"
make --no-print-directory build-images VERSION="$version"
make --no-print-directory chart-lint
echo "release: dry run of v$version passed; push the tag v$version to publish"
