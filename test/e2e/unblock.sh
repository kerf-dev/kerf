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
if [ "$cur" != "e2e-hub-unreachable-invalid-token" ]; then
  exit 0
fi
tok=$(kubectl --context "$hub_ctx" -n resource-optimizer get secret hub-dev-tokens -o jsonpath="{.data.$cluster}")
kubectl --context "$spoke_ctx" -n "$ns" patch secret "$secret" --type merge -p "{\"data\":{\"token\":\"$tok\"}}" >/dev/null
echo "restored the $cluster agent hub token"
