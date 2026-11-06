# Kerf web UI

The primary interface for platform and FinOps teams (D-014): where the savings are, why each
recommendation was made, and what automation did.

| Page                                                          | What it answers                                                                                                                                                                                                                                                                                     |
| ------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Overview** `/`                                              | Monthly savings, CPU/memory requested vs. used vs. recommended, risk counts, savings trend and per-cluster split, top 10 opportunities, modes and phases.                                                                                                                                           |
| **Opportunities** `/opportunities`                            | Every workload with its latest recommendation. Filters (cluster, namespace, kind, mode, risk, min savings, search), server-side sort, column chooser, CSV export. All state lives in the URL, so views can be shared.                                                                               |
| **Workload** `/workloads/<cluster>/<namespace>/<kind>/<name>` | Current vs. recommended per container, usage charts (usage, P95/P99, request, limit, recommendation), "Why this recommendation" (statistic → headroom → rounding → limit rule), confidence and data coverage, risk flags explained, effective modes and the policy that set them, history timeline. |
| **Clusters** `/clusters`                                      | Agent heartbeat, version, coverage, auto-patch/blocked counts and savings per cluster.                                                                                                                                                                                                              |
| **Activity** `/activity`                                      | Audit log across clusters with type, cluster, namespace, workload and time filters, plus a volume chart.                                                                                                                                                                                            |

**Admin area (D-022…D-025).** Signed-in operators and admins also get:

| Page                                                          | What it does                                                                                                                                                                                                                   | Role                            |
| ------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------- |
| **Automation** `/admin`                                       | Pause / resume per cluster and fleet-wide (reason, auto-resume), sync and readiness per cluster                                                                                                                                | operator (resume `prod`: admin) |
| **Policies** `/admin/policies`                                | Hub + Git policies (badges), editor with selector builder and live match list, effective-policy simulator, version history with diff and revert, YAML export                                                                   | view: operator, edit: admin     |
| **Defaults** `/admin/defaults`                                | Global defaults form with built-in hints and an impact preview before saving                                                                                                                                                   | admin                           |
| **Clusters** `/clusters/<name>`, `/clusters/new`              | Readiness checklist with remediations and re-check, cluster defaults, agent token create/rotate/revoke (shown once), install snippets (Helm / values / Argo CD), onboarding wizard with a metrics test before any agent exists | viewer / operator / admin       |
| **Recommender**, **Pricing**, **Metrics sources**, **Access** | Profiles + Recompute now; discounts and fixed rates; datasources with test; users and API tokens                                                                                                                               | admin                           |
| **Audit** `/admin/audit`                                      | Every config change with actor, reason and a before/after diff                                                                                                                                                                 | operator                        |

Write actions in existing pages: the workload **Automation** card (override modes/strategy/profile,
Exclude, Clear override, Approve once, "why these modes"), bulk **Set mode… / Exclude** in Opportunities
(with count and savings impact), and an Overview banner for clusters with blocking readiness failures.
Every write asks for a reason (audit log), sends `If-Match` (a `412` shows "someone else changed this"
with Reload), and asks to type the cluster name for `prod` clusters. Admins can override
`409 cluster_not_automation_ready` with an audited checkbox.

**Copy as…** on the workload page and the Opportunities bulk bar hands a recommendation over as a strategic merge patch,
a `kubectl patch` command, a Kustomize patch or Helm values, for one workload or a selection (all roles; see
[docs/copy-as-patch.md](../docs/copy-as-patch.md)). `npm run test:patch-dry-run` checks the output against a cluster
with `--dry-run=server`.

Press <kbd>⌘K</kbd> anywhere to search workloads and namespaces. Light, dark and system themes.

## Develop

```bash
make web-dev        # Vite on :5173 with the MSW mock API (2 clusters × ~200 workloads, no hub needed)
make web-dev-live   # Vite on :5173, /v1 proxied to the real hub (KERF_HUB_URL, default http://localhost:18080)
make web-verify     # API types check, lint, prettier, typecheck, unit + contract tests, build, Playwright smoke
make deploy-web     # build + push localhost:5005/kerf/web, helm install into ro-hub → http://localhost:19090
```

In mock mode, the **Mock API** menu in the top bar switches to an empty fleet, a slow hub or a failing
hub. Sign in first, like with a real hub. Mock users: `admin`, `operator`, `viewer` (password
`dev-password`; `sam` must change the password, `dana` is disabled).

## How it fits together

- **Typed client.** `src/api/schema.d.ts` is generated from `api/openapi/hub.yaml` (`npm run gen:api`).
  `npm run gen:api:check` fails when it is stale. Calls go through `openapi-fetch` (`src/api/client.ts`)
  and TanStack Query hooks (`src/api/queries.ts`).
- **Workload ids** travel as one URL-encoded path segment (`/` → `%2F`). The client guards this. UI routes
  use the readable `/workloads/<cluster>/<ns>/<kind>/<name>` form (`src/lib/workload-id.ts`).
- **Mock API** (`src/mocks/`) is a deterministic, seeded fleet, including the admin endpoints (session,
  roles, CSRF, If-Match, readiness, preview). `handlers.test.ts` and `admin.test.ts` validate every mock
  response against `hub.yaml` with a JSON Schema validator, so the UI built on the mocks matches the real hub.
  The service worker is preferred. Where service workers are unavailable, it falls back to patching `fetch`.
- **Sign-in is required** ([docs/auth.md](../docs/auth.md)). `POST /v1/auth/login` sets the HttpOnly
  `kerf_session` cookie; `GET /v1/me` gives the role and the CSRF token, which the client sends as
  `X-CSRF-Token` on writes (`src/api/auth.ts`). The app shell renders nothing but a loading screen until
  `/v1/me` answers. A `401` sends the browser to `/login?next=<page>`, with a "Your session ended" notice
  when the tab had a session; after sign-in it returns to `next` (same-origin paths only, `safeNext`).
- **No token in the browser or the proxy.** The browser only talks to its own origin. nginx proxies `/v1/`
  (all methods) and forwards only the `kerf_session` cookie: never a bearer token, and never what the
  browser sends in `Authorization`. The raw `$request_uri` is passed through so `%2F` is never decoded.
  `X-Forwarded-For` is replaced by the client nginx sees (behind `TRUSTED_PROXIES`, the address they add),
  so the hub's per-address sign-in limits cannot be dodged. In live dev mode the Vite proxy forwards the
  cookie the same way.

## Production image and chart

- `Dockerfile`: a Node 22 build stage, then `nginx:1.29-alpine`. Config lives in `nginx/`. `05-real-ip.envsh`
  turns `TRUSTED_PROXIES` into nginx `set_real_ip_from` lines, and the nginx template is rendered at start.
  `/healthz` is the probe endpoint.
- `deploy/charts/web`: Deployment and Service (plus optional ingress, HPA, PDB, NetworkPolicy).
  - `hub.upstream` is the hub Service's host:port. It must be fully qualified, because nginx resolves it at
    request time and does not apply search domains.
  - `trustedProxies`: CIDRs of the ALB or ingress controller in front (docs/auth.md).
  - The removed `auth.anonymousRead` and `token` make the chart fail with an upgrade message.
  - `values-dev.yaml` sets NodePort 30090 (host port 19090 on k3d).
- **Browser tests through a real proxy** (`e2e-live/`, `playwright.live.config.ts`): sign-in, the way back
  to the page asked for, sign-out, an ended session and an anonymous visit that sees nothing, against
  `E2E_WEB_URL` (default the dev fleet, http://localhost:19090). The Go e2e suite runs them
  (`TestWebSignInBrowser`).

## Stack

React 19, TypeScript (strict), Vite 7, Tailwind CSS v4, shadcn/ui-style components on Radix, TanStack
Query + Table, Apache ECharts, React Router 7, MSW 2, Vitest, Playwright. Fonts (Geist) are bundled, so
there are no external requests.
