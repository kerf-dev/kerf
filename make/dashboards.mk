# Grafana dashboards (WO-05). Source: dashboards/src/build.py; output: dashboards/*.json.

DASHBOARDS_SRC := $(ROOT)/dashboards/src
PYTHON         ?= python3

.PHONY: dashboards dashboards-check dashboards-verify dashboards-verify-vm dashboards-preview dashboards-preview-delete

##@ Dashboards (WO-05)

dashboards: ## Regenerate dashboards/*.json from dashboards/src/build.py
	@$(PYTHON) $(DASHBOARDS_SRC)/build.py

dashboards-check: ## Static checks: valid JSON, up to date, descriptions, contract metric names/labels
	@for f in $(ROOT)/dashboards/*.json; do jq empty "$$f"; done
	@$(PYTHON) $(DASHBOARDS_SRC)/verify.py

dashboards-verify: dashboards-check ## Run every panel query through Grafana for each cluster (dev env)
	@$(PYTHON) $(DASHBOARDS_SRC)/verify.py --live

dashboards-verify-vm: dashboards-check ## Same as dashboards-verify, straight against VictoriaMetrics
	@$(PYTHON) $(DASHBOARDS_SRC)/verify.py --live --via vm

dashboards-preview: ## Import the dashboards into Grafana as *-preview copies (before merge)
	@$(PYTHON) $(DASHBOARDS_SRC)/preview.py

dashboards-preview-delete: ## Delete the *-preview copies from Grafana
	@$(PYTHON) $(DASHBOARDS_SRC)/preview.py --delete

verify-generate: dashboards-check
