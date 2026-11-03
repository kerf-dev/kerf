# WO-07 Mutating admission webhook

- **Phase / wave:** 2 / 1
- **Depends on:** WO-00
- **Owns:** `cmd/webhook/`, `internal/webhook/`, `deploy/charts/webhook/`, `make/webhook.mk`
- **Decisions:** D-007

## Goal
New pods of an `in-place` workload start with the recommended resources, so scale-outs, evictions and redeploys keep the change without touching the pod template.

## Deliverables
- Mutating webhook on pod `CREATE`: resolve the owning workload from ownerReferences (Pod → ReplicaSet → Deployment, Pod → StatefulSet/DaemonSet/Job → CronJob), look up its `RightsizingRecommendation` from an informer cache, and if `phase=Applied` and `strategy=in-place`, set container requests/limits. Never calls the hub.
- Adds an annotation on the pod recording the values it injected.
- `failurePolicy: Ignore`, timeout ≤ 2s, namespace selector excluding `kube-system` and its own namespace, object selector opt-out label.
- 2+ replicas, PodDisruptionBudget, topology spread; certificates from cert-manager.
- Metrics: admissions mutated / skipped / errors, latency.
- Helm chart.

## Definition of done
On k3d, scaling an `in-place` workload creates pods with the recommended values while the Deployment spec still matches Git; killing the webhook pods does not block pod creation.

## Verification
```bash
go test ./internal/webhook/... -cover          # owner resolution for every kind, phase/strategy filtering
make dev-up && tilt up webhook agent
kubectl --context k3d-ro-spoke-1 scale deploy/overprovisioned --replicas=3
kubectl --context k3d-ro-spoke-1 get pods -l app=overprovisioned -o jsonpath='{..resources.requests.cpu}'   # recommended value on all
kubectl --context k3d-ro-spoke-1 -n resource-optimizer scale deploy/webhook --replicas=0
kubectl --context k3d-ro-spoke-1 scale deploy/overprovisioned --replicas=4 && kubectl --context k3d-ro-spoke-1 rollout status deploy/overprovisioned
```
