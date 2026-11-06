# End-to-end suite (test/e2e), with the admin-area scenarios and the test tiers.
# Runs against the existing k3d environment; see test/e2e/README.md.

E2E_TIMEOUT        ?= 40m
E2E_PARALLEL       ?= 32
E2E_RUN            ?=
E2E_FLAGS          ?=
E2E_LOG            ?= $(ROOT)/bin/e2e.log
E2E_JSON           ?= $(ROOT)/bin/e2e.json
# Smoke tier: one scenario per area (audit, in-place apply, webhook, rollback, admin
# policy, readiness), for pull requests.
E2E_SMOKE_TESTS    ?= TestAudit|TestInPlaceCPU|TestWebhook|TestRollback|TestAdminPolicy|TestReadiness|TestMaintenance|TestWebSignIn
E2E_SPOKE1_CONTEXT ?= k3d-ro-spoke-1
E2E_SPOKE2_CONTEXT ?= k3d-ro-spoke-2
export E2E_SPOKE1_CONTEXT E2E_SPOKE2_CONTEXT

E2E_LABEL := e2e.kerf.getkerf.dev/suite=true

##@ End-to-end

.PHONY: e2e
e2e: ## Run the full e2e suite against the running clusters (E2E_RUN=TestAudit to pick scenarios).
	@mkdir -p $(dir $(E2E_LOG))
	@$(MAKE) --no-print-directory e2e-unblock
	@set -o pipefail; rc=0; t0=$$(date +%s); $(GO) build -o $(LOCALBIN)/e2esummary ./test/e2e/e2esummary; \
	$(GO) test -tags e2e -count=1 -timeout $(E2E_TIMEOUT) -parallel $(E2E_PARALLEL) -json ./test/e2e/... \
	  $(if $(E2E_RUN),-run '$(E2E_RUN)') $(E2E_FLAGS) 2>&1 | tee $(E2E_JSON) | $(LOCALBIN)/e2esummary -t0 $$t0 | tee $(E2E_LOG) || rc=$$?; \
	$(MAKE) --no-print-directory e2e-unblock; \
	echo "e2e log: $(E2E_LOG) (go test -json: $(E2E_JSON))"; \
	grep -E '^(--- FAIL|FAIL|ok)' $(E2E_LOG) || true; \
	exit $$rc

.PHONY: e2e-smoke
e2e-smoke: E2E_RUN := ^($(E2E_SMOKE_TESTS))$$
e2e-smoke: E2E_LOG := $(ROOT)/bin/e2e-smoke.log
e2e-smoke: E2E_JSON := $(ROOT)/bin/e2e-smoke.json
e2e-smoke: e2e ## Run the smoke tier (E2E_SMOKE_TESTS): one scenario per area, ~4 min.

.PHONY: e2e-unblock
e2e-unblock: ## Restore the spoke-2 agent's hub token if a killed run left scenario 11's outage behind.
	@bash test/e2e/unblock.sh

.PHONY: e2e-clean
e2e-clean: e2e-unblock ## Delete everything the suite created (namespaces, policies, Argo CD apps, hub policies/clusters/pauses).
	@E2E_SWEEP=1 $(GO) test -tags e2e -count=1 -run '^TestHubSweep$$' ./test/e2e/... >/dev/null
	@for c in $(E2E_SPOKE1_CONTEXT) $(E2E_SPOKE2_CONTEXT); do \
	  kubectl --context $$c delete rightsizingpolicies -l $(E2E_LABEL) --ignore-not-found; \
	  kubectl --context $$c delete namespaces -l $(E2E_LABEL) --ignore-not-found --wait=false; \
	done
	@kubectl --context $(E2E_SPOKE1_CONTEXT) -n argocd delete applications.argoproj.io -l $(E2E_LABEL) --ignore-not-found --wait=false

.PHONY: e2e-vet
e2e-vet: ## Compile and vet the e2e suite (no cluster needed).
	$(GO) vet -tags e2e ./test/e2e/...
