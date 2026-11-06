# Shared variables and repo-wide targets.
#
# Component makefiles (make/<component>.mk) may:
#   - add their own targets, prefixed with the component name (e.g. build-hub, docker-hub);
#   - hook into the aggregate targets below by adding prerequisites, e.g. `generate: generate-hub`.
# They must not redefine recipes of the targets in this file.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

ROOT        := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)
LOCALBIN    ?= $(ROOT)/bin
GO          ?= go
GOFLAGS     ?=
export PATH := $(LOCALBIN):$(PATH)

MODULE := github.com/kerf-dev/kerf

# Pinned tool versions.
CONTROLLER_GEN_VERSION ?= v0.23.0
GOLANGCI_LINT_VERSION  ?= v2.15.0
REDOCLY_CLI_VERSION    ?= 1.34.23

CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_GEN_VERSION)
GOLANGCI_LINT  ?= $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)

##@ General

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n"} \
	  /^[a-zA-Z0-9_.-]+:.*?##/ { printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2 } \
	  /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: build
build: ## Build every Go package.
	$(GO) build $(GOFLAGS) ./...

.PHONY: test
test: ## Run unit tests.
	$(GO) test $(GOFLAGS) ./...

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet $(GOFLAGS) ./...

.PHONY: fmt
fmt: ## Run gofmt on all Go files.
	gofmt -w $$(git ls-files '*.go')

.PHONY: lint
lint: golangci-lint ## Run golangci-lint.
	$(GOLANGCI_LINT) run ./...

.PHONY: generate
generate: controller-gen ## Generate deepcopy code and CRD manifests (api/crds).
	$(CONTROLLER_GEN) object paths=./api/...
	rm -f api/crds/*.yaml
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=api/crds

.PHONY: verify-generate
verify-generate: generate ## Fail if generated files are out of date.
	@git diff --exit-code -- api/ || { echo "generated files are stale: run 'make generate' and commit"; exit 1; }
	@test -z "$$(git ls-files --others --exclude-standard -- api/)" || { echo "untracked generated files:"; git ls-files --others --exclude-standard -- api/; exit 1; }

.PHONY: lint-openapi
lint-openapi: ## Lint the hub OpenAPI contract.
	npx --yes @redocly/cli@$(REDOCLY_CLI_VERSION) lint api/openapi/hub.yaml

.PHONY: ci
ci: build vet lint test verify-generate lint-openapi ## Everything CI runs.

##@ Tools

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Install controller-gen into ./bin.
$(CONTROLLER_GEN): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
	mv $(LOCALBIN)/controller-gen $@

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Install golangci-lint into ./bin.
$(GOLANGCI_LINT): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(LOCALBIN)/golangci-lint $@
