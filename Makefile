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
PROVIDERS =
# Nested modules that are tested alongside the library but keep their own
# dependencies out of it.
SUBMODULES = conformance $(PROVIDERS)

.PHONY: build deps test vet fmt tidy lint vuln check release-check compliance spec-update clean

build:
	$(GO) build ./...
	@for m in $(SUBMODULES); do (cd $$m && $(GO) build ./...) || exit 1; done

# The root package is the shared vocabulary and must build from the
# standard library alone; the WebSocket library belongs to ./websocket.
deps:
	@deps=$$($(GO) list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' . | grep -v '^github.com/ChristopherDavenport/openresponses' || true); \
	  test -z "$$deps" || { echo "root package depends on: $$deps"; exit 1; }

test:
	$(GO) test -race ./...
	@for m in $(SUBMODULES); do (cd $$m && $(GO) test -race ./...) || exit 1; done

vet:
	$(GO) vet ./...
	@for m in $(SUBMODULES); do (cd $$m && $(GO) vet ./...) || exit 1; done

tidy:
	$(GO) mod tidy
	@for m in $(SUBMODULES); do (cd $$m && $(GO) mod tidy) || exit 1; done

fmt:
	gofmt -l . && test -z "$$(gofmt -l .)"

lint:
	$(STATICCHECK) ./...
	@for m in $(SUBMODULES); do (cd $$m && $(STATICCHECK) ./...) || exit 1; done

vuln:
	$(GOVULNCHECK) ./...
	@for m in $(SUBMODULES); do (cd $$m && $(GOVULNCHECK) ./...) || exit 1; done

# Everything CI runs, minus the compliance suite.
check: fmt vet deps lint vuln test

# Builds and tests each provider module outside the workspace, against the
# root version its go.mod requires, which is what consumers get. Run it
# before tagging a provider; it fails while a provider depends on root
# changes that are not tagged yet.
release-check:
	@for m in $(PROVIDERS); do (cd $$m && GOWORK=off $(GO) vet ./... && GOWORK=off $(GO) test ./...) || exit 1; done

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
