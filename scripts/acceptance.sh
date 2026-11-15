#!/usr/bin/env bash
# Runs every acceptance test (a Go test named on a `Tests:` line of a checklist
# scenario, docs/acceptance/phase-N.md) with the integration and e2e build
# tags, and prints the scoreboard for the phase's checklist,
# docs/acceptance/phase-$PHASE.md (default 1): one line per scenario (pass,
# fail, not run, no test, manual) and the number passing out of the automated
# scenarios. A scenario with an empty `Tests:` line is `no test`: not passing.
#
# The e2e tests run against the kind cluster E2E_CLUSTER (`make acceptance`
# passes it through).
#
# The scoreboard is also written to build/acceptance/scoreboard.txt, with the
# test output in build/acceptance/go.log. A failing test does not stop the
# scoreboard: it shows as fail.
set -uo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 1

phase="${PHASE:-1}"
checklist="docs/acceptance/phase-$phase.md"
[[ -f "$checklist" ]] || { echo "acceptance: no checklist $checklist" >&2; exit 2; }

out=build/acceptance
results="$out/results"
rm -rf "$out"
mkdir -p "$results"

# Harbor and the operator on kind for the e2e tests (and the integration tests
# that read HARBOR_URL); a cluster this script created is deleted afterwards
# unless KEEP_CLUSTER=1. Without Docker those tests skip.
if [[ -z "${HARBOR_URL:-}" ]] && docker info >/dev/null 2>&1; then
	export E2E_CLUSTER="${E2E_CLUSTER:-harbor-op-e2e}"
	export HARBOR_CLUSTER="$E2E_CLUSTER"
	created=true
	if kind get clusters 2>/dev/null | grep -qx "$E2E_CLUSTER"; then created=false; fi
	# shellcheck disable=SC2329 # runs through the EXIT trap
	cleanup() {
		if [[ "$created" == true && "${KEEP_CLUSTER:-}" != 1 ]]; then test/harbor/down.sh || true; fi
	}
	trap cleanup EXIT
	if env_file="$(test/e2e/up.sh)"; then
		set -a
		# shellcheck disable=SC1090
		source "$env_file"
		set +a
	else
		echo "acceptance: could not start Harbor and the operator; e2e tests will fail or skip" >&2
	fi
fi

checklist_args=()
for file in docs/acceptance/phase-*.md; do checklist_args+=(--checklist "$file"); done
pattern=$(./scripts/acceptance-scoreboard.py --go-run-pattern "${checklist_args[@]}") || exit 1
if [[ -n "$pattern" ]]; then
	echo "acceptance: go test -run '$pattern' (log: $out/go.log)" >&2
	timeout 90m go test -tags 'integration e2e' -count=1 -timeout 80m -json -run "$pattern" ./... \
		>"$results/go-acceptance.json" 2>"$out/go.log" ||
		echo "acceptance: some acceptance tests failed; see $out/go.log and $results/go-acceptance.json" >&2
else
	echo "acceptance: no acceptance tests to run" >&2
fi

{
	echo "Phase $phase ($checklist)"
	./scripts/acceptance-scoreboard.py --checklist "$checklist" --results "$results" --sources .
} | tee "$out/scoreboard.txt"
