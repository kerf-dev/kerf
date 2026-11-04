# WO-03 hand-over (pricing service)

Branch `wo-03-pricing` (worktree `.claude/worktrees/wo-03-pricing`). Deployed to ro-hub as Helm release
`pricing`, namespace `resource-optimizer`. ClusterIP Service `pricing:9090` with prometheus.io scrape
annotations, scraped by the hub vmagent's `annotated-services` job (honor_labels: true keeps `cluster`).
No NodePort (30090 is reassigned to the web UI). For a host view, use
`kubectl --context k3d-ro-hub -n resource-optimizer port-forward svc/pricing 9090`.

## Series (docs/contracts/metrics.md section 2, unchanged)
`ro_{node,cpu_core,memory_gib}_hourly_cost_usd{cluster,node,instance_type,capacity_type,region,zone,provider}`.
There is one series per node of every cluster that has `kube_node_labels`, including ro-spoke-* and synth-*.
The rates are per **allocatable** vCPU and GiB. The invariant is exact: cpu*alloc_cores + mem*alloc_gib == node.
Ops series carry `cluster="ro-hub"`: `ro_pricing_nodes{status,reason}`, `ro_pricing_refreshes_total{result}`,
`ro_pricing_last_success_timestamp_seconds`, `ro_pricing_refresh_duration_seconds`.

## Refresh
Every refresh re-runs node discovery over **all** clusters. The interval is 1m in dev
(values-dev.yaml) and 5m by default (`interval`). After a failed refresh it retries in 15s.
New clusters and nodes show up on the next refresh with no restart and no config change.
This was verified live: synth-a and synth-b, 5 nodes each, appeared without any change.
Prices are cached for 24h (`priceTTL`). vmagent scrapes every 30s.

## For the hub (WO-04)
- **Always aggregate with `max by (cluster, node)(...)` before counting or joining.** The vmagent scrape
  adds `instance`, `job` and `kubernetes_pod`, so for about 5 min after a pricing rollout the old pod's
  series coexist with the new ones (2 series per node).
- Savings rate: join on `(cluster, node)` through `kube_pod_info` (pod -> node), or use a per-cluster
  average such as `avg by (cluster)(ro_cpu_core_hourly_cost_usd)`. Use `max by (cluster,node)(...)` before
  joining to drop the extra labels.
- **k3d caveat:** the real spokes' allocatable is the Docker VM's (all host cores and RAM), not the
  m6i.xlarge/c6a.2xlarge shape. So the per-core and per-GiB rates on ro-spoke-* are far lower than on
  real EKS. synth-* nodes report EKS-like allocatable, so their rates are realistic.
- Weight: by default, 1 vCPU ≈ 10.3 GiB, derived from the c/m/r prices of the 6i and 6a families.
  The fallback is 9. `--cpu-memory-weight` fixes it.
- A node stays priced for `--node-lookback` after its last sample. The default is 10m; dev uses 24h
  so synth-* (a backfill) stays priced for a day after the backfill ran. Re-run the WO-02 backfill or
  a live mode to keep synth nodes current beyond that.
- Unpriced nodes do not get series. They are counted in `ro_pricing_nodes{status="skipped",reason}`,
  where the reasons are no-instance-type|no-region|unknown-capacity-type|no-allocatable|no-price|provider-error.

## dev/prices.yaml
Covers eu-west-1, on-demand plus spot (spot can be set per zone), for these families:
m5/m6i/m6a/m6g/m7i, c5/c6i/c6a/c6g/c7i and r5/r6i/r6a/r6g/r7i, in sizes large to 4xlarge.
It covers every type WO-02 knows: m6i.xlarge, m6i.2xlarge, c6a.2xlarge and r6i.xlarge.
The image bakes it in at /etc/resource-optimizer/prices.yaml. The chart value `prices` overrides it.

## AWS provider
`--provider aws` uses the default credential chain (IRSA through `serviceAccount.annotations`). It needs
`pricing:GetProducts` and `ec2:DescribeSpotPriceHistory`. Prices are cached for 24h, and the stale
price is served on transient errors. The tests replay recorded HTTP bodies
(`internal/pricing/testdata/aws`) through the real SDK clients. Those bodies were assembled offline
in the AWS wire format, because this environment has no AWS access. Re-capture them once real access exists.

## Environment note
During my deploy, VictoriaMetrics on ro-hub was OOMKilled repeatedly (limit is set in
dev/helm/victoria-metrics.yaml, WO-01), most likely during the WO-02 30-day backfill.
