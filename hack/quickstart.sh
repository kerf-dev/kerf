#!/usr/bin/env bash
# Kerf on a laptop: one command from nothing to a signed-in UI with priced recommendations for demo workloads.
#
#   hack/quickstart.sh                 create a kind (or k3d) cluster, install Kerf, wait for recommendations
#   hack/quickstart.sh down            remove everything the quickstart created
#   hack/quickstart.sh status          is it running, and the URL and password
#
# Needs Docker, kubectl, helm and kind or k3d. Docs: docs/quickstart.md. Development only: it installs a single
# PostgreSQL pod, a self-signed webhook certificate and generated demo data. For a real install see
# docs/install/eks.md.
set -euo pipefail

REPO="kerf-dev/kerf"
CHART_OCI="oci://ghcr.io/kerf-dev/kerf/charts/kerf"
NAMESPACE="kerf"
RELEASE="kerf"
CLUSTER="quickstart"        # the cluster's name inside Kerf (values-quickstart.yaml: global.clusterName)

cmd="up"
runtime=""
name="kerf-quickstart"
use_current=0
version=""
local_build=0
no_build=0
port=8080
timeout="15m"
assume_yes=0

usage() {
  cat <<'EOF'
Usage: hack/quickstart.sh [up|down|status] [options]

  up (default)   create the cluster (unless --use-current), install Kerf, wait for the demo recommendations
  down           remove everything: the cluster the quickstart created, or with --use-current the release,
                 the demo namespace and the Kerf CRDs
  status         whether it is running, the URL and the admin password

Options:
  --runtime kind|k3d   cluster tool (default: kind if installed, else k3d)
  --name NAME          cluster name, kerf-quickstart[-suffix] (default: kerf-quickstart); other names are refused so the
                       quickstart can never touch a cluster of yours
  --use-current        install into the current kubectl context instead of creating a cluster (asks first)
  --version X.Y.Z      the published release to install (default: the latest, pre-releases included while there is no
                       final one)
  --local              build the images from this checkout and load them into the cluster (for contributors)
  --no-build           with --local: load the kerf/<component>:local images that already exist instead of building
  --port N             host port of the UI (default: 8080; with --use-current the UI is port-forwarded instead)
  --timeout DURATION   how long to wait for the install and for the recommendations (default: 15m)
  --yes                do not ask for confirmation (--use-current)
  -h, --help           this text
EOF
}

die() { echo "quickstart: $*" >&2; exit 1; }
log() { echo "==> $*"; }

# ---- arguments --------------------------------------------------------------------------------------------------
if [ $# -gt 0 ]; then
  case "$1" in up|down|status) cmd=$1; shift ;; esac
fi
while [ $# -gt 0 ]; do
  case "$1" in
    --runtime) runtime=${2:?--runtime needs kind or k3d}; shift 2 ;;
    --name) name=${2:?--name needs a value}; shift 2 ;;
    --use-current) use_current=1; shift ;;
    --version) version=${2:?--version needs a value}; version=${version#v}; shift 2 ;;
    --local) local_build=1; shift ;;
    --no-build) no_build=1; local_build=1; shift ;;
    --port) port=${2:?--port needs a value}; shift 2 ;;
    --timeout) timeout=${2:?--timeout needs a value}; shift 2 ;;
    --yes|-y) assume_yes=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "unknown argument: $1" ;;
  esac
done

case "$name" in
  kerf-quickstart|kerf-quickstart-[a-z0-9]*) ;;
  *) die "--name must be kerf-quickstart or kerf-quickstart-<suffix> (got '$name')" ;;
esac
if [ "$use_current" = 1 ] && [ "$local_build" = 1 ]; then
  die "--local loads the images into a kind or k3d cluster it created: do not combine it with --use-current"
fi

need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required: $2"; }

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$here/.." && pwd)
tmp=""
cleanup() { [ -n "$tmp" ] && rm -rf "$tmp"; return 0; }
trap cleanup EXIT

# ---- runtime and context ----------------------------------------------------------------------------------------
detect_runtime() {
  if [ -n "$runtime" ]; then
    case "$runtime" in kind|k3d) ;; *) die "--runtime must be kind or k3d" ;; esac
    return
  fi
  if command -v kind >/dev/null 2>&1; then runtime=kind
  elif command -v k3d >/dev/null 2>&1; then runtime=k3d
  else die "kind or k3d is required: https://kind.sigs.k8s.io/ or https://k3d.io/"; fi
}

if [ "$use_current" = 1 ]; then
  need kubectl "https://kubernetes.io/docs/tasks/tools/"
  context=$(kubectl config current-context 2>/dev/null) || die "no current kubectl context"
  case "$context" in
    k3d-ro-*|*-ro-hub|*-ro-spoke-*) die "refusing to use context '$context'" ;;
  esac
else
  detect_runtime
  case "$runtime" in kind) context="kind-$name" ;; k3d) context="k3d-$name" ;; esac
fi

kc() { kubectl --context "$context" "$@"; }
hm() { helm --kube-context "$context" "$@"; }

cluster_exists() {
  case "$runtime" in
    kind) kind get clusters 2>/dev/null | grep -qx "$name" ;;
    k3d) k3d cluster list --no-headers 2>/dev/null | awk '{print $1}' | grep -qx "$name" ;;
  esac
}

# ---- helpers ----------------------------------------------------------------------------------------------------
admin_password() {
  kc -n "$NAMESPACE" get secret kerf-quickstart-admin -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || true
}

ui_url() {
  if [ "$use_current" = 1 ]; then echo "http://localhost:$port (after: kubectl --context $context -n $NAMESPACE port-forward svc/web $port:8080)"
  else echo "http://localhost:$port"; fi
}

print_access() {
  local pw; pw=$(admin_password)
  echo
  echo "  Kerf is running."
  echo "    URL:       $(ui_url)"
  echo "    User:      admin"
  echo "    Password:  ${pw:-<not created yet>}"
  echo
  echo "  Remove everything: hack/quickstart.sh down$([ "$use_current" = 1 ] && echo " --use-current")$([ "$name" != kerf-quickstart ] && echo " --name $name")"
}

random_token() { openssl rand -hex 20; }

latest_version() {
  need curl "to look up the latest release"
  local v
  v=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -1)
  if [ -z "$v" ]; then
    # No final release yet: the newest one, a pre-release.
    v=$(curl -fsSL "https://api.github.com/repos/$REPO/releases?per_page=10" 2>/dev/null | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -1)
  fi
  [ -n "$v" ] || die "could not find a release of $REPO (GitHub API); pass --version X.Y.Z or use --local"
  echo "$v"
}

# ---- up ---------------------------------------------------------------------------------------------------------
create_cluster() {
  need docker "https://docs.docker.com/get-docker/"
  docker info >/dev/null 2>&1 || die "Docker is not running"
  if cluster_exists; then
    log "cluster $name exists: reusing it"
    return
  fi
  log "creating the $runtime cluster $name"
  case "$runtime" in
    kind)
      need kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
      kind create cluster --name "$name" --wait 180s --config - <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraPortMappings:
      - {containerPort: 30090, hostPort: $port, listenAddress: "127.0.0.1", protocol: TCP}
EOF
      ;;
    k3d)
      need k3d "https://k3d.io/#installation"
      # No Traefik, no metrics-server, no service load balancer: Kerf brings what it needs.
      k3d cluster create "$name" --agents 0 --wait \
        --port "127.0.0.1:$port:30090@server:0" \
        --k3s-arg "--disable=traefik@server:0" \
        --k3s-arg "--disable=metrics-server@server:0" \
        --k3s-arg "--disable=servicelb@server:0"
      ;;
  esac
}

remove_cluster() {
  case "$runtime" in
    kind) kind delete cluster --name "$name" >/dev/null 2>&1 || true ;;
    k3d) k3d cluster delete "$name" >/dev/null 2>&1 || true ;;
  esac
}

confirm_current() {
  [ "$assume_yes" = 1 ] && return
  echo "This installs Kerf (a PostgreSQL pod, VictoriaMetrics, demo workloads) into the cluster behind the kubectl"
  echo "context '$context', namespaces $NAMESPACE and kerf-demo."
  printf "Continue? [y/N] "
  read -r a
  case "$a" in y|Y|yes) ;; *) die "cancelled" ;; esac
}

# Build the five images from this checkout and load them into the cluster. Tag: local.
build_local_images() {
  need docker "https://docs.docker.com/get-docker/"
  local c
  for c in hub pricing agent webhook web; do
    if [ "$no_build" = 1 ]; then
      docker image inspect "kerf/$c:local" >/dev/null 2>&1 || die "--no-build: image kerf/$c:local does not exist"
      continue
    fi
    log "building kerf/$c:local"
    if [ "$c" = web ]; then docker build -q -f "$root/web/Dockerfile" -t "kerf/$c:local" "$root/web" >/dev/null
    else docker build -q -f "$root/cmd/$c/Dockerfile" -t "kerf/$c:local" "$root" >/dev/null; fi
  done
  log "loading the images into $name"
  case "$runtime" in
    kind) kind load docker-image --name "$name" kerf/hub:local kerf/pricing:local kerf/agent:local kerf/webhook:local kerf/web:local ;;
    k3d) k3d image import --cluster "$name" kerf/hub:local kerf/pricing:local kerf/agent:local kerf/webhook:local kerf/web:local ;;
  esac
}

wait_recommendations() {
  # With --use-current there is no host port: go through a temporary port-forward.
  local base="http://127.0.0.1:$port" pf="" jar pw deadline n
  command -v jq >/dev/null 2>&1 || { log "jq not found: not waiting for the recommendations (open the UI in a few minutes)"; return 0; }
  if [ "$use_current" = 1 ]; then
    kc -n "$NAMESPACE" port-forward svc/web "$port:8080" >/dev/null 2>&1 &
    pf=$!
    # shellcheck disable=SC2064
    trap "kill $pf 2>/dev/null; cleanup" EXIT
  fi
  jar="$tmp/cookies"; pw=$(admin_password)
  local secs=900
  case "$timeout" in *m) secs=$(( ${timeout%m} * 60 )) ;; *s) secs=${timeout%s} ;; esac
  deadline=$(( $(date +%s) + secs ))
  log "waiting for the demo recommendations (the hub looks every 2 minutes)"
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if curl -fsS -c "$jar" -H 'Content-Type: application/json' \
         -d "{\"username\":\"admin\",\"password\":\"$pw\"}" "$base/v1/auth/login" >/dev/null 2>&1; then
      # Done when every demo workload has a recommendation and at least one is priced (savings other than 0).
      n=$(curl -fsS -b "$jar" "$base/v1/workloads?cluster=$CLUSTER&namespace=kerf-demo" 2>/dev/null \
            | jq '[.items[]? | select(.recommendationID != null)] as $r
                  | if (.items | length) > 0 and ($r | length) == (.items | length)
                    then [$r[] | select((.monthlySavingsUSD.total // 0) != 0)] | length else 0 end' 2>/dev/null || echo 0)
      if [ "${n:-0}" -ge 1 ] 2>/dev/null; then
        log "the demo workloads have recommendations, $n of them priced"
        # The readiness checks ran once, before the first scrapes and before the agent had pulled its config: ask for
        # fresh ones until none fails (a few minutes at most), so the UI starts clean. Best effort.
        csrf=$(curl -fsS -c "$jar" -H 'Content-Type: application/json' -d "{\"username\":\"admin\",\"password\":\"$pw\"}" \
                 "$base/v1/auth/login" 2>/dev/null | jq -r '.csrfToken // empty' 2>/dev/null || true)
        for _ in 1 2 3 4 5 6; do
          curl -fsS -b "$jar" -X POST -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' -d '{}' \
            "$base/v1/clusters/$CLUSTER/readiness/recheck" >/dev/null 2>&1 || true
          failing=$(curl -fsS -b "$jar" "$base/v1/clusters/$CLUSTER/readiness" 2>/dev/null \
                      | jq '[.checks[]? | select(.status == "fail")] | length' 2>/dev/null || echo 1)
          [ "${failing:-1}" = 0 ] && break
          sleep 30
        done
        [ -n "$pf" ] && kill "$pf" 2>/dev/null || true
        return 0
      fi
    fi
    sleep 10
  done
  [ -n "$pf" ] && kill "$pf" 2>/dev/null || true
  echo "quickstart: no recommendations after $timeout. Kerf is installed; check: kubectl --context $context -n $NAMESPACE get pods; logs of deploy/hub" >&2
  return 1
}

do_up() {
  need kubectl "https://kubernetes.io/docs/tasks/tools/"
  need helm "https://helm.sh/docs/intro/install/"
  need openssl "to generate the tokens"
  tmp=$(mktemp -d)
  local start; start=$(date +%s)

  if [ "$use_current" = 1 ]; then confirm_current
  else create_cluster || { remove_cluster; die "could not create the $runtime cluster $name (is Docker short of CPU or memory?); removed what it left, run again"; }; fi

  # Chart and values: this checkout (--local), or the published chart of a release.
  local chart values extra
  extra=()
  if [ "$local_build" = 1 ]; then
    chart="$root/deploy/charts/kerf"; values="$chart/values-quickstart.yaml"
    build_local_images
    extra+=(--set-string global.imageRegistry= )
    for c in hub pricing agent webhook web; do extra+=(--set-string "$c.image.tag=local" --set "$c.image.pullPolicy=Never"); done
  else
    [ -n "$version" ] || version=$(latest_version)
    log "installing Kerf $version from $CHART_OCI"
    hm pull "$CHART_OCI" --version "$version" --untar --untardir "$tmp" >/dev/null || die "could not pull the chart $version"
    chart="$tmp/kerf"; values="$chart/values-quickstart.yaml"
    [ -f "$values" ] || die "release $version has no quickstart: use a newer --version, or --local from a checkout"
  fi

  # Tokens: reused when this cluster already has them (a second run is an upgrade).
  local agent_token pricing_token
  agent_token=$(kc -n "$NAMESPACE" get secret hub-dev-tokens -o "jsonpath={.data.$CLUSTER}" 2>/dev/null | base64 -d 2>/dev/null || true)
  pricing_token=$(kc -n "$NAMESPACE" get secret hub-dev-tokens -o jsonpath='{.data.pricing}' 2>/dev/null | base64 -d 2>/dev/null || true)
  [ -n "$agent_token" ] || agent_token=$(random_token)
  [ -n "$pricing_token" ] || pricing_token=$(random_token)

  # A failed or interrupted install cannot be upgraded into a working one (the demo history is written once, after
  # the first install, and the database password is new): start over, without the volumes of the first attempt.
  local fresh=1
  hm list --namespace "$NAMESPACE" -q 2>/dev/null | grep -qx "$RELEASE" && fresh=0
  if hm list --namespace "$NAMESPACE" --failed --pending -q 2>/dev/null | grep -qx "$RELEASE"; then
    fresh=1
    log "removing the install that did not finish"
    hm uninstall "$RELEASE" --namespace "$NAMESPACE" --wait >/dev/null 2>&1 || true
    kc -n "$NAMESPACE" delete pvc data-kerf-postgres-0 data-kerf-vm-0 --ignore-not-found --wait >/dev/null 2>&1 || true
    agent_token=$(random_token); pricing_token=$(random_token)
  fi

  local args
  args=(--namespace "$NAMESPACE" --create-namespace -f "$values"
    --set-string "hub.devSeed.tokens.$CLUSTER=$agent_token"
    --set-string "hub.devSeed.tokens.pricing=$pricing_token"
    --set-string "agent.hub.tokenSecret.token=$agent_token"
    --set-string "pricing.hub.tokenSecret.token=$pricing_token"
    ${extra[@]+"${extra[@]}"}
    --wait --timeout "$timeout")
  if [ "$fresh" = 1 ]; then
    # The hub fills the days before its first run from the history in VictoriaMetrics when it starts, and never
    # again. The demo history is written by a job after the install, so start the hub only after that: install
    # with the hub scaled to zero (and the pricing service, which is not ready without it) so that the job runs once
    # everything else is up, then start them.
    log "helm install: metrics, database and demo history (waits, up to $timeout)"
    hm upgrade --install "$RELEASE" "$chart" "${args[@]}" --set hub.replicas=0 --set pricing.enabled=false >/dev/null
    log "starting the hub and the pricing service"
  else
    log "helm upgrade (waits for the pods, up to $timeout)"
  fi
  hm upgrade --install "$RELEASE" "$chart" "${args[@]}" >/dev/null

  wait_recommendations || { print_access; exit 1; }
  print_access
  echo "  Took $(( ($(date +%s) - start) / 60 )) min $(( ($(date +%s) - start) % 60 )) s."
}

# ---- down -------------------------------------------------------------------------------------------------------
do_down() {
  if [ "$use_current" = 1 ]; then
    need helm "https://helm.sh/docs/intro/install/"
    confirm_current_down
    log "uninstalling Kerf from context $context"
    hm uninstall "$RELEASE" --namespace "$NAMESPACE" --wait >/dev/null 2>&1 || true
    kc delete namespace kerf-demo "$NAMESPACE" --ignore-not-found --wait >/dev/null
    # The CRDs are kept by helm uninstall: they would take every policy with them, so they go last.
    kc delete crd rightsizingpolicies.kerf.getkerf.dev rightsizingrecommendations.kerf.getkerf.dev --ignore-not-found >/dev/null
    log "removed"
    return
  fi
  if ! cluster_exists 2>/dev/null; then log "no $runtime cluster named $name: nothing to remove"; return; fi
  log "deleting the $runtime cluster $name"
  case "$runtime" in
    kind) kind delete cluster --name "$name" ;;
    k3d) k3d cluster delete "$name" ;;
  esac
}

confirm_current_down() {
  [ "$assume_yes" = 1 ] && return
  echo "This removes Kerf, its namespaces $NAMESPACE and kerf-demo, and the Kerf CRDs (with every policy) from the"
  echo "cluster behind the kubectl context '$context'."
  printf "Continue? [y/N] "
  read -r a
  case "$a" in y|Y|yes) ;; *) die "cancelled" ;; esac
}

# ---- status -----------------------------------------------------------------------------------------------------
do_status() {
  if [ "$use_current" != 1 ] && ! cluster_exists; then echo "no $runtime cluster named $name (hack/quickstart.sh to create it)"; exit 1; fi
  kc -n "$NAMESPACE" get pods 2>/dev/null || die "Kerf is not installed in context $context"
  print_access
}

case "$cmd" in
  up) do_up ;;
  down) do_down ;;
  status) do_status ;;
esac
