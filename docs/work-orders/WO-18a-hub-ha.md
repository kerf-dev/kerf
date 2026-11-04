# WO-18a Hub: run with 2+ replicas

- **Phase / wave:** 5 / 1 (parallel with WO-18b and WO-18c)
- **Depends on:** main at the commit that adds this file.
- **Owns:** `internal/hub/**`, `internal/recommender/**` (only where scheduling is touched), `cmd/hub/**`, `db/migrations/0012_*.sql` (number assigned).
- **Does not own:** `deploy/charts/**`, which belongs to WO-18b. Report any chart value you need, e.g. a new env var.
- **Decisions:** D-028 (hub HA). Audit: `docs/work-orders/WO-18-eks-packaging.md` section 1.

## Problem
Today two hub pods on one database would:
- both run the recommender scheduler, the readiness metrics checks, the recheck expiry, the prune and sweep jobs and any other ticker-driven work, so everything happens twice and recommendation ids churn;
- both run the goose migrations at start with no lock (`internal/hub/store/store.go`, `goose.NewProvider` without a session locker);
- queue "Recompute now" (`POST /v1/admin/recompute`) in the memory of whichever pod got the request.

## Deliverables
1. **Leader election on Postgres.**
   - One dedicated connection holds `pg_try_advisory_lock(<constant key>)`. The holder is the leader and runs every background loop. The others retry every 5 s.
   - Losing the connection means losing leadership: stop the loops (cancel their context) and retry.
   - Failover takes 15 s or less after the leader pod is killed. With a hard kill, Postgres releases the lock when the TCP connection closes; set a keepalive or `tcp_user_timeout` so a dead peer is detected within that bound.
   - With one replica (dev), behaviour is unchanged: it becomes leader at start.
2. **Every replica serves the whole API.** Nothing request-scoped may depend on in-memory state that only the leader has. Audit every handler for caches and in-memory queues, and list what you found and fixed in the handover.
3. **Migrations under a lock.** Use goose's Postgres session locker (`goose.WithSessionLocker` with the Postgres locker), so two pods starting together apply each migration once and the second waits.
4. **Recompute requests in the database.** Migration `0012_recompute_requests.sql` (or extend an existing table additively).
   - The API inserts a request. The leader picks it up within one poll (5 s or less) and marks it done.
   - The API response and the audit event stay as they are (contract `api/openapi/hub.yaml` unchanged).
5. **Metrics and health.**
   - New metrics: `ro_hub_leader` (gauge 0/1) and `ro_hub_leader_transitions_total`.
   - `/readyz` doesn't depend on leadership. Every replica is ready once migrations are done and Postgres is reachable.
   - Add both metrics to `docs/contracts/metrics.md`. That's an additive contract change: state it in your report.
6. **Logs:** log leadership changes at INFO with the pod name (`POD_NAME` env, falling back to the hostname).

## Verification
```bash
make ci
go test ./internal/hub/... -run 'Leader|Migrate|Recompute' -count=1 -v    # Postgres tests (testcontainers or the existing harness)
```
Required tests:
- **Two-hub test:** two hub `Server`s on one database for 3 recommender intervals. Recommender runs are counted once per interval, not twice, and recommendation ids don't churn more than with one hub.
- **Failover:** stop the leader's lock connection; the second instance takes over within 15 s and runs the next interval.
- **Concurrent migrate:** two `Migrate` calls in parallel on an empty database; both return without error, and each migration is applied once.
- **Recompute through the follower:** a recompute posted to the non-leader runs on the leader.

Live check on k3d: don't deploy the hub yourself; the orchestrator deploys it after merging. Document the exact commands the orchestrator should run to scale the hub to 2, kill the leader, and watch `ro_hub_leader` fail over.

## Rules
Branch `wo-18a-hub-ha`; push the branch, don't push main, don't open a PR. Write `docs/handover/WO-18a-handover.md`. Never deploy the hub; the shared database is affected.
