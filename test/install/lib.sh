# WO-18e staging: shared variables and the AWS stand-ins. Sourced by staging.sh.
# Everything here replaces an AWS piece of docs/install/eks.md; staging.sh otherwise runs the guide's
# commands as written.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
INSTALL=$ROOT/test/install
# shellcheck source=/dev/null
. "$INSTALL/versions.env"

# --- Step 0 of the guide, with staging values --------------------------------------------------------
export AWS_REGION=eu-west-1
export VERSION=${VERSION:-0.1.0-rc.2}
export HUB_CLUSTER=ro-stg-hub
export HUB_HOST=hub.ro-staging.test
export UI_HOST=ui.ro-staging.test
export METRICS_URL=http://vmsingle.monitoring.svc:8428
export ACCOUNT=000000000000
export ECR=localhost:5005                     # stand-in: the k3d registry, as pushed to from this machine
export ECR_IN_CLUSTER=k3d-ro-registry:5005    # the same registry as the clusters pull from it
export REGISTRY=$ECR/kerf
export NS=kerf
export WORK=${WORK:-$ROOT/bin/staging} && mkdir -p "$WORK"
export HUB_CTX=k3d-ro-stg-hub
export HUB_VPC_CIDR=10.42.0.0/16              # stand-in: the k3s pod CIDR (traefik's pods reach hub/web)
export CLUSTER=ro-stg-spoke
export SPOKE_CTX=k3d-ro-stg-spoke
export SPOKE_METRICS_URL=http://prometheus-operated.monitoring.svc:9090
export SPOKE_VPC_CIDR=10.42.0.0/16            # stand-in: k3s's API server reaches the webhook from cni0
export HELM_OCI_FLAGS=--plain-http            # stand-in: the local registry speaks HTTP
export WIZ_CLUSTER=ro-stg-spoke-2             # WO-18f: the workload cluster installed with the wizard's snippet
export WIZ_CTX=k3d-ro-stg-spoke-2
export AGENT_SECRET=kerf-agent-token
INGRESS_PORT=19443                            # host port of the hub cluster's traefik ("the ALB")

log() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
kh() { kubectl --context "$HUB_CTX" "$@"; }
ks() { kubectl --context "$SPOKE_CTX" "$@"; }
kw() { kubectl --context "$WIZ_CTX" "$@"; }

# The ALB/Route 53 stand-in for the shell: https://$HUB_HOST and https://$UI_HOST go to traefik on the
# hub cluster, verified against the staging CA (ACM's public chain stand-in).
curl() { command curl --connect-to "$HUB_HOST:443:127.0.0.1:$INGRESS_PORT" \
  --connect-to "$UI_HOST:443:127.0.0.1:$INGRESS_PORT" --cacert "$WORK/ca.crt" "$@"; }

# --- Secrets Manager stand-in --------------------------------------------------------------------------
# A ClusterSecretStore `aws-secrets-manager` with ESO's Fake provider on both clusters; its data is
# kept in $WORK/secrets.json. `aws secretsmanager create-secret|put-secret-value|get-secret-value|
# list-secrets` behave like the real CLI for the arguments the guide uses.
SM=$WORK/secrets.json
sm_apply() {
  [ -f "$SM" ] || echo '{}' > "$SM"
  local store
  store=$(jq '{apiVersion: "external-secrets.io/v1", kind: "ClusterSecretStore",
    metadata: {name: "aws-secrets-manager"},
    spec: {provider: {fake: {data: [to_entries[] | {key: .key, value: .value}]}}}}' "$SM")
  for ctx in "$HUB_CTX" "$SPOKE_CTX" "$WIZ_CTX"; do
    kubectl --context "$ctx" get crd clustersecretstores.external-secrets.io >/dev/null 2>&1 || continue
    echo "$store" | kubectl --context "$ctx" apply -f - >/dev/null
  done
}
aws() {
  [ "${1:-}" = secretsmanager ] || { echo "staging: aws $1 has no stand-in (skipped)" >&2; return 0; }
  local op=$2; shift 2; local name="" value="" query=""
  while [ $# -gt 0 ]; do case $1 in
    --name|--secret-id) name=$2; shift 2 ;;
    --secret-string) value=$2; shift 2 ;;
    --query) query=$2; shift 2 ;;
    *) shift ;;
  esac; done
  [ -f "$SM" ] || echo '{}' > "$SM"
  case $op in
    create-secret|put-secret-value)
      jq --arg k "$name" --arg v "$value" '.[$k] = $v' "$SM" > "$SM.new" && mv "$SM.new" "$SM"
      sm_apply; [ "$query" = Name ] && echo "$name"; return 0 ;;
    get-secret-value) jq -r --arg k "$name" '.[$k]' "$SM" ;;
    list-secrets) jq -r 'keys | join("  ")' "$SM" ;;
    *) echo "staging: aws secretsmanager $op has no stand-in" >&2; return 1 ;;
  esac
}
