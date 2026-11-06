# Approve once

Approve once lets you apply the current recommendation of a workload that stays in `audit`. It is the way to try
automation on one workload, or to take one change you have reviewed, without turning on `auto-patch`.

## What it does

When you approve a recommendation, the hub records an approval for that recommendation. The agent then:

1. applies the recommendation once, treating the resources in `audit` as if they were `auto-patch`. Resources in
   `off` stay off;
2. applies every [guardrail](guardrails.md), as for `auto-patch`: the learning period, the confidence, the maximum
   change, the cooldown, freeze and maintenance windows, and the rest;
3. records an `applied` event with the reason `ApprovedOnce`, naming the approval and who approved it;
4. leaves the workload in `audit`. The next recommendation is not applied unless you approve it too.

The change is health-watched and rolled back like any other ([rollback](rollback.md)).

Approving does not bypass a guardrail. A change that a guardrail blocks stays blocked, and the approval can expire
before the block ends.

## Approve a recommendation

In the UI, open the workload and choose **Approve once**. Enter a reason. The button is disabled when there is no
recommendation, when no resource is in `audit`, or when an approval is already waiting for the agent.

Through the API, post to `POST /v1/workloads/{id}/approve` ([hub API](../reference/api/README.md)):

```json
{
  "recommendationID": "0b9b5c1e-4f7c-4a52-9a43-1f1d3f6b7c10",
  "reason": "reviewed with the checkout team",
  "expiresAt": "2026-11-08T09:00:00Z"
}
```

The hub refuses an approval when:

- the recommendation is not the workload's latest (`409`, code `recommendation_stale`);
- neither CPU nor memory is in `audit` (`409`, code `not_in_audit`);
- the cluster has not reached `AutomationReady` (`409`, code `cluster_not_automation_ready`), unless an admin sets
  `overrideReadiness: true`. The override is audited.

## Expiry and consumption

| State | Meaning |
|---|---|
| `pending` | Waiting for the agent to apply it |
| `consumed` | The agent applied it. The hub marks it consumed when it stores the `ApprovedOnce` event |
| `expired` | `expiresAt` passed before the agent applied it |
| `superseded` | A newer approval or a newer recommendation replaced it |

An approval expires after 24 hours by default. You can set `expiresAt` up to 7 days ahead. A new approval replaces
an earlier one that was not consumed.

After an approved change on a workload that uses the `rollout` or `deferred` strategy, the agent keeps ownership of
the changed fields, so a self-healing Argo CD does not revert them ([GitOps](gitops.md)). It keeps them until a
recommendation with different values arrives, the resource is set to `off`, or the health watch rolls the change
back.

## Who can do it

| Role | Can approve once |
|---|---|
| `viewer` | No |
| `operator` | Yes, on any cluster |
| `admin` | Yes, and may override the readiness check |

The read-only `ui` token counts as a viewer. Every approval is recorded in the configuration audit log as
`WorkloadApproved`, with the reason, and shows in the history of the workload. An agent installed with
`readOnly: true` does not apply approvals.

## Related

- [Modes](modes.md)
- [Guardrails](guardrails.md)
- [GitOps and Argo CD](gitops.md)
- [Admin area contract](../contracts/admin.md)
- [Events contract](../contracts/events.md#reason-meanings)
