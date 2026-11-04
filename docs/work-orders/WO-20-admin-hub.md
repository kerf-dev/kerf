# WO-20 Admin area: hub

- **Phase / wave:** 4 / 1
- **Depends on:** WO-19 merged
- **Owns:** `internal/hub/` (new packages `auth`, `admin`, `config`), `internal/recommender/` (read settings from DB), `deploy/charts/hub/`, `make/hub.mk`
- **Decisions:** D-022, D-023, D-024

## Deliverables
- **Auth:**
  - local users (bcrypt cost ≥12)
  - bootstrap admin from a Secret on an empty DB
  - dev chart seeds `viewer`, `operator`, `admin`
  - sessions in Postgres, HttpOnly + Secure (configurable for http dev) + SameSite=Strict cookie
  - CSRF double-submit
  - login rate limit + lockout after N failures
  - role checks per the contract
  - every write records `config_changed` with actor and reason
- **Settings and policies:**
  - versioned store, optimistic locking via ETag/If-Match, soft delete, revert
  - shared validator (WO-19)
  - `ro-global-defaults` and `ro-cluster-defaults` are created on first start from today's built-in defaults and the cluster's chart defaults (if reported)
- **Preview and effective policy:** compute using the hub's workload inventory and the agents' reported policy inventory (Git policies included). This must be the same precedence algorithm as the agent: reuse the agent's `policy` package or move it to a shared package.
- **Agent config endpoint:** policies for that cluster (global + cluster + workload overrides + cluster-scoped selectors), pause flag, open approvals; ETag changes only when content changes.
- **Recommender** reads profiles, interval and retention from settings (hot reload on change); pricing adjustments are applied to savings and stored in `recommendations.pricing`.
- **Clusters and tokens:** register (returns the agent token once + Helm snippet values), rotate, revoke, delete (soft), metadata.

## Definition of done
All admin endpoints are implemented per the contract, with ≥85 % coverage on new packages. The Postgres tests pass. Live on ro-hub: login, change settings, recompute, and the config ETag changes.

## Verification
```bash
make test-hub && RO_TEST_DATABASE_URL=... go test -race ./internal/hub/... ./internal/recommender/...
make lint
curl -c jar -d '{"username":"admin","password":"..."}' localhost:18080/auth/login   # + admin calls with CSRF
```
