# Common tasks for mini-git. Run `make help` to list them.
#
# CI runs the same targets, so `make check` on your machine tells you whether
# a pull request will pass.

GO        ?= go
BINARY    := mgit
# The version stamped into the binary: the git tag (v0.1.0) on a release
# commit, otherwise a description such as v0.1.0-3-gabc1234-dirty.
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)
COVERDIR  := coverage
# The lowest total coverage (in percent) that `make cover` accepts.
COVER_MIN ?= 80
DIST      := dist
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.DEFAULT_GOAL := help
.PHONY: help build install test race cover lint fmt fmt-check vet vuln tidy tidy-check check hooks dist clean

help: ## List the targets
	@echo "usage: make <target>"
	@echo
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-11s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build ./mgit
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/mgit

install: ## Install mgit into $(go env GOPATH)/bin
	$(GO) install -trimpath -ldflags "$(LDFLAGS)" ./cmd/mgit

test: ## Run the unit tests and the end-to-end tests against git
	$(GO) test -count=1 ./...

race: ## Run the tests with the race detector
	$(GO) test -race -count=1 ./...

# Coverage counts the unit tests and the end-to-end tests together: the
# end-to-end tests run an mgit binary built with -cover, which writes what it
# ran into $GOCOVERDIR (see e2e/helpers_test.go).
cover: ## Measure test coverage and check the minimum
	rm -rf $(COVERDIR)
	mkdir -p $(COVERDIR)/unit $(COVERDIR)/e2e
	$(GO) test -count=1 -cover -coverpkg=./... ./cmd/... ./internal/... -args -test.gocoverdir=$(CURDIR)/$(COVERDIR)/unit
	GOCOVERDIR=$(CURDIR)/$(COVERDIR)/e2e $(GO) test -count=1 ./e2e/
	$(GO) tool covdata percent -i=$(COVERDIR)/unit,$(COVERDIR)/e2e
	$(GO) tool covdata textfmt -i=$(COVERDIR)/unit,$(COVERDIR)/e2e -o $(COVERDIR)/coverage.out
	@$(GO) tool cover -func=$(COVERDIR)/coverage.out | awk -v min=$(COVER_MIN) '/^total:/ { \
		sub("%", "", $$3); printf "total coverage: %s%% (minimum %s%%)\n", $$3, min; \
		if ($$3 + 0 < min) { print "coverage is below the minimum"; exit 1 } }'

lint: ## Run golangci-lint (settings in .golangci.yml)
	golangci-lint run ./...

fmt: ## Format the code
	gofmt -w .

fmt-check: ## Fail if any Go file is not formatted
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then \
		echo "These files need formatting (run make fmt):"; echo "$$unformatted"; exit 1; fi

vet: ## Run go vet
	$(GO) vet ./...

vuln: ## Look for known vulnerabilities with govulncheck
	govulncheck ./...

tidy: ## Run go mod tidy
	$(GO) mod tidy

tidy-check: ## Fail if go.mod or go.sum is not tidy
	$(GO) mod tidy -diff

check: fmt-check vet lint race cover vuln tidy-check ## Run everything CI runs

hooks: ## Install the pre-commit hooks
	pre-commit install

dist: ## Cross-compile release binaries and checksums into dist/
	rm -rf $(DIST)
	mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; if [ "$$os" = windows ]; then ext=.exe; fi; \
		out=$(DIST)/$(BINARY)-$(VERSION)-$$os-$$arch$$ext; echo "  $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/mgit \
			|| exit 1; \
	done
	cd $(DIST) && if command -v sha256sum >/dev/null; then sha256sum $(BINARY)-*; \
		else shasum -a 256 $(BINARY)-*; fi > SHA256SUMS

clean: ## Remove build, coverage and release output
	rm -rf $(BINARY) $(BINARY).exe $(DIST) $(COVERDIR)
