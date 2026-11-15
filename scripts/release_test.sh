#!/usr/bin/env bash
# Tests of scripts/release.sh (the dry run), without builds.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

fail() { echo "release_test: $*" >&2; exit 1; }

printf '# Changelog\n\n## 0.0.0-test\n\n- A change.\n\n## 0.0.0-other\n\n- Other.\n' >"$dir/changelog.md"

out=$(CHANGELOG_FILE="$dir/changelog.md" SKIP_BUILD=true VERSION=0.0.0-test ./scripts/release.sh 2>&1) ||
	fail "dry run failed: $out"
grep -q "would push chart oci://ghcr.io/soliddowant/charts/harbor-operator:0.0.0-test" <<<"$out" || fail "no chart line: $out"
grep -q "would push image ghcr.io/soliddowant/harbor-operator:0.0.0-test" <<<"$out" || fail "no image line: $out"
grep -q "A change" <<<"$out" || fail "no notes: $out"
grep -q "Other" <<<"$out" && fail "notes include the next section"

if CHANGELOG_FILE="$dir/changelog.md" SKIP_BUILD=true VERSION=0.0.1 ./scripts/release.sh >/dev/null 2>&1; then
	fail "a version without a changelog section passed"
fi
if CHANGELOG_FILE="$dir/changelog.md" SKIP_BUILD=true VERSION=bad ./scripts/release.sh >/dev/null 2>&1; then
	fail "a malformed version passed"
fi
echo "release_test: passed"
