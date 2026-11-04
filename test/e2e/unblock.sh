#!/usr/bin/env bash
# Undo scenario 11's hub outage if a killed run left it behind: restore the spoke-2 agent's hub
# token from the hub's dev token Secret (key = cluster name). No-op when the token is intact.
set -euo pipefail

spoke_ctx=${E2E_SPOKE2_CONTEXT:-k3d-ro-spoke-2}
hub_ctx=${E2E_HUB_CONTEXT:-k3d-ro-hub}
cluster=${E2E_SPOKE2:-ro-spoke-2}
ns=${E2E_AGENT_NAMESPACE:-resource-optimizer}
secret=${E2E_AGENT_TOKEN_SECRET:-resource-optimizer-agent-token}

# Older suite versions used a NetworkPolicy for the outage.
kubectl --context "$spoke_ctx" -n "$ns" delete networkpolicy e2e-hub-unreachable --ignore-not-found >/dev/null

cur=$(kubectl --context "$spoke_ctx" -n "$ns" get secret "$secret" -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null || true)
if [ "$cur" = "e2e-hub-unreachable-invalid-token" ]; then
  tok=$(kubectl --context "$hub_ctx" -n resource-optimizer get secret hub-dev-tokens -o jsonpath="{.data.$cluster}")
  kubectl --context "$spoke_ctx" -n "$ns" patch secret "$secret" --type merge -p "{\"data\":{\"token\":\"$tok\"}}" >/dev/null
  echo "restored the $cluster agent hub token"
fi

# Scenario 8 scales the webhook to 0 and records the original replicas in an annotation.
webhook_ns=${E2E_WEBHOOK_NAMESPACE:-resource-optimizer}
webhook=${E2E_WEBHOOK_DEPLOYMENT:-webhook}
for ctx in "${E2E_SPOKE1_CONTEXT:-k3d-ro-spoke-1}" "$spoke_ctx"; do
  was=$(kubectl --context "$ctx" -n "$webhook_ns" get deploy "$webhook" \
    -o jsonpath='{.metadata.annotations.e2e\.resource-optimizer\.oz\.io/scaled-from}' 2>/dev/null || true)
  if [ -n "$was" ]; then
    kubectl --context "$ctx" -n "$webhook_ns" scale deploy "$webhook" --replicas="$was" >/dev/null
    kubectl --context "$ctx" -n "$webhook_ns" annotate deploy "$webhook" e2e.resource-optimizer.oz.io/scaled-from- >/dev/null
    echo "scaled the webhook on $ctx back to $was"
  fi
done
