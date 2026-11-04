# Local dev environment (WO-01): k3d hub + two spokes. Included by the root Makefile (include make/*.mk);
# until that exists, run e.g. `make -f make/dev.mk dev-up`. See dev/README.md.

DEV_SCRIPTS := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))../dev/scripts)

.PHONY: dev-up dev-down dev-reset dev-purge dev-status dev-verify dev-dashboards dev-workloads

dev-up: ## Create/converge ro-hub, ro-spoke-1, ro-spoke-2 with monitoring, Argo CD and sample workloads
	@$(DEV_SCRIPTS)/up.sh

dev-down: ## Delete the three clusters and the image registry (keeps the docker.io pull cache)
	@$(DEV_SCRIPTS)/down.sh

dev-reset: ## dev-down + dev-up
	@$(DEV_SCRIPTS)/down.sh
	@$(DEV_SCRIPTS)/up.sh

dev-purge: ## dev-down and also remove the docker.io pull cache and the ro-dev network
	@$(DEV_SCRIPTS)/down.sh --purge

dev-status: ## Pod counts per cluster and access URLs
	@$(DEV_SCRIPTS)/status.sh

dev-verify: ## Run the WO-01 verification checks against the running fleet
	@$(DEV_SCRIPTS)/verify.sh

dev-dashboards: ## Re-provision Grafana dashboards from dashboards/
	@$(DEV_SCRIPTS)/sync-dashboards.sh

dev-workloads: ## Push dev/workloads/ to the spokes (git server + Argo CD on spoke-1, kubectl apply on spoke-2)
	@$(DEV_SCRIPTS)/sync-workloads.sh
