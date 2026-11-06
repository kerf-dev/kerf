# HPA coordination

A HorizontalPodAutoscaler (HPA) that scales on CPU or memory utilization divides a pod's usage by its requests. If
Kerf lowers the requests alone, utilization rises and the HPA adds replicas. HPA coordination avoids that: Kerf
changes the requests and the HPA's utilization target together, so the HPA keeps scaling at the same usage as
before.

## How it works

An HPA computes the replicas it wants as `ceil(replicas x utilization / target)`, where utilization is usage divided
by the sum of the pod's requests. Kerf keeps the product `requests x target` constant, so the replica count stays the
same at the same usage:

```text
new target = old target x (sum of requests before) / (sum of requests after)
```

For example, an HPA with a CPU target of 50 % on a container that requests `1000m`. The recommendation is `700m`.
The new target is about 71 %, and the HPA scales at the same CPU usage as before.

Coordination follows these rules:

- **One change.** The agent patches the pod template and the HPA target in the same step. It writes both with
  server-side apply under the field manager `kerf`. The agent needs permission to patch HPAs, which `readOnly`
  removes.
- **Rollout.** A coordinated change always uses the `rollout` strategy, never `in-place`
  ([apply strategies](apply-strategies.md)).
- **Target bounds.** The target stays within 40 to 90 % by default. The hub adjusts the recommended requests to keep
  it there. A current target that is already outside the bounds is allowed as it is.
- **Rollback and release.** A [rollback](rollback.md) restores the HPA target with the requests. When the workload
  leaves `auto-patch`, the agent releases the HPA target with the pod template.

## Turn it on

Coordination is off by default. Turn it on in a policy, and optionally set the bounds the agent enforces:

```yaml
spec:
  hpa:
    coordinate: true
    targetBounds:
      minUtilization: 50
      maxUtilization: 85
```

The agent blocks a change whose new target would leave these bounds, with the reason `HPACoupled`. The hub itself
keeps the recommended target within 40 to 90 %.

## When a workload stays audit-only

A workload whose HPA scales on CPU or memory utilization carries the risk flag `hpa-coupled`. When the HPA's
resource is in `auto-patch`, Kerf makes no change and records `blocked` with the reason `HPACoupled` if:

- `spec.hpa.coordinate` is off, which is the default;
- the HPA scales on more than one utilization metric, or on any other metric (pods, object or external), or on a
  per-container metric;
- the HPA was at `maxReplicas` in the recommendation window;
- a container has no request of the HPA's resource;
- keeping the target within its bounds would lower a request that must not decrease, because the container is
  throttled or OOM-killed;
- the new target would leave the policy's bounds.

The recommendation still appears, with its savings, so you see what the workload would gain. The reason the hub
does not coordinate is in the `hpa.skipReason` of the recommendation: `max-replicas-reached`,
`unsupported-metrics`, `missing-request` or `target-out-of-bounds`.

Only the HPA's resource matters. If the HPA scales on CPU and CPU is not in `auto-patch`, the HPA does not hold the
workload back.

## See it

The `applied` event of a coordinated change carries the HPA name, the resource and the target before and after
(`hpa`, `hpaResource`, `hpaTargetFrom`, `hpaTargetTo` in the [event details](../contracts/events.md#payload)). The
`RightsizingRecommendation` records the target in `spec.hpa`, and the one it replaced in `status.previousHPATarget`.

## Related

- [Policies](policies.md)
- [Apply strategies](apply-strategies.md)
- [Rollback](rollback.md)
- [Custom resources](../reference/crds.md#rightsizingpolicy-spec)
