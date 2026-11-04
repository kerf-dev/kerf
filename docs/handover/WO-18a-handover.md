# WO-18a hand-over (hub: 2+ replicas)

Branch `wo-18a-hub-ha` (from main 0fa222b). D-028. Not deployed: the orchestrator deploys the hub
from `main` after merging (migration `0012` applies to the shared database).

## What changed

**Leader election** (`internal/hub/leader.go`, `internal/hub/ha.go`, `cmd/hub/main.go`)
- Every replica runs an `Elector` on one dedicated Postgres connection
  (`application_name = ro-hub-elector:<pod>`). It `LISTEN`s on channel `ro_hub` and, while
  following, tries `pg_try_advisory_lock(8245859369407884620)` (`store.LeaderLockKey`,
  "ro-hub-L") every `ha.leaderRetry` (5 s).
- The holder is the leader and runs `Server.RunLeader`: recommender schedule + backfill
  (`Job.Loop`), recompute requests (`Job.RecomputeLoop`), readiness (hourly metrics checks,
  minute agent checks, re-check expiry, readiness gauges), metrics snapshot refresh, recompute
  request pruning (7 d).
- The leader pings its lock connection every `ha.leaderCheck` (2 s, timeout 2 s). A failed ping or
  a broken connection = leadership lost: the work's context is cancelled, the elector waits for
  it to stop, reconnects after 1 s and follows.
- Lock connection: server-side `tcp_keepalives_idle=4, interval=2, count=2, tcp_user_timeout=8000`
  and client-side keepalives (4 s/2 s/2). Postgres releases the lock at once when the pod dies
  (socket closed; test with `pg_terminate_backend`: takeover 4.3–4.7 s, bounded by the 5 s retry), within ~8 s when its node or network dies. The old leader stops (≤ 4 s) before
  Postgres can hand the lock on (≥ 6 s). Followers retry every 5 s: failover ≤ ~13 s.
- Graceful stop (SIGTERM): stop the leader work, `pg_advisory_unlock`, `NOTIFY ro_hub 'released'`
  → a follower takes the lock at once (test: 70 ms). Then the HTTP server drains (10 s).
- One replica (dev): it takes the lock at start, so it leads at once, as before.
- On every takeover (`Job.Takeover`): runs left `running` by a dead leader are marked `failed`
  ("interrupted: the hub leader changed during the run"); recompute requests it was running are
  marked failed and queued again (same scope and `requestedAt`); the `ro_recommender_*` per-cluster
  gauges are seeded from the database.
- Run at start: on becoming leader the recommender runs at once (`runOnStart`), **unless a regular
  run succeeded within the last min(interval, 5 m)**; the next run then comes one interval after
  that run. A rolling update that moves the leadership twice so runs once, not twice.
- Logs (INFO, field `pod` = `POD_NAME` or the hostname): `hub leadership acquired`,
  `hub leadership lost` (`reason`).

**Migrations** — `store.Migrate` uses goose's Postgres session locker (`lock.NewPostgresSessionLocker`,
goose's default lock id, retry every 1 s for up to 10 min). The other start-up writes (dev seed,
bootstrap admin, session prune, local datasource upsert, readiness gauges) run under a second
advisory lock (`store.InitLockKey`), one replica at a time.

**Recompute requests in the database** — migration `db/migrations/0012_recompute_requests.sql`
(table `recompute_requests`, unique partial index = one queued request per scope). Any replica
inserts (`QueueRecompute`, coalescing into a queued request of the same scope) and sends
`NOTIFY ro_hub 'recompute'`; the leader runs it at once when notified, else within one poll
(`ha.recomputePoll`, 2 s). The request is claimed (`started_at`) only once the run lock is held,
so coalescing works exactly as before. The request id is the run id. API response, log line and
`api/openapi/hub.yaml` are unchanged (there is no audit event for recompute, before or now).

**Health** — `/readyz` unchanged: ready once migrations (and the start-up writes) are done and
Postgres answers; leadership plays no part. `/healthz` unchanged.

**Metrics** (additive contract change, `docs/contracts/metrics.md` §3 "Hub replicas"):
- New `ro_hub_leader` (gauge 0/1) and `ro_hub_leader_transitions_total` (counter, every change
  of `ro_hub_leader` on that replica). No labels (no `cluster`: they describe the hub process).
- Database-state series are exported **by the leader only** (otherwise N replicas scraped per pod
  by the `annotated-services` endpoints job export N copies): all `ro_recommendation_*`,
  `ro_workload_mode`, `ro_workload_phase`, `ro_cluster_readiness*`,
  `ro_recommender_last_success_timestamp_seconds`, `ro_recommender_pricing_fallback`,
  `ro_recommender_recommendations`. Per-process series stay on every replica and add up:
  `ro_actions_total`, `ro_hub_http_*`, `ro_recommender_runs_total`, `ro_recommender_run_duration_seconds`.
  The dashboards already `sum(increase(ro_actions_total…))` and `max by (…)` the gauges, so they
  need no change.

## Audit: in-memory state behind the API (deliverable 2)

| Found | Where | Fixed / why it is fine |
|---|---|---|
| Recompute queue (`Job.pending`, goroutine per request) | `job.go` | **Fixed**: table `recompute_requests`, run by the leader. |
| Recommender interval reset (`PUT /v1/admin/settings/recommender` → `Job.SetInterval`, in-memory channel) | `admin_settings.go` | **Fixed**: the handler sends `NOTIFY 'settings'`; the leader reloads the interval from the settings. A new leader loads it at start anyway. |
| Readiness triggers (`triggerChecks` → `Runner.Trigger`, in-memory channel; registration, binding, datasource change, re-check) | `onboarding.go` | **Fixed**: on a follower they become `NOTIFY 'readiness:<cluster>'`; the leader triggers its runner. Best effort, as before (the hourly run and the run at takeover catch up). |
| Bearer-token cache (30 s); revocation cleared it only on the replica that served it | `authn.go` | **Fixed**: revocations (token revoke, cluster delete) send `NOTIFY 'tokens'`; every replica drops its cache. While a replica's notification connection is down it doesn't use the cache. |
| Readiness gauges set by whichever replica stored a result | `readiness/metrics.go` | **Fixed**: leader-only export, refreshed from the database every minute (`Gauges.Retain` drops deregistered clusters). |
| Recommendation metrics snapshot (`Metrics.snapshot`), per-cluster run gauges | `metrics.go` | **Fixed**: leader-only export; snapshot refreshed by the leader every minute (agent reports on a follower show within a minute); run gauges seeded from the database at takeover. |
| Incomplete-metrics baseline (`Job.baseline/incomplete`) | `job.go` | Leader-only by design; a new leader starts from the stored live inventory, as after a restart (the grace period restarts). |
| Heartbeat throttle (`Server.heartbeats`, ≤ 1 write / 15 s / cluster) | `server.go` | Per replica, harmless: each replica writes at most every 15 s. |
| Login rate limiters (`ipLimiter`, `userLimiter`) | `authn.go` | Per replica: with N replicas the per-minute limits are N× as loose. The account lockout (5 failures → 15 min) is in the database and holds across replicas. Accepted, not changed. |
| Datasource HTTP-client cache | `datasource/registry.go` | Re-validated against the stored version on every call: safe. |
| `api_tokens.last_used_at` throttle | store | In SQL: safe. |
| Agent pull/prune bookkeeping, sessions, approvals, `resumeAt`, lockouts | store | In the database: safe. |

## For WO-18b (chart) — values the hub now needs or allows
- `POD_NAME` env from the downward API (`fieldRef: metadata.name`); without it the hostname is
  used, which is the pod name anyway.
- `strategy: Recreate` is no longer needed; `RollingUpdate` is safe. `terminationGracePeriodSeconds`
  ≥ 30 (leader stop ≤ 15 s + HTTP drain 10 s).
- The database URL must reach Postgres directly or through a **session**-mode pooler: the leader
  lock and `LISTEN` need a real session (not PgBouncer transaction mode / RDS Proxy pinning-free
  mode). RDS directly is fine. PostgreSQL ≥ 12 (`tcp_user_timeout`).
- Optional hub config keys (defaults fine): `ha.leaderRetry` (5s), `ha.leaderCheck` (2s),
  `ha.recomputePoll` (2s).
- A PDB with `minAvailable: 1` for 2 replicas.

## Live failover check on k3d (for the orchestrator, after deploying main)

```bash
export KCTX=k3d-ro-hub NS=resource-optimizer
# 1. Two replicas (until WO-18b's chart sets replicas/strategy, scale by hand; a later helm upgrade resets it).
kubectl --context $KCTX -n $NS scale deploy/hub --replicas=2
kubectl --context $KCTX -n $NS rollout status deploy/hub --timeout=120s
kubectl --context $KCTX -n $NS get pods -l app.kubernetes.io/name=hub -o wide

# 2. Who leads? Per pod (ro_hub_leader 1 on exactly one) ...
# (the image is distroless: read /metrics through the API server's pod proxy)
for p in $(kubectl --context $KCTX -n $NS get pods -l app.kubernetes.io/name=hub -o jsonpath='{.items[*].metadata.name}'); do
  echo "$p: $(kubectl --context $KCTX get --raw "/api/v1/namespaces/$NS/pods/$p:8080/proxy/metrics" | grep -E '^ro_hub_leader(_transitions_total)? ' | tr '\n' ' ')"
done
# ... and in Postgres (the holder of the leader lock):
kubectl --context $KCTX -n postgres exec postgres-0 -- psql -U ro -d resource_optimizer -Atc \
  "SELECT a.application_name, a.client_addr FROM pg_locks l JOIN pg_stat_activity a USING (pid)
   WHERE l.locktype = 'advisory' AND l.granted AND l.classid = 1919888744 AND l.objid = 1969368396"
kubectl --context $KCTX -n $NS logs -l app.kubernetes.io/name=hub --since=10m | grep -E 'leadership|migrations applied'

# 3. Watch the leader series (VictoriaMetrics, 30 s scrape) in a second terminal:
watch -n 5 "curl -s http://localhost:18428/api/v1/query --data-urlencode 'query=ro_hub_leader' | jq -c '.data.result[] | [.metric.kubernetes_pod, .value[1]]'"

# 4a. Hard kill the leader (no graceful step-down); the other pod must log 'hub leadership acquired' within 15 s.
LEADER=$(kubectl --context $KCTX -n postgres exec postgres-0 -- psql -U ro -d resource_optimizer -Atc \
  "SELECT substr(a.application_name, 16) FROM pg_locks l JOIN pg_stat_activity a USING (pid)
   WHERE l.locktype = 'advisory' AND l.granted AND l.classid = 1919888744 AND l.objid = 1969368396")
echo "leader: $LEADER"; date -u +%T
kubectl --context $KCTX -n $NS delete pod "$LEADER" --grace-period=0 --force
kubectl --context $KCTX -n $NS logs -l app.kubernetes.io/name=hub --since=1m -f | grep --line-buffered -E 'leadership|recommender run finished|interrupted'

# 4b. Graceful (SIGTERM): handover is immediate ('released' notification).
kubectl --context $KCTX -n $NS delete pod <current-leader-pod>

# 5. Recompute through whichever pod the Service picks (repeat a few times): the leader runs it.
curl -s -X POST -H "Authorization: Bearer $(kubectl --context $KCTX -n $NS get secret hub-dev-tokens -o jsonpath='{.data.admin}' | base64 -d)" \
  http://localhost:18080/v1/admin/recompute | jq .
kubectl --context $KCTX -n postgres exec postgres-0 -- psql -U ro -d resource_optimizer -Atc \
  "SELECT id, cluster, requested_at, started_at, finished_at, error FROM recompute_requests ORDER BY requested_at DESC LIMIT 5"

# 6. Back to one replica.
kubectl --context $KCTX -n $NS scale deploy/hub --replicas=1
```
Expected: exactly one pod with `ro_hub_leader 1`; after 4a the other pod logs `hub leadership
acquired` within 15 s of the kill (`sum(ro_hub_leader)` is 0 for at most one or two scrapes) and
the recommender keeps its cadence (no run twice per interval: `SELECT started_at FROM
recommender_runs WHERE NOT backfill ORDER BY started_at DESC LIMIT 5`); after 5 every request has
`finished_at` within seconds.

## Verification (run on this branch)
The shared Postgres (localhost:15432, NodePort on ro-hub) kept dropping while other tracks ran
multi-arch builds, so the Postgres tests ran against a private throwaway container instead
(`docker run --name ro-wo18a-pg -p 127.0.0.1:25432:5432 postgres:18.6-alpine`, removed after).
```bash
RO_TEST_DATABASE_URL=postgres://ro:ro@127.0.0.1:25432/postgres?sslmode=disable make ci       # exit 0, golangci-lint 0 issues, 28 packages ok
RO_TEST_DATABASE_URL=postgres://ro:ro@127.0.0.1:25432/postgres?sslmode=disable \
  go test ./internal/hub/ -run 'Leader|Migrate|Recompute' -count=1 -race -v                 # ok, 3 runs in a row
```
| Test (`internal/hub/ha_test.go`) | Result |
|---|---|
| `TestLeaderTwoHubs` — 2 hubs, interval 2 s, 3.5 intervals | PASS: 3–4 regular runs (one hub's worth), one recommendation id per workload per run, follower never loads metrics, `sum(ro_hub_leader)=1`, both serve the API and `/readyz`, state series on the leader only |
| `TestLeaderFailover` — `pg_terminate_backend` of the leader's elector connection, production retry/check | PASS: takeover 4.0–4.7 s; the new leader runs the next interval; the old one stops loading metrics, reconnects as follower (transitions 2); graceful stop hands over in 60–70 ms |
| `TestMigrateConcurrent` — 2 `Migrate` at once on an empty DB | PASS: no errors, each of the 12 migrations applied once, one caller applied them all |
| `TestRecomputeViaFollower` | PASS: 202 from the follower, the leader ran it 110–360 ms later (via `NOTIFY`), request row finished, run covers c1 only |
| `TestLeaderInterruptedRecompute` | PASS: a `running` run is marked failed at takeover, its recompute is queued again and served |
| `TestLeaderFollowerTokenRevocation` | PASS: a token revoked through one replica is refused at once by the other, which had cached it |
| `TestRecompute` (existing, now through the table) | PASS |

Also fixed on the way: a data race on `Metrics.lastRefresh` between `RefreshLoop` and `Refresh`
(present on main; `-race` caught it in the new tests).
