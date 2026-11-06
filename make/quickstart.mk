# Artifact Hub lint and the laptop quickstart. Included by the root Makefile.

# Artifact Hub's linter (https://github.com/artifacthub/hub/releases): `ah` on PATH, or AH=/path/to/ah.
AH ?= ah

##@ Artifact Hub

.PHONY: charts-ah-lint
charts-ah-lint: ## Lint the umbrella chart's Artifact Hub metadata (ah lint; install ah from github.com/artifacthub/hub/releases).
	@command -v $(AH) >/dev/null || { echo "ah not found: install it from https://github.com/artifacthub/hub/releases or set AH=/path/to/ah"; exit 1; }
	$(AH) lint --kind helm --path deploy/charts/kerf
	@# artifacthub-repo.yml must stay valid YAML (it is pushed as-is by the release workflow).
	@python3 -c 'import sys, yaml; yaml.safe_load(open("artifacthub-repo.yml"))' 2>/dev/null || ruby -ryaml -e 'YAML.load_file("artifacthub-repo.yml")'
