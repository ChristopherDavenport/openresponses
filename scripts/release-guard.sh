#!/usr/bin/env bash
# Checks a tag is safe to push before it is pushed. A pushed tag is
# permanent: the module proxy and the checksum database keep the version
# forever, so a mistake can only be retracted, never withdrawn.
#
#   scripts/release-guard.sh v0.0.11
#   scripts/release-guard.sh providers/anthropic/v0.0.11
#
# Exits non-zero, with the reason, if the tag would publish something a
# consumer cannot build or would resolve against a root version older
# than the one already released.

set -euo pipefail

TAG="${1:-}"
MODULE_ROOT="github.com/ChristopherDavenport/openresponses"
# proxy.golang.org lowercases uppercase letters as !<lower>.
ESCAPED_ROOT="github.com/!christopher!davenport/openresponses"

die() { echo "release-guard: $*" >&2; exit 1; }
ok()  { echo "  ok  $*"; }

[ -n "$TAG" ] || die "usage: $0 <tag>"

# Newest version wins a version sort; -V orders v0.0.9 before v0.0.10.
newest() { sort -V | tail -1; }

published() { # module-escaped version -> 0 if the proxy serves it
  curl -fsS -o /dev/null "https://proxy.golang.org/$1/@v/$2.info" 2>/dev/null
}

echo "release-guard: $TAG"

# --- Repository state -------------------------------------------------
[ -z "$(git status --porcelain)" ] || die "working tree is dirty; commit or stash first"
ok "working tree clean"

git rev-parse -q --verify "refs/tags/$TAG" >/dev/null \
  && die "tag $TAG already exists locally"
[ -z "$(git ls-remote --tags origin "refs/tags/$TAG")" ] \
  || die "tag $TAG already exists on origin"
ok "tag is new"

# --- Root version already released ------------------------------------
ROOT_LATEST="$(git tag -l 'v*' | newest)"
[ -n "$ROOT_LATEST" ] || die "no root tag found; cannot establish the version floor"

case "$TAG" in
  v*)
    # Root tag: must move forward.
    [ "$(printf '%s\n%s\n' "$ROOT_LATEST" "$TAG" | newest)" = "$TAG" ] \
      || die "$TAG does not sort above the current root release $ROOT_LATEST"
    ok "$TAG is newer than $ROOT_LATEST"
    ;;

  providers/*/v*)
    PROVIDER_DIR="${TAG%/v*}"                 # providers/anthropic
    PROVIDER_VERSION="v${TAG##*/v}"           # v0.0.11
    [ -d "$PROVIDER_DIR" ] || die "no such provider directory: $PROVIDER_DIR"

    # The rule the v0.0.1 incident broke: a provider must never publish a
    # version that sorts below the newest root release. Consumers select
    # the highest provider version, and its go.mod then pins the root.
    # A provider below the root silently downgrades everyone.
    [ "$(printf '%s\n%s\n' "$ROOT_LATEST" "$PROVIDER_VERSION" | newest)" = "$PROVIDER_VERSION" ] \
      || die "$PROVIDER_VERSION sorts below the current root release $ROOT_LATEST;
            a provider version under the root silently downgrades consumers
            (this is what providers/*/v0.0.1 did). Tag $ROOT_LATEST or later."
    ok "$PROVIDER_VERSION is at or above the root release $ROOT_LATEST"

    # Whatever root the provider requires has to be fetchable, or the
    # module is unbuildable for everyone but this working copy.
    REQUIRED_ROOT="$(cd "$PROVIDER_DIR" && go mod edit -json \
      | awk -v m="$MODULE_ROOT" '$0 ~ "\""m"\"" {found=1} found && /"Version"/ {gsub(/[",]/,""); print $2; exit}')"
    [ -n "$REQUIRED_ROOT" ] || die "$PROVIDER_DIR/go.mod does not require $MODULE_ROOT"

    published "$ESCAPED_ROOT" "$REQUIRED_ROOT" \
      || die "$PROVIDER_DIR requires $MODULE_ROOT@$REQUIRED_ROOT, which the proxy does not serve.
            Tag and push the root first, or correct the require."
    ok "requires $MODULE_ROOT@$REQUIRED_ROOT, which is published"

    [ "$(printf '%s\n%s\n' "$REQUIRED_ROOT" "$ROOT_LATEST" | newest)" = "$REQUIRED_ROOT" ] \
      || echo "  warn  requires $REQUIRED_ROOT but $ROOT_LATEST is released; intended?"

    # Build it the way a consumer does: outside the workspace, against
    # the published root rather than the checkout next door.
    echo "  ..  building $PROVIDER_DIR outside the workspace"
    (cd "$PROVIDER_DIR" && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./... >/dev/null) \
      || die "$PROVIDER_DIR fails to build or test against $MODULE_ROOT@$REQUIRED_ROOT"
    ok "builds and tests against the published root"
    ;;

  *)
    die "unrecognised tag shape: $TAG (expected vX.Y.Z or providers/<name>/vX.Y.Z)"
    ;;
esac

echo "release-guard: $TAG is safe to push"
