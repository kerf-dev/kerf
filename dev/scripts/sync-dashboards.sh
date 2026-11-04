#!/usr/bin/env bash
# Pack every *.json under dashboards/ into ConfigMap monitoring/grafana-dashboards on ro-hub.
# Grafana file-provisions it into folder "resource-optimizer" (the ConfigMap volume refreshes within ~1 min).
# Nested paths are flattened: dashboards/fleet/overview.json -> key fleet_overview.json.
. "$(dirname "$0")/lib.sh"
export LOG_PREFIX=dashboards

args=()
count=0
if [[ -d "$ROOT/dashboards" ]]; then
  while IFS= read -r -d '' f; do
    rel="${f#"$ROOT/dashboards/"}"
    args+=("--from-file=${rel//\//_}=$f")
    count=$((count + 1))
  done < <(find "$ROOT/dashboards" -type f -name '*.json' -print0 | sort -z)
fi

kc "$HUB" -n monitoring create configmap grafana-dashboards ${args[@]+"${args[@]}"} --dry-run=client -o yaml \
  | kc "$HUB" apply --server-side --force-conflicts -f - >/dev/null
log "provisioned $count dashboard(s) from dashboards/"
