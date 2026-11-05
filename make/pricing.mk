# Pricing service (WO-03). Included by the root Makefile.

PRICING_IMAGE ?= localhost:5005/kerf/pricing
PRICING_TAG   ?= dev-$(shell date +%Y%m%d-%H%M%S)
# Expand once: a recursive $(shell date) would give docker push and helm different tags.
PRICING_TAG   := $(PRICING_TAG)
PRICING_VM_URL ?= http://localhost:18428

.PHONY: build-pricing test-pricing run-pricing docker-pricing deploy-pricing lint-pricing-chart

build-pricing: ## Build the pricing binary into bin/pricing
	$(GO) build $(GOFLAGS) -o $(LOCALBIN)/pricing ./cmd/pricing

test-pricing: ## Unit tests with coverage for the pricing service
	$(GO) test $(GOFLAGS) -cover ./internal/pricing/... ./cmd/pricing/...

run-pricing: ## Run pricing locally (static provider, host VictoriaMetrics) on :9090
	$(GO) run ./cmd/pricing --provider static --prices dev/prices.yaml --vm-url $(PRICING_VM_URL) --interval 1m

docker-pricing: ## Build and push the pricing image to the local k3d registry
	docker build -f cmd/pricing/Dockerfile -t $(PRICING_IMAGE):$(PRICING_TAG) .
	docker push $(PRICING_IMAGE):$(PRICING_TAG)

deploy-pricing: ## Build, push and helm-install pricing into ro-hub/kerf (same path as Tilt)
	$(ROOT)/dev/scripts/tilt-deploy.sh pricing ro-hub

lint-pricing-chart: ## helm lint the pricing chart
	helm lint deploy/charts/pricing -f deploy/charts/pricing/values-dev.yaml
