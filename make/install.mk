# Staging install that follows docs/install/eks.md on two fresh k3d clusters
# (ro-stg-hub, ro-stg-spoke). AWS pieces are replaced by local stand-ins; see test/install/lib.sh.
# Uses about 3 GiB of memory next to the dev fleet.

STAGING_VERSION ?= 0.1.0-rc.2
STAGING := VERSION=$(STAGING_VERSION) ./test/install/staging.sh

.PHONY: install-staging install-staging-verify install-staging-wizard install-staging-upgrade install-staging-uninstall install-staging-down

install-staging: ## Staging: clusters, prerequisites, release STAGING_VERSION, then the guide's steps 3-5.
	@if ! helm show chart oci://localhost:5005/kerf/charts/kerf --version $(STAGING_VERSION) --plain-http >/dev/null 2>&1; then \
	  $(MAKE) release REGISTRY=localhost:5005/kerf VERSION=$(STAGING_VERSION); fi
	$(STAGING) up
	$(STAGING) install

install-staging-verify: ## Staging: leader, HTTPS, AutomationReady, one auto-patch, hub failover.
	$(STAGING) verify

install-staging-wizard: ## Staging: a third cluster ro-stg-spoke-2, registered via the API and installed with the wizard's Helm snippet.
	$(STAGING) wizard

install-staging-upgrade: ## Staging: upgrade to STAGING_UPGRADE_VERSION (released first) with a no-5xx probe.
	@test -n "$(STAGING_UPGRADE_VERSION)" || { echo "STAGING_UPGRADE_VERSION is required"; exit 1; }
	@if ! helm show chart oci://localhost:5005/kerf/charts/kerf --version $(STAGING_UPGRADE_VERSION) --plain-http >/dev/null 2>&1; then \
	  $(MAKE) release REGISTRY=localhost:5005/kerf VERSION=$(STAGING_UPGRADE_VERSION); fi
	$(STAGING) upgrade $(STAGING_UPGRADE_VERSION)

install-staging-uninstall: ## Staging: the guide's step 9 (uninstall, then remove everything).
	$(STAGING) uninstall

install-staging-down: ## Staging: delete both staging clusters and bin/staging.
	$(STAGING) down
