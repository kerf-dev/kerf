# WO-14c hand-over (web polish)

Branch `wo-14c-web-polish` (pushed, head cb6c3b4): f382510 deps + live fixes, 72fe98e merge of wo-04b-hub-fixes
(63cf1a0, the same SHA as on origin; merged rather than rebased because wo-04b branched from an older main),
cb6c3b4 D-016 UI. Deployed: release `web`, image web:wo14c-2, http://localhost:19090, against hub:wo04b-2.

## Verification (all pass)
npm ci (0 vulnerabilities, from 18 incl. 2 critical) · lint + prettier · typecheck · vitest 39/39 ·
build · gen:api:check · e2e 9/9 · docker build/run/curl OK.
Live hub responses (summary, clusters, facets, workloads, detail, usage, history, trend, audit) validated
against the merged hub.yaml.

## Dependency notes
echarts 6.1, vitest 5, eslint 10, jsdom 30. `overrides: { minimatch: ^11.0.4 }` fixes the moderate ReDoS chain.
redocly 1.x (inside openapi-typescript 7, the latest) calls minimatch only for remote $ref URL matching, which our
local codegen never reaches. gen:api:check passes, and it would fail loudly if that changed.

## Findings for other tracks
1. WO-06 agent: `skipped/ModeAudit` is re-emitted for every workload on every loop (04:05, 04:10, …),
   which contradicts D-017 (once per workload/type/reason). Also a `released/ModeChanged` event was stored twice
   within the same second for e2e-smoke-1/web. The UI now collapses both cases, but the audit log grows ~50
   rows per 5 min.
2. WO-04 trend: the last point equals the summary now (verified for all 4 clusters). The backfilled history is
   ~40× lower than current values (synth-a ≈ $150 from 28 Oct to 3 Nov, then $6.4k), so the 30d chart is
   flat and then jumps.
3. WO-04 usage: `recommendation` comes back as a single point at "now" rather than a step series over the
   window. The UI draws it as a labelled level, but a series built from the recommendation history would let
   the chart show how the recommendation moved.
4. The hub recommends every 5 min in dev with identical values, so the UI collapses unchanged runs (×N).
