# shellcheck shell=bash
# Shared helpers for dev/scripts/*.sh. Source, don't execute. Must stay bash 3.2 compatible (macOS).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEV="$ROOT/dev"
export PATH="$HOME/.local/bin:$HOME/go/bin:$HOME/sdk/go/bin:$PATH"

set -a
# shellcheck source=../versions.env
. "$DEV/versions.env"
set +a

HUB=ro-hub
SPOKES=(ro-spoke-1 ro-spoke-2)
CLUSTERS=("$HUB" "${SPOKES[@]}")
NETWORK=ro-dev
REGISTRY=ro-registry          # container k3d-ro-registry, push to localhost:5005
REGISTRY_PORT=5005
CHART_CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/kerf/charts"

log()  { printf '\033[1;34m[%s]\033[0m %s\n' "${LOG_PREFIX:-dev}" "$*" >&2; }
warn() { printf '\033[1;33m[%s] WARN:\033[0m %s\n' "${LOG_PREFIX:-dev}" "$*" >&2; }
die()  { printf '\033[1;31m[%s] ERROR:\033[0m %s\n' "${LOG_PREFIX:-dev}" "$*" >&2; exit 1; }

ctx() { echo "k3d-$1"; }
kc()  { local c="$1"; shift; kubectl --context "$(ctx "$c")" "$@"; }

cluster_exists()   { k3d cluster get "$1" >/dev/null 2>&1; }
container_exists() { docker inspect "$1" >/dev/null 2>&1; }

require_tools() {
  local t missing=""
  for t in docker k3d kubectl helm; do command -v "$t" >/dev/null || missing="$missing $t"; done
  [[ -z "$missing" ]] || die "missing tools:$missing (see dev/README.md)"
  docker info >/dev/null 2>&1 || die "Docker is not running"
}

# fetch_chart <name> <repo-url> <version>  -> prints path to the cached .tgz (works offline once cached)
fetch_chart() {
  local name="$1" repo="$2" version="$3" tgz
  tgz="$CHART_CACHE/$name-$version.tgz"
  if [[ ! -f "$tgz" ]]; then
    mkdir -p "$CHART_CACHE"
    helm pull "$name" --repo "$repo" --version "$version" -d "$CHART_CACHE" >/dev/null
  fi
  echo "$tgz"
}

# helm_install <cluster> <namespace> <release> <chart-name> <repo> <version> [helm args...]
helm_install() {
  local cluster="$1" ns="$2" release="$3" chart="$4" repo="$5" version="$6"; shift 6
  local tgz; tgz="$(fetch_chart "$chart" "$repo" "$version")"
  log "$cluster: helm $release ($chart $version)"
  helm upgrade --install "$release" "$tgz" \
    --kube-context "$(ctx "$cluster")" --namespace "$ns" --create-namespace \
    --wait --timeout 8m "$@" >/dev/null
}

hub_ip() {
  docker inspect -f "{{(index .NetworkSettings.Networks \"$NETWORK\").IPAddress}}" "k3d-$HUB-server-0"
}
