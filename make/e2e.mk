# WO-09 end-to-end suite (test/e2e), with the admin-area scenarios of WO-24. Runs against the existing k3d environment; see test/e2e/README.md.

E2E_TIMEOUT        ?= 40m
E2E_PARALLEL       ?= 32
E2E_RUN            ?=
E2E_FLAGS          ?=
E2E_LOG            ?= $(ROOT)/bin/e2e.log
E2E_SPOKE1_CONTEXT ?= k3d-ro-spoke-1
E2E_SPOKE2_CONTEXT ?= k3d-ro-spoke-2
export E2E_SPOKE1_CONTEXT E2E_SPOKE2_CONTEXT

E2E_LABEL := e2e.kerf.getkerf.dev/suite=true

##@ End-to-end (WO-09, WO-24)

.PHONY: e2e
e2e: ## Run the e2e suite against the running clusters (E2E_RUN=TestAudit to pick scenarios).
	@mkdir -p $(dir $(E2E_LOG))
	@$(MAKE) --no-print-directory e2e-unblock
	@set -o pipefail; rc=0; $(GO) test -tags e2e -count=1 -timeout $(E2E_TIMEOUT) -parallel $(E2E_PARALLEL) -v ./test/e2e/... \
	  $(if $(E2E_RUN),-run '$(E2E_RUN)') $(E2E_FLAGS) 2>&1 | tee $(E2E_LOG) || rc=$$?; \
	$(MAKE) --no-print-directory e2e-unblock; \
	echo "e2e log: $(E2E_LOG)"; \
	grep -E '^(--- |ok|FAIL|PASS)' $(E2E_LOG) | grep -v '^    ' || true; \
	exit $$rc

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
