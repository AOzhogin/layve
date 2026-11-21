# layve — build, test, lint and security checks. "make help" lists targets.
#
# golangci-lint and govulncheck are pinned as tools in go.mod and run with
# "go tool", so nothing needs to be installed besides Go (and Docker for the
# integration targets).

GO         ?= go
BIN        ?= bin/layve
PKGS       := ./...
# The tag on the current commit, else the short commit hash, else "dev".
VERSION    ?= $(shell git describe --tags --exact-match 2>/dev/null || \
                git rev-parse --short HEAD 2>/dev/null || echo dev)

# Release builds: static (no cgo), no symbol table or DWARF (-s -w), no local
# paths (-trimpath), no embedded VCS data or build ID, so the same source
# gives the same binary.
BUILDENV   := CGO_ENABLED=0
BUILDFLAGS := -trimpath -buildvcs=false
LDFLAGS    := -s -w -buildid= -X main.version=$(VERSION)
PLATFORMS  ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
REMOTE_DIR ?= /tmp/layve-remote
TEST_IMAGE := nginx:1.27-alpine

LINT := $(GO) tool golangci-lint
VULN := $(GO) tool govulncheck

# GoReleaser is not a go.mod tool: its dependency tree is huge and its
# authors advise against "go tool". "make goreleaser" installs this version
# when it is missing.
GORELEASER_VERSION ?= v2.20.3
GORELEASER ?= $(shell command -v goreleaser 2>/dev/null || echo $(shell $(GO) env GOPATH)/bin/goreleaser)

.DEFAULT_GOAL := help

##@ Build

.PHONY: build
build: ## Build bin/layve
	$(BUILDENV) $(GO) build $(BUILDFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/layve

.PHONY: install
install: ## Install layve into $GOBIN
	$(BUILDENV) $(GO) install $(BUILDFLAGS) -ldflags "$(LDFLAGS)" ./cmd/layve

.PHONY: cross
cross: ## Build static binaries for all release platforms into dist/
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		out=dist/layve-$$os-$$arch$$ext; echo "$$out"; \
		$(BUILDENV) GOOS=$$os GOARCH=$$arch \
			$(GO) build $(BUILDFLAGS) -ldflags "$(LDFLAGS)" -o $$out ./cmd/layve || exit 1; \
	done

##@ Test

.PHONY: test
test: ## Unit tests
	$(GO) test $(PKGS)

.PHONY: race
race: ## Unit tests with the race detector
	$(GO) test -race -count=1 $(PKGS)

.PHONY: cover
cover: ## Unit tests with coverage (coverage.out, coverage.html)
	$(GO) test -coverprofile=coverage.out $(PKGS)
	@$(GO) tool cover -func=coverage.out | tail -1
	$(GO) tool cover -html=coverage.out -o coverage.html

.PHONY: integration
integration: ## Integration tests against the local Docker daemon and Docker Hub
	@docker image inspect $(TEST_IMAGE) >/dev/null 2>&1 || docker pull $(TEST_IMAGE)
	LAYVE_INTEGRATION=1 $(GO) test -count=1 -run 'Integration|Live' \
		./internal/adapter/docker/ ./internal/pkg/dockerhub/

.PHONY: remote
remote: ## Round trip through a throwaway Debian host over ssh (Docker needed)
	@docker image inspect $(TEST_IMAGE) >/dev/null 2>&1 || docker pull $(TEST_IMAGE)
	@eval "$$(test/remote/up.sh $(REMOTE_DIR))" && \
		LAYVE_INTEGRATION=1 LAYVE_REMOTE_CONTEXT=debian \
		$(GO) test -count=1 -run 'Integration(Remote|SSHTunnel)' ./internal/adapter/docker/; \
		status=$$?; \
		[ $$status -eq 0 ] || docker logs layve-remote 2>&1 | tail -50; \
		docker rm -f layve-remote >/dev/null 2>&1; exit $$status

.PHONY: smoke
smoke: build ## CLI end-to-end test against the local Docker daemon
	@docker image inspect $(TEST_IMAGE) >/dev/null 2>&1 || docker pull $(TEST_IMAGE)
	test/cli/smoke.sh $(BIN)

##@ Quality

.PHONY: fmt
fmt: ## Format code (gofmt + goimports)
	$(LINT) fmt

.PHONY: fmt-check
fmt-check: ## Fail if code is not formatted
	@# The exit code tells: 1 with a diff on stdout when files need
	@# formatting. stderr may carry "go: downloading" lines on a cold cache.
	@$(LINT) fmt --diff || { echo "run: make fmt"; exit 1; }

.PHONY: vet
vet: ## go vet
	$(GO) vet $(PKGS)

.PHONY: lint
lint: ## golangci-lint (configured in .golangci.yml)
	$(LINT) run $(PKGS)

# Extra govulncheck flags, e.g. VULN_FLAGS="-db file:///path/to/vulndb" offline.
VULN_FLAGS ?=

.PHONY: vuln
vuln: ## Known vulnerabilities in dependencies and the Go toolchain (govulncheck)
	$(VULN) $(VULN_FLAGS) $(PKGS)

.PHONY: tidy
tidy: ## go mod tidy and verify
	$(GO) mod tidy
	$(GO) mod verify

##@ Release

.PHONY: goreleaser
goreleaser: ## Install GoReleaser (version GORELEASER_VERSION) unless it is on PATH
	@command -v goreleaser >/dev/null 2>&1 || [ -x "$(GORELEASER)" ] || \
		$(GO) install github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)

.PHONY: release-check
release-check: goreleaser ## Validate .goreleaser.yaml
	$(GORELEASER) check

.PHONY: snapshot
snapshot: goreleaser ## Build every archive and package into dist/ without publishing
	$(GORELEASER) release --snapshot --clean --skip=sign,sbom

##@ All together

.PHONY: check
check: fmt-check vet lint race vuln ## Everything that needs no Docker

.PHONY: ci
ci: check integration smoke ## Everything CI runs, except the remote host

.PHONY: clean
clean: ## Remove build and coverage output
	rm -rf bin dist coverage.out coverage.html

.PHONY: version
version: ## Print the version a build would get
	@echo $(VERSION)

.PHONY: help
help: ## Show this help
	@awk 'BEGIN { FS = ":.*## " } \
		/^##@/ { printf "\n%s\n", substr($$0, 5) } \
		/^[a-zA-Z_-]+:.*## / { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
