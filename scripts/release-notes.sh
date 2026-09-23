#!/usr/bin/env bash
# Prints the CHANGELOG section for a version, with the heading replaced
# by the bare version, for use as an annotated tag message. The release
# workflow publishes that message as the GitHub release notes.
#
# Each published module keeps its own CHANGELOG.md, so the module
# directory selects which one is read; omit it for the root.
#
#   scripts/release-notes.sh v0.1.0
#   scripts/release-notes.sh v0.1.0 providers/anthropic

set -euo pipefail

VERSION="${1:-}"
DIR="${2:-.}"
[ -n "$VERSION" ] || { echo "usage: $0 vX.Y.Z [module-dir]" >&2; exit 1; }

changelog="$DIR/CHANGELOG.md"
[ -f "$changelog" ] || { echo "release-notes: no $changelog" >&2; exit 1; }

section() {
  awk -v v="$VERSION" '/^## / { p = ($2 == v) } p' "$1" | sed "1s/.*/$VERSION/"
}

notes="$(section "$changelog")"

# A module that did not change this release need not carry a section of
# its own; it is still tagged, because every module shares the version.
# Fall back to the root's notes, and say so, so a silently empty tag
# message is never the quiet outcome.
if [ -z "$notes" ] && [ "$DIR" != "." ]; then
  echo "release-notes: $changelog has no $VERSION section; using the root changelog" >&2
  notes="$(section CHANGELOG.md)"
fi

[ -n "$notes" ] \
  || { echo "release-notes: no $VERSION section in $changelog or CHANGELOG.md" >&2; exit 1; }

printf '%s\n' "$notes"
