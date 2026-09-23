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
# Provider adapters: published nested modules under providers/, one per
# model API, each requiring a released root version. go.work builds them
# against the local root; release-check builds them the way consumers do.
PROVIDERS = providers/anthropic providers/gemini
# Nested modules that are tested alongside the library but keep their own
# dependencies out of it.
SUBMODULES = conformance $(PROVIDERS)
# The modules whose go.mod may replace a first-party one. Exactly one
# qualifies, and it is named rather than inferred: conformance is not in
# PROVIDERS, so release-check never builds it the way a consumer would,
# and no tag shape the release workflow fires on (v*, providers/*/v*)
# can name it, so it cannot reach the module proxy. Its replace of the
# root at v0.0.0 is how it validates the tree it ships with. Naming the
# module keeps a replace appearing in a provider tomorrow a failure.
NO_REPLACE_EXEMPT = conformance

.PHONY: build deps no-replace test vet fmt tidy tidy-check lint vuln check \
	release-check release-guard compliance spec-update clean

build:
	$(GO) build ./...
	@for m in $(SUBMODULES); do (cd $$m && $(GO) build ./...) || exit 1; done

# The root package is the shared vocabulary and must build from the
# standard library alone; the WebSocket library belongs to ./websocket.
deps:
	@deps=$$($(GO) list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' . | grep -v '^github.com/ChristopherDavenport/openresponses' || true); \
	  test -z "$$deps" || { echo "root package depends on: $$deps"; exit 1; }

# No module in the repository may replace a first-party one, except the
# modules named in NO_REPLACE_EXEMPT. A replace is a property of the main
# module and consumers ignore it, so a provider carrying one builds green
# everywhere here — including under release-check, which is the whole
# point of release-check — while shipping a go.mod that names a root
# version it was never built against. go.work is how the tree is built
# against the tree. This runs in check rather than only at release
# because re-adding a replace is exactly how the hole opens, and one
# make tidy afterwards settles every other gate. It keys on the module
# path rather than on "=> ..", so a replace pointing anywhere is caught
# and a third-party pin is not; the trailing slash keeps a differently
# named org from matching. There is deliberately no opt-out flag: the
# exemption list above is the only one, and widening it is a change to
# this file, reviewable in the diff.
no-replace:
	@for m in $(NO_REPLACE_EXEMPT); do \
	  for p in $(PROVIDERS); do \
	    test "$$m" != "$$p" || { echo "$$m is exempt from no-replace but is in PROVIDERS; a released module may not replace a first-party one"; exit 1; }; \
	  done; \
	done
	@for m in . $(filter-out $(NO_REPLACE_EXEMPT),$(SUBMODULES)); do \
	  if grep -v '^[[:space:]]*//' $$m/go.mod | grep -q 'github.com/ChristopherDavenport/.*=>'; then \
	    echo "$$m/go.mod replaces a first-party module; published modules require released versions and go.work builds them against the tree (see CONTRIBUTING.md)"; \
	    exit 1; \
	  fi; \
	done

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
check: fmt tidy-check vet deps no-replace lint vuln test

# Builds and tests each provider module outside the workspace, against the
# root version its go.mod requires, which is what consumers get. Run it
# before tagging a provider; it fails while a provider depends on root
# changes that are not tagged yet.
release-check:
	@for m in $(PROVIDERS); do (cd $$m && GOWORK=off $(GO) vet ./... && GOWORK=off $(GO) test ./...) || exit 1; done

# Checks one tag is safe to push, before it is pushed. A pushed tag is
# permanent, so this is the last point at which a mistake is free:
#   make release-guard TAG=providers/anthropic/v0.0.11
release-guard:
	@test -n "$(TAG)" || { echo "usage: make release-guard TAG=<tag>"; exit 1; }
	@scripts/release-guard.sh "$(TAG)"

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
