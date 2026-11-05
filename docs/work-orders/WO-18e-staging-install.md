# WO-18e Staging install: follow the EKS guide on a fresh cluster

- **Phase / wave:** 5 / 3 (after WO-18a, 18b, 18c and 18d are merged)
- **Owns:** `test/install/**` (new), `make/install.mk` (new). Fixes to `docs/install/eks.md` and `deploy/examples/eks/**` when the guide turns out to be wrong; record each fix in the handover.
- **Does not own:** the component charts, the umbrella chart's templates, app code. If the chart needs a change, report it with the failing step.
- **Run by:** the orchestrator. It creates a new k3d cluster, which no execution track may do.

## Goal
Prove that someone who has only `docs/install/eks.md` can install the system. Install from the **published OCI chart and images**, following the guide step by step. AWS-only pieces are replaced by local stand-ins, and every substitution is written down.

## Environment: `ro-staging`
- k3d cluster `ro-staging`: 1 server and 2 agents, nodes labelled `topology.kubernetes.io/zone=staging-a|b|c`, using the shared registry `k3d-ro-registry:5005`. Memory budget: about 4 GiB in total. The dev fleet keeps running.
- **Stand-ins** (the only deviations from the guide):

| EKS piece | Stand-in on k3d |
|---|---|
| ECR | `localhost:5005/ro-staging` (`make release REGISTRY=… VERSION=0.1.0-rc.2`) |
| AWS Load Balancer Controller + ALB + ACM | ingress-nginx (or the bundled traefik), self-signed TLS through cert-manager; `ingress.className` switched, ALB annotations left in place (ignored) |
| Secrets Manager | External Secrets Operator with the **Fake** provider, holding the same keys and JSON shape as the guide's Secrets Manager layout |
| RDS PostgreSQL 16 | in-cluster Postgres 16 with TLS (cert-manager CA) and `sslmode=verify-full` using that CA as the "RDS CA bundle" |
| Pod Identity | the pricing static provider (no AWS calls) |
| Prometheus/VictoriaMetrics on the workload cluster | VictoriaMetrics single node with kube-state-metrics and cAdvisor scraping, as the guide's prerequisites say |

## Steps
1. Create the cluster and install the prerequisites the guide lists: cert-manager, ESO, the ingress controller, metrics with kube-state-metrics and cAdvisor, and Postgres.
2. Run the guide's release step against the local registry. Check that the images are multi-arch.
3. Install `mode: all` from `oci://localhost:5005/ro-staging/charts/resource-optimizer` with `deploy/examples/eks/values-hub.yaml` and `values-spoke.yaml`, merged into one staging overlay `test/install/values-staging.yaml` that holds only the stand-in differences.
4. Run every check command in the guide and compare the output with the guide's expected output:
   - pods ready; `/readyz`
   - `sum(ro_hub_leader) == 1`
   - UI reachable through the ingress; first admin sign-in using the ESO-provided bootstrap secret
5. Onboard the cluster through the UI wizard, as the guide says (API calls standing in for clicks are fine). Put the token into the Fake store and let ESO deliver it.
6. Reach *Ready (audit)* and then *Automation ready*. Turn on auto-patch for one test workload (`test/install/fixtures/`) and see one patch applied.
7. **Failover:** delete the hub leader pod. A new leader appears within 15 s, and a recompute runs on it.
8. **Upgrade:** run `helm upgrade` to `0.1.0-rc.3` (a rebuild with the same code). Hub API requests during the roll get no 5xx (a curl loop).
9. **Uninstall** as the guide says. CRDs and the datasource-key secret remain, as documented.
10. Script steps 1–9 as `make install-staging`, `make install-staging-verify` and `make install-staging-down` in `make/install.mk`, so the run can be repeated.

## Verification
- `make install-staging && make install-staging-verify` passes from a clean state. Paste the output.
- `helm template` + kubeconform (1.35) for the EKS example values (unchanged from 18d).
- The dev fleet is unaffected: both spokes stay *Automation ready*, and `make ci` stays green.
- Handover `docs/handover/WO-18e-handover.md`:
  - every guide step that needed a fix
  - every stand-in, and what the real EKS install must do instead
  - resource usage
