#!/usr/bin/env bash
# WO-18e: install resource-optimizer on two fresh k3d clusters by following docs/install/eks.md.
#   staging.sh up        clusters + the guide's prerequisites (step 1) + AWS stand-ins (step 3)
#   staging.sh install   steps 4 and 5 of the guide (hub cluster, first sign-in, tokens, workload cluster)
#   staging.sh verify    every Check of steps 4-6, failover, upgrade without 5xx
#   staging.sh uninstall step 9
#   staging.sh down      delete both clusters
# Run `make release REGISTRY=localhost:5005/resource-optimizer VERSION=$VERSION` first (step 2).
# shellcheck source=lib.sh
. "$(dirname "$0")/lib.sh"
cd "$ROOT"

hub_node_ip() { docker inspect -f '{{(index .NetworkSettings.Networks "ro-dev").IPAddress}}' k3d-ro-stg-hub-server-0; }

clusters() {
  log "clusters"
  for c in ro-stg-hub ro-stg-spoke; do
    k3d cluster list "$c" >/dev/null 2>&1 || k3d cluster create --config "$INSTALL/k3d/$c.yaml" --image "$K3S_IMAGE"
  done
}

vmsingle() { # ctx name port svctype nodeport
  local np=""; [ -n "${5:-}" ] && np=", nodePort: $5"
  sed -e "s|VM_IMAGE|$VM_IMAGE|; s|NAME|$2|g; s|PORT NODEPORT|$3$np|; s|PORT|$3|g; s|SVCTYPE|$4|" \
    "$INSTALL/manifests/vmsingle.yaml" | kubectl --context "$1" apply -f - >/dev/null
}

prereqs() {
  log "step 1: prerequisites (cert-manager, ESO, Prometheus Operator CRDs, metrics) and the stand-ins"
  for ctx in "$HUB_CTX" "$SPOKE_CTX"; do
    helm --kube-context "$ctx" upgrade --install cert-manager "$CERT_MANAGER_CHART" --version "$CERT_MANAGER_VERSION" \
      -n cert-manager --create-namespace --set crds.enabled=true --wait >/dev/null
    helm --kube-context "$ctx" upgrade --install external-secrets "$ESO_CHART" --version "$ESO_VERSION" \
      -n external-secrets --create-namespace --wait >/dev/null
    helm --kube-context "$ctx" upgrade --install prometheus-operator-crds "$PROM_CRDS_CHART" --version "$PROM_CRDS_VERSION" \
      -n monitoring --create-namespace >/dev/null
    kubectl --context "$ctx" apply -f "$INSTALL/manifests/ca.yaml" >/dev/null 2>&1 || true
  done
  # One CA for both clusters (an ACM public chain is trusted everywhere): copy the hub's CA to the spoke.
  kh -n cert-manager wait certificate/ro-staging-ca --for=condition=Ready --timeout=2m >/dev/null
  kh -n cert-manager get secret ro-staging-ca -o json | jq 'del(.metadata.uid,.metadata.resourceVersion,.metadata.creationTimestamp,.metadata.annotations,.metadata.labels,.metadata.managedFields)' \
    | ks apply -f - >/dev/null
  ks -n cert-manager delete certificate ro-staging-ca --ignore-not-found >/dev/null
  kh -n cert-manager get secret ro-staging-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > "$WORK/ca.crt"
  sm_apply

  # Metrics: the hub's datasource (METRICS_URL) and, on the workload cluster, kube-state-metrics,
  # a Prometheus stand-in on 9090 (SPOKE_METRICS_URL), and vmagent remote-writing to both.
  vmsingle "$HUB_CTX" vmsingle 8428 NodePort 30428
  vmsingle "$SPOKE_CTX" prometheus-operated 9090 ClusterIP
  helm --kube-context "$SPOKE_CTX" upgrade --install kube-state-metrics "$KSM_CHART" --version "$KSM_VERSION" \
    -n monitoring -f "$ROOT/dev/helm/kube-state-metrics.yaml" >/dev/null
  helm --kube-context "$SPOKE_CTX" upgrade --install vmagent "$VMAGENT_CHART" --version "$VMAGENT_VERSION" \
    -n monitoring -f "$ROOT/dev/helm/vmagent.yaml" \
    --set "remoteWrite[0].url=http://$(hub_node_ip):30428/api/v1/write" \
    --set "remoteWrite[1].url=http://prometheus-operated.monitoring.svc:9090/api/v1/write" \
    --set-string "config.global.external_labels.cluster=$CLUSTER" >/dev/null

  # Route 53 stand-in inside the clusters.
  for ctx in "$HUB_CTX" "$SPOKE_CTX"; do
    sed -e "s|HUB_NODE_IP|$(hub_node_ip)|; s|HUB_HOST|$HUB_HOST|; s|UI_HOST|$UI_HOST|" "$INSTALL/manifests/coredns-custom.yaml" \
      | kubectl --context "$ctx" apply -f - >/dev/null
    kubectl --context "$ctx" -n kube-system rollout restart deploy/coredns >/dev/null
  done

  # RDS stand-in: CloudNativePG, master user ro_admin.
  helm --kube-context "$HUB_CTX" upgrade --install cnpg "$CNPG_CHART" --version "$CNPG_VERSION" \
    -n cnpg-system --create-namespace --wait >/dev/null
  kh create namespace rds --dry-run=client -o yaml | kh apply -f - >/dev/null
  if ! kh -n rds get secret ro-admin-master >/dev/null 2>&1; then
    kh -n rds create secret generic ro-admin-master --type=kubernetes.io/basic-auth \
      --from-literal=username=ro_admin --from-literal=password="$(openssl rand -hex 24)" >/dev/null
  fi
  sed "s|PG_IMAGE|$PG_IMAGE|" "$INSTALL/manifests/postgres.yaml" | kh apply -f - >/dev/null
  until kh -n rds get cluster ro-hub -o jsonpath='{.status.readyInstances}' 2>/dev/null | grep -q 1; do sleep 5; done
  echo "prerequisites ready"
}

# --- Step 3 stand-ins ----------------------------------------------------------------------------------
export RDS_HOST=ro-hub-rw.rds.svc
step3() {
  log "step 3.2: database role and database (RDS stand-in)"
  kh create namespace "$NS" --dry-run=client -o yaml | kh apply -f - >/dev/null
  if ! kh -n rds exec ro-hub-1 -c postgres -- psql -tAc "select 1 from pg_roles where rolname='ro'" | grep -q 1; then
    export APP_PW=$(openssl rand -hex 24)
    MASTER_PW=$(kh -n rds get secret ro-admin-master -o jsonpath='{.data.password}' | base64 -d)
    # The guide's command, as written:
    { echo "$MASTER_PW"
      echo "CREATE ROLE ro LOGIN PASSWORD '$APP_PW';"
      echo "GRANT ro TO ro_admin;"
      echo "CREATE DATABASE resource_optimizer OWNER ro;"
    } | kh -n "$NS" run psql-setup --rm -i --restart=Never --image=postgres:16-alpine -- \
      sh -c "read -r PGPASSWORD; export PGPASSWORD; exec psql 'host=$RDS_HOST user=ro_admin dbname=postgres sslmode=require' -v ON_ERROR_STOP=1"
    unset MASTER_PW
    log "step 3.3: Secrets Manager (Fake provider)"
    aws secretsmanager create-secret --name resource-optimizer/hub/db \
      --secret-string "$(jq -cn --arg p "$APP_PW" '{username: "ro", password: $p}')"
    aws secretsmanager create-secret --name resource-optimizer/hub/bootstrap-admin \
      --secret-string "$(jq -cn --arg p "$(openssl rand -base64 24)" '{password: $p}')"
    aws secretsmanager create-secret --name resource-optimizer/hub/datasource-key \
      --secret-string "$(jq -cn --arg k "$(openssl rand -base64 32)" '{key: $k}')"
    unset APP_PW
  fi
  echo "Check: $(aws secretsmanager list-secrets --filters Key=name,Values=resource-optimizer/ --query 'SecretList[].Name' --output text)"
  echo "Check: ClusterSecretStore Ready=$(kh get clustersecretstore aws-secrets-manager -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
  log "step 3.5: certificate (ACM stand-in)"
  sed -e "s|HUB_HOST|$HUB_HOST|; s|UI_HOST|$UI_HOST|" "$INSTALL/manifests/tls.yaml" | kh apply -f - >/dev/null
  kh -n "$NS" wait certificate/ro-staging-tls --for=condition=Ready --timeout=2m
}

# --- Step 4: hub cluster -------------------------------------------------------------------------------
step4() {
  log "step 4.1: values file"
  sed -e "s|ro-hub.c0ffee0c0ffe.eu-west-1.rds.amazonaws.com|$RDS_HOST|g" \
      -e "s|arn:aws:acm:eu-west-1:123456789012:certificate/00000000-0000-0000-0000-000000000000|staging|g" \
      -e "s|hub.ro.example.com|$HUB_HOST|g" -e "s|ui.ro.example.com|$UI_HOST|g" \
      -e "s|http://vmsingle.monitoring.svc:8428|$METRICS_URL|g" \
      -e "s|10.0.0.0/16|$HUB_VPC_CIDR|g" -e "s|ops-eu1|$HUB_CLUSTER|g" \
      -e "s|123456789012|$ACCOUNT|g" -e "s|eu-west-1|$AWS_REGION|g" \
      deploy/examples/eks/values-hub.yaml > "$WORK/values-hub.yaml"
  # Fix the registry host (ECR's is $ACCOUNT.dkr.ecr...; the stand-in is localhost:5005).
  sed -i '' -e "s|$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com|$ECR_IN_CLUSTER|g" "$WORK/values-hub.yaml"
  echo "Check (grep, expect nothing):"; grep -nE '123456789012|example\.com|c0ffee|ops-eu1|00000000-0000' "$WORK/values-hub.yaml" || true
  helm template resource-optimizer "oci://$REGISTRY/charts/resource-optimizer" --version "$VERSION" $HELM_OCI_FLAGS -n "$NS" \
    -f "$WORK/values-hub.yaml" -f "$INSTALL/values-staging-hub.yaml" | grep -E '^kind: (Deployment|Ingress|ExternalSecret)$' | sort | uniq -c

  log "step 4.2: CA bundle (RDS stand-in: the CloudNativePG CA)"
  kh -n rds get secret ro-hub-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > "$WORK/global-bundle.pem"
  kh -n "$NS" create configmap rds-ca-bundle --from-file=global-bundle.pem="$WORK/global-bundle.pem" --dry-run=client -o yaml | kh apply -f - >/dev/null
  echo "Check: $(kh -n "$NS" get configmap rds-ca-bundle -o jsonpath='{.data.global-bundle\.pem}' | grep -c 'BEGIN CERTIFICATE') certificate(s)"

  log "step 4.3: install"
  helm --kube-context "$HUB_CTX" upgrade --install resource-optimizer "oci://$REGISTRY/charts/resource-optimizer" $HELM_OCI_FLAGS \
    --version "$VERSION" -n "$NS" -f "$WORK/values-hub.yaml" -f "$INSTALL/values-staging-hub.yaml"
  kh -n "$NS" rollout status deploy/hub --timeout=10m
  kh -n "$NS" rollout status deploy/web --timeout=5m
  kh -n "$NS" get externalsecrets
  kh -n "$NS" get pods -l app.kubernetes.io/instance=resource-optimizer
  leader_check

  log "step 4.4: DNS (stand-in: CoreDNS hosts + curl --connect-to)"
  echo "Check: $(curl -sS https://$HUB_HOST/readyz) $(curl -sS -o /dev/null -w '%{http_code}' https://$UI_HOST/)"

  log "step 4.5: first sign-in, ui and pricing tokens"
  signin
  for role in ui pricing; do
    if [ "$(aws secretsmanager get-secret-value --secret-id resource-optimizer/hub/$role-token)" = null ]; then
      val=$(curl -sS -b "$WORK/cookies" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
        -d "$(jq -cn --arg r $role '{role: $r, description: ("resource-optimizer " + $r), reason: "install"}')" \
        https://$UI_HOST/v1/admin/tokens | jq -r .value)
      aws secretsmanager create-secret --name resource-optimizer/hub/$role-token \
        --secret-string "$(jq -cn --arg t "$val" '{token: $t}')" --query Name --output text
    fi
  done
  kh -n "$NS" annotate externalsecret web-hub-token pricing-hub-token force-sync=$(date +%s) --overwrite
  sleep 10
  kh -n "$NS" rollout restart deploy/web
  kh -n "$NS" get externalsecrets
  kh -n "$NS" rollout status deploy/pricing --timeout=5m
  kh -n "$NS" rollout status deploy/web --timeout=5m
  echo "Check: $(curl -sS https://$UI_HOST/v1/me | jq -c '{authMethod, role}')"
}

signin() {
  export ADMIN_PW=$(aws secretsmanager get-secret-value --secret-id resource-optimizer/hub/bootstrap-admin --query SecretString --output text | jq -r .password)
  CSRF=$(curl -sS -c "$WORK/cookies" -H 'Content-Type: application/json' \
    -d "$(jq -cn --arg p "$ADMIN_PW" '{username: "admin", password: $p}')" https://$UI_HOST/v1/auth/login | jq -r .csrfToken)
  export CSRF
  [ -n "$CSRF" ] && [ "$CSRF" != null ] || { echo "sign-in failed"; return 1; }
  echo "Check: signed in (csrf token ${#CSRF} chars)"
}

leader_check() {
  echo "Check: readyz=$(kh get --raw "/api/v1/namespaces/$NS/services/hub:8080/proxy/readyz")"
  local sum=0
  for p in $(kh -n "$NS" get pods -l app.kubernetes.io/name=hub --field-selector=status.phase=Running -o name); do
    v=$(kh get --raw "/api/v1/namespaces/$NS/pods/${p#pod/}:8080/proxy/metrics" | awk '/^ro_hub_leader /{print $2}')
    echo "  ${p#pod/} ro_hub_leader $v"; sum=$((sum + ${v:-0}))
  done
  echo "  sum=$sum"; [ "$sum" = 1 ]
}

# --- Step 5: the workload cluster ----------------------------------------------------------------------
step5() {
  log "step 5.1: prepare (ClusterSecretStore is applied by the stand-in; cert-manager)"
  echo "Check: $(ks get clustersecretstore aws-secrets-manager -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
  ks -n cert-manager get deploy

  log "step 5.2: register the cluster (the wizard's API calls)"
  signin
  if [ "$(aws secretsmanager get-secret-value --secret-id resource-optimizer/clusters/$CLUSTER/agent-token)" = null ]; then
    register_cluster
  fi

  log "step 5.3: install"
  sed -e "s|hub.ro.example.com|$HUB_HOST|g" -e "s|prod-eu1|$CLUSTER|g" -e "s|10.1.0.0/16|$SPOKE_VPC_CIDR|g" \
      -e "s|http://prometheus-operated.monitoring.svc:9090|$SPOKE_METRICS_URL|g" \
      -e "s|123456789012|$ACCOUNT|g" -e "s|eu-west-1|$AWS_REGION|g" \
      deploy/examples/eks/values-spoke.yaml > "$WORK/values-spoke-$CLUSTER.yaml"
  sed -i '' -e "s|$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com|$ECR_IN_CLUSTER|g" "$WORK/values-spoke-$CLUSTER.yaml"
  grep -nE '123456789012|example\.com|prod-eu1' "$WORK/values-spoke-$CLUSTER.yaml" || true
  # caBundle: the staging CA is private (ACM's chain is public); the guide's troubleshooting says to set it.
  helm --kube-context "$SPOKE_CTX" upgrade --install resource-optimizer "oci://$REGISTRY/charts/resource-optimizer" $HELM_OCI_FLAGS \
    --version "$VERSION" -n "$NS" --create-namespace -f "$WORK/values-spoke-$CLUSTER.yaml" -f "$INSTALL/values-staging-spoke.yaml" \
    --set-file agent.hub.caBundle.pem="$WORK/ca.crt" --wait --timeout 10m
  ks -n "$NS" get externalsecret resource-optimizer-agent-token
  ks -n "$NS" get pods -l app.kubernetes.io/instance=resource-optimizer
  ks -n "$NS" logs deploy/resource-optimizer-agent | grep -m1 'starting agent' || true

  log "step 5.4: readiness"
  ks get --raw "/api/v1/namespaces/$NS/services/https:webhook:443/proxy/healthz" 2>&1 | tail -1 || true
  curl -sS -b "$WORK/cookies" -H "X-CSRF-Token: $CSRF" -X POST "https://$UI_HOST/v1/admin/recompute?cluster=$CLUSTER" | jq -c .
  wait_state AutomationReady 20
}

register_cluster() {
  # The wizard (web/src) calls POST /v1/clusters with the identity and metrics binding, then shows the
  # agent token once. Same calls here.
  local body token
  body=$(jq -cn --arg c "$CLUSTER" '{name: $c, environment: "non-prod", owner: "platform", provider: "aws",
    region: "eu-west-1", metrics: {datasourceId: "local", clusterLabel: "cluster", clusterLabelValue: $c},
    createAgentToken: true, reason: "staging install (WO-18e)"}')
  token=$(curl -sS -b "$WORK/cookies" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' -d "$body" \
    https://$UI_HOST/v1/admin/clusters | tee "$WORK/register.json" | jq -r '.agentToken.value // empty')
  [ -n "$token" ] || { echo "register failed: $(jq -c 'del(.agentToken,.token)' "$WORK/register.json")"; return 1; }
  AGENT_TOKEN=$token
  aws secretsmanager create-secret --name resource-optimizer/clusters/$CLUSTER/agent-token \
    --secret-string "$(jq -cn --arg t "$AGENT_TOKEN" '{token: $t}')" --query Name --output text
  unset AGENT_TOKEN
}

readiness() {
  curl -sS -b "$WORK/cookies" https://$UI_HOST/v1/clusters/$CLUSTER/readiness \
    | jq -c '{state, summary, failing: [.checks[] | select(.status != "pass") | {id, severity, status}]}'
}

wait_state() { # state minutes
  local end=$(( $(date +%s) + $2 * 60 )) r="" last=$(date +%s)
  while [ "$(date +%s)" -lt $end ]; do
    r=$(readiness); echo "  $(date +%H:%M:%S) $r"
    echo "$r" | jq -e --arg s "$1" '.state == $s' >/dev/null && return 0
    # The recommender runs daily, and it needs some minutes of metrics before it sees a workload; a
    # run requested right after the install finds nothing. As the guide's step 5.4 says, start a run:
    # every 3 minutes while recommendations are missing.
    if echo "$r" | jq -e '.failing[] | select(.id == "metrics.recommendations")' >/dev/null && [ $(( $(date +%s) - last )) -ge 180 ]; then
      echo "  recompute: $(curl -sS -b "$WORK/cookies" -H "X-CSRF-Token: $CSRF" -X POST "https://$UI_HOST/v1/admin/recompute?cluster=$CLUSTER" | jq -c '{runId}')"
      last=$(date +%s)
    fi
    curl -sS -o /dev/null -b "$WORK/cookies" -H "X-CSRF-Token: $CSRF" -X POST https://$UI_HOST/v1/clusters/$CLUSTER/readiness/recheck || true
    sleep 30
  done
  echo "state $1 not reached in $2 minutes"; return 1
}

# --- Step 6 and the WO-18e checks ----------------------------------------------------------------------
autopatch() {
  log "step 6: auto-patch one workload (namespace staging-demo)"
  signin
  ks apply -f "$INSTALL/fixtures/workload.yaml" >/dev/null
  ks -n staging-demo rollout status deploy/web --timeout=5m
  if ! curl -sS -b "$WORK/cookies" "https://$UI_HOST/v1/admin/policies?cluster=$CLUSTER" | jq -e '.items[]? | select(.name=="staging-demo")' >/dev/null; then
    body=$(jq -cn --arg c "$CLUSTER" '{type: "policy", name: "staging-demo", clusters: [$c], reason: "staging auto-patch check (WO-18e)",
      spec: {selector: {namespaces: ["staging-demo"]}, priority: 1000, cpu: {mode: "auto-patch"}, memory: {mode: "audit"},
             apply: {strategy: "in-place"}, guardrails: {learningPeriod: "0s", minConfidence: "0"}}}')
    curl -sS -b "$WORK/cookies" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' -d "$body" \
      "https://$UI_HOST/v1/admin/policies" | jq -c '{id, name, version}'
  fi
  local end=$(( $(date +%s) + 20 * 60 )) cpu=""
  while [ "$(date +%s)" -lt $end ]; do
    cpu=$(ks -n staging-demo get pods -l app=web -o jsonpath='{.items[0].spec.containers[0].resources.requests.cpu}')
    phase=$(ks -n staging-demo get rsr -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
    echo "  $(date +%H:%M:%S) pod cpu request=$cpu recommendation phase=${phase:-none}"
    [ "$cpu" != 500m ] && [ -n "$cpu" ] && { echo "Check: applied, cpu request 500m -> $cpu"; return 0; }
    curl -sS -o /dev/null -b "$WORK/cookies" -H "X-CSRF-Token: $CSRF" -X POST "https://$UI_HOST/v1/admin/recompute?cluster=$CLUSTER" || true
    sleep 60
  done
  echo "auto-patch not applied in 20 minutes"; return 1
}

failover() {
  log "failover: delete the hub leader pod"
  local leader="" t0 t1
  for p in $(kh -n "$NS" get pods -l app.kubernetes.io/name=hub -o name); do
    [ "$(kh get --raw "/api/v1/namespaces/$NS/pods/${p#pod/}:8080/proxy/metrics" | awk '/^ro_hub_leader /{print $2}')" = 1 ] && leader=${p#pod/}
  done
  echo "  leader: $leader"
  t0=$(date +%s)
  kh -n "$NS" delete pod "$leader" --grace-period=0 --force >/dev/null 2>&1
  until leader_check >/dev/null 2>&1; do sleep 1; [ $(( $(date +%s) - t0 )) -gt 60 ] && { echo "no leader within 60 s"; return 1; }; done
  t1=$(date +%s); echo "Check: a new leader in $((t1 - t0)) s"; leader_check
  signin
  curl -sS -b "$WORK/cookies" -H "X-CSRF-Token: $CSRF" -X POST "https://$UI_HOST/v1/admin/recompute?cluster=$CLUSTER" | jq -c .
}

upgrade() { # VERSION=<new> (released beforehand)
  log "step 8: upgrade the hub to $1 with an API probe loop (expect no 5xx)"
  : > "$WORK/probe.txt"
  ( end=$(( $(date +%s) + 240 )); while [ "$(date +%s)" -lt $end ]; do
      for u in "https://$HUB_HOST/readyz" "https://$UI_HOST/v1/me"; do
        c=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$u" 2>/dev/null) || c=000
        echo "$c $(date +%H:%M:%S) $u" >> "$WORK/probe.txt"
      done
      sleep 0.5; done ) &
  local probe=$!
  helm --kube-context "$HUB_CTX" upgrade resource-optimizer "oci://$REGISTRY/charts/resource-optimizer" $HELM_OCI_FLAGS \
    --version "$1" -n "$NS" -f "$WORK/values-hub.yaml" -f "$INSTALL/values-staging-hub.yaml" >/dev/null
  kh -n "$NS" rollout status deploy/hub --timeout=10m
  kh -n "$NS" rollout status deploy/web --timeout=5m
  helm --kube-context "$SPOKE_CTX" upgrade resource-optimizer "oci://$REGISTRY/charts/resource-optimizer" $HELM_OCI_FLAGS \
    --version "$1" -n "$NS" -f "$WORK/values-spoke-$CLUSTER.yaml" -f "$INSTALL/values-staging-spoke.yaml" \
    --set-file agent.hub.caBundle.pem="$WORK/ca.crt" --wait --timeout 10m >/dev/null
  wait $probe || true
  echo "Check: probe status codes: $(cut -d' ' -f1 "$WORK/probe.txt" | sort | uniq -c | tr '\n' ' ')"
  grep -vE '^200 ' "$WORK/probe.txt" || true
  helm --kube-context "$HUB_CTX" -n "$NS" history resource-optimizer | tail -2
  leader_check
  ! grep -qE '^(5..|000) ' "$WORK/probe.txt"
}

uninstall() {
  log "step 9: uninstall"
  helm --kube-context "$SPOKE_CTX" -n "$NS" uninstall resource-optimizer
  helm --kube-context "$HUB_CTX" -n "$NS" uninstall resource-optimizer
  echo "Check (kept on purpose): $(ks get crd -o name | grep resource-optimizer.oz.io | tr '\n' ' ')"
  echo "Check (kept on purpose): $(kh -n "$NS" get externalsecret,secret hub-datasource-key -o name 2>&1 | tr '\n' ' ')"
  ks delete crd rightsizingpolicies.resource-optimizer.oz.io rightsizingrecommendations.resource-optimizer.oz.io
  ks delete namespace "$NS"
  kh -n "$NS" delete externalsecret hub-datasource-key
  kh delete namespace "$NS"
  echo "Check: crds=[$(ks get crd | grep resource-optimizer.oz.io || true)] releases=[$(helm --kube-context "$HUB_CTX" -n "$NS" list -q)]"
}

verify() {
  log "verify"
  leader_check
  echo "Check: $(curl -sS https://$HUB_HOST/readyz) $(curl -sS -o /dev/null -w '%{http_code}' https://$UI_HOST/)"
  signin
  wait_state AutomationReady 20
  autopatch
  failover
}

case "${1:-}" in
  up) clusters; prereqs ;;
  install) step3; step4; step5 ;;
  verify) verify ;;
  recompute) signin >/dev/null; curl -sS -b "$WORK/cookies" -H "X-CSRF-Token: $CSRF" -X POST "https://$UI_HOST/v1/admin/recompute?cluster=$CLUSTER" | jq -c . ;;
  upgrade) upgrade "${2:?version}" ;;
  uninstall) uninstall ;;
  step3|step4|step5|signin|leader_check|readiness|prereqs|clusters|autopatch|failover) "$1" ;;
  down) k3d cluster delete ro-stg-hub ro-stg-spoke; rm -rf "$WORK" ;;
  *) echo "usage: $0 up|install|verify|uninstall|down"; exit 2 ;;
esac
