# Hub targets. Hooked into the shared targets in make/common.mk only by prerequisites.

HUB_IMAGE_REPO ?= localhost:5005/kerf/hub
HUB_TAG        ?= dev-$(shell git rev-parse --short HEAD 2>/dev/null || echo local)
HUB_CONTEXT    ?= k3d-ro-hub
HUB_NAMESPACE  ?= kerf

##@ Hub

.PHONY: build-hub
build-hub: ## Build the hub binary into bin/hub.
	$(GO) build $(GOFLAGS) -o $(LOCALBIN)/hub ./cmd/hub

.PHONY: test-hub
test-hub: ## Unit tests of the hub and the recommender (DB tests need KERF_TEST_DATABASE_URL).
	$(GO) test $(GOFLAGS) ./internal/recommender/... ./internal/hub/... -cover

.PHONY: run-hub
run-hub: ## Run the hub on the host against the shared k3d environment (dev/hub.yaml).
	$(GO) run ./cmd/hub --config dev/hub.yaml

.PHONY: docker-hub
docker-hub: ## Build and push the hub image to the local registry.
	docker build -f cmd/hub/Dockerfile -t $(HUB_IMAGE_REPO):$(HUB_TAG) .
	docker push $(HUB_IMAGE_REPO):$(HUB_TAG)

.PHONY: deploy-hub
deploy-hub: docker-hub ## Build, push and helm upgrade --install the hub into ro-hub.
	helm --kube-context $(HUB_CONTEXT) upgrade --install hub deploy/charts/hub \
	  --namespace $(HUB_NAMESPACE) --create-namespace \
	  -f deploy/charts/hub/values-dev.yaml \
	  --set image.repository=k3d-ro-registry:5005/kerf/hub --set image.tag=$(HUB_TAG) --wait
