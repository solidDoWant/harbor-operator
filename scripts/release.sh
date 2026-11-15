#!/usr/bin/env bash
# Dry run of a release (`make release VERSION=x.y.z`): checks the version, that
# CHANGELOG.md has its section (the release notes), that the binary, image and
# chart build, and that the packaged chart's version and default image tag are
# the release version. It prints what the release workflow would push. It never
# tags or pushes. Publishing is pushing the tag v$VERSION: the release workflow
# (.github/workflows/release.yml) then builds and pushes the image and chart and
# creates the GitHub release.
#
# SKIP_BUILD=true skips the binary and image builds (tests only).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

version="${VERSION:-}"
if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
	echo "release: VERSION must look like 0.1.0, 0.1.0-rc.1 or 0.0.0-test, got '${version}'" >&2
	exit 2
fi
if git rev-parse -q --verify "refs/tags/v$version" >/dev/null; then
	echo "release: tag v$version already exists" >&2
	exit 1
fi

echo "release: notes for v$version:"
./scripts/changelog-section.sh "$version"
if [[ "${SKIP_BUILD:-false}" != "true" ]]; then
	make --no-print-directory build VERSION="$version"
	make --no-print-directory build-images VERSION="$version"
fi
make --no-print-directory chart-lint

# The workflow packages the chart with --version and --app-version set to the
# release version; check that the result is what a user would install.
chart=deploy/charts/harbor-operator
outdir=$(mktemp -d)
trap 'rm -rf "$outdir"' EXIT
helm package "$chart" --version "$version" --app-version "$version" --destination "$outdir" >/dev/null
package="$outdir/harbor-operator-$version.tgz"
if [[ ! -f "$package" ]]; then
	echo "release: helm did not produce harbor-operator-$version.tgz" >&2
	exit 1
fi
chart_version=$(helm show chart "$package" | awk '$1 == "version:" { print $2 }')
app_version=$(helm show chart "$package" | awk '$1 == "appVersion:" { print $2 }')
if [[ "$chart_version" != "$version" || "$app_version" != "$version" ]]; then
	echo "release: chart version '$chart_version' and appVersion '$app_version' must both be '$version'" >&2
	exit 1
fi
image="ghcr.io/soliddowant/harbor-operator:$version"
if ! helm template harbor-operator "$package" --namespace harbor-operator | grep -Fq "image: \"$image\"" &&
	! helm template harbor-operator "$package" --namespace harbor-operator | grep -Fq "image: $image"; then
	echo "release: the chart does not deploy the image $image by default" >&2
	exit 1
fi

echo "release: would push image $image (linux/amd64, linux/arm64)"
case "$version" in
*-*) ;;
*) echo "release: would tag image ghcr.io/soliddowant/harbor-operator:latest" ;;
esac
echo "release: would push chart oci://ghcr.io/soliddowant/charts/harbor-operator:$version"
echo "release: would create GitHub release v$version with the notes above"
echo "release: dry run of v$version passed; push the tag v$version to publish"
