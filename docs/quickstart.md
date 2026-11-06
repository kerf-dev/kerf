# Quickstart: Kerf on your laptop

One command takes you from nothing to a signed-in Kerf UI with priced recommendations for a few demo workloads, on a
local [kind](https://kind.sigs.k8s.io/) or [k3d](https://k3d.io/) cluster. It is for **evaluation only**: it installs
a single PostgreSQL pod, makes its own webhook certificate and generates the usage history of the demo workloads. To
see what Kerf says about your own workloads, install it for real ([Install on Amazon EKS](install/eks.md)).

## What you need

- Docker (running), `kubectl` and `helm`, and `kind` **or** `k3d`.
- About 4 CPUs and 8 GiB of memory free for Docker. Kerf and the demo take about 3 GiB.
- Internet access: the images come from `ghcr.io`, Docker Hub and `registry.k8s.io`.
- `jq` is optional: with it the script waits until the recommendations are there before it returns.

## Run it

```bash
git clone https://github.com/kerf-dev/kerf && cd kerf
hack/quickstart.sh
```

The script creates a cluster named `kerf-quickstart`, installs the latest published release of Kerf into it, waits
until the demo workloads have recommendations, and prints:

```text
  Kerf is running.
    URL:       http://localhost:8080
    User:      admin
    Password:  <generated>
```

Open the URL and sign in. **Opportunities** lists the demo workloads, ranked by estimated monthly savings, each with
the current, recommended and used CPU and memory.

Time from nothing to the recommendations, measured on a laptop: see [Timing](#timing).

### Options

| Option | |
|---|---|
| `--runtime kind\|k3d` | The cluster tool. Default: kind if it is installed, else k3d. |
| `--name NAME` | Cluster name: `kerf-quickstart` or `kerf-quickstart-<suffix>`. Other names are refused, so the script can never create, change or delete a cluster of yours. |
| `--use-current` | Install into the current kubectl context instead of creating a cluster. It asks first (`--yes` skips the question) and installs into the namespaces `kerf` and `kerf-demo`. The UI is not mapped to a host port: the script port-forwards it while it waits; afterwards run `kubectl -n kerf port-forward svc/web 8080:8080`. |
| `--version X.Y.Z` | The published release to install. Default: the latest, including pre-releases while there is no final release. |
| `--local` | Build the five images from your checkout and load them into the cluster instead of pulling a release. For contributors; the first build takes a few minutes. |
| `--port N` | Host port of the UI. Default 8080. |
| `--timeout 15m` | How long to wait for the install and for the recommendations. |

`hack/quickstart.sh status` shows the pods, the URL and the password again. Running the script a second time upgrades
the release in place and keeps the data.

## Remove everything

```bash
hack/quickstart.sh down
```

This deletes the cluster the script created. After `--use-current` it uninstalls the release, deletes the namespaces
`kerf` and `kerf-demo` and the two Kerf CRDs (with every policy), and leaves the rest of the cluster alone:

```bash
hack/quickstart.sh down --use-current
```

## What it installed

One Helm release, `kerf`, in the namespace `kerf` ([chart values](../deploy/charts/kerf/values-quickstart.yaml),
`global.mode: all`, `dev: true`):

| What | Quickstart | A real install |
|---|---|---|
| Kerf | hub, web UI, pricing service, agent and webhook, one replica each | the same chart, hub and workload clusters (`global.mode` hub and spoke), two replicas and leader election |
| PostgreSQL | one pod (`kerf-postgres`), dev only, no backup, no TLS | your own PostgreSQL 12 or later, such as RDS, with verified TLS |
| Metrics | VictoriaMetrics (`kerf-vm`) scraping the kubelets' cAdvisor, kube-state-metrics (`kerf-ksm`) and the Kerf services | your Prometheus or VictoriaMetrics |
| Webhook certificate | made by the chart (`webhook.tls.selfSigned`), not renewed | cert-manager |
| Sign-in | user `admin`, password generated into the Secret `kerf-quickstart-admin`; plain HTTP | your own admin password from a secret store, HTTPS |
| Prices | the static price table: the node counts as one `m6i.xlarge` in `eu-west-1` | the AWS price list, or your own table |
| Demo workloads | six Deployments in `kerf-demo` that request far more than they use | your workloads |
| Demo history | 30 days of generated usage, written by a one-off Job (`kerf-quickstart-history`) | what your Prometheus has collected |

The chart's values schema refuses the quickstart settings (`quickstart.enabled`, `webhook.tls.selfSigned`, a bundled
PostgreSQL, inline secrets) unless `dev: true`, so they cannot reach a production install by accident.

### The demo workloads

The six workloads in `kerf-demo` (`web-frontend`, `api-gateway`, `orders-service`, `report-worker`, `search-indexer`,
`cache`) run a `pause` container: they do nothing. What Kerf reads for them is a **generated** history: the Job
`kerf-quickstart-history` writes 30 days of CPU and memory usage (the generator in [tools/synth](../tools/synth)) for
the cluster `quickstart` into VictoriaMetrics. Most of them request three to ten times what the history says they use;
`search-indexer` runs close to its memory limit and `cache` is about right, so you see decreases, an increase and a
workload that stays as it is.

Kerf only recommends here: the cluster is in `audit`, nothing is applied.

### Your own workloads

Your own workloads in the same cluster are measured too, but they have no history yet: Kerf needs several days of
usage for a recommendation with the production profiles, and the UI shows how long that will take for each workload.
Keep the quickstart cluster running for a few days if you want to see that.

## Move to a real install

1. Follow [Install on Amazon EKS](install/eks.md), or the [chart reference](../deploy/charts/kerf/README.md) for other
   clusters. Use `global.mode: hub` for the central cluster and `spoke` for each workload cluster.
2. Replace what the quickstart stood in for: a managed PostgreSQL (`hub.database.*`), your Prometheus or
   VictoriaMetrics (`hub.config.prometheusURL`), cert-manager for the webhook, a registry and your own admin password
   (`hub.auth.bootstrapAdmin`). Leave `dev` and `quickstart` out of your values.
3. Delete the quickstart (`hack/quickstart.sh down`).

## Timing

The whole run, from nothing to the recommendations in the UI, measured in the project's CI on GitHub Actions
runners (4 CPUs, 16 GiB), where the images are also built from source (the longest part, and one you skip when you
install a published release):

| Cluster tool | From nothing to priced recommendations |
|---|---|
| kind | 5 min 28 s |
| k3d | 4 min 38 s |

Where the time goes: creating the cluster (about 30 s), pulling the base images (PostgreSQL, VictoriaMetrics,
kube-state-metrics), the install and the demo history (about 3 minutes), then up to two minutes until the hub's next
run. On a slower connection or machine the image pulls dominate; the script waits up to 15 minutes (`--timeout`).

## Troubleshooting

| Symptom | What to do |
|---|---|
| `kind or k3d is required` | Install one of them (links in the error). |
| Port 8080 is in use | `hack/quickstart.sh --port 18080`. |
| The script says no recommendations after the timeout | `kubectl --context kind-kerf-quickstart -n kerf get pods` (or `k3d-kerf-quickstart`); every pod should be `Running`. `kubectl -n kerf logs job/kerf-quickstart-history` shows the history job and `kubectl -n kerf logs deploy/hub` the recommender. |
| Pods are `Pending` | Docker has too little CPU or memory; raise it in Docker's settings and run the script again. |
| Image pulls fail | Check access to `ghcr.io`, `docker.io` and `registry.k8s.io`, or use `--local` once the base images are cached. |
| You forgot the password | `hack/quickstart.sh status`. |

## Without the script

The script creates the cluster, passes the generated tokens and waits. The install itself is one command; the tokens
the agent and the pricing service use to reach the hub are the only values you have to make up:

```bash
helm install kerf oci://ghcr.io/kerf-dev/kerf/charts/kerf --version <version> -n kerf --create-namespace \
  -f values-quickstart.yaml \
  --set-string hub.devSeed.tokens.quickstart=<token1> --set-string agent.hub.tokenSecret.token=<token1> \
  --set-string hub.devSeed.tokens.pricing=<token2> --set-string pricing.hub.tokenSecret.token=<token2>
```

The demo history is written by a Job right after the first install, and the hub computes the days before its first run
from the history when it starts (and never again). So the script installs with the hub switched off (`--set
hub.replicas=0 --set pricing.enabled=false`; the pricing service is not ready without the hub), waits for the Job, then runs the same command again without that flag. Do the same by hand, or
the savings trend starts empty.

`values-quickstart.yaml` is in the chart (`helm pull oci://ghcr.io/kerf-dev/kerf/charts/kerf --version <version>
--untar`) and in [deploy/charts/kerf](../deploy/charts/kerf/values-quickstart.yaml).
