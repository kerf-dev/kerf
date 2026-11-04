# Agent (WO-06) targets. Included by the root Makefile.

AGENT_IMAGE      ?= localhost:5005/resource-optimizer/agent
AGENT_IMAGE_PULL ?= k3d-ro-registry:5005/resource-optimizer/agent
AGENT_TAG        ?= dev-$(shell date +%Y%m%d-%H%M%S)
# Expand once: a recursive $(shell date) would give docker push and helm different tags.
AGENT_TAG        := $(AGENT_TAG)
AGENT_CHART      := deploy/charts/agent
AGENT_NAMESPACE  ?= resource-optimizer
AGENT_CLUSTERS   ?= ro-spoke-1 ro-spoke-2
ENVTEST_K8S      ?= 1.35.0
SETUP_ENVTEST    ?= $(LOCALBIN)/setup-envtest

##@ Agent (WO-06)

.PHONY: build-agent
build-agent: ## Build the agent binary into ./bin/agent.
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(LOCALBIN)/agent ./cmd/agent

.PHONY: agent-envtest
agent-envtest: | $(LOCALBIN) ## Download envtest binaries (kube-apiserver, etcd) into ./bin/envtest.
	@test -x $(SETUP_ENVTEST) || GOBIN=$(LOCALBIN) $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.23
	$(SETUP_ENVTEST) use $(ENVTEST_K8S) --bin-dir $(LOCALBIN)/envtest -p path

.PHONY: test-agent
test-agent: agent-envtest ## Unit + envtest tests of the agent, with coverage.
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S) --bin-dir $(LOCALBIN)/envtest -p path)" \
	  $(GO) test $(GOFLAGS) ./internal/agent/... -cover

.PHONY: agent-sync-crds
agent-sync-crds: ## Copy api/crds into the agent chart (run after `make generate`).
	cp api/crds/*.yaml $(AGENT_CHART)/crds/

.PHONY: lint-agent-chart
lint-agent-chart: ## helm lint the agent chart.
	helm lint $(AGENT_CHART) --set clusterName=lint

.PHONY: docker-agent
docker-agent: ## Build and push the agent image to the k3d registry (AGENT_TAG=...).
	docker build -f cmd/agent/Dockerfile --build-arg VERSION=$(AGENT_TAG) -t $(AGENT_IMAGE):$(AGENT_TAG) .
	docker push $(AGENT_IMAGE):$(AGENT_TAG)

.PHONY: deploy-agent
deploy-agent: docker-agent ## Build, push and helm-install the agent on every spoke (AGENT_CLUSTERS=...).
	@for c in $(AGENT_CLUSTERS); do \
	  extra=""; for f in $(AGENT_CHART)/values-dev.yaml $(AGENT_CHART)/values-$$c.yaml; do [ -f $$f ] && extra="$$extra -f $$f"; done; \
	  echo "helm upgrade --install agent -> k3d-$$c/$(AGENT_NAMESPACE)"; \
	  helm upgrade --install agent $(AGENT_CHART) --kube-context k3d-$$c -n $(AGENT_NAMESPACE) --create-namespace \
	    --wait --timeout 5m $$extra --set-string clusterName=$$c \
	    --set-string image.repository=$(AGENT_IMAGE_PULL) --set-string image.tag=$(AGENT_TAG) $(AGENT_HELM_ARGS) || exit 1; \
	done

.PHONY: agent-crds
agent-crds: ## Server-side apply the CRDs on every spoke (upgrades; Helm's crds/ only installs).
	@for c in $(AGENT_CLUSTERS); do kubectl --context k3d-$$c apply --server-side --force-conflicts -f api/crds/; done
