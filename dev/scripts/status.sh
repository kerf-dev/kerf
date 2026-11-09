#!/usr/bin/env bash
# Show cluster state and access URLs.
. "$(dirname "$0")/lib.sh"

for c in "${CLUSTERS[@]}"; do
  if cluster_exists "$c"; then
    printf '%-11s %s\n' "$c" "$(kc "$c" get pods -A --no-headers 2>/dev/null \
      | awk '{s[$4]++} END {for (k in s) printf "%s=%d ", k, s[k]}')"
  else
    printf '%-11s %s\n' "$c" "absent"
  fi
done
cat <<'URLS'

  VictoriaMetrics  http://localhost:18428        (in-cluster: victoria-metrics.monitoring.svc:8428 on ro-hub,
                                                  victoria-metrics.hub.svc:8428 on the spokes)
  Grafana          http://localhost:13000        (anonymous admin; admin/admin)
  Postgres         postgres://kerf:kerf@localhost:15432/kerf
  Dex (OIDC)       http://localhost:15556/dex    (Sign in with Dex: dex-admin@example.com / ro-dev-dex-admin-pass,
                                                  dex-operator, dex-viewer, dex-nobody; dev/manifests/hub/dex.yaml)
  Keycloak (SAML)  http://localhost:18180        (Sign in with Keycloak: kc-admin / ro-dev-kc-admin-pass, kc-operator,
                                                  kc-viewer, kc-nobody; dev/manifests/hub/keycloak.yaml)
  Argo CD          http://localhost:18090        (ro-spoke-1; admin/admin123)
  Git server       git://localhost:19418/workloads.git (ro-spoke-1, seeded from dev/workloads/)
  Registry         push localhost:5005/<image>, pull k3d-ro-registry:5005/<image>
  Kube contexts    k3d-ro-hub (:16443), k3d-ro-spoke-1 (:16444), k3d-ro-spoke-2 (:16445)
URLS
