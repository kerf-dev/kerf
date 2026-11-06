# The documentation site (getkerf.dev/docs): MkDocs Material over docs/, reference pages generated into
# docs/reference/. Python packages are pinned with hashes in tools/docs/requirements.txt.
# Guide: tools/docs/README.md.

DOCS_VENV   ?= $(ROOT)/.venv-docs
DOCS_PY     := $(DOCS_VENV)/bin/python
DOCS_PYTHON ?= python3
DOCS_OUT    ?= $(ROOT)/_site/docs

$(DOCS_VENV)/.installed: tools/docs/requirements.txt
	$(DOCS_PYTHON) -m venv $(DOCS_VENV)
	$(DOCS_PY) -m pip install --quiet --require-hashes -r tools/docs/requirements.txt
	touch $@

##@ Documentation

.PHONY: docs-venv
docs-venv: $(DOCS_VENV)/.installed ## Create the Python environment of the docs build.

.PHONY: docs-generate
docs-generate: docs-venv ## Regenerate docs/reference/ (chart values, CRDs, hub API).
	$(DOCS_PY) tools/docs/generate.py

.PHONY: docs-check-generated
docs-check-generated: docs-venv ## Fail when docs/reference/ is out of date.
	$(DOCS_PY) tools/docs/generate.py --check

.PHONY: docs-test
docs-test: docs-venv ## Unit tests of the docs tooling.
	$(DOCS_PY) -m unittest discover -s tools/docs -p 'test_*.py'

.PHONY: docs-build
docs-build: docs-venv ## Build the dev docs into _docs-site/ (strict: warnings fail).
	KERF_DOCS_STRICT=true $(DOCS_PY) -m mkdocs build

.PHONY: docs-serve
docs-serve: docs-venv ## Serve the docs on http://127.0.0.1:8000 with live reload.
	$(DOCS_PY) -m mkdocs serve

.PHONY: docs-site
docs-site: docs-venv ## Build every version, as the Pages workflow does, into $(DOCS_OUT).
	$(DOCS_PY) tools/docs/build_site.py --out $(DOCS_OUT)

.PHONY: docs-lock
docs-lock: ## Re-resolve tools/docs/requirements.txt (with hashes) from requirements.in.
	$(DOCS_PYTHON) -m venv $(ROOT)/.venv-docs-lock
	$(ROOT)/.venv-docs-lock/bin/pip install --quiet pip-tools
	$(ROOT)/.venv-docs-lock/bin/pip-compile --generate-hashes --strip-extras --quiet -o tools/docs/requirements.txt tools/docs/requirements.in
	rm -rf $(ROOT)/.venv-docs-lock

.PHONY: docs-verify
docs-verify: docs-check-generated docs-test docs-build ## Everything the docs CI job runs (the link check runs on the assembled site).

ci: docs-verify
