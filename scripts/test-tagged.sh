#!/usr/bin/env bash
# Runs the Go tests behind one build tag (`make test-integration`,
# `make test-e2e`): only the packages with a `//go:build <tag>` file, with the
# race detector. Passes with "no tests yet" while no such file exists.
#
#   scripts/test-tagged.sh integration
#   E2E_CLUSTER=harbor-op-12 scripts/test-tagged.sh e2e
#
# GO_TEST_JSON=file also writes the `go test -json` events there
# (scripts/ci/go-test-json.sh).
#
# For `e2e` the script runs test/e2e/up.sh (Harbor, the operator image loaded
# into kind, the operator from its chart) and deletes a cluster it created
# afterwards, unless KEEP_CLUSTER=1.
#
# For `integration` the script starts (or reuses) Harbor on kind with
# test/harbor/up.sh and exports HARBOR_URL, HARBOR_ADMIN_USER and
# HARBOR_ADMIN_PASSWORD, unless HARBOR_URL is already set. It deletes a
# cluster it created afterwards, unless KEEP_CLUSTER=1. Without Docker the
# tests skip with a message.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

tag="${1:?usage: test-tagged.sh integration|e2e}"
mapfile -t packages < <(
	git ls-files --cached --others --exclude-standard -- '*.go' |
		xargs -r grep -l -E "^//go:build .*\\b${tag}\\b" |
		xargs -r -n1 dirname | sort -u | sed 's|^|./|'
)
if ((${#packages[@]} == 0)); then
	echo "test-$tag: no tests yet"
	exit 0
fi

case "$tag" in
e2e) minutes=50 ;;
*) minutes=25 ;;
esac
echo "test-$tag: ${packages[*]}"

if [[ "$tag" == integration && -z "${HARBOR_URL:-}" ]]; then
	if docker info >/dev/null 2>&1; then
		export HARBOR_CLUSTER="${HARBOR_CLUSTER:-harbor-op-int}"
		created=true
		if kind get clusters 2>/dev/null | grep -qx "$HARBOR_CLUSTER"; then created=false; fi
		# shellcheck disable=SC2329 # runs through the EXIT trap
		cleanup() {
			if [[ "$created" == true && "${KEEP_CLUSTER:-}" != 1 ]]; then
				test/harbor/down.sh || true
			fi
		}
		trap cleanup EXIT
		env_file="$(test/harbor/up.sh)"
		set -a
		# shellcheck disable=SC1090
		source "$env_file"
		set +a
	else
		echo "test-integration: Docker is not available; tests that need Harbor will skip" >&2
	fi
fi

if [[ "$tag" == e2e ]]; then
	if ! docker info >/dev/null 2>&1; then
		echo "test-e2e: Docker is not available; the e2e tests skip" >&2
	else
		export E2E_CLUSTER="${E2E_CLUSTER:-harbor-op-e2e}"
		export HARBOR_CLUSTER="$E2E_CLUSTER"
		created=true
		if kind get clusters 2>/dev/null | grep -qx "$E2E_CLUSTER"; then created=false; fi
		# shellcheck disable=SC2329 # runs through the EXIT trap
		cleanup() {
			if [[ "$created" == true && "${KEEP_CLUSTER:-}" != 1 ]]; then
				test/harbor/down.sh || true
			fi
		}
		trap cleanup EXIT
		env_file="$(test/e2e/up.sh)"
		set -a
		# shellcheck disable=SC1090
		source "$env_file"
		set +a
	fi
fi

status=0
timeout "$((minutes + 5))m" ./scripts/ci/go-test-json.sh -race -count=1 -tags "$tag" -timeout "${minutes}m" "${packages[@]}" || status=$?
exit "$status"
