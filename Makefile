GO ?= go
BUN ?= $(HOME)/.bun/bin/bun
COMPLIANCE_DIR ?= .cache/openresponses
COMPLIANCE_PORT ?= 8000

.PHONY: build test vet fmt compliance spec-update clean

build:
	$(GO) build ./...

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l . && test -z "$$(gofmt -l .)"

# Runs the official compliance suite from openresponses/openresponses
# against the echo adapter. Requires bun.
compliance: build
	@test -d $(COMPLIANCE_DIR) || git clone --depth 1 https://github.com/openresponses/openresponses.git $(COMPLIANCE_DIR)
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
	$(GO) test ./...

clean:
	rm -rf .cache
