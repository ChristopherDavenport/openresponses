# Contributing

Issues and pull requests are welcome.

## Before you start

The library tracks the Open Responses specification, version
2026-04-24. Wire shapes follow the specification, not any one provider.
Anything a provider adds beyond it goes through the extension types
and never becomes a first-class field. Adapters for particular model
APIs live under `providers/`, each as its own module; the root package
never imports one and never learns a provider's shapes. The design and
the mapping each adapter follows are in `providers/PLAN.md`.

For anything larger than a bug fix, open an issue first so the shape of
the change can be discussed before you spend time on it.

## Development

Go 1.25 or later is required. The full local check is:

```sh
make check        # gofmt, tidiness, vet, deps, replaces, staticcheck, govulncheck, race tests
```

The individual targets are `fmt`, `tidy-check`, `vet`, `deps`,
`replaces`, `lint`, `vuln`, `test` and `tidy`. `lint` and `vuln` run
staticcheck and govulncheck through `go run`, which may download a
newer Go toolchain the first time. `check` is everything CI runs except
`build`, which `vet` and `test` already cover, and `compliance`, which
needs bun and clones a repository; run that one separately before a
release. The workflows enumerate the targets one per step rather than
running `make check`, so a target added to `check` needs a step in
`.github/workflows/ci.yml` too.

The repository has several modules, joined by `go.work`. The library
is at the root. The `conformance` module validates every marshalled
shape against the OpenAPI document in `testdata/` and is nested so its
JSON Schema dependency stays out of the library's dependency graph.
Each directory under `providers/` is a published adapter module for
one model API; its `go.mod` requires the root at the version the whole
repository is released at and replaces it with the tree, so one pull
request can change the root and the adapters it affects. The Makefile
targets cover every module; a bare `go test ./...` at the root does
not cross module boundaries, even in workspace mode. Workspace mode
rejects `-mod=mod`, so a `GOFLAGS=-mod=mod` in your environment has to
go.

Every provider carries a `replace` of the root pointing at the tree, and
`make replaces`, part of `check`, refuses a first-party require that
lacks one. This is load-bearing rather than cosmetic: every module is
released at one version and requires the root at exactly that version,
which the proxy cannot serve until the tag is pushed. `go mod tidy`
ignores `go.work`, so the `replace` is what lets the release commit
resolve, tidy and build. Lose one and the next release fails at `make
tidy`, or silently pins that provider to the previous release.

Consumers ignore a `replace` in a dependency and get the `require`,
which names the commit the provider was tagged from — `release-guard`
proves that correspondence before any tag. `conformance` is unpublished
and has always carried a replace; it is no longer an exemption, just a
module like the others, and its root requirement of `v0.0.0` means
nothing, which is the point.

The upshot: a consumer who takes only `providers/anthropic` at vX.Y.Z
gets root vX.Y.Z, the exact commit it was built and tested against. The
workspace build and the consumer build are the same code, so there is no
`release-check` — it existed to catch drift that can no longer occur.

The official compliance suite needs [bun](https://bun.sh):

```sh
make compliance
```

It fetches the commit of `openresponses/openresponses` pinned by
`COMPLIANCE_REF` in the Makefile into `.cache/`, serves the `echo`
adapter on port 8000 and runs the suite against it. CI runs the same
pinned commit. A weekly workflow runs the suite from upstream `main` and
diffs the published OpenAPI document against `testdata/`, so drift shows
up as a scheduled failure rather than a broken pull request. To move the
pin, update `COMPLIANCE_REF`, run `make spec-update` to refresh the
document, and fix whatever the conformance tests report.

## Pull requests

- Keep the change focused; unrelated cleanups belong in their own PR.
- Add or update tests. Wire-shape changes usually need a golden fixture
  under `testdata/golden/` and a conformance case.
- Run `make check` before pushing. CI runs the same steps on the minimum
  and current Go versions, plus the compliance suite.
- Note user-visible changes under *Unreleased* in `CHANGELOG.md`.

## Releases

`CLAUDE.md` holds the full procedure and the reasoning, including what to
do when a tag goes out wrong. The essentials:

Every published module is released at one version, from one commit, and
each provider requires the root at exactly that version. With the root
changelog's *Unreleased* section written — and each provider's, if it
changed:

```sh
make release VERSION=v0.1.0
```

points every provider's root require at `v0.1.0`, dates every changelog
that has an *Unreleased* section, runs `make tidy` and `make check`,
reads the requires back to confirm tidy did not move them, commits, then
guards and tags the root, guards and tags each provider, and pushes the
branch and every tag with `git push origin --atomic`.

`--atomic` is the point of the single push. Pushing in stages leaves a
window in which the only resolvable provider version points at the
previous root — that is what mis-numbered `providers/*/v0.0.1`.

Each provider's `go.mod` carries a `replace` of the root pointing at the
tree, and `make replaces` (part of `check`) refuses a first-party require
that lacks one. Requiring the version being released means the release
commit names a version the proxy cannot serve until its tag is pushed,
and `go mod tidy` ignores `go.work`; the `replace` is what lets tidy,
build and test resolve it locally. Consumers ignore a `replace` in a
dependency and get the `require`, which names the commit the provider was
tagged from. This is the same shape OpenTelemetry-Go publishes.

`make release-guard TAG=<tag>` is what stands between a mistake and a
permanent one. It refuses a dirty tree, a tag that already exists, a
version that sorts below the current root release or does not move its
module forward, a first-party require that does not name that version, a
root tag that is not this commit, and a module that will not build with
`GOWORK=off`. `make release` runs it for every tag it writes, and the
root is guarded and tagged first because a provider's guard needs the
root tag to exist.

Nothing is public until the push. If a guard refuses, `git reset --hard
HEAD~1` and `git tag -d` whatever was written.

The release workflow publishes a GitHub release per tag, and the Go
module proxy picks the versions up. Before v1.0.0 the API may change
between minor versions; the changelog records every break. A pushed
version is permanent — the proxy and the checksum database keep it
forever — so a bad one is superseded and `retract`ed, never deleted.
