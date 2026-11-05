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

# The umbrella chart (WO-18d): rendered per mode with the values each mode requires ("defaults") and
# with the EKS examples (deploy/examples/eks).
UMBRELLA            := resource-optimizer
UMBRELLA_DIR        := $(CHARTS_DIR)/$(UMBRELLA)
UMBRELLA_EXAMPLES   := deploy/examples/eks
UMBRELLA_REQUIRED   := --set-string global.imageRegistry=123456789012.dkr.ecr.eu-west-1.amazonaws.com \
  --set-string global.clusterName=lint --set-string hub.database.existingSecret=lint-db --set-string agent.hub.url=https://hub.lint
UMBRELLA_EXAMPLE_hub   := -f $(UMBRELLA_EXAMPLES)/values-hub.yaml
UMBRELLA_EXAMPLE_spoke := -f $(UMBRELLA_EXAMPLES)/values-spoke.yaml
UMBRELLA_EXAMPLE_all   := -f $(UMBRELLA_EXAMPLES)/values-hub.yaml -f $(UMBRELLA_EXAMPLES)/values-spoke.yaml
# Deployments (app.kubernetes.io/name) and CRDs each mode must render, and no others (the gate).
UMBRELLA_WANT_hub   := hub pricing web
UMBRELLA_WANT_spoke := resource-optimizer-agent resource-optimizer-webhook
UMBRELLA_WANT_all   := hub pricing resource-optimizer-agent resource-optimizer-webhook web
UMBRELLA_CRDS_hub   := 0
UMBRELLA_CRDS_spoke := 2
UMBRELLA_CRDS_all   := 2
# The umbrella's dev render for charts-diff-dev (mode all).
UMBRELLA_DEV        := --set global.mode=all --set dev=true --set-string global.clusterName=ro-hub \
  --set-string hub.database.url=postgres://dev@db/dev

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
	done; \
	$(MAKE) --no-print-directory charts-lint-umbrella

# Rendered through the charts/ symlinks, so it always uses this tree's component charts.
.PHONY: charts-lint-umbrella
charts-lint-umbrella: kubeconform yq ## helm lint + kubeconform of the umbrella chart x {hub, spoke, all} x {defaults, examples}; checks each mode's components; the example manifests.
	@mkdir -p $(KUBECONFORM_CACHE); set -e; out=$(KUBECONFORM_CACHE)/umbrella.yaml; \
	for m in hub spoke all; do \
	  case $$m in hub) ex="$(UMBRELLA_EXAMPLE_hub)"; want="$(UMBRELLA_WANT_hub)"; ncrd=$(UMBRELLA_CRDS_hub);; \
	    spoke) ex="$(UMBRELLA_EXAMPLE_spoke)"; want="$(UMBRELLA_WANT_spoke)"; ncrd=$(UMBRELLA_CRDS_spoke);; \
	    all) ex="$(UMBRELLA_EXAMPLE_all)"; want="$(UMBRELLA_WANT_all)"; ncrd=$(UMBRELLA_CRDS_all);; esac; \
	  for v in defaults examples; do \
	    case $$v in defaults) vf="$(UMBRELLA_REQUIRED)";; examples) vf="$$ex";; esac; \
	    echo "== $(UMBRELLA) (mode $$m, $$v)"; \
	    lo=$$(helm lint --strict $(UMBRELLA_DIR) --set global.mode=$$m $$vf --quiet 2>&1) || { echo "$$lo"; exit 1; }; \
	    echo "$$lo" | grep -v 'symbolic link' || true; \
	    helm template $(UMBRELLA) $(UMBRELLA_DIR) -n resource-optimizer --set global.mode=$$m $$vf 2>/dev/null > $$out; \
	    $(KUBECONFORM) -strict -summary -kubernetes-version $(CHARTS_K8S_VERSION) -cache $(KUBECONFORM_CACHE) \
	      -skip CustomResourceDefinition -schema-location default -schema-location '$(CRDS_CATALOG)' $$out; \
	    got=$$($(YQ) e -N 'select(.kind == "Deployment") | .spec.template.metadata.labels["app.kubernetes.io/name"]' $$out | sort | xargs); \
	    crds=$$($(YQ) e -N 'select(.kind == "CustomResourceDefinition") | .metadata.name' $$out | grep -c . || true); \
	    [ "$$got" = "$$want" ] || { echo "mode $$m: Deployments [$$got], want [$$want]"; exit 1; }; \
	    [ "$$crds" = "$$ncrd" ] || { echo "mode $$m: $$crds CRDs, want $$ncrd"; exit 1; }; \
	    echo "   components: $$got; CRDs: $$crds"; \
	  done; \
	done; \
	echo "== $(UMBRELLA) wiring (mode all, release $(UMBRELLA), namespace resource-optimizer)"; \
	helm template $(UMBRELLA) $(UMBRELLA_DIR) -n resource-optimizer --set global.mode=all $(UMBRELLA_REQUIRED) --set-string agent.hub.url= 2>/dev/null > $$out; \
	hub=hub.resource-optimizer.svc.cluster.local:8080; \
	web=$$($(YQ) e -N 'select(.kind == "Deployment" and .metadata.name == "web") | .spec.template.spec.containers[0].env[] | select(.name == "HUB_UPSTREAM") | .value' $$out); \
	pricing=$$($(YQ) e -N 'select(.kind == "Deployment" and .metadata.name == "pricing") | .spec.template.spec.containers[0].args[] | select(test("^--hub-url="))' $$out); \
	agent=$$($(YQ) e -N 'select(.kind == "Deployment" and .metadata.name == "resource-optimizer-agent") | .spec.template.spec.containers[0].args[] | select(test("^--hub-url="))' $$out); \
	echo "   web HUB_UPSTREAM=$$web  pricing $$pricing  agent $$agent"; \
	[ "$$web" = "$$hub" ] && [ "$$pricing" = "--hub-url=http://$$hub" ] && [ "$$agent" = "--hub-url=http://$$hub" ] || { echo "wiring to the hub Service $$hub is wrong"; exit 1; }; \
	echo "== $(UMBRELLA_EXAMPLES): manifests and IAM policies"; \
	$(KUBECONFORM) -strict -summary -kubernetes-version $(CHARTS_K8S_VERSION) -cache $(KUBECONFORM_CACHE) \
	  -schema-location default -schema-location '$(CRDS_CATALOG)' \
	  $(UMBRELLA_EXAMPLES)/applicationset.yaml $(UMBRELLA_EXAMPLES)/argocd-ecr-repo.yaml $(UMBRELLA_EXAMPLES)/external-secrets/*.yaml; \
	for f in $(UMBRELLA_EXAMPLES)/iam/*.json; do jq -e '.Version and .Statement' $$f >/dev/null || { echo "$$f: not an IAM policy"; exit 1; }; done; \
	echo "umbrella chart OK"

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
	done; \
	if [ -d $$base/$(UMBRELLA_DIR) ]; then \
	  for side in base head; do \
	    if [ $$side = base ]; then d=$$base/$(UMBRELLA_DIR); else d=$(UMBRELLA_DIR); fi; \
	    helm template $(UMBRELLA) $$d -n resource-optimizer $(UMBRELLA_DEV) 2>/dev/null | $(CHARTS_NORMALISE) > $$base/umbrella.$$side.yaml; \
	  done; \
	  if diff -u --label "$(UMBRELLA)@$(CHARTS_DIFF_BASE)" --label "$(UMBRELLA)@HEAD" $$base/umbrella.base.yaml $$base/umbrella.head.yaml > $$base/umbrella.diff; then \
	    echo "== $(UMBRELLA) (mode all): no difference"; \
	  else echo "== $(UMBRELLA) (mode all): differences"; cat $$base/umbrella.diff; fi; \
	else echo "== $(UMBRELLA): not in $(CHARTS_DIFF_BASE) (new chart)"; fi

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
