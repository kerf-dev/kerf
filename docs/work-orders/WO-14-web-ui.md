# WO-14 Web UI

- **Phase / wave:** 1 / 1 (pulled forward from phase 4)
- **Depends on:** WO-00 (`api/openapi/hub.yaml` incl. the UI read endpoints). Develops against a mock API until WO-04 lands.
- **Owns:** `web/`, `deploy/charts/web/`, `make/web.mk`
- **Decisions:** D-005, D-014

## Goal
A modern, fast web app where platform and FinOps teams see savings opportunities, understand why each recommendation was made, and see what automation did.

## Stack (D-014)
React 19 + TypeScript (strict), Vite, Tailwind CSS v4, shadcn/ui components, TanStack Query and TanStack Table,
Apache ECharts for charts, React Router. Typed client generated from `api/openapi/hub.yaml` with `openapi-typescript` +
`openapi-fetch`. Mock API with MSW, seeded with realistic data for 2 clusters × ~200 workloads. Light and dark theme.

## Pages
1. **Overview** — KPI tiles (monthly savings estimate, CPU/memory requested vs. used vs. recommended, workloads by mode, risk count), savings by cluster, savings trend, top 10 opportunities.
2. **Opportunities** — the main table: filters (cluster, namespace, kind, mode, risk flag, min savings, search), sort, column chooser, CSV export, URL-synced state, row → detail.
3. **Workload detail** — per container: current vs. recommended (requests/limits) with delta and savings; usage charts (usage, P95/P99 lines, request, limit, recommendation) with time-range picker; "Why this recommendation" panel (window, percentiles, headroom, profile, strategy, confidence breakdown); risk flags explained; effective CPU/memory modes and which policy set them; history timeline (recommendations and audit events).
4. **Clusters** — health of each cluster's agent (last heartbeat, version), counts, savings.
5. **Activity** — audit log with filters (applied, skipped, blocked, fallback, rolled back, released, failed).

Empty, loading and error states everywhere; keyboard accessible; works at 1280px and down to tablet width.

## Delivery
- Multi-stage Dockerfile → static files served by nginx; `/api/*` proxied to the hub service.
- Helm chart `deploy/charts/web`; Tilt picks it up automatically (WO-01 Tiltfile convention).
- `make web-dev` runs Vite with MSW mocks; `make web-dev-live` proxies to a real hub.

## Definition of done
All pages work against the mock API with realistic data; the production build runs in the local hub cluster and talks to the real hub when it exists.

## Verification
```bash
cd web && npm ci && npm run lint && npm run typecheck && npm run test -- --run && npm run build
npx playwright install chromium && npm run e2e        # smoke: each page renders, table filter + sort, detail charts render
docker build -t resource-optimizer/web:dev . && docker run --rm -d -p 8088:80 resource-optimizer/web:dev && curl -sf localhost:8088/ | grep -q '<div id="root">'
```
