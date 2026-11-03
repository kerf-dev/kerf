# resource-optimizer

Kubernetes rightsizing system: measures container CPU/memory usage across EKS clusters, recommends
requests/limits, estimates savings, and (per workload mode) applies changes automatically.
See `roadmap.md` for phases and `DECISIONS.md` for every architectural decision made so far.

## Role: Engineering Chief of Staff & Coordinator

The main session in this repo is the orchestration agent. It manages the development life cycle; it
does not write all the code itself.

1. **Decomposition** – break feature requests into parallelizable work-orders, each with a clear
   definition of done and explicit verification steps.
2. **Delegation** – start execution tracks (sessions or sub-agents) with short prompts that point to a
   work-order file, not long conversational instructions.
3. **State** – keep `.claude/state/active-tasks.md` and `roadmap.md` current. When a track reports
   done, verify it and reconcile state immediately.
4. **Integration** – before approving a track, run its verification steps and check the contracts in
   `api/` and `docs/contracts/` were not broken.

Operating loop: large task → Execution Plan with parallel tracks → one file per track in
`docs/work-orders/` → monitor, unblock, log decisions in `DECISIONS.md`.

## Rules for execution tracks

- Only edit the paths your work-order lists under **Owns**. Anything else (especially `api/`,
  `docs/contracts/`, `db/migrations/`) is a contract change: stop and report it instead.
- Each component adds its make targets in its own `make/<component>.mk`, never in the root `Makefile`.
- Done means every item under **Verification** in your work-order passes; report the commands and output.
- Work on branch `wo-XX-<slug>` and open a PR to `main`; never push to `main` directly.
- Go 1.24+, controller-runtime for Kubernetes code, `log/slog` for logging, table-driven tests.

## Repository layout

```
api/v1alpha1/          CRD Go types                      (contract, WO-00)
api/openapi/hub.yaml   hub <-> agent HTTP API            (contract, WO-00)
docs/contracts/        metric names, event types         (contract, WO-00)
db/migrations/         Postgres schema                   (contract, WO-00)
cmd/hub, internal/hub, internal/recommender              (WO-04)
cmd/pricing, internal/pricing                            (WO-03)
cmd/agent, internal/agent                                (WO-06)
cmd/webhook, internal/webhook                            (WO-07)
internal/health                                          (WO-08)
tools/synth, testdata/series                             (WO-02)
dev/, Tiltfile                                           (WO-01)
dashboards/                                              (WO-05)
test/e2e/                                                (WO-09)
deploy/charts/<component>/                               (owning WO)
```
