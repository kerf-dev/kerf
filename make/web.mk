# Web UI (WO-14): React SPA in web/, nginx image, Helm chart deploy/charts/web.
# Included by the root Makefile. Node 22+ must be on PATH (see CLAUDE.md "Toolchain").

WEB_DIR        := web
WEB_IMAGE      ?= localhost:5005/kerf/web
WEB_TAG        ?= dev-$(shell date +%Y%m%d-%H%M%S)
# Expand once: a recursive $(shell date) would give docker push and helm different tags.
WEB_TAG        := $(WEB_TAG)
WEB_CONTEXT    ?= k3d-ro-hub
WEB_NAMESPACE  ?= kerf
# Real hub for `web-dev-live` (WO-04 NodePort 30080) and its development `ui` token
# (deploy/charts/hub/values-dev.yaml). The Vite proxy adds the token; the browser never sees it.
KERF_HUB_URL     ?= http://localhost:18080
KERF_HUB_TOKEN   ?= ro-dev-ui-97446cd3a095035d6290abc5

.PHONY: web-install web-dev web-dev-live web-lint web-typecheck web-test web-e2e web-build web-check-api \
        web-verify docker-web push-web deploy-web web-screenshots

$(WEB_DIR)/node_modules: $(WEB_DIR)/package-lock.json
	cd $(WEB_DIR) && npm ci --no-audit --no-fund
	@touch $@

web-install: $(WEB_DIR)/node_modules ## Install web UI dependencies (npm ci).

web-dev: $(WEB_DIR)/node_modules ## Run the web UI on :5173 against the MSW mock API (no hub needed).
	cd $(WEB_DIR) && npm run dev

web-dev-live: $(WEB_DIR)/node_modules ## Run the web UI on :5173, proxying /v1 to a real hub (KERF_HUB_URL, KERF_HUB_TOKEN).
	cd $(WEB_DIR) && KERF_HUB_URL=$(KERF_HUB_URL) KERF_HUB_TOKEN=$(KERF_HUB_TOKEN) npm run dev:live

web-lint: $(WEB_DIR)/node_modules ## ESLint + Prettier check for the web UI.
	cd $(WEB_DIR) && npm run lint && npm run format:check

web-typecheck: $(WEB_DIR)/node_modules ## TypeScript strict type check.
	cd $(WEB_DIR) && npm run typecheck

web-test: $(WEB_DIR)/node_modules ## Unit and contract tests (mock responses validated against api/openapi/hub.yaml).
	cd $(WEB_DIR) && npm run test -- --run

web-check-api: $(WEB_DIR)/node_modules ## Fail if web/src/api/schema.d.ts is stale vs api/openapi/hub.yaml.
	cd $(WEB_DIR) && npm run gen:api:check

web-e2e: $(WEB_DIR)/node_modules ## Playwright smoke tests against the mock API.
	cd $(WEB_DIR) && npx playwright install chromium && npm run e2e

web-build: $(WEB_DIR)/node_modules ## Production build into web/dist.
	cd $(WEB_DIR) && npm run build

WEB_SCREENSHOTS_DIR ?= docs/screenshots

web-screenshots: $(WEB_DIR)/node_modules ## Capture every page (light, dark, tablet) from the mock API into docs/screenshots/.
	mkdir -p $(WEB_SCREENSHOTS_DIR)
	cd $(WEB_DIR) && SCREENSHOTS_DIR=$(abspath $(WEB_SCREENSHOTS_DIR)) npx playwright test screenshots

web-verify: web-check-api web-lint web-typecheck web-test web-build web-e2e ## Everything under WO-14 "Verification" except docker.

docker-web: ## Build the web image (nginx + SPA).
	docker build -t $(WEB_IMAGE):$(WEB_TAG) $(WEB_DIR)

push-web: docker-web ## Build and push the web image to the k3d registry.
	docker push $(WEB_IMAGE):$(WEB_TAG)

deploy-web: push-web ## Build, push and helm upgrade --install the web UI into ro-hub (http://localhost:19090).
	helm upgrade --install web deploy/charts/web --kube-context $(WEB_CONTEXT) -n $(WEB_NAMESPACE) --create-namespace \
	  -f deploy/charts/web/values-dev.yaml \
	  --set-string image.repository=k3d-ro-registry:5005/kerf/web --set-string image.tag=$(WEB_TAG) \
	  --wait --timeout 3m
	@echo "web UI: http://localhost:19090"
