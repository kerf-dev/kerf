# Mutating admission webhook (WO-07) targets. Included by the root Makefile.

WEBHOOK_IMAGE         ?= localhost:5005/resource-optimizer/webhook
WEBHOOK_IMAGE_PULL    ?= k3d-ro-registry:5005/resource-optimizer/webhook
WEBHOOK_TAG           ?= dev-$(shell date +%Y%m%d-%H%M%S)
WEBHOOK_CHART         := deploy/charts/webhook
WEBHOOK_NAMESPACE     ?= resource-optimizer
WEBHOOK_CLUSTERS      ?= ro-spoke-1 ro-spoke-2
WEBHOOK_ENVTEST_K8S   ?= 1.35.0
WEBHOOK_SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest

##@ Webhook (WO-07)

.PHONY: build-webhook
build-webhook: ## Build the webhook binary into ./bin/webhook.
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(LOCALBIN)/webhook ./cmd/webhook

.PHONY: webhook-envtest
webhook-envtest: | $(LOCALBIN) ## Download envtest binaries (kube-apiserver, etcd) into ./bin/envtest.
	@test -x $(WEBHOOK_SETUP_ENVTEST) || GOBIN=$(LOCALBIN) $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.23
	$(WEBHOOK_SETUP_ENVTEST) use $(WEBHOOK_ENVTEST_K8S) --bin-dir $(LOCALBIN)/envtest -p path

.PHONY: test-webhook
test-webhook: webhook-envtest ## Unit + envtest tests of the webhook, with coverage.
	KUBEBUILDER_ASSETS="$$($(WEBHOOK_SETUP_ENVTEST) use $(WEBHOOK_ENVTEST_K8S) --bin-dir $(LOCALBIN)/envtest -p path)" \
	  $(GO) test $(GOFLAGS) ./internal/webhook/... -cover

.PHONY: lint-webhook-chart
lint-webhook-chart: ## helm lint the webhook chart.
	helm lint $(WEBHOOK_CHART) --set clusterName=lint

.PHONY: docker-webhook
docker-webhook: ## Build and push the webhook image to the k3d registry (WEBHOOK_TAG=...).
	docker build -f cmd/webhook/Dockerfile --build-arg VERSION=$(WEBHOOK_TAG) -t $(WEBHOOK_IMAGE):$(WEBHOOK_TAG) .
	docker push $(WEBHOOK_IMAGE):$(WEBHOOK_TAG)

.PHONY: deploy-webhook
deploy-webhook: docker-webhook ## Build, push and helm-install the webhook on every spoke (WEBHOOK_CLUSTERS=...).
	@for c in $(WEBHOOK_CLUSTERS); do \
	  extra=""; for f in $(WEBHOOK_CHART)/values-dev.yaml $(WEBHOOK_CHART)/values-$$c.yaml; do [ -f $$f ] && extra="$$extra -f $$f"; done; \
	  echo "helm upgrade --install webhook -> k3d-$$c/$(WEBHOOK_NAMESPACE)"; \
	  helm upgrade --install webhook $(WEBHOOK_CHART) --kube-context k3d-$$c -n $(WEBHOOK_NAMESPACE) --create-namespace \
	    --wait --timeout 5m $$extra --set-string clusterName=$$c \
	    --set-string image.repository=$(WEBHOOK_IMAGE_PULL) --set-string image.tag=$(WEBHOOK_TAG) $(WEBHOOK_HELM_ARGS) || exit 1; \
	done
