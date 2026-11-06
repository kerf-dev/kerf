# Artifact Hub lint and the laptop quickstart. Included by the root Makefile.

# Artifact Hub's linter (https://github.com/artifacthub/hub/releases): `ah` on PATH, or AH=/path/to/ah.
AH ?= ah

##@ Artifact Hub

.PHONY: charts-ah-lint
charts-ah-lint: yq ## Lint the umbrella chart's Artifact Hub metadata (ah lint; install ah from github.com/artifacthub/hub/releases).
	@command -v $(AH) >/dev/null || { echo "ah not found: install it from https://github.com/artifacthub/hub/releases or set AH=/path/to/ah"; exit 1; }
	$(AH) lint --kind helm --path deploy/charts/kerf
	@# artifacthub-repo.yml is pushed as it is by the release workflow: it must stay valid YAML.
	@$(YQ) e '.' artifacthub-repo.yml >/dev/null && echo "artifacthub-repo.yml is valid YAML"

##@ Quickstart

QUICKSTART_VALUES := $(UMBRELLA_DIR)/values-quickstart.yaml
# The tokens hack/quickstart.sh generates, as dummies for rendering.
QUICKSTART_TOKENS := --set-string hub.devSeed.tokens.quickstart=lint --set-string hub.devSeed.tokens.pricing=lint \
  --set-string agent.hub.tokenSecret.token=lint --set-string pricing.hub.tokenSecret.token=lint

.PHONY: charts-lint-quickstart
charts-lint-quickstart: kubeconform ## helm lint + kubeconform of the quickstart values; the schema refuses the quickstart settings without dev: true.
	@mkdir -p $(KUBECONFORM_CACHE); set -e; out=$(KUBECONFORM_CACHE)/quickstart.yaml; \
	echo "== $(UMBRELLA) (quickstart values)"; \
	lo=$$(helm lint --strict $(UMBRELLA_DIR) -f $(QUICKSTART_VALUES) $(QUICKSTART_TOKENS) --quiet 2>&1) || { echo "$$lo"; exit 1; }; \
	echo "$$lo" | grep -v 'symbolic link' || true; \
	helm template $(UMBRELLA) $(UMBRELLA_DIR) -n kerf -f $(QUICKSTART_VALUES) $(QUICKSTART_TOKENS) 2>/dev/null > $$out; \
	$(KUBECONFORM) -strict -summary -kubernetes-version $(CHARTS_K8S_VERSION) -cache $(KUBECONFORM_CACHE) \
	  -skip CustomResourceDefinition -schema-location default -schema-location '$(CRDS_CATALOG)' $$out; \
	for s in quickstart.enabled=true webhook.tls.selfSigned.enabled=true; do \
	  if helm template $(UMBRELLA) $(UMBRELLA_DIR) -n kerf $(UMBRELLA_REQUIRED) --set global.mode=all --set $$s >/dev/null 2>&1; then \
	    echo "$$s must be refused without dev: true"; exit 1; fi; \
	  helm template $(UMBRELLA) $(UMBRELLA_DIR) -n kerf $(UMBRELLA_REQUIRED) --set global.mode=all --set $$s 2>&1 | grep -q "must be one of false" || { echo "$$s: refused, but not by the schema"; exit 1; }; \
	done; \
	echo "quickstart values OK; the schema refuses the quickstart settings without dev: true"

charts-lint: charts-lint-quickstart
