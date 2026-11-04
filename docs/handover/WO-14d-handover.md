# WO-14d handover (web UI polish, recommendation history)

Branch `wo-14d-web` (from origin/main 828458d), head `c0f842b`, pushed. Deployed: release `web` =
`web:wo14d-2` at http://localhost:19090 (hub `wo12-dd3a826` at the time).

## What changed
- Usage chart draws the WO-04c recommendation step series with a dot per change (tooltip "from → to",
  time), an end label with the current value, legend "Recommendation (changed N× in range)". P95/P99
  labels moved below their lines. The single-point fallback (labelled level line) is kept for old hubs.
- Top bar: "Fleet savings $X/mo" on every page (link to Opportunities; tooltip with $/yr, CPU/memory
  split, scope). Read-only — no write actions added (D-019 recompute stays admin/CLI).
- formatPercent never shows 0%/100% for partial ratios ("<1%", ">99%"); Overview 30d change uses the
  signed delta ("±0%", "+2%"); sparkline starts at the first non-zero trend point.
- Clusters: skeleton while savings load (cards briefly rendered without savings).
- Opportunities at tablet width: confidence/risk hidden by default (<1280 px), mode/phase too (<1024 px),
  so request columns are in view; only explicit column choices persist. Count no longer spins forever
  on errors.
- Activity: no empty volume chart above the empty state. Error states: 5xx titles "The hub is
  unavailable" / "The hub returned an error" + "usually temporary" hint.
- `make web-screenshots` → `docs/screenshots/` (6 pages light+dark, 5 tablet, mock API).

## Verification (all exit 0)
`make web-verify`: gen:api:check up to date, eslint + prettier, tsc, vitest 4 files / 49 tests, build,
Playwright 11/11 (2 new: header, recommendation legend; tablet columns; cluster savings).
`npm audit`: 0. Docker image: `/` serves `<div id="root">`, SPA routes 200, `/v1` 502 without hub.
Live via :19090: `/v1/summary`, `/v1/workloads/facets`, encoded synth-a usage → 200.

## Findings for other tracks (live data, 2026-11-04 ~04:20–04:57 UTC)
1. WO-04 trend: the 00:00 UTC bucket of 2026-11-04 drops to ~0 (synth-a 718 vs ~6,400, synth-b 0 vs
   ~3,400; ro-spoke-* start that hour), visible as a spike down at the right edge of the 30d chart.
2. WO-04 history: node-metrics-agent has runs at 2026-11-04T01:35Z with monthlySavingsUSD all 0 and
   pricing.source "default" (×7 consecutive) between normal runs ($250–296, source metrics). Backfill
   runs also report pricing.source "default".
3. Hub outage ~04:33–04:50 UTC: `hub:wo11-1` queried `recommendations.hpa` before its migration ran →
   every endpoint 500; agents logged `HubUnreachable` blocked events. Recovered with `hub:wo12-dd3a826`.
4. Activity at 04:43 UTC still shows a workload's `skipped/ModeAudit` twice in one minute
   (argocd-application-controller, ro-spoke-1) — re-check after the WO-08b agent redeploy. The UI
   collapses repeats either way.
