# GitOps and Argo CD

Kerf is built to run next to a GitOps tool. Most changes leave the manifests in Git untouched, the few that write
the pod template use one field manager that Argo CD can ignore, and policies can live in Git like everything else.

## In-place resizes cause no drift

The default strategy, `in-place`, resizes running pods through the `resize` subresource. It does not edit the
Deployment, StatefulSet or other workload, so the pod template still matches Git and Argo CD sees nothing to sync.
The webhook gives new pods the same values ([apply strategies](apply-strategies.md)).

## Rollouts and HPA targets: one field manager

The `rollout` and `deferred` strategies, and [HPA coordination](hpa.md), write the pod template or the HPA target.
The agent writes them with server-side apply under the field manager `kerf`, and only the CPU and memory fields of the
resources in `auto-patch`. Argo CD must ignore the fields that manager owns, or a sync reverts the change. Add this
entry to `argocd-cm` in every Argo CD instance that syncs workloads on the cluster:

```yaml
resource.customizations.ignoreDifferences.all: |
  managedFieldsManagers:
  - kerf
```

Then give the Applications the sync option `RespectIgnoreDifferences=true`. `ServerSideApply=true` is recommended.

The hub offers this entry in the add-cluster wizard, next to the install command, and as the `argocdIgnoreDifferences`
field of the install snippet. The same snippet can render a full Argo CD `Application` with the ignore rule and
these sync options. The readiness check `integration.argocd` warns when `argocd-cm` lacks the entry
([readiness](../contracts/readiness.md)).

An existing `resource.customizations.ignoreDifferences.all` has to be extended by hand: add `kerf` to its
`managedFieldsManagers` list. The step-by-step commands are in
[Turning on auto-patch](../install/eks.md#6-turning-on-auto-patch) and
[Argo CD instead of helm](../install/eks.md#7-argo-cd-instead-of-helm) in the EKS guide. The ApplicationSet in
[deploy/examples/eks](../../deploy/examples/eks/applicationset.yaml) already ignores the `kerf` manager.

## Leaving auto-patch

When a workload leaves `auto-patch`, the agent releases the fields it owns, records a `released` event, and Argo CD
restores the Git values on its next sync. A freeze window or a pause delays the release until it ends, so Argo CD does
not change the workload in the middle of a freeze.

After an [approved change](approve-once.md), the agent keeps ownership until a newer recommendation with different
values arrives, the resource is set to `off`, or the health watch rolls the change back. A self-healing Argo CD does
not revert an approved change in between.

## Policies in Git

You can keep policies as `RightsizingPolicy` resources in Git and let Argo CD apply them:

```yaml
apiVersion: kerf.getkerf.dev/v1alpha1
kind: RightsizingPolicy
metadata:
  name: payments-cpu-auto
spec:
  priority: 100
  selector:
    namespaces: ["payments"]
  cpu:
    mode: auto-patch
  memory:
    mode: audit
  apply:
    strategy: in-place
```

- **Git policies compete by priority** with the policies you create in the UI. The highest priority wins, and ties go
  to the name that sorts first ([policies](policies.md#how-a-policy-is-chosen)).
- **The agent never touches them.** It writes only resources labelled `kerf.getkerf.dev/managed-by=hub`, which are the
  policies from the UI. If a hub policy has the name of a Git policy, the hub policy is not applied and the hub reports
  it as invalid.
- **They show read-only in the UI.**
- **A cluster's policy source** is `hub`, `git` or `both`. With `git`, the hub sends no policies to that cluster and
  its policy pages are read-only.
- **Workload annotations** (`kerf.getkerf.dev/mode` and the others) belong in the manifest in Git when Git manages
  the workload ([modes](modes.md#where-the-effective-mode-comes-from)).

The Kerf chart installs the CRDs with `helm.sh/resource-policy: keep`, so uninstalling the chart keeps every policy.
Argo CD does not honor that annotation, so the example ApplicationSet keeps the objects when an Application is
deleted.

## Related

- [Install on Amazon EKS](../install/eks.md)
- [Apply strategies](apply-strategies.md)
- [Policies](policies.md)
- [Approve once](approve-once.md)
- [Readiness checks](../contracts/readiness.md)
