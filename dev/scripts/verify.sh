#!/usr/bin/env bash
# The fleet verification checks, runnable at any time against the running fleet.
. "$(dirname "$0")/lib.sh"
export LOG_PREFIX=verify
fail=0

q() { curl -sf -G localhost:18428/api/v1/query --data-urlencode "query=$1"; }
clusters_in() { { grep -o '"cluster":"[^"]*"' || true; } | sort -u | tr '\n' ' '; }

for metric in container_cpu_usage_seconds_total container_memory_working_set_bytes \
              kube_pod_container_resource_requests kube_pod_container_resource_limits \
              kube_node_labels kube_horizontalpodautoscaler_spec_target_metric; do
  got="$(q "count by (cluster)($metric)" | clusters_in)"
  if [[ "$got" == *ro-spoke-1* && "$got" == *ro-spoke-2* ]]; then
    log "ok   $metric: $got"
  else
    log "FAIL $metric: '$got'"; fail=1
  fi
done

dups="$(q 'count by (cluster, job)(kube_pod_info{job!="kube-state-metrics"})' | clusters_in)"
[[ -z "$dups" ]] && log "ok   no duplicate kube-state-metrics scrapes" || { log "FAIL kube_* series from a second job: $dups"; fail=1; }

for s in "${SPOKES[@]}"; do
  bad="$(kc "$s" get pods -A --no-headers | grep -E 'Error|CrashLoop|ImagePull|Pending' | grep -v '^default *memory-leak-' || true)"
  if [[ -n "$bad" ]]; then log "FAIL $s unhealthy pods:"; echo "$bad"; fail=1; else log "ok   $s pods (memory-leak excepted)"; fi
done

app="$(kc ro-spoke-1 -n argocd get application workloads -o jsonpath='{.status.sync.status}/{.status.health.status}')"
[[ "$app" == Synced/* ]] && log "ok   Argo CD app workloads: $app" || { log "FAIL Argo CD app workloads: $app"; fail=1; }

code="$(curl -s -o /dev/null -w '%{http_code}' localhost:13000/api/health)"
[[ "$code" == 200 ]] && log "ok   Grafana /api/health" || { log "FAIL Grafana /api/health: $code"; fail=1; }

code="$(curl -s -o /dev/null -w '%{http_code}' localhost:15556/dex/.well-known/openid-configuration)"
[[ "$code" == 200 ]] && log "ok   Dex discovery" || { log "FAIL Dex discovery (http://localhost:15556/dex): $code"; fail=1; }

code="$(curl -s -o /dev/null -w '%{http_code}' localhost:18180/realms/kerf/protocol/saml/descriptor)"
[[ "$code" == 200 ]] && log "ok   Keycloak SAML metadata" || { log "FAIL Keycloak SAML metadata (http://localhost:18180/realms/kerf): $code"; fail=1; }

exit "$fail"
