# The `kerf` command-line tool

`kerf report` answers one question from a laptop, with nothing installed in the cluster: **how much are we
over-requesting?** It reads your existing Prometheus, VictoriaMetrics or Amazon Managed Service for Prometheus,
runs the same recommender as the Kerf hub, and prints the workloads sorted by monthly saving.

```bash
kerf report --prometheus-url https://prometheus.example.com \
  --cluster-label cluster=prod-eu-1 --profile production --format table
```

It only reads. It keeps no state, writes nothing to the metrics source or the cluster, and sends no telemetry:
the only connections are to the metrics source and, when you ask for them, to AWS (prices) and to the cluster's API
server (HPAs and JVM settings). Applying changes, history, approvals and the UI need the
[full install](install/eks.md).

## Install

Every [release](https://github.com/kerf-dev/kerf/releases) has `kerf` binaries for Linux and macOS, amd64 and arm64
(`kerf_<version>_<os>_<arch>`), with a `.sigstore.json` signature bundle for each and a `kerf_SHA256SUMS` file. They are
built and signed by the release workflow, like the images. Download one, verify it and put it on your `PATH`:

```bash
version=0.3.0
os=$(uname -s | tr A-Z a-z); arch=$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/)
bin=kerf_${version}_${os}_${arch}
curl -fsSLO https://github.com/kerf-dev/kerf/releases/download/v$version/$bin
curl -fsSLO https://github.com/kerf-dev/kerf/releases/download/v$version/$bin.sigstore.json

cosign verify-blob --bundle $bin.sigstore.json \
  --certificate-identity-regexp '^https://github.com/kerf-dev/kerf/.github/workflows/release.yaml@' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com $bin

install -m 0755 $bin /usr/local/bin/kerf
kerf version
```

Verifying needs cosign **v3** or newer. To build it yourself: `make build-cli` writes `bin/kerf`.

## What your metrics need

The report uses the series of the [metrics contract](contracts/metrics.md): cAdvisor
(`container_cpu_usage_seconds_total`, `container_memory_working_set_bytes`, the CFS throttling counters) and
kube-state-metrics (pod owners, requests and limits, restarts and OOM kills, HPAs). That is what most Prometheus
installs (kube-prometheus-stack, VictoriaMetrics k8s stack, a managed Prometheus) already scrape. The more history
there is, the better the result: the `production` profile reads 14 days, `dev` 7 and `high-availability` 28. Workloads
with less history get a lower confidence and the `low-confidence` flag.

## Flags

| Flag | Meaning |
|---|---|
| `--prometheus-url` | Query base URL: the part before `/api/v1/query`. Also `KERF_PROMETHEUS_URL`. For Amazon Managed Service for Prometheus: `https://aps-workspaces.<region>.amazonaws.com/workspaces/<id>` |
| `--cluster-label label=value` | The label that selects the cluster's series, for example `cluster=prod-eu-1`. Leave it out when the metrics source holds one cluster |
| `--matcher 'env="prod"'` | An extra label matcher on every series (repeatable) |
| `--profile` | `dev`, `production` (default) or `high-availability`: the same windows, percentiles and headroom as the hub |
| `--format` | `table` (default), `csv`, `json` or `markdown` |
| `--namespace` | Only this namespace (repeatable) |
| `--limit N`, `--min-saving USD` | Show only the N best workloads, or those saving at least this much per month. The totals still cover everything |
| `--prices` | `aws`, or the path of a static price file. Without it, default rates apply (see below) |
| `--default-region` | The AWS region of nodes without a region label |
| `--kubeconfig`, `--kube-context` | Also read HPAs and JVM settings from the cluster (see below) |
| `--timeout` | Timeout of one request to the metrics source (default 2m) |
| `--quiet` | No progress messages on stderr |

The exit status is 0 when the report was produced, 1 on an error (the message says what to fix) and 2 for a wrong
command line.

## Authentication to the metrics source

The options are the ones of a hub [datasource](contracts/readiness.md).

| Need | Flags |
|---|---|
| Headers, such as the tenant of Mimir or Cortex | `--header "X-Scope-OrgID: tenant-1"` (repeatable) |
| Bearer token | `--token-file path` or `KERF_PROMETHEUS_TOKEN` |
| Basic auth | `--username user` with `--password-file path` or `KERF_PROMETHEUS_PASSWORD` |
| A private CA | `--ca-file ca.pem` |
| Client certificate (mTLS) | `--client-cert cert.pem --client-key key.pem` |
| Self-signed certificate, for a test | `--insecure-skip-verify` |
| AWS SigV4 (Amazon Managed Service for Prometheus) | automatic for an `aps-workspaces` URL (the region comes from the URL); otherwise `--sigv4 --sigv4-region <region>`, and `--sigv4-role-arn` to assume a role |

Secrets are read from files or the environment, not from flags, so they do not show up in the process list. They are
never printed.

SigV4 signs every query with the credentials of the AWS default chain (environment variables, a profile, SSO, an
instance or pod identity). The identity needs `aps:QueryMetrics` on the workspace:

```bash
AWS_PROFILE=prod kerf report \
  --prometheus-url https://aps-workspaces.eu-west-1.amazonaws.com/workspaces/ws-1234abcd-... \
  --cluster-label cluster=prod-eu-1
```

## Prices

Savings need a price per vCPU-hour and per GiB-hour. Without `--prices` the report uses default rates (an
m6i.xlarge in eu-west-1) and says so at the top. For your own prices, the report finds the cluster's nodes in the metrics
(`kube_node_labels`, `kube_node_status_allocatable`), prices them like the pricing service does, splits each node's
price into a CPU and a memory rate, and averages them over the nodes:

- `--prices static.yaml`: a static price table, the same format as the one in the chart's pricing values
  ([`dev/prices.yaml`](../dev/prices.yaml) is an example): instance types with their vCPU and GiB, and the on-demand
  and spot prices per region.
- `--prices aws`: the AWS Pricing API (and EC2 spot price history for spot nodes), with the credentials of the
  default chain. It needs `pricing:GetProducts` and `ec2:DescribeSpotPriceHistory`; nothing is written.

If no node can be priced (a missing label or an instance type that is not in the table), the report says so and falls
back to the default rates.

## HPAs and JVM settings from the cluster

The metrics already tell which workloads have an HPA. With a kubeconfig the report also reads them, and the JVM
options of the containers, from the cluster, which finds HPAs that kube-state-metrics does not export and reads
`-Xmx` and `-XX:MaxRAMPercentage`:

```bash
kerf report --prometheus-url ... --cluster-label cluster=prod-eu-1 --kubeconfig ~/.kube/config --kube-context prod-eu-1
```

It lists HPAs, Deployments, StatefulSets, DaemonSets and CronJobs, and nothing else. The context must be the cluster
the metrics describe.

## The output

The default table is aligned and sorted by monthly saving:

```text
Cluster prod-eu-1, profile production (14 days of history), as of 2026-11-06 09:41 UTC. Rates: $0.0268 per vCPU-hour, $0.0067 per GiB-hour (default rates).

NAMESPACE  WORKLOAD                 REPLICAS  CPU NOW  CPU REC  MEM NOW  MEM REC  SAVING/MO  CONF  FLAGS
shop       Deployment/checkout      6.0       2        850m     4Gi      2Gi      $107.86    0.94  -
data       StatefulSet/ledger       3.0       4        1500m    16Gi     9Gi      $73.19     0.92  -
...

41 workloads, estimated saving $812.40 per month.
To keep recommendations current and apply them automatically, install Kerf: https://getkerf.dev/docs/install/eks
```

*CPU and memory now / rec* are the requests per pod, summed over the containers; *saving* is for all replicas. The
flags are the hub's risk flags: `throttled`, `oom-killed`, `restarting`, `hpa-coupled`, `sidecar`, `short-lived`,
`low-confidence`, `jvm-fixed-heap`. A negative saving means the workload needs *more* than it requests.

`--format json` has everything the hub's recommendation has, including the per-container values and the explanation of
each; `csv` is one line per workload; `markdown` is the table for a pull request or a ticket. The table and Markdown
end with the line pointing to the full install; the JSON has it in `next`, and the CSV keeps stdout to the data (the
line goes to stderr).

## How it matches the hub

`kerf report` runs the hub's recommender: the same queries, profiles, percentiles, headroom, rounding, limit policy,
confidence and risk flags. For the same history and profile, the numbers are the hub's. A test
(`internal/report`) checks this on the synthetic histories in `testdata/series`.

What differs: there are no per-workload settings (every workload uses the profile you pass and the default limit
policy), no history of earlier recommendations, and no automation. The savings are estimates from the requests and
the rates, as in the hub.

## Next steps

When the numbers are worth acting on, install Kerf to keep them current, approve and apply changes with guardrails and
rollback, and see the trend: [Install on Amazon EKS](install/eks.md).
