# WO-24 hand-over (admin area: end-to-end)

Branch `wo-24-admin-e2e` from origin/main b92ee28. Owns `test/e2e/`, `make/e2e.mk` (plus this
hand-over). Decisions D-022..D-027. Suite docs: `test/e2e/README.md` ("Admin area").
Nothing was deployed and no product code was changed.

## What was added

- `test/e2e/admin_client_test.go`: session client (login as the dev users `viewer`/`operator`/
  `admin`, `ro_session` cookie, `X-CSRF-Token`, one session per user for the whole suite, logout
  at the end), helpers for policies, approvals, pause, clusters, readiness, config audit, the
  spoke-2 lock and the hub sweep. Types come from `internal/hub/hubapi`, so the suite fails to
  compile if the contract changes under it.
- `test/e2e/admin_test.go`: the scenarios below.
- `TestHubUnreachable` now takes the spoke-2 lock around its apply and its outage.
- `make e2e-clean` also sweeps the hub; `make e2e-unblock` (run before and after every `make e2e`)
  also restores the agent's `pods/resize` rule and a scaled-down kube-state-metrics.
- `E2E_PARALLEL` default 16 → 32 (more tests; those waiting for the spoke-2 lock hold a slot).

| WO # | Test | Where | Notes |
|---|---|---|---|
| 1, 4 | `TestAdminPolicy` | spoke-1 | create (audit) → hub CR v1 → update auto-patch (If-Match) → CR v2, `Applied` → actor in `PolicyCreated/Updated` → revert to v1 → v3, CR audit, agent resolves audit, `PolicyReverted` → delete → CR gone |
| 2 | `TestAdminPause` | spoke-2 | operator pause; waits until the agent applied the paused config before switching a workload to auto-patch; one `blocked/Paused`; rollback (restarts) still happens; resume → `Applied`; audit rows name the operator |
| 3 | `TestApproveOnce` | spoke-1 | operator approve, `applied/ApprovedOnce`, in-place, approval `consumed`, `WorkloadApproved` row; next recommendation id is not applied (pods and applied-event count unchanged for 105 s) |
| 5 | `TestGitPolicyUntouched` | spoke-1 | hub policy with a Git CR's name: CR untouched (generation, UID, labels, annotations, spec) also after the hub policy is deleted; inventory: Git item valid, hub item invalid with generation 0 |
| 6 | `TestAdminRoles` | test cluster | viewer 403 on writes; operator 403 on global defaults, policies, settings; 403 `csrf_invalid` without the token; operator pause/resume of a non-prod cluster |
| 7 | `TestApproveOnceArgoCD` | spoke-1 | approve once, rollout, Argo CD self-heal app: agent keeps ownership, hard sync + 75 s self-heal keep the value |
| 8 | `TestOnboarding` | `e2e-onb-*` | datasource test with ro-spoke-1's label, registration with the binding, recheck runs metrics checks, state `Registered`, 409 `cluster_not_automation_ready`, operator override 403, admin override 201 + `after.overrideReadiness`, cluster deleted |
| 9 | `TestReadiness/{ro-spoke-1,ro-spoke-2}` | both | recheck answered by the agent → `AutomationReady`, all capabilities granted |
| 10 | `TestReadinessRBAC` | spoke-2 | `pods/resize` rule removed → `ReadyAudit`, `rbac.resize` fail, `ReadinessChanged` (system/readiness), 409 on auto-patch; restored (cleanup registered before the change, also `make e2e-unblock`) → `AutomationReady` |
| 11 | `TestReadinessKSMDown` | spoke-2 | skipped unless `RO_E2E_DISRUPTIVE=1`; **not run** (no approval to scale kube-state-metrics) |
| 12 | – | – | optional Playwright smoke: not done |

**Spoke-2 lock.** A pause is cluster-wide, a missing `pods/resize` fails in-place resizes, and the hub
outage hides rechecks, so these phases are serialised by one in-process mutex: the hub outage, the
pause section, the RBAC section, the KSM section, the spoke-2 readiness check, and the two "switch to
auto-patch → Applied" phases on spoke-2. Setup (deploy, wait for recommendations) runs in parallel.
The spoke-1 admin workloads request 100m (not 250m) to stay within spoke-1's 8 cores.

**Hub objects.** Tests delete the policies and clusters they create and resume what they paused
(`t.Cleanup`). Before the scenarios, and in `make e2e-clean`, the suite sweeps policies and clusters
named `e2e-*` and pauses whose reason starts with `e2e ` (left by a killed run).

**Run marker and field ownership** (after the orchestrator's note of 12:2x UTC). An agent deploy
to ro-spoke-2 at 11:48 UTC ran during the RBAC scenario of the quick run (that scenario creates no
namespace, so the pre-deploy check saw nothing). Its `helm upgrade` failed on a ClusterRole conflict
with field manager `e2e.test`: a plain update by the suite owns the atomic `rules` list. The quick run
itself passed. Now:
- TestMain creates `e2e-run-<id>` on both spokes at the start and deletes it at the end (stale markers
  of killed runs are deleted at the next start and by `make e2e-clean`).
- All suite writes use field manager `resource-optimizer-e2e`; after restoring the agent ClusterRole,
  the agent token Secret, the webhook Deployment or kube-state-metrics, the suite removes its
  managedFields entry (JSON patch of `metadata.managedFields`, guarded by resourceVersion). Values stay;
  Helm's next server-side apply takes the fields back without a conflict (checked with a server
  dry-run apply as `helm`).
- `make e2e-unblock` drops leftover suite entries (`resource-optimizer-e2e` and the old `e2e.test`).
- Leftovers of the earlier runs removed by hand: `e2e.test` on the spoke-2 agent ClusterRole, the
  spoke-2 agent token Secret and the spoke-1 webhook Deployment (the last two from WO-09's scenarios).
- Check: `make e2e E2E_RUN='TestReadinessRBAC|TestAdminRoles'` PASS (73 s); marker present during the
  run and terminating after; ClusterRole managedFields afterwards: `helm Apply` only.

## Results (shared environment, 2026-11-04 UTC)

Hub and agents main-5d25dd7, web main-cbc813c, both spokes AutomationReady at the start.

| Run | Result | Time |
|---|---|---|
| quick (`TestAdminRoles|TestOnboarding|TestReadiness`) | PASS | 1.7 min |
| 1 | PASS — every test (3 skips: JVM memory on swap nodes, KSM without the flag, the sweep helper) | 17m59s |
| 2 (back to back) | PASS — same | 16m38s |
| final 1 (main-1e72c9e merged, 12:37) | 2 failures: `TestApproveOnce` (approval superseded, test race, fixed in a713fd7), `TestWebhook/webhook_down…` (agent drift-repair bug above) | 14m06s |
| final 2a (a713fd7, 12:52) | PASS (two approvals superseded and approved again) | 14m43s |
| final 2b (back to back) | `TestApproveOnceArgoCD`: a suite-triggered run 32 s after the approved apply gave a newer recommendation with other values; the agent ended the D-027 hold as designed and Argo CD started self-healing. Fixed in 4b508d5 (recomputes held for the whole test; a newer recommendation with other values ends the self-heal check) | 14m37s |
| **final 3a** (4b508d5, 13:22 UTC) | **PASS** — all tests (skips: JVM memory on swap nodes, KSM without the flag, the sweep helper); approvals superseded once each and approved again | **16m10s** |
| **final 3b** (back to back) | **PASS** | **14m06s** |

Final `make e2e && make e2e` on 4b508d5 (origin/main 98c1edd merged; hub, web and agents main-1e72c9e):
30m16s wall for the pair, each run 14–16 min. Afterwards: no `e2e-*` hub policies or pauses, no e2e
namespaces or CRs, only `helm` (and `k3s`) field managers on the agent ClusterRole, token Secret,
webhook and kube-state-metrics on both spokes, both spokes AutomationReady (ro-spoke-2 recovers
~20 s after a run: the hub-outage scenario ends last).

`make e2e && make e2e`: 34 min wall in total; each run is well under 30 min (go test 1072 s and
984 s). The longest test is still WO-09's `TestHubUnreachable` (its outage plus waiting for the
spoke-2 lock); the admin scenarios finish within 10 min.

No leftovers after the runs: no `e2e-*` hub policies, no pause, no e2e namespaces or CRs, the
`pods/resize` rule in place on both spokes, both spokes AutomationReady; the sweep at the start of
run 2 found nothing to clean.
Each run leaves two soft-deleted clusters (`e2e-roles-*`, `e2e-onb-*`) in the hub database: the API
has only a soft delete.

## Findings for other tracks

- **Agent bug: drift repair stops after the next recommender run.** `internal/agent/controller/
  reconciler.go:479` repairs drift (`keepLive`) only while `rsr.Status.AppliedRecommendationID ==
  rec.ID`. The hub gives every workload a new recommendation id on every run, even with the same
  values; a new recommendation within the 5% tolerance is `Skipped/NoSignificantChange` and never
  reaches `keepLive`. So pods created while the webhook was down (or any in-place drift) stay at the
  template values from the next run on: in production, drift repair works until the next daily run.
  Seen in the first final run on main-1e72c9e (12:40 UTC): `TestWebhook/webhook_down_does_not_block_
  pod_creation` failed because a hub run landed 30 s after the apply. Suggested fix: repair while
  Applied and the new recommendation is within tolerance of the applied values (like WO-21b's
  "new id **and** different values" rule for the approve-once hold). The suite now holds its own
  recompute requests during that window and logs "KNOWN AGENT BUG" when the hub's periodic run still
  lands in it; it does not hide the failure.
- **D-027 hold is short on a busy hub**: the agent released the approved field 32 s after the
  apply because a recommender run issued a newer recommendation with other values (WO-21b's rule).
  On Argo CD apps with self-heal, approved values then last until the next run whose values differ
  (in production: usually the next daily run). Working as designed; worth stating in the approve UI.
- **Approvals are superseded by every recommender run** (by design: only the latest recommendation
  can be approved). With the suite's recompute requests every 2 minutes, `TestApproveOnce` lost
  its approval to a run between the approval and the agent's next pull (first final run). The
  suite now holds its recompute requests while it waits and approves again when the hub reports the
  approval `superseded` (up to 3 times). In production, with daily runs, the window is a pull
  interval; the UI may want to say that an approval is void after the next run.

- **Gating of cluster-less policies (WO-25, by design):** a policy without `clusters` (or a global
  default) is gated against every registered cluster. In dev that includes `synth-a`/`synth-b`,
  which are never AutomationReady, so any fleet-wide auto-patch policy is refused unless an admin
  overrides it. The suite always lists clusters explicitly. Worth a line in the admin UI.
- **Onboarding a cluster bound to an existing spoke's label** makes the hub recommend that spoke's
  workloads a second time under the new name until the cluster is deleted (the WO asks for this
  binding; the scenario lasts a few seconds).
- Readiness timings seen live: recheck → agent answer + new state 30–50 s; `pods/resize` removed →
  `ReadyAudit` in 30 s; restored → `AutomationReady` in 50 s.

## Not done

- Scenario 11 not run (needs `RO_E2E_DISRUPTIVE=1` and the user's OK); compiled and skipped.
- Scenario 12 (Playwright smoke) not written (optional).
