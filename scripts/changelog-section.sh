#!/usr/bin/env bash
# Prints the body of the `## <version>` section of CHANGELOG.md (the release
# notes), without its heading. Fails when there is no such section or it is
# empty. CHANGELOG_FILE overrides the file (tests).
#
#   scripts/changelog-section.sh 0.1.0
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

version="${1:?usage: changelog-section.sh <version>}"
version="${version#v}"
notes=$(awk -v heading="## $version" '
	$0 == heading || index($0, heading " ") == 1 { found = 1; next }
	found && /^## / { exit }
	found { print }
' "${CHANGELOG_FILE:-CHANGELOG.md}")
if [[ -z "${notes//[[:space:]]/}" ]]; then
	echo "changelog-section: CHANGELOG.md has no '## $version' section, or it is empty" >&2
	exit 1
fi
printf '%s\n' "$notes"
