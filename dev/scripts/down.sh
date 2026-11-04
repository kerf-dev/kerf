#!/usr/bin/env bash
# Delete the three clusters and the image registry. The docker.io pull-through cache is kept so the
# next dev-up does not re-download images; pass --purge (make dev-purge) to remove it as well.
. "$(dirname "$0")/lib.sh"

for c in "${CLUSTERS[@]}"; do
  if cluster_exists "$c"; then
    log "deleting cluster $c"
    k3d cluster delete "$c" >/dev/null
  fi
done
if container_exists "k3d-$REGISTRY"; then
  log "deleting registry k3d-$REGISTRY"
  k3d registry delete "$REGISTRY" >/dev/null
fi
if [[ "${1:-}" == --purge ]]; then
  if container_exists k3d-ro-dockerio-cache; then
    log "deleting docker.io cache"
    k3d registry delete ro-dockerio-cache >/dev/null
  fi
  docker volume rm ro-dockerio-cache >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
fi
log "dev-down done"
