# CLAUDE.md

Guidance for working in this repository. The release section is the part
that has already gone wrong once; read it before touching a tag.

## Layout

Four Go modules in one repository, joined by `go.work`:

| Path                  | Module path                  | Published |
| --------------------- | ---------------------------- | --------- |
| `.`                   | `…/openresponses`            | yes       |
| `providers/anthropic` | `…/providers/anthropic`      | yes       |
| `providers/gemini`    | `…/providers/gemini`         | yes       |
| `conformance`         | `…/conformance`              | no        |

The root package depends on the standard library alone; `make deps`
enforces it. Provider SDKs stay in the provider modules so they never
enter the root's dependency graph.

## Everyday commands

```sh
make check          # fmt, tidiness, vet, deps, no-replace, staticcheck, govulncheck, race tests
make release-check  # providers built the way consumers build them (GOWORK=off)
make compliance     # official suite, needs bun
```

A bare `go test ./...` does not cross module boundaries even in
workspace mode. Use the Makefile targets.

The workflows enumerate targets one per step rather than running `make
check`, so a target added to `check` needs a step in
`.github/workflows/ci.yml` or it never runs in CI.

## Releases

### What is permanent

Pushing a tag publishes that version. Within minutes `proxy.golang.org`
has the zip and `sum.golang.org` has its hash, **both forever**. There
is no unpublish. Deleting the git tag does not remove the version; it
only makes it unverifiable, which is strictly worse. A bad version can
only be superseded and `retract`ed.

So the expensive mistakes all happen at `git push origin <tag>`. Every
guard below exists to run before that line.

### The versioning rule

All published modules share one version line. **A provider version must
never sort below the newest root version.**

Consumers resolve the highest provider version available, and that
provider's `go.mod` then pins the root. A provider numbered below the
root therefore drags every consumer's root module *backwards*, silently,
through minimal version selection.

This is exactly what happened on 2026-09-21: `providers/*/v0.0.1` was
tagged while root was at v0.0.10, from a commit that still required root
v0.0.9. Anyone resolving it got downgraded from v0.0.10 to v0.0.9.
Downstream read it as a poisoned proxy cache. It was not — proxy content
was byte-identical to git throughout — it was a mis-numbered tag.

### Before any tag

```sh
make release-guard TAG=providers/anthropic/v0.0.11
```

It refuses to proceed unless the tree is clean, the tag is new, the
version sorts at or above the current root release, the root version the
provider requires is actually published, and the provider builds, vets
and tests with `GOWORK=off`. Run it for root tags too.

### Releasing the root only

```sh
make check
make release-guard TAG=v0.1.0
git tag -a v0.1.0 -m "v0.1.0: one line per user-visible change"
git push origin v0.1.0
```

The tag message becomes the GitHub release notes, so write it as notes.

### Releasing providers only

Providers may ship on their own when the root has not moved. They keep
requiring the already-published root.

```sh
make release-guard TAG=providers/anthropic/v0.0.11
make release-guard TAG=providers/gemini/v0.0.11
git tag -a providers/anthropic/v0.0.11 -m "…"
git tag -a providers/gemini/v0.0.11    -m "…"
git push origin --atomic \
  providers/anthropic/v0.0.11 providers/gemini/v0.0.11
```

### Releasing the root and providers together

Do **not** push the root tag, then bump the providers, then push theirs.
That is what opened the ten-minute window in which the only resolvable
provider version pointed at the wrong root.

Tag everything from one commit and push in one transaction:

```sh
# one commit: root changes, provider `require` bumped to the version
# about to be tagged, changelogs
make release-guard TAG=v0.1.0
git tag -a v0.1.0                      -m "…"
git tag -a providers/anthropic/v0.1.0  -m "…"
git tag -a providers/gemini/v0.1.0     -m "…"
git push origin --atomic v0.1.0 providers/anthropic/v0.1.0 providers/gemini/v0.1.0
```

`--atomic` lands all refs in a single transaction, so no window exists
in which one is visible without the others.

**The providers keep requiring the last published root.** They are not
bumped to the root version being tagged in the same push. Sharing a
version number does not mean requiring it: a consumer on root v0.1.0 and
`providers/anthropic` v0.1.0 whose `go.mod` requires root v0.0.11 still
resolves root v0.1.0, because minimal version selection takes the
maximum. Nobody is downgraded, which is the only thing the rule above
protects.

Forward-referencing the version being tagged — writing `require …
openresponses v0.1.0` in the same commit that becomes v0.1.0 — is legal
for the proxy and is how OpenTelemetry releases, but it does not work
here: `make tidy-check` resolves that requirement from the proxy, so the
release commit cannot pass CI. It is also invisible to `release-check`,
the gate that proves a provider builds against what it claims.

When a provider genuinely needs code that only exists in the root
version being released, that is two releases, in order:

1. Tag and push the root.
2. Bump the provider's `require` to it, let CI go green, then tag and
   push the provider.

`release-guard` enforces the ordering: it refuses a provider tag whose
required root is not yet on the proxy. Accept the short window in that
case — a consumer inside it gets "no matching version" for the provider,
which is a clean failure, not a silent downgrade.

### When it has already gone wrong

Retract; never delete. In the affected module's `go.mod`:

```
// Why this version is bad, and what to use instead.
retract v0.0.1
```

Then release a higher version carrying that `retract`. Go reads
retractions from the highest available version, so the fix only takes
effect once the new version is published. `providers/*` currently
retract v0.0.1 this way.

## Why `go.work` hides release bugs

Inside the workspace, providers build against the root *checkout*, not
against the root version their `go.mod` names. Everything passes locally
while being unbuildable for consumers. `GOWORK=off` is the consumer's
view, which is why `make release-check` sets it, CI runs it on every
push, and `release-guard` runs it before a tag.

Never "fix" a provider build by editing `go.work`, and never by adding a
`replace` of a first-party module. A `replace` belongs to the main module
and consumers ignore it, so a provider carrying one builds green
everywhere here — `release-check` included, which defeats the one gate
that speaks for consumers — while shipping a `go.mod` naming a root
version it was never built against. `make no-replace`, part of `check`,
refuses one; `conformance` is the single exemption, and it is never
published.
