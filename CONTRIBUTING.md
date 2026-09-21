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
make check        # gofmt, vet, staticcheck, govulncheck, race tests, both modules
```

The individual targets are `fmt`, `vet`, `lint`, `vuln`, `test` and
`tidy`. `lint` and `vuln` run staticcheck and govulncheck through
`go run`, which may download a newer Go toolchain the first time.

The repository has several modules, joined by `go.work`. The library
is at the root. The `conformance` module validates every marshalled
shape against the OpenAPI document in `testdata/` and is nested so its
JSON Schema dependency stays out of the library's dependency graph.
Each directory under `providers/` is a published adapter module for
one model API; its `go.mod` requires a released root version, and the
workspace builds it against the local root instead, so one pull
request can change the root and the adapters it affects. The Makefile
targets cover every module; a bare `go test ./...` at the root does
not cross module boundaries, even in workspace mode. Workspace mode
rejects `-mod=mod`, so a `GOFLAGS=-mod=mod` in your environment has to
go.

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

Releases are annotated tags. The tag message becomes the GitHub release
notes, so write it as one:

```sh
git tag -a v0.1.0 -m "v0.1.0: one line per user-visible change"
git push origin v0.1.0
```

The release workflow publishes the GitHub release, and the Go module
proxy picks the version up from the tag. Before v1.0.0 the API may
change between minor versions; the changelog records every break.

Provider modules are tagged `providers/<name>/vX.Y.Z` and versioned
independently of the root. Before tagging one, make sure its `go.mod`
requires a root version that carries everything it uses, then run
`make release-check`, which builds each provider outside the workspace
the way consumers do; the release workflow runs the same check for
provider tags. A root change that breaks a provider ships in order:
tag the root, bump the provider's `require`, `make release-check`, tag
the provider. Each provider keeps its own `CHANGELOG.md`.
