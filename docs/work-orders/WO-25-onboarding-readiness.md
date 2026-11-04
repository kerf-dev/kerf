# WO-25 Cluster onboarding, metrics sources and readiness

- **Phase / wave:** 4 / 2
- **Depends on:** WO-19 merged. Builds on WO-20 (hub) and WO-21 (agent): start from main after they merge, or coordinate files with them.
- **Owns:**
  - hub: `internal/hub/datasource/`, `internal/hub/readiness/`, `internal/hub/install/` (new), plus the binding-aware query builder in `internal/recommender/queries.go`
  - agent: `internal/agent/selfcheck/` (new)
  - pricing: `cmd/pricing`, `internal/pricing` (datasource-aware discovery)
  - charts for those three
- **Decisions:** D-022, D-025. Plan: `docs/plans/cluster-onboarding.md` (sections 2–5)

## Deliverables
- **Datasource registry:** Prometheus/VictoriaMetrics/Thanos/Mimir/AMP, with auth none/bearer/basic/SigV4/mTLS and custom headers. Secrets are encrypted at rest with a key from a Secret and are never returned by the API. Test endpoint.
- **Per-cluster metrics binding:**
  - The recommender, usage endpoint and pricing build every selector from the binding (label key, value and extra matchers) and re-label the results to the registered cluster name.
  - Without any configuration, the current behaviour (`local` datasource, `cluster` label) is unchanged.
- **Metrics checks:** every row of plan section 4 "Metrics", with remediation texts. They run at registration, on demand and every hour, and are stored with history.
- **Agent self-checks:** every row of plan section 4 "Cluster": RBAC per capability via SelfSubjectAccessReview, resize discovery, webhook health with a dry-run pod create, Argo CD `argocd-cm` check, swap/static managers, clock skew. They run at start, every 10 minutes and on the recheck flag, and are reported in the status batch.
- **Readiness:** the state machine Registered → Agent connected → Metrics OK → Ready (audit) → Automation ready. `auto-patch` is gated on Automation ready (hub API refuses it unless an admin overrides with a reason, which is audited). The metric `ro_cluster_readiness{cluster,check,severity}` is exported. State transitions are emitted as audit events.
- **Install renderer:** Helm command / values file / Argo CD Application, plus the Argo CD `ignoreDifferences` snippet; the token appears once.

## Definition of done
On k3d:
1. Register `ro-spoke-2` again through the API with a custom label binding; the metrics checks pass.
2. Scale kube-state-metrics on ro-spoke-2 to 0 → a blocking check within one check cycle. Restore it → pass.
3. Remove `pods/resize` from the agent ClusterRole → the cluster drops to Ready (audit) and auto-patch is refused. Restore it.
4. A `synth-a` binding with a renamed label (relabelled copy, or a matcher on a fixture) produces the same recommendations as before.

## Verification
```bash
make ci
go test ./internal/hub/datasource/... ./internal/hub/readiness/... ./internal/agent/selfcheck/... -cover
curl -s -b jar localhost:18080/v1/clusters/ro-spoke-2/readiness | jq '.state, [.checks[] | select(.status!="pass")]'
```
