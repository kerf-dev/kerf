# Back up and restore Kerf

Kerf keeps its state in three places: the hub's PostgreSQL database, a few Kubernetes Secrets, and the Helm
values and `RightsizingPolicy` files you keep in Git. Back up those three.

> **Note.** Kerf does not back anything up for you. It has no backup job, no export command and no schedule.
> Use the backup tools you already run for PostgreSQL, Secrets and Git.

## What to back up

| State | Where it lives | Back it up with |
|---|---|---|
| Hub database | Your PostgreSQL (Amazon RDS on EKS) | Your database backups: RDS snapshots, or `pg_dump` |
| Datasource key | Secret `hub-datasource-key` on the hub cluster | Your secret store |
| Other secrets | Database URL, bootstrap admin, `ui` and `pricing` tokens, agent tokens | Your secret store |
| Helm values | `values-hub.yaml`, `values-spoke-<cluster>.yaml` | Git |
| Policies from Git | `RightsizingPolicy` manifests | Git |

### The hub database

The tables hold these groups of data (`db/migrations`):

- **Clusters and tokens:** the registered clusters with their metadata, metrics bindings and datasources, and
  API tokens as SHA-256 hashes, never as values.
- **Workloads and recommendations:** the workload inventory, every recommendation run, the latest
  recommendation per workload and the status each agent reported.
- **Audit log:** workload events and configuration changes. Append-only.
- **Policies and settings:** policies made in the UI with every version, defaults, overrides, the recommender
  and pricing settings with every version, and the automation pause state.
- **Users and sessions:** local users with bcrypt password hashes, and browser sessions.
- **Approvals, readiness and maintenance:** approve-once grants, readiness results and history, queued
  recompute requests, component versions and the last update check.

Recommendations older than `hub.config.history.retention` (90 days by default) are deleted, and the latest one
per workload is always kept. A backup is therefore not an archive beyond that window.

A database backup is the only way back for policies made in the UI, users, tokens and the audit log. Take one
before every [hub upgrade](upgrade.md).

### Secrets

- **Datasource key.** The hub encrypts the credentials of your metrics sources with it. Without this exact key
  the stored credentials cannot be read. Keep a copy outside the cluster. The chart keeps the Secret when you
  uninstall, but a lost cluster takes it along. ([Where it comes from](../install/eks.md#33-secrets-manager))
- **Tokens.** The database holds only hashes of tokens. The values live in the Secrets of the components that
  use them: each agent's Secret in its workload cluster, the `ui` token in the web chart's Secret, the
  `pricing` token in the pricing chart's Secret. A token you create after the snapshot you restore is unknown
  to the restored database.
- **Bootstrap admin.** The hub creates it only when the database has no users, so it matters for a restore
  into an empty database.

### Values and policies in Git

The chart rejects inline secrets outside development installs, so your values files hold no secrets and are
safe to keep in Git. This includes a custom price table (`pricing.prices`).

Policies from the UI are in the database. Policies you apply as `RightsizingPolicy` resources belong in
Git: that is the copy you restore. Each agent writes the hub's policies as `RightsizingPolicy` resources with the
label `kerf.getkerf.dev/managed-by=hub` and recreates them from the hub, so you do not back those up.
`RightsizingRecommendation` resources are written by the agent from the hub's recommendations in the same way.

### What is not Kerf's state

Your metrics store holds the usage history. Kerf reads it and does not copy it. After a loss of that history the
recommendations start again from the data that remains, and the readiness check `metrics.history` warns until
it covers the profile window. Back up the metrics store with its own tools.

## Restore

Restore in this order.

1. **Database.** Restore the snapshot into a PostgreSQL that the hub reaches directly or through a
   session-mode pooler, and point `hub.database` (or the Secret `hub.database.existingSecret` names) at it.
2. **Secrets.** Recreate the datasource key under its old name, then the database URL and bootstrap admin
   Secrets. Use the same key as before.
3. **Hub cluster.** Run the `helm upgrade --install` of your values file. If the snapshot is older than the
   chart, the first hub pod applies the missing migrations before it reports ready.
4. **Workload clusters.** Nothing to do if they survived. For a rebuilt cluster, install the chart with its
   values file and its agent token.

## After a restore

```bash
kubectl --context <hub-cluster> get --raw "/api/v1/namespaces/kerf/services/hub:8080/proxy/readyz"
kubectl --context <hub-cluster> -n kerf get pods -l app.kubernetes.io/instance=kerf
```

- The hub answers `ok`, and `sum(kerf_hub_leader)` is 1.
- Sign in. If a user or token you created after the snapshot is gone, create it again.
- Each agent pulls from the hub every minute by default. After a few minutes **Admin → Maintenance** shows
  every agent as connected and each cluster's readiness page shows `agent.connected` as passed.
  An agent that answers `401` holds a token the restored database does not know: create a new agent token and
  update its Secret ([troubleshooting](troubleshooting.md#agent-not-connecting)).
- Datasources that use stored credentials still pass `datasource.reachable`. If they do not, the key may differ
  from the one that encrypted the credentials: restore the right key, or enter the credentials again under
  **Admin → Metrics sources**.
- The recommender runs once a day by default. To refresh recommendations at once, an admin can start a run with
  `POST /v1/admin/recompute?cluster=<cluster>`.

## Related

- [Upgrade](upgrade.md)
- [Install on Amazon EKS](../install/eks.md)
- [Cluster readiness](readiness.md)
- [Troubleshooting](troubleshooting.md)
