# Policies

A `RightsizingPolicy` says which workloads to rightsize and how: the mode of CPU and memory, the apply strategy, the
guardrails, HPA and JVM handling, and the profile that shapes the recommendation. It is cluster-scoped, and its
short name is `rsp`.

## An example

```yaml
apiVersion: kerf.getkerf.dev/v1alpha1
kind: RightsizingPolicy
metadata:
  name: team-a-cpu-auto
spec:
  priority: 100
  selector:
    namespaces: ["team-a-*"]
    excludeNamespaces: ["team-a-sandbox"]
    matchLabels:
      tier: backend
    kinds: [Deployment, StatefulSet]
  profile: production
  cpu:
    mode: auto-patch
    limits: keep
  memory:
    mode: audit
    limitEqualsRequest: true
  apply:
    strategy: in-place
  guardrails:
    maxChangePercent: 30
    minConfidence: "0.8"
```

Every field is optional. A field you leave out inherits its value from the layer below (see [Layers](#layers)).
Durations such as `cooldown` are strings like `24h`. The full field list is in the
[custom resources reference](../reference/crds.md#rightsizingpolicy-spec).

## Selectors

A policy selects workloads with `spec.selector`. All criteria you set must match. An empty selector matches every
workload.

| Field | Matches |
|---|---|
| `namespaces` | A workload whose namespace matches any glob, such as `team-*` |
| `excludeNamespaces` | Never matches a namespace that fits any glob, even if `namespaces` lists it |
| `matchLabels` | Labels that must all be present with equal values on the workload object, not the pod template |
| `kinds` | `Deployment`, `StatefulSet`, `DaemonSet` or `CronJob` |
| `workloads` | An exact `kind`, `namespace` and `name`. The hub uses it for the override of one workload |

## How a policy is chosen

Several policies can match one workload. Only one wins:

1. The highest `spec.priority` wins (0 to 1000000).
2. On a tie, the policy name that sorts first wins.

Fields are not merged across competing policies: the winner supplies its fields, and everything it leaves unset
comes from the layers below. A policy that loses is still listed in the workload's effective policy, with the
reason. `status.matchedWorkloads` counts the workloads for which a policy is the winner.

## Layers

The effective policy of a workload is built from layers. Each field is resolved separately, from the highest layer
that sets it:

| Layer, lowest first | Where it comes from |
|---|---|
| Built-in defaults | Mode `audit`, strategy `in-place`, profile `production`, and the [guardrail defaults](guardrails.md#defaults) |
| Global default | The hub policy `ro-global-defaults`, sent to every cluster |
| Cluster default | The agent chart value `clusterDefaults`, then the hub policy `ro-cluster-defaults` |
| Policy | The winning policy: a hub policy or a `RightsizingPolicy` from Git |
| Workload override | The hub policy `ro-override-…`, created from the workload page in the UI |
| Annotation | `kerf.getkerf.dev/…` annotations on the workload ([modes](modes.md#where-the-effective-mode-comes-from)) |

Hub defaults and overrides are layers, not competitors: they never win or lose on priority. The hub gives its
policies these priorities: global default 0, cluster default 10, your policies 1 to 899999, a workload override
900000. Names starting with `ro-` are reserved for the hub.

A list of `maintenanceWindows` or `freezeWindows` set in a higher layer replaces the list from a lower layer. The
cluster-wide freeze set on the agent applies in addition to every policy.

### Hub policies and Git policies

Policies you create in the UI live in the hub. Each agent writes them to its cluster as `RightsizingPolicy`
resources labelled `kerf.getkerf.dev/managed-by=hub` and changes only resources with that label. Policies you
commit to Git keep working, take part in the choice by priority, and show read-only in the UI. Each cluster has a
policy source: `hub`, `git` or `both`. See [GitOps](gitops.md).

## The effective policy

The workload page in the UI shows the effective policy: every layer, whether it applied, and which layer set each
field. The same data is at `GET /v1/workloads/{id}/effective-policy`
([hub API](../reference/api/README.md)). On the cluster, `kubectl get rsr -A` shows the effective modes and strategy
of each workload.

## Profiles

The profile sets how the hub turns usage into a recommendation. CPU is a percentile of usage plus headroom. Memory
is the higher of a percentile and the peak, plus a buffer.

| Profile | Usage window | CPU percentile | Memory percentile | CPU headroom | Memory buffer |
|---|---|---|---|---|---|
| `dev` | 7 days | 0.90 | 0.95 | 10 % | 10 % |
| `production` (default) | 14 days | 0.95 | 0.99 | 15 % | 15 % |
| `high-availability` | 28 days | 0.99 | 0.99 | 30 % | 25 % |

These are the built-in values. An admin can change them under **Admin → Recommender**. A container that was
recently throttled or OOM-killed never gets a decrease for that resource.

Three more fields change the recommended values: `cpu.limits` (`keep` by default, `remove`, or `ratio` with
`cpu.limitRatio`) and `memory.limitEqualsRequest` (`true` by default). When a policy changes a profile or one of
these fields, the agent ignores recommendations computed with the old settings (`RecommendationStale`) until the
next hub run.

## Related

- [Modes](modes.md)
- [Guardrails](guardrails.md)
- [Apply strategies](apply-strategies.md)
- [Custom resources](../reference/crds.md#rightsizingpolicy-spec)
- [Admin area contract](../contracts/admin.md)
