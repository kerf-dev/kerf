# WO-02 synthetic metrics (tools/synth). See tools/synth/README.md.

SYNTH_VM_URL   ?= http://localhost:18428
SYNTH_SCENARIO ?= tools/synth/scenarios/fleet-default.yaml
SYNTH_DAYS     ?=
SYNTH_FLAGS    ?=
SYNTH_BIN      := $(LOCALBIN)/synth
# Live mode keeps its binary, scenario copy, pid and log here (outside the checkout, so it keeps
# running when a worktree is removed).
SYNTH_LIVE_DIR ?= $(HOME)/.cache/kerf/synth-live
# The GPU scenario (cluster synth-gpu) runs its own live mode next to the default fleet's.
SYNTH_GPU_SCENARIO ?= tools/synth/scenarios/fleet-gpu.yaml
SYNTH_GPU_LIVE_DIR ?= $(HOME)/.cache/kerf/synth-live-gpu

synth_live_running = [ -f $(SYNTH_LIVE_DIR)/pid ] && kill -0 $$(cat $(SYNTH_LIVE_DIR)/pid) 2>/dev/null

##@ Synthetic data (WO-02)

.PHONY: synth-build
synth-build: ## Build the synth CLI into ./bin/synth.
	$(GO) build $(GOFLAGS) -o $(SYNTH_BIN) ./tools/synth

.PHONY: synth-backfill
synth-backfill: synth-build ## Replace synth-a/synth-b in VictoriaMetrics with the default fleet (30d up to now), then run live mode.
	@if $(synth_live_running); then kill $$(cat $(SYNTH_LIVE_DIR)/pid); fi
	$(SYNTH_BIN) backfill --scenario $(SYNTH_SCENARIO) --vm-url $(SYNTH_VM_URL) --replace \
	  $(if $(SYNTH_DAYS),--days $(SYNTH_DAYS)) $(SYNTH_FLAGS)
	@$(MAKE) --no-print-directory synth-live SYNTH_SCENARIO=$(SYNTH_SCENARIO)

.PHONY: synth-backfill-small
synth-backfill-small: SYNTH_SCENARIO = tools/synth/scenarios/fleet-small.yaml
synth-backfill-small: synth-backfill ## Same with the small pattern scenario (synth-<pattern>-0 workloads).

.PHONY: synth-backfill-gpu
synth-backfill-gpu: ## Replace synth-gpu with the GPU scenario (30d up to now), then run its live mode; synth-a/synth-b are left alone.
	@$(MAKE) --no-print-directory synth-backfill SYNTH_SCENARIO=$(SYNTH_GPU_SCENARIO) SYNTH_LIVE_DIR=$(SYNTH_GPU_LIVE_DIR)

.PHONY: synth-live
synth-live: synth-build ## (Re)start live mode in the background: append new samples every minute.
	@mkdir -p $(SYNTH_LIVE_DIR)
	@if $(synth_live_running); then kill $$(cat $(SYNTH_LIVE_DIR)/pid); sleep 1; fi
	@cp $(SYNTH_BIN) $(SYNTH_LIVE_DIR)/synth
	@cp $(SYNTH_SCENARIO) $(SYNTH_LIVE_DIR)/scenario.yaml
	@nohup $(SYNTH_LIVE_DIR)/synth live --scenario $(SYNTH_LIVE_DIR)/scenario.yaml --vm-url $(SYNTH_VM_URL) \
	  >> $(SYNTH_LIVE_DIR)/live.log 2>&1 & echo $$! > $(SYNTH_LIVE_DIR)/pid
	@sleep 2; if $(synth_live_running); then \
	  echo "synth live running (pid $$(cat $(SYNTH_LIVE_DIR)/pid), log $(SYNTH_LIVE_DIR)/live.log)"; \
	  else echo "synth live failed to start:"; tail -n 5 $(SYNTH_LIVE_DIR)/live.log; exit 1; fi

.PHONY: synth-live-stop
synth-live-stop: ## Stop live mode.
	@if $(synth_live_running); then kill $$(cat $(SYNTH_LIVE_DIR)/pid) && echo "synth live stopped"; else echo "synth live not running"; fi

.PHONY: synth-live-stop-gpu
synth-live-stop-gpu: ## Stop the live mode of the GPU scenario.
	@$(MAKE) --no-print-directory synth-live-stop SYNTH_LIVE_DIR=$(SYNTH_GPU_LIVE_DIR)

.PHONY: synth-live-status
synth-live-status: ## Show whether live mode runs, with its last log lines.
	@if $(synth_live_running); then echo "running (pid $$(cat $(SYNTH_LIVE_DIR)/pid))"; else echo "not running"; fi
	@tail -n 5 $(SYNTH_LIVE_DIR)/live.log 2>/dev/null || true

.PHONY: synth-verify
synth-verify: synth-build ## Check with PromQL that the scenario is in VictoriaMetrics with the expected shapes.
	$(SYNTH_BIN) verify --scenario $(SYNTH_SCENARIO) --vm-url $(SYNTH_VM_URL)

.PHONY: synth-verify-gpu
synth-verify-gpu: SYNTH_SCENARIO = $(SYNTH_GPU_SCENARIO)
synth-verify-gpu: synth-verify ## Same for the GPU scenario (node GPUs, requests, DCGM series).

.PHONY: synth-describe
synth-describe: synth-build ## Show what a scenario expands to (nodes, workloads, series volume).
	$(SYNTH_BIN) describe --scenario $(SYNTH_SCENARIO) $(if $(SYNTH_DAYS),--days $(SYNTH_DAYS))

.PHONY: synth-delete
synth-delete: synth-build synth-live-stop ## Stop live mode and delete every synth-a/synth-b series.
	$(SYNTH_BIN) delete --vm-url $(SYNTH_VM_URL)

.PHONY: synth-fixtures
synth-fixtures: ## Regenerate the unit-test fixtures in testdata/series.
	$(GO) run ./tools/synth fixtures --clean --out testdata/series

.PHONY: test-synth
test-synth: ## Run the synth unit tests.
	$(GO) test $(GOFLAGS) ./tools/synth/...
