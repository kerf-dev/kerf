# Command-line tool (`kerf`). Included by the root Makefile. Guide: docs/cli.md.
#
#   make build-cli                       build bin/kerf for this machine
#   make test-cli                        tests of the command and of the report package
#   make release-cli VERSION=0.3.0       build the four release binaries into bin/release/cli (nothing is pushed
#                                        or signed here; the release workflow signs them with cosign)
#
# The release binaries are named kerf_<version>_<os>_<arch>, for linux and darwin on amd64 and arm64, with the
# version set at build time (`kerf version`), and listed in kerf_SHA256SUMS.

CLI_DIR       ?= $(LOCALBIN)/release/cli
CLI_PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
CLI_LDFLAGS    = -s -w -X main.version=$(VERSION)
CLI_SHA256    ?= $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

##@ Command-line tool

.PHONY: build-cli
build-cli: ## Build the kerf command-line tool into bin/kerf.
	$(GO) build $(GOFLAGS) -o $(LOCALBIN)/kerf ./cmd/kerf

.PHONY: test-cli
test-cli: ## Tests of the kerf command and the report package.
	$(GO) test $(GOFLAGS) -cover ./cmd/kerf/... ./internal/report/...

.PHONY: release-cli
release-cli: ## Build the kerf binaries for linux and darwin (amd64, arm64) and their checksums into bin/release/cli.
	@mkdir -p $(CLI_DIR)
	@set -e; for p in $(CLI_PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; out=$(CLI_DIR)/kerf_$(VERSION)_$${os}_$${arch}; \
	  echo "building $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build $(GOFLAGS) -trimpath -ldflags '$(CLI_LDFLAGS)' -o $$out ./cmd/kerf; \
	done
	@cd $(CLI_DIR) && $(CLI_SHA256) kerf_$(VERSION)_* > kerf_SHA256SUMS && cat kerf_SHA256SUMS
