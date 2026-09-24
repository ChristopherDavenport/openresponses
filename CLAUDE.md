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
make check      # fmt, tidiness, vet, deps, replaces, staticcheck, govulncheck, race tests
make compliance # official suite, needs bun
make release VERSION=vX.Y.Z   # the whole release; see below before running it
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

All published modules are released **at one version, from one commit**,
and each requires the root at **exactly that version**.

Two things follow, and both matter:

- A consumer who takes only `providers/anthropic` at vX.Y.Z gets root
  vX.Y.Z — the exact commit that provider was built and tested against.
  The shared version line is a fact about the dependency graph, not a
  naming convention.
- A provider can never be numbered below the root, so it can never drag
  a consumer's root module backwards through minimal version selection.
  That is what `providers/*/v0.0.1` did on 2026-09-21: tagged while root
  was at v0.0.10, from a commit that still required root v0.0.9, so
  anyone resolving it was downgraded. Downstream read it as a poisoned
  proxy cache. It was not — proxy content was byte-identical to git
  throughout — it was a mis-numbered tag pointing at a mismatched root.
  Under this rule that tag could not have been written.

There is a third consequence, and it is why this repository has fewer
release gates than it used to: the workspace build and the consumer
build are now the same code. `make check` builds each provider against
the root in the tree; a consumer builds it against root vX.Y.Z, which is
that same tree at the tagged commit. Nothing can drift between them, so
nothing needs a gate to catch the drift.

### Why the `replace` directives are load-bearing

Each provider's `go.mod` carries `replace …/openresponses => ../..`, and
`make replaces`, part of `check`, refuses a first-party require that
lacks one. This is the inverse of the rule this repository used to carry,
and it is not cosmetic.

Requiring the version being released means the release commit names a
version that does not exist on the proxy until its tag is pushed.
`go mod tidy` ignores `go.work`, so without the replace, tidy would try
to fetch it and the release commit could not be built, tidied or
committed. The replace is what lets a release name its own version.

A replace belongs to the main module, so consumers ignore it and get the
require. That is safe here *only* because the require names the commit
the module is tagged from — which `release-guard` proves, by checking
that the root tag of that version points at the same commit. Remove that
check and the replace becomes the hazard it is in a repository that
releases modules at independent versions.

This is what OpenTelemetry-Go does, and for the same reason. Its
published `otel/sdk@v1.46.0` requires `otel v1.46.0` and ships
`replace go.opentelemetry.io/otel => ../`; its published `go.sum` has no
first-party entries at all, because tidy never resolves one from the
proxy. Ours look the same.

One real cost: `go install pkg@version` rejects a module carrying
`replace` directives. The providers ship no commands — the binaries are
in the root module, which has no replaces — so it does not bite today.
It is the thing to check before a provider grows one.

`conformance` is unpublished and has always carried a replace; under this
rule it is no longer an exemption, just a module like the others. Its
root requirement is `v0.0.0` and means nothing, which is the point.

### Before any tag

```sh
make release-guard TAG=providers/anthropic/v0.1.0
```

It refuses to proceed unless the tree is clean, the tag is new locally
and on origin, the version sorts at or above the current root release and
moves that module forward, every first-party require names exactly that
version, the root tag of that version is this very commit, and the module
builds with `GOWORK=off`.

A provider tag is only checkable once the root tag exists, so guard and
tag in the order `make release` uses: root first, then each provider.

### The release

Everything from one commit, one transaction. With the root changelog's
*Unreleased* section written — and each provider's, if it changed:

```sh
make release VERSION=v0.1.0
```

That points every provider's root require at v0.1.0, dates every
changelog that has an *Unreleased* section, tidies, runs `make check`,
reads the requires back to confirm tidy did not move them, commits, then
guards and tags the root, guards and tags each provider, and runs:

```sh
git push origin --atomic HEAD v0.1.0 \
  providers/anthropic/v0.1.0 providers/gemini/v0.1.0
```

`--atomic` lands every ref in a single transaction, so no window exists
in which one tag is visible without the others — or in which a published
`go.mod` names a version the proxy cannot serve. Pushing in stages is
what opened the window that mis-numbered `providers/*/v0.0.1`.

A provider that did not change this release still gets a tag, because
every module shares the version; its tag carries the root's notes.

Nothing is public until that push. If a guard refuses, `git reset --hard
HEAD~1` and `git tag -d` whatever was written.

### The ruleset on `main`

Unlike the sibling repositories, `main` here carries a ruleset: pull
request required, `Checks` required, no deletion, no force-push. The
release pushes `HEAD` directly, so the ruleset carries one bypass actor
— repository **admin**, mode **always** — and that bypass is what makes
`make release` work here. Everything that is not a release still goes
through a pull request.

If a release push is ever rejected with `GH013: Repository rule
violations found for refs/heads/main`, the bypass is what to check. Do
**not** re-phase the release so each piece goes through a pull request:
phasing is exactly what v0.0.11 and v0.0.12 removed, and it is what
mis-numbered `providers/*/v0.0.1` in the first place. The ruleset
targets **branches only**, so tag pushes were never affected — v0.0.12
was cut by putting the release commit through a PR and pushing the three
tags afterwards, which works but needs a human in the middle.

Two traps if you edit the ruleset by API. `PUT` replaces it wholesale,
so a request that omits a rule's `parameters` silently drops the
required check name and the allowed merge methods — send the whole rule
or use the UI. And `git push --dry-run` does **not** evaluate rulesets:
it reports success for a push the server will reject, so the only honest
check that a release will land is a real push.

There is no phased release and no `release-check`. Both existed to manage
a provider requiring a *previous* root; that situation no longer arises.

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

## What `go.work` is and is not for

`go.work` joins the modules so editors and `go build ./...` in any
directory see the tree. It is *not* what makes the release work — the
`replace` directives are, because `go mod tidy` ignores the workspace
entirely. If you find yourself reaching for `go.work` to fix a module
resolution problem, check `make replaces` first.

Never "fix" a provider by pointing a replace somewhere outside the
repository, and never by letting a first-party require drift off the
released version. `make replaces` and `scripts/versions.sh check` are the
two gates, and `release-guard` runs the second again before a tag.
