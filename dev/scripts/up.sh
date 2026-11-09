#!/usr/bin/env bash
# Bring up (or converge) the local fleet: registries, ro-hub, ro-spoke-1, ro-spoke-2 and everything on them.
# Idempotent: re-running skips existing clusters and re-applies charts/manifests.
. "$(dirname "$0")/lib.sh"

START=$SECONDS
require_tools
LOGDIR="$(mktemp -d "${TMPDIR:-/tmp}/ro-dev-up.XXXXXX")"

mem_bytes=$(docker info --format '{{.MemTotal}}')
if (( mem_bytes < 7 * 1024 * 1024 * 1024 )); then
  warn "Docker has $((mem_bytes / 1024 / 1024)) MiB of memory; 8 GiB or more is recommended (dev/README.md)"
fi

# --- registries -------------------------------------------------------------------------------------
docker network inspect "$NETWORK" >/dev/null 2>&1 || docker network create "$NETWORK" >/dev/null
if ! container_exists "k3d-$REGISTRY"; then
  log "creating registry k3d-$REGISTRY (localhost:$REGISTRY_PORT)"
  k3d registry create "$REGISTRY" --port "127.0.0.1:$REGISTRY_PORT" --image "$REGISTRY_IMAGE" \
    --default-network "$NETWORK" >/dev/null
fi
if ! container_exists k3d-ro-dockerio-cache; then
  log "creating docker.io pull-through cache k3d-ro-dockerio-cache"
  k3d registry create ro-dockerio-cache --port 127.0.0.1:5006 --image "$REGISTRY_IMAGE" --default-network "$NETWORK" \
    --proxy-remote-url https://registry-1.docker.io -v ro-dockerio-cache:/var/lib/registry >/dev/null
fi
docker start "k3d-$REGISTRY" k3d-ro-dockerio-cache >/dev/null  # no-op when running

# --- clusters ---------------------------------------------------------------------------------------
create_cluster() {
  local c="$1"
  if cluster_exists "$c"; then
    log "cluster $c exists"
    docker start "k3d-$c-server-0" >/dev/null 2>&1 || true
  else
    log "creating cluster $c ($K3S_IMAGE)"
    k3d cluster create --config "$DEV/k3d/$c.yaml" --image "$K3S_IMAGE" >"$LOGDIR/create-$c.log" 2>&1 \
      || { cat "$LOGDIR/create-$c.log"; die "k3d cluster create $c failed"; }
  fi
}
# Hub first: it creates the network entry the spokes resolve, and its IP feeds the spoke hub-link.
create_cluster "$HUB"
pids=()
for s in "${SPOKES[@]}"; do create_cluster "$s" & pids+=($!); done
for p in "${pids[@]}"; do wait "$p" || die "cluster creation failed"; done
for c in "${CLUSTERS[@]}"; do
  kc "$c" wait --for=condition=Ready node --all --timeout=120s >/dev/null
done
HUB_IP="$(hub_ip)"
[[ -n "$HUB_IP" ]] || die "could not determine hub IP on network $NETWORK"
log "hub node IP on $NETWORK: $HUB_IP"

# --- per-cluster installs (run in parallel) -----------------------------------------------------------
install_hub() {
  export LOG_PREFIX=$HUB
  kc "$HUB" create namespace monitoring --dry-run=client -o yaml | kc "$HUB" apply -f - >/dev/null
  "$DEV/scripts/sync-dashboards.sh" >/dev/null
  kc "$HUB" apply -f "$DEV/manifests/hub/postgres.yaml" >/dev/null
  kc "$HUB" apply -f "$DEV/manifests/hub/dex.yaml" >/dev/null
  kc "$HUB" apply -f "$DEV/manifests/hub/keycloak.yaml" >/dev/null
  helm_install "$HUB" monitoring vm victoria-metrics-single "$VM_REPO" "$VM_SINGLE_CHART_VERSION" \
    -f "$DEV/helm/victoria-metrics.yaml"
  helm_install "$HUB" monitoring grafana grafana "$GRAFANA_REPO" "$GRAFANA_CHART_VERSION" \
    -f "$DEV/helm/grafana.yaml"
  helm_install "$HUB" monitoring vmagent victoria-metrics-agent "$VM_REPO" "$VM_AGENT_CHART_VERSION" \
    -f "$DEV/helm/vmagent-hub.yaml"
  kc "$HUB" -n postgres rollout status statefulset/postgres --timeout=5m >/dev/null
  kc "$HUB" -n dex rollout status deployment/dex --timeout=5m >/dev/null
  kc "$HUB" -n keycloak rollout status deployment/keycloak --timeout=5m >/dev/null
  log "hub ready"
}

install_spoke() {
  local c="$1"
  export LOG_PREFIX=$c
  sed "s/HUB_IP/$HUB_IP/g" "$DEV/manifests/spoke/hub-link.yaml" | kc "$c" apply -f - >/dev/null
  helm_install "$c" monitoring kube-state-metrics kube-state-metrics "$KSM_REPO" "$KSM_CHART_VERSION" \
    -f "$DEV/helm/kube-state-metrics.yaml"
  helm_install "$c" monitoring vmagent victoria-metrics-agent "$VM_REPO" "$VM_AGENT_CHART_VERSION" \
    -f "$DEV/helm/vmagent.yaml" --set-string "config.global.external_labels.cluster=$c"
  helm_install "$c" cert-manager cert-manager cert-manager "$CERT_MANAGER_REPO" "$CERT_MANAGER_CHART_VERSION" \
    -f "$DEV/helm/cert-manager.yaml"
  if [[ "$c" == ro-spoke-1 ]]; then
    helm_install "$c" argocd argocd argo-cd "$ARGOCD_REPO" "$ARGOCD_CHART_VERSION" -f "$DEV/helm/argocd.yaml"
    kc "$c" apply -f "$DEV/manifests/spoke-1/git-server.yaml" >/dev/null
  fi
  "$DEV/scripts/sync-workloads.sh" "$c" >/dev/null
  if [[ "$c" == ro-spoke-1 ]]; then
    kc "$c" apply -f "$DEV/manifests/spoke-1/argocd-app.yaml" >/dev/null
  fi
  log "platform ready"
}

pids=()
install_hub >"$LOGDIR/install-$HUB.log" 2>&1 & pids+=($!)
for s in "${SPOKES[@]}"; do install_spoke "$s" >"$LOGDIR/install-$s.log" 2>&1 & pids+=($!); done
failed=0
for p in "${pids[@]}"; do wait "$p" || failed=1; done
cat "$LOGDIR"/install-*.log >&2
((failed == 0)) || die "install failed, logs in $LOGDIR"

# --- wait for the fleet to be useful ----------------------------------------------------------------
log "waiting for Argo CD to sync the workloads on ro-spoke-1"
for _ in $(seq 1 60); do
  st="$(kc ro-spoke-1 -n argocd get application workloads -o jsonpath='{.status.sync.status}' 2>/dev/null || true)"
  [[ "$st" == Synced ]] && break
  sleep 5
done
[[ "$st" == Synced ]] || die "Argo CD application 'workloads' is '$st', expected Synced"

for s in "${SPOKES[@]}"; do
  log "$s: waiting for sample workloads"
  kc "$s" -n default wait --for=condition=Available deploy -l 'app,app!=memory-leak' --timeout=5m >/dev/null
  # OnDelete StatefulSets have no rollout status
  kc "$s" -n default wait --for=jsonpath='{.status.readyReplicas}'=2 statefulset/statefulset-ondelete --timeout=5m >/dev/null
  kc "$s" -n default rollout status daemonset/daemonset-agent --timeout=5m >/dev/null
done

log "waiting for both spokes' metrics in the hub (localhost:18428)"
spokes_with_metrics() {
  local q='count by (cluster)(kube_pod_container_resource_requests) and on(cluster) count by (cluster)(container_cpu_usage_seconds_total)'
  curl -sf -G localhost:18428/api/v1/query --data-urlencode "query=$q" | grep -o '"cluster":"ro-spoke-[12]"' | sort -u | wc -l | tr -d ' '
}
for _ in $(seq 1 60); do
  n="$(spokes_with_metrics || true)"
  [[ "$n" == 2 ]] && break
  sleep 5
done
[[ "$n" == 2 ]] || die "hub sees metrics from $n/2 spokes"

rm -rf "$LOGDIR"
log "dev-up done in $((SECONDS - START))s"
"$DEV/scripts/status.sh"
