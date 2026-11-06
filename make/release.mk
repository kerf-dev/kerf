# Release: multi-arch images and OCI Helm charts for a registry such as ECR.
# Included by the root Makefile. Guide: docs/release.md.
#
#   make release       REGISTRY=<acct>.dkr.ecr.<region>.amazonaws.com/kerf VERSION=0.1.0
#   make release-chart REGISTRY=... VERSION=...      (charts only; `release` runs it too)
#   make release-dry                                 (build both platforms and package charts, push nothing)
#   make release-notes VERSION=0.2.0                 (print the GitHub Release notes: highlights, changes by type, install)
#   make changelog     VERSION=0.2.0                 (print the changes as Keep a Changelog sections, to curate into CHANGELOG.md)
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
# The umbrella chart (what docs/install/eks.md and the hub's cluster wizard install) and the
# component charts (installs from before the umbrella: docs/install/eks.md, "Moving from the component charts").
RELEASE_CHARTS    ?= kerf hub pricing agent webhook web
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
RELEASE_SOURCE   ?= https://github.com/kerf-dev/kerf

# CI: RELEASE_CACHE=<name> keeps each image's build cache in the GitHub Actions cache (scope
# <name>-<image>) for `make release-dry`; needs the Actions runtime env (crazy-max/ghaction-github-runtime).
RELEASE_CACHE ?=
comma := ,
RELEASE_CACHE_FLAGS = $(if $(RELEASE_CACHE),--cache-from type=gha$(comma)scope=$(RELEASE_CACHE)-$$img --cache-to type=gha$(comma)mode=max$(comma)scope=$(RELEASE_CACHE)-$$img)

RELEASE_BUILDX = docker buildx build --builder $(RELEASE_BUILDER) --platform $(PLATFORMS) \
  --build-arg VERSION=$(VERSION) $(RELEASE_BUILD_ARGS) \
  --label org.opencontainers.image.version=$(VERSION) \
  --label org.opencontainers.image.revision=$(RELEASE_REVISION) \
  --label org.opencontainers.image.source=$(RELEASE_SOURCE)

##@ Release

.PHONY: release release-images release-chart release-dry release-check release-builder release-builder-rm

release: release-images release-chart ## Build and push multi-arch images and OCI charts (REGISTRY=, VERSION=, PLATFORMS=).
	@echo "released $(VERSION) to $(REGISTRY):"; cat $(RELEASE_DIR)/images.txt $(RELEASE_DIR)/charts.txt

release-check:
	@test -n "$(REGISTRY)" || { echo "REGISTRY is required, e.g. REGISTRY=<acct>.dkr.ecr.<region>.amazonaws.com/kerf"; exit 1; }
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
# its trailing /kerf, and each component chart's image.repository becomes $(REGISTRY)/<name>.
# A user then installs the chart with no registry setting. Explicit values still win.
# The umbrella's Artifact Hub annotations follow the release too: every `image:` of artifacthub.io/images
# becomes $(REGISTRY)/<component>:$(VERSION), and artifacthub.io/prerelease is "true" for a -suffix version.
RELEASE_CHART_DEFAULT_REGISTRY ?=
RELEASE_IMAGE_HOST = $(patsubst %/kerf,%,$(REGISTRY))
RELEASE_PRERELEASE = $(if $(findstring -,$(VERSION)),true,false)

.PHONY: release-chart-package
release-chart-package:
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$' || \
	  { echo "VERSION must be semver without a leading v or build metadata (got '$(VERSION)')"; exit 1; }
	@if [ "$(RELEASE_CHART_DEFAULT_REGISTRY)" = 1 ]; then \
	  case "$(REGISTRY)" in */kerf) ;; *) echo "RELEASE_CHART_DEFAULT_REGISTRY=1 needs REGISTRY=<host>[/path]/kerf (got '$(REGISTRY)')"; exit 1 ;; esac; fi
	@rm -rf $(RELEASE_DIR)/charts $(RELEASE_DIR)/_stage; mkdir -p $(RELEASE_DIR)/charts $(RELEASE_DIR)/_stage
	@for c in $(RELEASE_CHARTS); do \
	  dir=$(RELEASE_CHART_DIR)/$$c; test -f $$dir/Chart.yaml || { echo "no chart at $$dir"; exit 1; }; \
	  : "Package a staged copy: links resolved (the umbrella's charts/ links the component charts)."; \
	  : "_stage: go ./... skips _-prefixed dirs, so the copied *_test.go never joins make ci."; \
	  stage=$(RELEASE_DIR)/_stage/$$c; cp -RL $$dir $$stage || exit 1; find $$stage -name "*_test.go" -delete; \
	  deps=""; grep -q '^dependencies:' $$stage/Chart.yaml && deps=--dependency-update; \
	  if [ -d $$stage/charts ]; then \
	    : "Every subchart at version/appVersion VERSION (its image tags default to it)."; \
	    for sub in $$stage/charts/*/Chart.yaml; do \
	      sed -e 's/^version:.*/version: $(VERSION)/' -e 's/^appVersion:.*/appVersion: "$(VERSION)"/' $$sub > $$sub.new && mv $$sub.new $$sub; \
	    done; deps=""; \
	    : "Artifact Hub metadata of the umbrella: the images of this release, pre-release for a -suffix version."; \
	    sed -E -e 's|^( +image: ).*/([a-z-]+):[^ ]+$$|\1$(REGISTRY)/\2:$(VERSION)|' \
	      -e 's|^  artifacthub.io/prerelease: .*|  artifacthub.io/prerelease: "$(RELEASE_PRERELEASE)"|' \
	      $$stage/Chart.yaml > $$stage/Chart.yaml.new && mv $$stage/Chart.yaml.new $$stage/Chart.yaml; \
	    if [ "$(RELEASE_CHART_DEFAULT_REGISTRY)" = 1 ]; then \
	      sed -e 's|^  imageRegistry: ""|  imageRegistry: "$(RELEASE_IMAGE_HOST)"|' $$stage/values.yaml > $$stage/values.yaml.new && \
	        mv $$stage/values.yaml.new $$stage/values.yaml; \
	    fi; \
	  else \
	    : "A component chart: its image tag defaults to the chart appVersion (= VERSION), not the dev tag."; \
	    sed -E -e 's|^  tag: dev$$|  tag: ""|' \
	      $(if $(filter 1,$(RELEASE_CHART_DEFAULT_REGISTRY)),-e 's|^  repository: [^ ]+/kerf/([a-z-]+)$$|  repository: $(REGISTRY)/\1|',) \
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
	  $(RELEASE_BUILDX) $(RELEASE_CACHE_FLAGS) -f $$df --output type=cacheonly $$ctx; \
	done
	@echo "release-dry OK: $(RELEASE_IMAGES) built for $(PLATFORMS); charts in $(RELEASE_DIR)/charts"

# Release notes (tools/release/notes.sh, cliff.toml). The commit list needs git-cliff (an installed one, else the official
# container image, pinned by digest). VERSION must be given: it names the tag the commit range ends at.
.PHONY: release-notes changelog

RELEASE_NOTES_VERSION_CHECK = @case "$(origin VERSION)" in "command line"|environment) ;; \
  *) echo "VERSION is required, e.g. make $@ VERSION=0.2.0"; exit 1 ;; esac

release-notes: ## Print the GitHub Release notes of VERSION=X.Y.Z[-rc.N] (a local run lists the commits without authors).
	$(RELEASE_NOTES_VERSION_CHECK)
	@tools/release/notes.sh "$(VERSION)" $(if $(REGISTRY),"$(REGISTRY)")

changelog: ## Print the changes since the previous release as Keep a Changelog sections (VERSION=X.Y.Z), to curate into CHANGELOG.md.
	$(RELEASE_NOTES_VERSION_CHECK)
	@tools/release/notes.sh --changelog "$(VERSION)"
