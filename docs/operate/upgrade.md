# Upgrade Kerf

One chart, `kerf`, installs every component, so an upgrade is a `helm upgrade` of the same release on each
cluster. Upgrade the hub cluster first, then each workload cluster.

> **Note.** The EKS guide has the same steps with its variables: [Install on Amazon EKS, step 8](../install/eks.md#8-upgrade).
> This page adds the compatibility rules and the rollback.

## Before you start

1. Read [`CHANGELOG.md`](../../CHANGELOG.md) for every version between yours and the target. Each release
   lists its changes and how to upgrade.
2. Until 1.0.0, a minor version (0.2 to 0.3) may change chart values or the CRDs, and the notes say how to
   upgrade. Compare your values files with the [chart values](../reference/values/README.md) before a minor
   upgrade.
3. Open **Admin → Maintenance** and note the current versions ([details](../maintenance.md)).
4. Take a snapshot of the hub database ([backup](backup.md)). The hub applies database migrations when it
   starts and only ever runs them forward.
5. Keep the values files you installed with. `helm upgrade` needs them again.

## Order and version skew

The hub goes first because the hub decides what an agent may be:

| Rule | Where it shows |
|---|---|
| An agent whose major or minor version is **higher** than the hub's fails the check `agent.version`. The cluster drops below *Agent connected*. | [Readiness](readiness.md#agent-connected), Maintenance ("Newer than the hub") |
| An agent **older** than the hub's `config.onboarding.minAgentVersion` fails the same check. The value is empty by default, so there is no minimum. | Readiness |
| Any component, agent, webhook or pricing, whose version differs from the hub's is flagged. Older: upgrade the Kerf chart in that cluster. Newer: upgrade the hub first. | Maintenance |
| Development builds (`dev-…`) are not compared. | – |

A patch version ahead of the hub does not fail `agent.version`, but Maintenance still flags it. Finish the
upgrade of all clusters so that every component runs one version. The hub and the agent of a workload
cluster may differ for the time it takes to roll out.

## Upgrade the hub cluster

```bash
export VERSION=0.2.0
helm --kube-context <hub-cluster> upgrade kerf oci://ghcr.io/kerf-dev/kerf/charts/kerf \
  --version $VERSION -n kerf -f values-hub.yaml
kubectl --context <hub-cluster> -n kerf rollout status deploy/hub
```

Use your own registry in the chart reference if you mirror the charts. Release name `kerf` and namespace `kerf`
are the ones the install guides use. The Maintenance page prints both commands for the latest release, with
your chart reference when `hub.config.onboarding.chart` is set.

While it rolls:

- Each new hub pod applies pending database migrations before it reports ready. Replicas that start
  together wait for one another, under a Postgres lock. The startup probe allows 10 minutes.
- Maintenance shows replicas on different versions until the rollout ends.
- A replica that stops gives up the leader lock at once, and another one takes over. `sum(kerf_hub_leader)`
  is 0 for at most 15 seconds, and 1 otherwise.

Check the hub before you go on:

```bash
kubectl --context <hub-cluster> get --raw "/api/v1/namespaces/kerf/services/hub:8080/proxy/readyz"
```

Expected: `ok`. [Troubleshooting](troubleshooting.md#hub-ha-and-leader-election) covers a missing leader.

## Upgrade each workload cluster

```bash
helm --kube-context <workload-cluster> upgrade kerf oci://ghcr.io/kerf-dev/kerf/charts/kerf \
  --version $VERSION -n kerf -f values-spoke-<cluster>.yaml --wait
```

The agent's standby replica takes the lease while the other rolls, and the webhook's PodDisruptionBudget keeps
one pod. With Argo CD, change `targetRevision` of the ApplicationSet instead
([EKS guide, step 7](../install/eks.md#7-argo-cd-instead-of-helm)).

## CRDs

In mode `spoke` and `all` the chart renders the two CRDs as templates, so `helm upgrade` updates them. Install
the chart in mode `spoke` once per cluster: a CRD can belong to one release only. The CRDs carry
`helm.sh/resource-policy: keep`, so uninstalling the chart keeps every policy and recommendation.

If the agent reports `agent.crds` as failed with "CRDs older than the agent", the CRDs in the cluster lack fields
the agent knows. Upgrade the chart of that cluster. The check's own fix is
`kubectl apply --server-side --force-conflicts -f api/crds/`, run from a checkout of the release you installed.

## Verify

- **Admin → Maintenance:** every component shows the hub's version, and no message.
- **Readiness:** each cluster is back at the state it had.
- `helm --kube-context <cluster> -n kerf history kerf` lists the new revision as `deployed`.

## Roll back

```bash
helm --kube-context <cluster> -n kerf rollback kerf
```

Roll back the cluster that has the problem. Roll back workload clusters before the hub: an agent that is
newer than the hub fails `agent.version`.

Helm does not undo database migrations, and the hub applies them forward only. If you need the old schema,
restore the snapshot you took ([restore](backup.md#restore)).

## The update check

The hub looks for new releases once a day and shows the release notes and the commands on the Maintenance
page. It sends no data about your installation. Turn it off with `hub.updateCheck.enabled: false`, for
example in an air-gapped site. [Details and proxy settings](../maintenance.md#the-update-check). The chart's
alert `KerfUpdateAvailable` (severity `info`) fires an hour after a newer release exists.

## Related

- [Maintenance and updates](../maintenance.md)
- [Install on Amazon EKS](../install/eks.md#8-upgrade)
- [Backup and restore](backup.md)
- [Release process](../release.md)
- [Changelog](../../CHANGELOG.md)
