# Component Helm charts (WO-18b): lint, render checks and copies kept in sync. Included by the root Makefile.

CHARTS_DIR          := deploy/charts
CHARTS              := hub web pricing agent webhook
CHARTS_K8S_VERSION  ?= 1.35.0
CHARTS_DIFF_BASE    ?= main
KUBECONFORM_VERSION ?= v0.7.0
YQ_VERSION          ?= v4.47.1
KUBECONFORM         ?= $(LOCALBIN)/kubeconform-$(KUBECONFORM_VERSION)
YQ                  ?= $(LOCALBIN)/yq-$(YQ_VERSION)
KUBECONFORM_CACHE   ?= $(LOCALBIN)/kubeconform-cache
# CRD schemas (ServiceMonitor, VMServiceScrape, PrometheusRule, VMRule, ExternalSecret, cert-manager).
CRDS_CATALOG        ?= https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json

# Values every chart needs to render at all ("defaults" = values.yaml plus these).
CHART_REQUIRED_hub     := --set-string database.url=postgres://lint@db/lint
CHART_REQUIRED_web     :=
CHART_REQUIRED_pricing :=
CHART_REQUIRED_agent   := --set-string clusterName=lint
CHART_REQUIRED_webhook := --set-string clusterName=lint

# The dev render, as make deploy-<component> / Tilt install it (release name, values files).
CHART_DEV_hub     := hub     -f values-dev.yaml
CHART_DEV_web     := web     -f values-dev.yaml
CHART_DEV_pricing := pricing -f values-dev.yaml --set-string clusterName=ro-hub
CHART_DEV_agent   := agent   -f values-dev.yaml -f values-ro-spoke-1.yaml --set-string clusterName=ro-spoke-1 --include-crds
CHART_DEV_webhook := webhook -f values-dev.yaml --set-string clusterName=ro-spoke-1 --include-crds

##@ Charts (WO-18b)

.PHONY: charts-sync
charts-sync: ## Copy api/crds, dashboards/*.json and the shared _common.tpl into the charts.
	@for c in agent webhook; do mkdir -p $(CHARTS_DIR)/$$c/files/crds; rm -f $(CHARTS_DIR)/$$c/files/crds/*.yaml; cp api/crds/*.yaml $(CHARTS_DIR)/$$c/files/crds/; done
	@mkdir -p $(CHARTS_DIR)/hub/files/dashboards; rm -f $(CHARTS_DIR)/hub/files/dashboards/*.json; cp dashboards/*.json $(CHARTS_DIR)/hub/files/dashboards/
	@for c in web pricing agent webhook; do sed 's/"hub\./"'$$c'./g' $(CHARTS_DIR)/hub/templates/_common.tpl > $(CHARTS_DIR)/$$c/templates/_common.tpl; done
	@echo "charts synced"

.PHONY: charts-check-sync
charts-check-sync: ## Fail when a chart copy (CRDs, dashboards, _common.tpl) drifts from its source.
	@rc=0; \
	for c in agent webhook; do diff -rq api/crds $(CHARTS_DIR)/$$c/files/crds >/dev/null || { echo "$$c: files/crds differs from api/crds"; rc=1; }; done; \
	diff -rq dashboards $(CHARTS_DIR)/hub/files/dashboards -x src -x README.md >/dev/null || { echo "hub: files/dashboards differs from dashboards/"; rc=1; }; \
	for c in web pricing agent webhook; do sed 's/"hub\./"'$$c'./g' $(CHARTS_DIR)/hub/templates/_common.tpl | cmp -s - $(CHARTS_DIR)/$$c/templates/_common.tpl || { echo "$$c: templates/_common.tpl differs from the hub master"; rc=1; }; done; \
	[ $$rc = 0 ] && echo "chart copies in sync" || { echo "run: make charts-sync"; exit 1; }

.PHONY: charts-lint
charts-lint: charts-check-sync charts-check-crds kubeconform yq ## helm lint + helm template + kubeconform (k8s $(CHARTS_K8S_VERSION)) of every chart: defaults, dev, ci/values-prod.yaml.
	@mkdir -p $(KUBECONFORM_CACHE); set -e; \
	for c in $(CHARTS); do \
	  d=$(CHARTS_DIR)/$$c; \
	  case $$c in hub) req="$(CHART_REQUIRED_hub)";; web) req="$(CHART_REQUIRED_web)";; pricing) req="$(CHART_REQUIRED_pricing)";; agent) req="$(CHART_REQUIRED_agent)";; webhook) req="$(CHART_REQUIRED_webhook)";; esac; \
	  for v in defaults dev prod; do \
	    case $$v in defaults) vf="";; dev) vf="-f $$d/values-dev.yaml";; prod) vf="-f $$d/ci/values-prod.yaml";; esac; \
	    echo "== $$c ($$v)"; \
	    helm lint --strict $$d $$req $$vf --quiet; \
	    helm template $$c $$d -n resource-optimizer --include-crds $$req $$vf \
	      | $(KUBECONFORM) -strict -summary -kubernetes-version $(CHARTS_K8S_VERSION) -cache $(KUBECONFORM_CACHE) \
	          -skip CustomResourceDefinition -schema-location default -schema-location '$(CRDS_CATALOG)'; \
	  done; \
	done

# The Kubernetes JSON schemas have no CustomResourceDefinition schema, so kubeconform skips that
# kind; instead the rendered CRDs must equal api/crds apart from the labels and the keep annotation.
CRDS_STRIP = $(YQ) ea -P '[.] | map(select(. != null)) | sort_by(.metadata.name) | .[] | del(.metadata.labels) \
  | del(.metadata.annotations["helm.sh/resource-policy"]) | ... comments="" | sort_keys(..) | split_doc'

.PHONY: charts-check-crds
charts-check-crds: yq ## Rendered CRD templates (agent; webhook with crds.install) == api/crds + labels and the keep annotation.
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	cat api/crds/*.yaml | $(CRDS_STRIP) > $$tmp/want.yaml; \
	for c in agent webhook; do \
	  helm template $$c $(CHARTS_DIR)/$$c -n resource-optimizer --set-string clusterName=lint --set crds.install=true -s templates/crds.yaml > $$tmp/$$c.yaml; \
	  test "$$(grep -c 'helm.sh/resource-policy: keep' $$tmp/$$c.yaml)" = "$$(ls api/crds/*.yaml | wc -l | tr -d ' ')" || { echo "$$c: CRDs without helm.sh/resource-policy: keep"; exit 1; }; \
	  $(CRDS_STRIP) $$tmp/$$c.yaml > $$tmp/$$c.got.yaml; \
	  diff -u $$tmp/want.yaml $$tmp/$$c.got.yaml || { echo "$$c: rendered CRDs differ from api/crds"; exit 1; }; \
	done; echo "CRD templates match api/crds"

# Normalised render (key order, document order, no comments; random values masked) for charts-diff-dev.
CHARTS_NORMALISE = $(YQ) ea -P '[.] | map(select(. != null)) | sort_by(.kind, .metadata.namespace, .metadata.name) | .[] \
  | ... comments="" \
  | with(select(.kind == "Secret" and .metadata.name == "hub-datasource-key"); .data.key = "<random>") \
  | with(select(.kind == "Deployment" and .metadata.name == "hub"); .spec.template.metadata.annotations["checksum/secrets"] = "<random>") \
  | sort_keys(..) | split_doc'

.PHONY: charts-diff-dev
charts-diff-dev: yq ## Diff the dev render of every chart: $(CHARTS_DIFF_BASE) vs this tree (normalised YAML).
	@base=$$(mktemp -d); trap 'rm -rf $$base' EXIT; \
	git archive $(CHARTS_DIFF_BASE) $(CHARTS_DIR) | tar -x -C $$base; \
	for c in $(CHARTS); do \
	  case $$c in hub) a="$(CHART_DEV_hub)";; web) a="$(CHART_DEV_web)";; pricing) a="$(CHART_DEV_pricing)";; agent) a="$(CHART_DEV_agent)";; webhook) a="$(CHART_DEV_webhook)";; esac; \
	  set -- $$a; rel=$$1; shift; \
	  for side in base head; do \
	    if [ $$side = base ]; then d=$$base/$(CHARTS_DIR)/$$c; else d=$(CHARTS_DIR)/$$c; fi; \
	    args=$$(echo "$$*" | sed "s|-f |-f $$d/|g"); \
	    helm template $$rel $$d -n resource-optimizer $$args | $(CHARTS_NORMALISE) > $$base/$$c.$$side.yaml; \
	  done; \
	  if diff -u --label "$$c@$(CHARTS_DIFF_BASE)" --label "$$c@HEAD" $$base/$$c.base.yaml $$base/$$c.head.yaml > $$base/$$c.diff; then \
	    echo "== $$c: no difference"; \
	  else echo "== $$c: differences"; cat $$base/$$c.diff; fi; \
	done

##@ Tools

.PHONY: kubeconform
kubeconform: $(KUBECONFORM) ## Install kubeconform into ./bin.
$(KUBECONFORM): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install github.com/yannh/kubeconform/cmd/kubeconform@$(KUBECONFORM_VERSION)
	mv $(LOCALBIN)/kubeconform $@

.PHONY: yq
yq: $(YQ) ## Install yq into ./bin.
$(YQ): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GO) install github.com/mikefarah/yq/v4@$(YQ_VERSION)
	mv $(LOCALBIN)/yq $@
