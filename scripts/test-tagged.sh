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
exec timeout "$((minutes + 5))m" ./scripts/ci/go-test-json.sh -race -count=1 -tags "$tag" -timeout "${minutes}m" "${packages[@]}"
