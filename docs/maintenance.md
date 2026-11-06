# Maintenance and updates

**Admin → Maintenance** in the web UI (operators and admins) shows which version of Kerf runs where, and whether
a newer release exists.

## Component versions

| Component | Where the version comes from |
|---|---|
| Hub | Each replica records its version every 30 seconds. The leader (the replica running the recommender) is marked. Replicas on different versions mean a rolling upgrade is in progress. |
| Web UI | The version the UI you are looking at was built with. |
| Pricing | Sent by the pricing service with its requests to the hub (every 5 minutes). |
| Agent | Sent by the agent of each workload cluster with its status reports. "Never" means the agent has not connected yet. |
| Webhook | The agent reads the image tag of the webhook pods behind the webhook Service with its self-checks (every 10 minutes). "Not installed" means the cluster has no Kerf webhook. |

Every version is compared with the hub's. An agent or webhook **older** than the hub: upgrade the Kerf chart in that
cluster. A component **newer** than the hub: upgrade the hub first; the hub cluster always goes first
([upgrade order](install/eks.md#8-upgrade)). Development builds (`dev-…`) are not compared.

## The update check

Once a day the hub leader reads the public GitHub releases of `kerf-dev/kerf`
(`https://api.github.com/repos/kerf-dev/kerf/releases`) and picks the latest stable version. Pre-releases count
only when the hub itself runs a pre-release of the same minor version (for example `0.3.0-rc.1` sees `0.3.0-rc.2`).

- **What is sent:** a plain unauthenticated `GET` with the header `User-Agent: kerf-hub/<version>`. No
  identifiers, cluster names, counts or other data about your installation. Repeated checks send the last
  `ETag`, so an unchanged list costs nothing against GitHub's limit of 60 requests per hour per address.
- **Check now** (admins) runs it at once; within 30 seconds of the last check it returns that result.
- **Result:** up to date, update available (with the release notes and the `helm upgrade` commands for the hub
  cluster and each workload cluster), failed (with the error: no route to GitHub, a proxy error, the rate
  limit), disabled, or development build.
- **Metric and alert:** `kerf_update_available{current,latest}` is 1 when an update exists
  ([metrics](contracts/metrics.md#update-check)); the chart's alert `KerfUpdateAvailable` (severity `info`,
  `hub.prometheusRule.alerts.updateAvailable`) fires after an hour.

### Configure or turn it off

```yaml
hub:
  updateCheck:
    enabled: true        # false for air-gapped sites or without egress to api.github.com
    interval: 24h        # at least 10m; a random delay of up to a tenth of it (at most 1h) is added
    url: https://api.github.com/repos/kerf-dev/kerf/releases   # a mirror of the releases API
  # Behind an HTTP proxy:
  extraEnv:
    - {name: HTTPS_PROXY, value: http://proxy.example.internal:3128}
    - {name: NO_PROXY, value: .svc,.cluster.local,10.0.0.0/8}
```

With the hub NetworkPolicy on (`hub.networkPolicy.enabled`), egress to port 443 is allowed by default
(`networkPolicy.egress.ports`).
