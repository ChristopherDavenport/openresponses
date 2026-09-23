GO ?= go
BUN ?= $(HOME)/.bun/bin/bun
COMPLIANCE_DIR ?= .cache/openresponses
COMPLIANCE_PORT ?= 8000
COMPLIANCE_REPO ?= https://github.com/openresponses/openresponses.git
# Commit of openresponses/openresponses the compliance run is pinned to, so
# an upstream change cannot break CI on its own. Override with
# COMPLIANCE_REF=main to track upstream (the upstream-drift workflow does).
COMPLIANCE_REF ?= 92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c
STATICCHECK ?= $(GO) run honnef.co/go/tools/cmd/staticcheck@latest
GOVULNCHECK ?= $(GO) run golang.org/x/vuln/cmd/govulncheck@latest
# The module path of the root, which every first-party require and
# replace is written against. go list -m reports every module in the
# workspace, so the root has to be asked for outside it.
MODULE := $(shell GOWORK=off $(GO) list -m)

# Provider adapters: published nested modules under providers/, one per
# model API. Each requires the root at exactly the version the whole
# repository is released at, and replaces it with the tree — see
# replaces below, and CLAUDE.md for why the two go together.
PROVIDERS = providers/anthropic providers/gemini
# Nested modules that are tested alongside the library but keep their own
# dependencies out of it. conformance is never published; its root
# requirement is v0.0.0 and means nothing, which is the point.
SUBMODULES = conformance $(PROVIDERS)

.PHONY: build deps replaces test vet fmt tidy tidy-check lint vuln check \
	release-guard release release-commit compliance spec-update clean

build:
	$(GO) build ./...
	@for m in $(SUBMODULES); do (cd $$m && $(GO) build ./...) || exit 1; done

# The root package is the shared vocabulary and must build from the
# standard library alone; the WebSocket library belongs to ./websocket.
deps:
	@deps=$$($(GO) list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' . | grep -v '^github.com/ChristopherDavenport/openresponses' || true); \
	  test -z "$$deps" || { echo "root package depends on: $$deps"; exit 1; }

# Every first-party module a nested module requires must also be
# replaced, at a path that exists. This is the inverse of the rule this
# repository used to carry, and it is load-bearing rather than cosmetic.
#
# Every published module is released at one version, from one commit,
# and requires its siblings at exactly that version — a version the proxy
# cannot serve until the tag is pushed. go mod tidy ignores go.work, so
# the replace is what lets the release commit resolve, tidy and build.
# Lose one and the next release fails at make tidy, or silently pins that
# module to the previous release.
#
# A replace is a property of the main module, so consumers ignore it and
# get the require. That is safe here only because the require names the
# commit the module is tagged from; release-guard is what proves it.
replaces:
	@scripts/check-replaces.sh $(SUBMODULES)

test:
	$(GO) test -race ./...
	@for m in $(SUBMODULES); do (cd $$m && $(GO) test -race ./...) || exit 1; done

vet:
	$(GO) vet ./...
	@for m in $(SUBMODULES); do (cd $$m && $(GO) vet ./...) || exit 1; done

tidy:
	$(GO) mod tidy
	@for m in $(SUBMODULES); do (cd $$m && $(GO) mod tidy) || exit 1; done

# Fails when go mod tidy would change any go.mod or go.sum, without
# writing, so a stray dependency shows up in make check and not only in
# CI's diff.
tidy-check:
	$(GO) mod tidy -diff
	@for m in $(SUBMODULES); do (cd $$m && $(GO) mod tidy -diff) || exit 1; done

fmt:
	gofmt -l . && test -z "$$(gofmt -l .)"

lint:
	$(STATICCHECK) ./...
	@for m in $(SUBMODULES); do (cd $$m && $(STATICCHECK) ./...) || exit 1; done

vuln:
	$(GOVULNCHECK) ./...
	@for m in $(SUBMODULES); do (cd $$m && $(GOVULNCHECK) ./...) || exit 1; done

# Everything CI runs except build, which vet and test already cover, and
# compliance, which needs bun and clones a repository. Note that the
# workflows enumerate these targets one per step rather than running
# make check, so a target added here needs a step in ci.yml or it never
# runs in CI.
check: fmt tidy-check vet deps replaces lint vuln test

# Checks one tag is safe to push, before it is pushed. A pushed tag is
# permanent — the proxy and the checksum database keep the version
# forever — so this is the last point at which a mistake is free:
#   make release-guard TAG=providers/anthropic/v0.0.12
release-guard:
	@test -n "$(TAG)" || { echo "usage: make release-guard TAG=<tag>"; exit 1; }
	@scripts/release-guard.sh "$(TAG)"

# Every tag a release writes: the root and one per provider, all at the
# same version, all from the one commit below. conformance is not here;
# it is never published.
RELEASE_TAGS = $(VERSION) $(patsubst %,%/$(VERSION),$(PROVIDERS))

# Cut a release:
#
#   make release VERSION=v0.1.0
#
# Every published module is released at one version, from one commit, and
# requires the root at exactly that version. So the first thing this does
# is point every provider at VERSION — a version that does not exist yet.
# That resolves because each provider replaces the root with the tree
# (see replaces above); tidy, build and test all see the code being
# tagged, which is the code the version will contain.
#
# The consequence worth naming: a consumer who takes only
# providers/anthropic at vX.Y.Z gets root vX.Y.Z, the exact commit that
# provider was built and tested against. There is no drift to gate
# against, which is why there is no release-check here.
#
# --atomic lands every ref in one transaction, so no window exists in
# which one tag is visible without the others, and none in which a
# published go.mod names a version the proxy cannot serve. Staging the
# pushes is what opened the window that mis-numbered providers/*/v0.0.1.
#
# The root is guarded and tagged first, then each provider, because a
# provider's guard proves the root tag of that version names this commit
# — which it cannot do before that tag exists. Every tag is local until
# the push on the last line; if a guard refuses, undo with
# git reset --hard HEAD~1 and git tag -d the tags written.
release: release-commit
	@scripts/release-guard.sh "$(VERSION)"
	@notes="$$(scripts/release-notes.sh $(VERSION))" || exit 1; \
	 git tag -a $(VERSION) -m "$$notes"
	@set -e; for m in $(PROVIDERS); do \
	  scripts/release-guard.sh "$$m/$(VERSION)"; \
	  notes="$$(scripts/release-notes.sh $(VERSION) $$m)"; \
	  git tag -a $$m/$(VERSION) -m "$$notes"; \
	done
	git push origin --atomic HEAD $(RELEASE_TAGS)

# Bump every first-party requirement to VERSION, date every changelog
# that has an Unreleased section, check everything, commit. Nothing here
# is pushed, so a failure costs a git reset and no more. TRAILER, when
# set, is appended to the commit message.
#
# The root changelog must have an Unreleased section; a provider that did
# not change this release need not, and its tag then carries the root's
# notes. Every module is tagged either way, because every module shares
# the version.
#
# go mod tidy is free to move a requirement the bump just set, so what
# landed is read back and asserted before anything is committed.
#
# The changelogs are dated through a temp file rather than sed -i, which
# is a GNU-ism: BSD sed reads the argument after -i as a backup suffix,
# so the GNU spelling fails outright on macOS, where these releases are
# cut.
release-commit:
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=vX.Y.Z"; exit 1; }
	@test "$(origin PROVIDERS)" = file || { echo "do not override PROVIDERS here: a command-line override propagates into the bump and check below, so a module would be tagged having checked a subset."; exit 1; }
	@grep -q '^## Unreleased$$' CHANGELOG.md || { echo "CHANGELOG.md has no Unreleased section"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree is not clean"; exit 1; }
	@scripts/versions.sh set $(VERSION) $(PROVIDERS)
	@set -e; for c in CHANGELOG.md $(patsubst %,%/CHANGELOG.md,$(PROVIDERS)); do \
	  grep -q '^## Unreleased$$' $$c || continue; \
	  sed 's/^## Unreleased$$/## $(VERSION) - '"$$(date +%F)"'/' $$c > $$c.tmp \
	    && mv $$c.tmp $$c || { rm -f $$c.tmp; exit 1; }; \
	done
	$(MAKE) tidy
	$(MAKE) check
	@scripts/versions.sh check $(VERSION) $(PROVIDERS)
	git add -A && git commit -q -m "Release $(VERSION)" $(if $(TRAILER),-m "$(TRAILER)")

# Runs the official compliance suite from openresponses/openresponses at
# COMPLIANCE_REF against the echo adapter. Requires bun.
compliance: build
	@test -d $(COMPLIANCE_DIR)/.git || (git init -q $(COMPLIANCE_DIR) && git -C $(COMPLIANCE_DIR) remote add origin $(COMPLIANCE_REPO))
	@git -C $(COMPLIANCE_DIR) fetch -q --depth 1 origin $(COMPLIANCE_REF) && git -C $(COMPLIANCE_DIR) checkout -q --detach FETCH_HEAD
	@cd $(COMPLIANCE_DIR) && $(BUN) install --ignore-scripts --silent
	$(GO) build -o .cache/openresponses-echo ./cmd/openresponses-echo
	@.cache/openresponses-echo -addr :$(COMPLIANCE_PORT) & echo $$! > .cache/echo.pid; \
	  sleep 1; \
	  cd $(COMPLIANCE_DIR) && $(BUN) run bin/compliance-test.ts --base-url http://localhost:$(COMPLIANCE_PORT)/v1 --api-key test; \
	  status=$$?; kill $$(cat ../echo.pid); exit $$status

# Fetches the latest OpenAPI document so the conformance tests surface
# schema drift.
spec-update:
	curl -fsSL https://raw.githubusercontent.com/openresponses/openresponses/main/public/openapi/2026-04-24/openapi.json -o testdata/openapi-2026-04-24.json
	cd conformance && $(GO) test ./...

clean:
	rm -rf .cache
