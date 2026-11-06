# Operate Kerf

Running Kerf day to day means four things: keep the versions of its components in step, keep a copy of the
state that matters, watch the signals it exports, and fix a cluster that falls out of its readiness state.
These pages cover each one.

Kerf has two kinds of state. The hub keeps everything in PostgreSQL: recommendations, the audit log,
policies made in the UI, users, tokens and the cluster list. Each workload cluster keeps only what the agent
derives from the hub, plus any `RightsizingPolicy` you apply from Git.

## Pages

| Page | Read it when |
|---|---|
| [Upgrade](upgrade.md) | A new release is out, or you need to roll one back. |
| [Backup and restore](backup.md) | You plan the backups of the hub database, secrets and values, or you restore after a loss. |
| [Alerts and dashboards](alerts-dashboards.md) | An alert fires, or you set up Grafana and the chart's alert rules. |
| [Cluster readiness](readiness.md) | A cluster is stuck below *Automation ready*, or `auto-patch` is refused. |
| [Troubleshooting](troubleshooting.md) | Something does not work and you start from the symptom. |
| [Maintenance and updates](../maintenance.md) | You want the versions of every component, or the check for a new release. |

## A routine

1. Open **Admin → Maintenance** after every upgrade. All components should run the hub's version.
2. Keep one alert route for the chart's alerts. [`KerfNoHubLeader`](alerts-dashboards.md#alerts) is the
   only one with severity `critical`.
3. Look at each cluster's readiness page when you add a cluster and when an alert names it.
4. Take a database snapshot before every hub upgrade.

## Related

- [Install on Amazon EKS](../install/eks.md)
- [Install on any cluster](../install/any-cluster.md)
- [Architecture](../architecture.md)
- [Metrics contract](../contracts/metrics.md)
