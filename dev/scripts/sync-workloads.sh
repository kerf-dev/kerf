#!/usr/bin/env bash
# Push dev/workloads/ to the spokes.
#   ro-spoke-1: re-seed the in-cluster git server (only when the files changed) and hard-refresh the
#               Argo CD Application, so Argo CD syncs the change like a Git commit.
#   ro-spoke-2: kubectl apply directly (no Argo CD there).
# Usage: sync-workloads.sh [cluster...]   (default: both spokes)
. "$(dirname "$0")/lib.sh"

targets=("$@")
((${#targets[@]})) || targets=("${SPOKES[@]}")
files=("$DEV"/workloads/*.yaml)

for c in "${targets[@]}"; do
  export LOG_PREFIX=$c
  if [[ "$c" == ro-spoke-1 ]]; then
    hash="$(cat "${files[@]}" | shasum -a 256 | cut -c1-16)"
    kc "$c" -n git-server create configmap workloads-seed "${files[@]/#/--from-file=}" --dry-run=client -o yaml \
      | kc "$c" apply --server-side --force-conflicts -f - >/dev/null
    current="$(kc "$c" -n git-server get deploy git-server \
      -o jsonpath='{.spec.template.metadata.annotations.dev\.resource-optimizer/seed-hash}' 2>/dev/null || true)"
    if [[ "$current" != "$hash" ]]; then
      kc "$c" -n git-server patch deploy git-server --type merge \
        -p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"dev.resource-optimizer/seed-hash\":\"$hash\"}}}}}" >/dev/null
    fi
    kc "$c" -n git-server rollout status deploy/git-server --timeout=3m >/dev/null
    if kc "$c" -n argocd get application workloads >/dev/null 2>&1; then
      kc "$c" -n argocd annotate application workloads argocd.argoproj.io/refresh=hard --overwrite >/dev/null
    fi
    log "git server seeded ($hash), Argo CD refresh requested"
  else
    kc "$c" -n default apply -f "$DEV/workloads/" >/dev/null
    log "workloads applied"
  fi
done
