#!/usr/bin/env bash
# Undo scenario 11's hub outage if a killed run left it behind: restore the spoke-2 agent's hub
# token from the hub's dev token Secret (key = cluster name). No-op when the token is intact.
set -euo pipefail

spoke_ctx=${E2E_SPOKE2_CONTEXT:-k3d-ro-spoke-2}
hub_ctx=${E2E_HUB_CONTEXT:-k3d-ro-hub}
cluster=${E2E_SPOKE2:-ro-spoke-2}
ns=${E2E_AGENT_NAMESPACE:-kerf}
secret=${E2E_AGENT_TOKEN_SECRET:-kerf-agent-token}

# Older suite versions used a NetworkPolicy for the outage.
kubectl --context "$spoke_ctx" -n "$ns" delete networkpolicy e2e-hub-unreachable --ignore-not-found >/dev/null

cur=$(kubectl --context "$spoke_ctx" -n "$ns" get secret "$secret" -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null || true)
if [ "$cur" = "e2e-hub-unreachable-invalid-token" ]; then
  tok=$(kubectl --context "$hub_ctx" -n kerf get secret hub-dev-tokens -o jsonpath="{.data.$cluster}")
  kubectl --context "$spoke_ctx" -n "$ns" patch secret "$secret" --type merge -p "{\"data\":{\"token\":\"$tok\"}}" >/dev/null
  echo "restored the $cluster agent hub token"
fi

# Scenario 8 scales the webhook to 0 and records the original replicas in an annotation.
webhook_ns=${E2E_WEBHOOK_NAMESPACE:-kerf}
webhook=${E2E_WEBHOOK_DEPLOYMENT:-webhook}
for ctx in "${E2E_SPOKE1_CONTEXT:-k3d-ro-spoke-1}" "$spoke_ctx"; do
  was=$(kubectl --context "$ctx" -n "$webhook_ns" get deploy "$webhook" \
    -o jsonpath='{.metadata.annotations.e2e\.kerf\.oz\.io/scaled-from}' 2>/dev/null || true)
  if [ -n "$was" ]; then
    kubectl --context "$ctx" -n "$webhook_ns" scale deploy "$webhook" --replicas="$was" >/dev/null
    kubectl --context "$ctx" -n "$webhook_ns" annotate deploy "$webhook" e2e.kerf.getkerf.dev/scaled-from- >/dev/null
    echo "scaled the webhook on $ctx back to $was"
  fi
done

# WO-24 scenario 10 removes the agent's pods/resize rule on spoke-2 for a few minutes.
role=${E2E_AGENT_CLUSTERROLE:-kerf-agent}
for ctx in "${E2E_SPOKE1_CONTEXT:-k3d-ro-spoke-1}" "$spoke_ctx"; do
  res=$(kubectl --context "$ctx" get clusterrole "$role" -o jsonpath='{.rules[*].resources}' 2>/dev/null || true)
  if [ -n "$res" ] && ! grep -q 'pods/resize' <<<"$res"; then
    kubectl --context "$ctx" patch clusterrole "$role" --type json \
      -p '[{"op":"add","path":"/rules/-","value":{"apiGroups":[""],"resources":["pods/resize"],"verbs":["patch"]}}]' >/dev/null
    echo "restored the pods/resize rule of $role on $ctx"
  fi
done

# WO-24 scenario 11 (KERF_E2E_DISRUPTIVE=1) scales kube-state-metrics on spoke-2 to 0.
ksm_ns=${E2E_KSM_NAMESPACE:-monitoring}
ksm=${E2E_KSM_DEPLOYMENT:-kube-state-metrics}
was=$(kubectl --context "$spoke_ctx" -n "$ksm_ns" get deploy "$ksm" \
  -o jsonpath='{.metadata.annotations.e2e\.kerf\.oz\.io/scaled-from}' 2>/dev/null || true)
if [ -n "$was" ]; then
  kubectl --context "$spoke_ctx" -n "$ksm_ns" scale deploy "$ksm" --replicas="$was" >/dev/null
  kubectl --context "$spoke_ctx" -n "$ksm_ns" annotate deploy "$ksm" e2e.kerf.getkerf.dev/scaled-from- >/dev/null
  echo "scaled $ksm on $spoke_ctx back to $was"
fi

# The suite's field-manager entries on Helm-owned objects make the next `helm upgrade` fail with a
# conflict: drop them (the values stay). e2e.test is the manager of suite versions before WO-24.
release_fields() { # context, kubectl get args...
  local ctx=$1; shift
  local patch
  patch=$(kubectl --context "$ctx" get "$@" --show-managed-fields -o json 2>/dev/null | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except ValueError:
    sys.exit()
mf = d["metadata"].get("managedFields", [])
keep = [m for m in mf if m["manager"] not in ("kerf-e2e", "e2e.test")]
if len(keep) < len(mf):
    print(json.dumps([{"op": "test", "path": "/metadata/resourceVersion", "value": d["metadata"]["resourceVersion"]},
                      {"op": "replace", "path": "/metadata/managedFields", "value": keep or [{}]}]))
') || return 0
  if [ -n "$patch" ]; then
    kubectl --context "$ctx" patch "$@" --type json -p "$patch" >/dev/null && echo "dropped the e2e field manager from $* on $ctx"
  fi
}
for ctx in "${E2E_SPOKE1_CONTEXT:-k3d-ro-spoke-1}" "$spoke_ctx"; do
  release_fields "$ctx" clusterrole "$role"
  release_fields "$ctx" -n "$ns" secret "$secret"
  release_fields "$ctx" -n "$webhook_ns" deployment "$webhook"
  release_fields "$ctx" -n "$ksm_ns" deployment "$ksm"
done
