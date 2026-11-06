#!/usr/bin/env bash
# Applies the "Copy as…" patches (strategic merge patch, kubectl patch, Kustomize, Helm values) to sample
# Deployments, StatefulSets, DaemonSets and CronJobs with `--dry-run=server`.
#
#   KERF_DRYRUN_CONTEXT=k3d-ro-spoke-1 npm run test:patch-dry-run
#
# It creates a throw-away namespace (kerf-dryrun-<random>), creates replica-less samples in it, runs
# src/lib/patch/dry-run.test.ts and deletes the namespace again. Nothing else in the cluster is touched.
set -euo pipefail

context="${KERF_DRYRUN_CONTEXT:-$(kubectl config current-context)}"
ns="kerf-dryrun-$(od -An -N3 -tx1 /dev/urandom | tr -d " \n")"

cleanup() {
  kubectl --context "$context" delete namespace "$ns" --wait=false --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

kubectl --context "$context" create namespace "$ns" >/dev/null
echo "dry-run check: context=$context namespace=$ns"
cd "$(dirname "$0")/.."
KERF_DRYRUN_CONTEXT="$context" KERF_DRYRUN_NAMESPACE="$ns" npx vitest --run src/lib/patch/dry-run.test.ts "$@"
