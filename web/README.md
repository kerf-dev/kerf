# resource-optimizer web UI (WO-14)

The primary interface for platform and FinOps teams (D-014): where the savings are, why each
recommendation was made, and what automation did.

| Page                                                          | What it answers                                                                                                                                                                                                                                                                                     |
| ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Overview** `/`                                              | Monthly savings, CPU/memory requested vs. used vs. recommended, risk counts, savings trend and per-cluster split, top 10 opportunities, modes and phases.                                                                                                                                           |
| **Opportunities** `/opportunities`                            | Every workload with its latest recommendation. Filters (cluster, namespace, kind, mode, risk, min savings, search), server-side sort, column chooser, CSV export. All state lives in the URL, so views can be shared.                                                                               |
| **Workload** `/workloads/<cluster>/<namespace>/<kind>/<name>` | Current vs. recommended per container, usage charts (usage, P95/P99, request, limit, recommendation), "Why this recommendation" (statistic → headroom → rounding → limit rule), confidence and data coverage, risk flags explained, effective modes and the policy that set them, history timeline. |
| **Clusters** `/clusters`                                      | Agent heartbeat, version, coverage, auto-patch/blocked counts and savings per cluster.                                                                                                                                                                                                              |
| **Activity** `/activity`                                      | Audit log across clusters with type, cluster, namespace, workload and time filters, plus a volume chart.                                                                                                                                                                                            |

Press <kbd>⌘K</kbd> anywhere to search workloads and namespaces. Light, dark and system themes.

## Develop

```bash
make web-dev        # Vite on :5173 with the MSW mock API (2 clusters × ~200 workloads, no hub needed)
make web-dev-live   # Vite on :5173, /v1 proxied to the real hub (RO_HUB_URL, default http://localhost:18080)
make web-verify     # API types check, lint, prettier, typecheck, unit + contract tests, build, Playwright smoke
make deploy-web     # build + push localhost:5005/resource-optimizer/web, helm install into ro-hub → http://localhost:19090
```

In mock mode, the **Mock API** menu in the top bar switches to an empty fleet, a slow hub or a failing
hub. Use it to check empty, loading and error states.

## How it fits together

- **Typed client.** `src/api/schema.d.ts` is generated from `api/openapi/hub.yaml` (`npm run gen:api`).
  `npm run gen:api:check` fails when it is stale. Calls go through `openapi-fetch` (`src/api/client.ts`)
  and TanStack Query hooks (`src/api/queries.ts`).
- **Workload ids** travel as one URL-encoded path segment (`/` → `%2F`). The client guards this. UI routes
  use the readable `/workloads/<cluster>/<ns>/<kind>/<name>` form (`src/lib/workload-id.ts`).
- **Mock API** (`src/mocks/`) is a deterministic, seeded fleet. `handlers.test.ts` validates every mock
  response against `hub.yaml` with a JSON Schema validator, so the UI built on the mocks matches the real hub.
  The service worker is preferred. Where service workers are unavailable, it falls back to patching `fetch`.
- **No token in the browser.** The browser only talks to its own origin. In production, nginx proxies
  `/v1/` to the hub and adds `Authorization: Bearer <ui token>` from a mounted Secret. Only GET/HEAD are
  forwarded, cookies are dropped, and the raw `$request_uri` is passed through so `%2F` is never decoded.
  In live dev mode, the Vite proxy does the same with `RO_HUB_TOKEN`.

## Production image and chart

- `Dockerfile`: a Node 22 build stage, then `nginx:1.29-alpine`. Config lives in `nginx/`. `05-hub-token.envsh`
  reads the token file (`HUB_TOKEN_FILE`), and the nginx template is rendered at start. `/healthz` is the
  probe endpoint.
- `deploy/charts/web`: Deployment, Service and an optional token Secret.
  - `hub.upstream` is the hub Service's host:port. It must be fully qualified, because nginx resolves it at
    request time and does not apply search domains.
  - `token.existingSecret` / `token.value` supply the `ui` token.
  - `values-dev.yaml` sets NodePort 30090 (host port 19090 on k3d) and the hub's dev `ui` token.

## Stack

React 19, TypeScript (strict), Vite 7, Tailwind CSS v4, shadcn/ui-style components on Radix, TanStack
Query + Table, Apache ECharts, React Router 7, MSW 2, Vitest, Playwright. Fonts (Geist) are bundled, so
there are no external requests.
