# Release (WO-18c, D-028): multi-arch images and OCI Helm charts for a registry such as ECR.
# Included by the root Makefile. Guide: docs/release.md.
#
#   make release       REGISTRY=<acct>.dkr.ecr.<region>.amazonaws.com/resource-optimizer VERSION=0.1.0
#   make release-chart REGISTRY=... VERSION=...      (charts only; `release` runs it too)
#   make release-dry                                 (build both platforms and package charts, push nothing)
#
# Images go to $(REGISTRY)/<name>:$(VERSION), charts to oci://$(REGISTRY)/charts/<chart>:$(VERSION).
# Results: bin/release/images.txt (`<repository> <tag> <digest>`) and bin/release/charts.txt.

REGISTRY  ?=
PLATFORMS ?= linux/amd64,linux/arm64
# Semver without a leading v. Default: the nearest v* tag (`v0.1.0-3-gabc1234` -> `0.1.0-3-gabc1234`),
# or 0.0.0-g<sha> when there is none; `-dirty` is appended for uncommitted changes.
VERSION   ?= $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null | \
                     sed -e 's/^v\([0-9]\)/\1/' -e t -e 's/^/0.0.0-g/')
VERSION   := $(VERSION)

RELEASE_IMAGES    ?= hub pricing agent webhook web
# The umbrella chart (WO-18d, what docs/install/eks.md and the hub's cluster wizard install) and the
# component charts (installs from before the umbrella: docs/install/eks.md, "Moving from the component charts").
RELEASE_CHARTS    ?= resource-optimizer hub pricing agent webhook web
RELEASE_CHART_DIR ?= deploy/charts
RELEASE_DIR       ?= $(LOCALBIN)/release
# buildx builder (docker-container driver: multi-platform output, SBOM and provenance attestations).
# Host networking lets it reach a registry on the build host's localhost (the k3d registry, :5005).
RELEASE_BUILDER   ?= ro-release
# Registries the builder talks to over plain HTTP. Baked into the builder when it is created:
# `make release-builder-rm` after changing it.
RELEASE_INSECURE_REGISTRIES ?= localhost:5005 127.0.0.1:5005
# Plain HTTP for helm push/pull to a local registry; empty for ECR.
RELEASE_HELM_FLAGS ?= $(if $(filter localhost:% 127.0.0.1:%,$(REGISTRY)),--plain-http,)
# Set to 1 to release from a tree with uncommitted changes (never for a real release).
RELEASE_ALLOW_DIRTY ?=
RELEASE_BUILD_ARGS  ?=

RELEASE_REVISION := $(shell git rev-parse HEAD 2>/dev/null)
RELEASE_SOURCE   ?= https://github.com/tomer1983/resource-optimizer

RELEASE_BUILDX = docker buildx build --builder $(RELEASE_BUILDER) --platform $(PLATFORMS) \
  --build-arg VERSION=$(VERSION) $(RELEASE_BUILD_ARGS) \
  --label org.opencontainers.image.version=$(VERSION) \
  --label org.opencontainers.image.revision=$(RELEASE_REVISION) \
  --label org.opencontainers.image.source=$(RELEASE_SOURCE)

##@ Release (WO-18c)

.PHONY: release release-images release-chart release-dry release-check release-builder release-builder-rm

release: release-images release-chart ## Build and push multi-arch images and OCI charts (REGISTRY=, VERSION=, PLATFORMS=).
	@echo "released $(VERSION) to $(REGISTRY):"; cat $(RELEASE_DIR)/images.txt $(RELEASE_DIR)/charts.txt

release-check:
	@test -n "$(REGISTRY)" || { echo "REGISTRY is required, e.g. REGISTRY=<acct>.dkr.ecr.<region>.amazonaws.com/resource-optimizer"; exit 1; }
	@case "$(REGISTRY)" in *://*|*/) echo "REGISTRY is host[/path], without scheme or trailing slash: $(REGISTRY)"; exit 1 ;; esac
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$' || \
	  { echo "VERSION must be semver without a leading v or build metadata (got '$(VERSION)')"; exit 1; }
	@if [ "$(RELEASE_ALLOW_DIRTY)" != 1 ] && [ -n "$$(git status --porcelain)" ]; then \
	  echo "working tree has uncommitted changes: commit them, or RELEASE_ALLOW_DIRTY=1 for a test release"; exit 1; fi

release-builder: ## Create the buildx builder for releases if it is missing (RELEASE_BUILDER=...).
	@if docker buildx inspect $(RELEASE_BUILDER) >/dev/null 2>&1; then exit 0; fi; \
	mkdir -p $(RELEASE_DIR); cfg=$(RELEASE_DIR)/buildkitd.toml; : > $$cfg; \
	for r in $(RELEASE_INSECURE_REGISTRIES); do printf '[registry."%s"]\n  http = true\n  insecure = true\n' $$r >> $$cfg; done; \
	echo "creating buildx builder $(RELEASE_BUILDER) (docker-container, network=host)"; \
	docker buildx create --name $(RELEASE_BUILDER) --driver docker-container --driver-opt network=host \
	  --buildkitd-config $$cfg --bootstrap

release-builder-rm: ## Remove the release buildx builder (and its build cache).
	docker buildx rm $(RELEASE_BUILDER)

release-images: release-check release-builder ## Build and push the images with SBOM and provenance; writes bin/release/images.txt.
	@mkdir -p $(RELEASE_DIR); : > $(RELEASE_DIR)/images.txt
	@# Dockerfile and context: the web UI builds from web/, the Go images from the repo root.
	@for img in $(RELEASE_IMAGES); do \
	  case $$img in web) df=web/Dockerfile ctx=web ;; *) df=cmd/$$img/Dockerfile ctx=. ;; esac; \
	  ref=$(REGISTRY)/$$img:$(VERSION); meta=$(RELEASE_DIR)/$$img.metadata.json; \
	  echo "==> $$ref ($(PLATFORMS))"; \
	  $(RELEASE_BUILDX) -f $$df --sbom=true --provenance=mode=max \
	    --annotation "index:org.opencontainers.image.version=$(VERSION)" \
	    --annotation "index:org.opencontainers.image.revision=$(RELEASE_REVISION)" \
	    --annotation "index:org.opencontainers.image.source=$(RELEASE_SOURCE)" \
	    --metadata-file $$meta -t $$ref --push $$ctx; \
	  digest=$$(jq -r '."containerimage.digest"' $$meta); \
	  case "$$digest" in sha256:*) ;; *) echo "no digest in $$meta"; exit 1 ;; esac; \
	  echo "$(REGISTRY)/$$img $(VERSION) $$digest" >> $(RELEASE_DIR)/images.txt; \
	done
	@echo "wrote $(RELEASE_DIR)/images.txt"; cat $(RELEASE_DIR)/images.txt

# Packages each chart with version and appVersion $(VERSION) into bin/release/charts.
# RELEASE_CHART_DEFAULT_REGISTRY=1 (the GitHub release workflow sets it) also makes the packaged charts
# default to the images of this release: the umbrella's global.imageRegistry becomes $(REGISTRY) without
# its trailing /resource-optimizer, and each component chart's image.repository becomes $(REGISTRY)/<name>.
# A user then installs the chart with no registry setting. Explicit values still win.
RELEASE_CHART_DEFAULT_REGISTRY ?=
RELEASE_IMAGE_HOST = $(patsubst %/resource-optimizer,%,$(REGISTRY))

.PHONY: release-chart-package
release-chart-package:
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$' || \
	  { echo "VERSION must be semver without a leading v or build metadata (got '$(VERSION)')"; exit 1; }
	@if [ "$(RELEASE_CHART_DEFAULT_REGISTRY)" = 1 ]; then \
	  case "$(REGISTRY)" in */resource-optimizer) ;; *) echo "RELEASE_CHART_DEFAULT_REGISTRY=1 needs REGISTRY=<host>[/path]/resource-optimizer (got '$(REGISTRY)')"; exit 1 ;; esac; fi
	@rm -rf $(RELEASE_DIR)/charts $(RELEASE_DIR)/_stage; mkdir -p $(RELEASE_DIR)/charts $(RELEASE_DIR)/_stage
	@for c in $(RELEASE_CHARTS); do \
	  dir=$(RELEASE_CHART_DIR)/$$c; test -f $$dir/Chart.yaml || { echo "no chart at $$dir"; exit 1; }; \
	  : "Package a staged copy: links resolved (the umbrella's charts/ links the component charts, WO-18d)."; \
	  : "_stage: go ./... skips _-prefixed dirs, so the copied *_test.go never joins make ci."; \
	  stage=$(RELEASE_DIR)/_stage/$$c; cp -RL $$dir $$stage || exit 1; find $$stage -name "*_test.go" -delete; \
	  deps=""; grep -q '^dependencies:' $$stage/Chart.yaml && deps=--dependency-update; \
	  if [ -d $$stage/charts ]; then \
	    : "Every subchart at version/appVersion VERSION (its image tags default to it)."; \
	    for sub in $$stage/charts/*/Chart.yaml; do \
	      sed -e 's/^version:.*/version: $(VERSION)/' -e 's/^appVersion:.*/appVersion: "$(VERSION)"/' $$sub > $$sub.new && mv $$sub.new $$sub; \
	    done; deps=""; \
	    if [ "$(RELEASE_CHART_DEFAULT_REGISTRY)" = 1 ]; then \
	      sed -e 's|^  imageRegistry: ""|  imageRegistry: "$(RELEASE_IMAGE_HOST)"|' $$stage/values.yaml > $$stage/values.yaml.new && \
	        mv $$stage/values.yaml.new $$stage/values.yaml; \
	    fi; \
	  else \
	    : "A component chart: its image tag defaults to the chart appVersion (= VERSION), not the dev tag."; \
	    sed -E -e 's|^  tag: dev$$|  tag: ""|' \
	      $(if $(filter 1,$(RELEASE_CHART_DEFAULT_REGISTRY)),-e 's|^  repository: [^ ]+/resource-optimizer/([a-z-]+)$$|  repository: $(REGISTRY)/\1|',) \
	      $$stage/values.yaml > $$stage/values.yaml.new && mv $$stage/values.yaml.new $$stage/values.yaml; \
	  fi; \
	  helm package $$stage $$deps --version $(VERSION) --app-version $(VERSION) -d $(RELEASE_DIR)/charts; \
	done

release-chart: release-check release-chart-package ## Package the charts (RELEASE_CHARTS=...) and helm push them to oci://$(REGISTRY)/charts.
	@: > $(RELEASE_DIR)/charts.txt
	@for c in $(RELEASE_CHARTS); do \
	  out=$$(helm push $(RELEASE_DIR)/charts/$$c-$(VERSION).tgz oci://$(REGISTRY)/charts $(RELEASE_HELM_FLAGS) 2>&1) || { echo "$$out"; exit 1; }; \
	  echo "$$out"; digest=$$(echo "$$out" | awk '/^Digest:/ {print $$2}'); \
	  echo "oci://$(REGISTRY)/charts/$$c $(VERSION) $$digest" >> $(RELEASE_DIR)/charts.txt; \
	done
	@echo "wrote $(RELEASE_DIR)/charts.txt"

release-dry: release-builder release-chart-package ## Build every image for $(PLATFORMS) and package the charts without pushing.
	@# Dockerfile and context: the web UI builds from web/, the Go images from the repo root.
	@for img in $(RELEASE_IMAGES); do \
	  case $$img in web) df=web/Dockerfile ctx=web ;; *) df=cmd/$$img/Dockerfile ctx=. ;; esac; \
	  echo "==> $$img ($(PLATFORMS), not pushed)"; \
	  $(RELEASE_BUILDX) -f $$df --output type=cacheonly $$ctx; \
	done
	@echo "release-dry OK: $(RELEASE_IMAGES) built for $(PLATFORMS); charts in $(RELEASE_DIR)/charts"
