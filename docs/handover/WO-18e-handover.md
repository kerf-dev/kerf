# WO-18e handover: staging install that follows the EKS guide

Run by the orchestrator, 2026-11-05, on main after WO-18d (7df084b). D-028.

**Result:** `docs/install/eks.md` installs and runs as written, apart from the AWS stand-ins listed below.
- The hub install is HA and connects to Postgres with `sslmode=verify-full`.
- The workload cluster reaches *Automation ready*, and one workload was auto-patched in place.
- A killed hub leader is replaced in under a second.
- A `helm upgrade` drops no requests. That needed one chart fix: a `preStop` sleep on the hub and web pods.

## How to run it

```bash
make install-staging STAGING_VERSION=0.1.0-rc.5        # releases the version if missing, clusters, steps 1-5
make install-staging-verify STAGING_VERSION=0.1.0-rc.5 # leader, HTTPS, AutomationReady, auto-patch, failover
make install-staging-upgrade STAGING_VERSION=0.1.0-rc.4 STAGING_UPGRADE_VERSION=0.1.0-rc.5   # probe: no 5xx
make install-staging-uninstall                         # step 9
make install-staging-down                              # delete both clusters and bin/staging
```

The install-staging targets, in `make/install.mk`, call `test/install/staging.sh`:
- `up`: the clusters and the guide's step 1
- `install`: the guide's steps 3, 4 and 5
- `verify`, `upgrade <v>`, `uninstall`, `down`
- single steps: `step3`, `step4`, `step5`, `autopatch`, `failover`, `readiness`

The script prints the guide's **Check** output at each step. Generated files and the fake Secrets Manager content live in `bin/staging/`, which is gitignored.

## Layout

| Guide | Staging |
|---|---|
| hub cluster (`mode: hub`) | k3d `ro-stg-hub`: 1 server + 1 agent in zones `eu-west-1a`/`eu-west-1b` (the hub spreads across them), API `127.0.0.1:16447` |
| workload cluster (`mode: spoke`) | k3d `ro-stg-spoke`: 1 server, API `127.0.0.1:16448` |
| | Both clusters are on the dev network `ro-dev` and use the shared registry. Memory: about 4.5 GiB next to the dev fleet (the three node containers: 1.9 + 1.9 + 0.7 GiB). |

## Stand-ins (the only deviations from the guide)

| Guide step | AWS piece | Stand-in | Real EKS install does |
|---|---|---|---|
| 0 | `aws sts`, `aws eks update-kubeconfig` | fixed `ACCOUNT=000000000000`, contexts `k3d-ro-stg-*` | as written |
| 1 | AWS LB Controller, Pod Identity Agent, VPC CNI policies | k3s's bundled traefik and servicelb; kube-router enforces NetworkPolicies | as written |
| 1 | Prometheus with kube-state-metrics and cAdvisor | spoke: kube-state-metrics + vmagent (external label `cluster`), remote-writing to a VictoriaMetrics on the hub cluster (`vmsingle.monitoring.svc:8428` = the guide's default `METRICS_URL`) and to a local one on **9090** named `prometheus-operated` (= the guide's `SPOKE_METRICS_URL`) | your Prometheus stack |
| 2 | ECR | `make release REGISTRY=localhost:5005/resource-optimizer`. The clusters pull the same registry as `k3d-ro-registry:5005`, so the values files' registry host is replaced with that. Helm uses `--plain-http`. | as written |
| 3.1, 3.6 | VPC CIDRs, security groups | `HUB_VPC_CIDR` = `SPOKE_VPC_CIDR` = `10.42.0.0/16`, the k3s pod CIDR. Traefik's pods and the API server (from `cni0`) come from it, so the NetworkPolicies are exercised with real allow-lists. | as written |
| 3.2 | RDS PostgreSQL 16 | CloudNativePG `ro-hub` in namespace `rds`, TLS on, with a managed master role `ro_admin` (`manifests/postgres.yaml`). The guide's `psql-setup` command runs **as written** against `ro-hub-rw.rds.svc`. | as written |
| 3.3, 3.4 | Secrets Manager, ESO IAM | ESO 2.14 **Fake** provider, ClusterSecretStore `aws-secrets-manager` on both clusters. A shell function `aws secretsmanager create-secret/put-secret-value/get-secret-value/list-secrets` (`test/install/lib.sh`) edits it, so the guide's `aws secretsmanager` lines run unchanged. | as written |
| 3.4 | Pod Identity for pricing | `pricing.provider: static` (`values-staging-hub.yaml`) | as written |
| 3.5, 4.4 | ACM, Route 53, ALB | A cert-manager CA (`ro-staging-ca`) issues `ro-staging-tls` for both hosts; ingress `className: traefik` with that secret. The ALB annotations are left in place and ignored. Inside the clusters, CoreDNS `coredns-custom` resolves `*.ro-staging.test` to the hub node. In the shell, `curl` is wrapped with `--connect-to … 127.0.0.1:19443 --cacert ca.crt`. | as written |
| 4.2 | RDS CA bundle | the ConfigMap `rds-ca-bundle` is built from CloudNativePG's CA (`ro-hub-ca`) | as written |
| 5.2 | the wizard | the wizard's own API call (`POST /v1/admin/clusters` with the metrics binding; it returns the agent token once) | the UI |
| 5.3 | ACM's public chain | `agent.hub.caBundle.pem` = the staging CA (the guide's troubleshooting entry for `x509`) | nothing (ACM is publicly trusted) |
| 6 | a policy in the UI | `POST /v1/admin/policies`: namespace `staging-demo`, CPU `auto-patch`, `in-place`, learning period 0 | the UI |

## Results (2026-11-05)

| Step | Check | Result |
|---|---|---|
| 2 | `make release VERSION=0.1.0-rc.2`; `imagetools inspect` | 5 images and 6 charts pushed; `linux/amd64` and `linux/arm64` |
| 3.2 | psql-setup | `CREATE ROLE`, `GRANT ROLE`, `CREATE DATABASE`, `pod "psql-setup" deleted` |
| 3.3 | list-secrets; store Ready | the 3 names; `True` |
| 4.1 | render | `3 Deployment`, `5 ExternalSecret`, `2 Ingress` (as the guide says) |
| 4.3 | ExternalSecrets, pods, leader | `hub-db`, `hub-bootstrap-admin`, `hub-datasource-key` synced; the two token ExternalSecrets `SecretSyncedError` and pricing waiting, as the guide says; `readyz=ok`; `ro_hub_leader` 1 + 0 |
| 4.4 | `curl https://$HUB_HOST/readyz`, UI | `ok 200` |
| 4.5 | sign-in; tokens; `/v1/me` | CSRF token; all five ExternalSecrets synced; pricing rolled out; `{"authMethod":"token","role":"viewer"}` |
| 5.3 | ExternalSecret, pods, log | `SecretSynced True`; 2 agent + 2 webhook pods `1/1`; `"msg":"starting agent"… "hub":"https://hub.ro-staging.test"` |
| 5.4 | webhook proxy; readiness | `the server could not find the requested resource` (reachable); `MetricsOK` then **`AutomationReady`** about 1 minute after the install, 0 blocking |
| 6 | auto-patch | recommendation `Pending` → `Applied` after about 7 minutes; pod CPU request 500m → 250m in place, 0 restarts |
| failover | delete the leader pod (`--force`) | new leader in < 1 s, sum 1; recompute accepted |
| 8 | `helm upgrade` 0.1.0-rc.4 → rc.5 under a probe (`/readyz` and `/v1/me` every 0.5 s) | **768 × 200**, 0 errors; revision `deployed`; leader sum 1; still `AutomationReady` |
| 9 | uninstall | CRDs and `hub-datasource-key` (ExternalSecret + Secret) kept as documented; after the removal commands nothing is left |
| clean run | `make install-staging-down && make install-staging && make install-staging-verify` | see below |

## What the run changed

1. **Charts: a `preStop` sleep on hub and web** (`preStopSleepSeconds: 5`, the built-in `sleep` action, so the images need no shell; commit 4b4eda5).
   - Before: the first upgrade (rc.2 → rc.3) saw **2 × 502 and 8 × timeout** out of 722 probes. The hub closed its listener on SIGTERM while traefik still routed to it.
   - After: 0 errors in 768 probes. The guide's "The hub rolls with no downtime" is now true.
   - The hub's grace period stays 30 s: 5 s preStop, ≤ 15 s step-down, 10 s drain.
2. **EKS example and guide for the ALB** (commit e275ef8): `values-hub.yaml` sets `preStopSleepSeconds: 15` on hub and web, and hub `terminationGracePeriodSeconds: 45`, because the ALB takes longer to deregister a target than traefik. Step 3.2 labels the namespace for the AWS Load Balancer Controller's pod readiness gate. *Not verified here (no ALB): check it with an upgrade under load on EKS.*
3. **Guide, step 1:** the hub cluster needs a scraper too. It must write the hub's and pricing's own series (`ro_pricing_nodes`, `ro_hub_leader`) into the `METRICS_URL` store. Without it, `pricing.coverage` warns "0 of 1 nodes priced" although pricing works, and the alerts have no data. Staging now runs a vmagent on the hub cluster; with it the check passes.
4. **Guide, step 5.4:** the recommender needs a few minutes of a new cluster's metrics. A run started right after the install logs `"workloads":0`, and the cluster stays at *Metrics OK* until the next daily run. The guide now says to run it again after five minutes. `staging.sh` re-requests a run every 3 minutes while `metrics.recommendations` fails.
5. **Guide, step 3.2:** a note that kubectl's notice *"All commands and output from this session will be recorded in container logs"* is generic: psql doesn't echo stdin, and `--rm` deletes the pod.
6. **Also fixed** (from the WO-18d follow-ups):
   - The hub sets `secretsKeyFile` for an ExternalSecret-only datasource key.
   - The agent self-check's webhook remediation uses the chart's label.
   - `docs/release.md` lists the umbrella chart.

## Notes for the real EKS install

- **Expected warnings on a fresh cluster:** `metrics.scrape-interval` reflects the staging vmagent (cAdvisor at about 1m46s), so set your scraper to 30 s or less.
- **More fresh-cluster warnings:** `metrics.history` and `metrics.throttling` warn on a fresh cluster. They pass once there is a day of data and some throttling samples. `metrics.hpa` stays `unknown` until the cluster has an HPA.
- **Argo CD (step 7)** was not run here. The ApplicationSet is checked by kubeconform only (WO-18d). Run it on EKS if you use Argo CD.

## Clean run

From nothing (both clusters deleted), with the scripts as committed:
```
make install-staging-down
make install-staging STAGING_VERSION=0.1.0-rc.5        # install rc=0 in 8m (clusters, add-ons, steps 3-5; AutomationReady after one re-requested run)
make install-staging-verify STAGING_VERSION=0.1.0-rc.5 # verify rc=0 in 3m
  Check: readyz=ok, ro_hub_leader sum=1, HTTPS ok 200, sign-in
  Check: applied, cpu request 500m -> 250m
  Check: a new leader in 2 s, sum=1
```
Two earlier clean runs failed because of me, not the guide. The first: I edited `staging.sh` while it was running. The second: the run lacked the recompute re-request (point 4 above).
