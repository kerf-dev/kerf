# WO-02 synthetic metrics (tools/synth). See tools/synth/README.md.

SYNTH_VM_URL   ?= http://localhost:18428
SYNTH_SCENARIO ?= tools/synth/scenarios/fleet-default.yaml
SYNTH_DAYS     ?=
SYNTH_FLAGS    ?=
SYNTH_BIN      := $(LOCALBIN)/synth

##@ Synthetic data (WO-02)

.PHONY: synth-build
synth-build: ## Build the synth CLI into ./bin/synth.
	$(GO) build $(GOFLAGS) -o $(SYNTH_BIN) ./tools/synth

.PHONY: synth-backfill
synth-backfill: synth-build ## Replace synth-a/synth-b in VictoriaMetrics with the default fleet (30d up to now).
	$(SYNTH_BIN) backfill --scenario $(SYNTH_SCENARIO) --vm-url $(SYNTH_VM_URL) --replace \
	  $(if $(SYNTH_DAYS),--days $(SYNTH_DAYS)) $(SYNTH_FLAGS)

.PHONY: synth-backfill-small
synth-backfill-small: SYNTH_SCENARIO = tools/synth/scenarios/fleet-small.yaml
synth-backfill-small: synth-backfill ## Replace synth-a/synth-b with the small pattern scenario (synth-<pattern>-0 workloads).

.PHONY: synth-describe
synth-describe: synth-build ## Show what a scenario expands to (nodes, workloads, series volume).
	$(SYNTH_BIN) describe --scenario $(SYNTH_SCENARIO) $(if $(SYNTH_DAYS),--days $(SYNTH_DAYS))

.PHONY: synth-delete
synth-delete: synth-build ## Delete every synth-a/synth-b series from VictoriaMetrics.
	$(SYNTH_BIN) delete --vm-url $(SYNTH_VM_URL)

.PHONY: synth-fixtures
synth-fixtures: ## Regenerate the unit-test fixtures in testdata/series.
	rm -f testdata/series/*.json
	$(GO) run ./tools/synth fixtures --out testdata/series

.PHONY: test-synth
test-synth: ## Run the synth unit tests.
	$(GO) test $(GOFLAGS) ./tools/synth/...
