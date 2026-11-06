# Installing kerf on Amazon EKS

This guide installs kerf from a release in your ECR. One chart,
`kerf` (`deploy/charts/kerf`), covers every cluster. `global.mode` selects
what a release installs:

| Cluster | `global.mode` | Installs |
|---|---|---|
| The hub cluster (one) | `hub` | hub (API, recommender, readiness; 2 replicas, Postgres leader), web UI, pricing |
| Each workload cluster | `spoke` | agent (2 replicas, leader election), admission webhook, the CRDs |
| Everything in one cluster | `all` | all of the above |

```
 browsers ──HTTPS──> ALB (UI_HOST) ──> web ──> hub Service ──> hub ×2 ──TLS verify-full──> RDS PostgreSQL
 workload clusters:  agent ──HTTPS + per-cluster token──> internal ALB (HUB_HOST) ──> hub
                     webhook <──9443── EKS control plane
 secrets: AWS Secrets Manager ──External Secrets Operator──> Kubernetes Secrets (every cluster)
 pricing: AWS Pricing API + EC2 spot prices through EKS Pod Identity
```

Each step gives the commands and then a **Check** with its expected output. Run everything from a
checkout of this repository at the release you install (the examples live in `deploy/examples/eks/`),
in one shell, so the variables from step 0 stay set. The design behind this layout: [architecture](../architecture.md#production-packaging).

**Contents:** [0. Variables](#0-variables) · [1. Prerequisites](#1-prerequisites) ·
[2. Images and chart](#2-images-and-chart) · [3. AWS resources](#3-aws-resources) ·
[4. Hub cluster](#4-hub-cluster) · [5. Each workload cluster](#5-each-workload-cluster) ·
[6. Turning on auto-patch](#6-turning-on-auto-patch) · [7. Argo CD instead of helm](#7-argo-cd-instead-of-helm) ·
[8. Upgrade](#8-upgrade) · [9. Uninstall](#9-uninstall) · [10. Troubleshooting](#10-troubleshooting)

## 0. Variables

Set these once. Replace the values on the right of the first block with yours.

```bash
export AWS_REGION=eu-west-1                       # region of the clusters, RDS, Secrets Manager and ECR
export VERSION=0.1.0                              # the release to install (step 2)
export HUB_CLUSTER=ops-eu1                        # EKS cluster name of the hub cluster
export HUB_HOST=hub.ro.example.com                # DNS name of the hub API (internal ALB; agents use it)
export UI_HOST=ui.ro.example.com                  # DNS name of the web UI
export ZONE_ID=Z0123456789EXAMPLE                 # Route 53 public hosted zone holding both names
export METRICS_URL=http://vmsingle.monitoring.svc:8428   # Prometheus-compatible query URL, from the hub cluster,
                                                  # holding the workload clusters' metrics with label `cluster`
```

The examples assume a namespace `monitoring` on every cluster (the scraper's namespace, which the
NetworkPolicies admit, and where the hub's dashboard ConfigMaps go). If yours is called something
else, replace `monitoring` in the two values files after the `sed` in steps 4.1 and 5.3. The
NetworkPolicies open egress by port. With the examples' NetworkPolicies, the port in `METRICS_URL`
must be 8428, 9090 or 443, and in `SPOKE_METRICS_URL` (step 5) 9090 or 443. Otherwise add it to
`pricing.networkPolicy.egress.ports` and `hub.networkPolicy.egress.ports` (hub) or
`agent.networkPolicy.egress.ports` (spoke).

Derived values (no editing):

```bash
export ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
export ECR=$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com
export REGISTRY=$ECR/kerf
export NS=kerf
export WORK=$HOME/ro-install && mkdir -p $WORK    # generated values files and downloads
aws eks update-kubeconfig --region $AWS_REGION --name $HUB_CLUSTER
export HUB_CTX=arn:aws:eks:$AWS_REGION:$ACCOUNT:cluster/$HUB_CLUSTER
```

**Check:**
```bash
kubectl --context $HUB_CTX get nodes -o wide | head -3
```
Expected: your hub cluster's nodes, `STATUS Ready`.

Per workload cluster you set `CLUSTER` and friends in [step 5](#5-each-workload-cluster).

## 1. Prerequisites

| What | Version | Where | Check (expected) |
|---|---|---|---|
| AWS CLI v2, `kubectl`, `jq`, `openssl`, `curl` | current | your shell | `aws --version` → `aws-cli/2.…` |
| Helm | ≥ 3.17 (for `--take-ownership`; Helm 4 works) | your shell | `helm version --short` → `v3.17.…` or later |
| EKS | 1.35 | every cluster | `kubectl --context $HUB_CTX version -o json \| jq -r .serverVersion.gitVersion` → `v1.35.…-eks-…` |
| EKS Pod Identity Agent add-on | current | every cluster | `aws eks describe-addon --cluster-name $HUB_CLUSTER --addon-name eks-pod-identity-agent --query addon.status --output text` → `ACTIVE` |
| AWS Load Balancer Controller | ≥ 2.7 | the hub cluster | `kubectl --context $HUB_CTX get ingressclass alb` → a row `alb  ingress.k8s.aws/alb` |
| External Secrets Operator | ≥ 0.17 (serves `external-secrets.io/v1`), installed in namespace `external-secrets` with ServiceAccount `external-secrets` (the Helm chart's defaults) | every cluster | `kubectl --context $HUB_CTX get crd externalsecrets.external-secrets.io -o jsonpath='{.spec.versions[?(@.served==true)].name}'` → contains `v1` |
| cert-manager (the webhook's serving certificate) | ≥ 1.15 | every workload cluster | `kubectl --context <cluster's context> get crd certificates.cert-manager.io` → one row |
| Prometheus or VictoriaMetrics with kube-state-metrics and the kubelet's cAdvisor metrics, scraper in namespace `monitoring` | any | every workload cluster | see below |
| A scraper on the hub cluster for the hub's and pricing's own metrics (the `serviceMonitor`s of `values-hub.yaml`), writing to the store behind `METRICS_URL` | any | the hub cluster | after 4.5: `curl -s "$METRICS_URL/api/v1/query" --data-urlencode 'query=count(kerf_pricing_nodes)'` → a number. Readiness (`pricing.coverage`) and the alerts read these series from that store. |
| Prometheus Operator CRDs (ServiceMonitor, PrometheusRule), Grafana with the dashboard sidecar | any | every cluster where the examples enable them (all of them, as shipped) | `kubectl --context <cluster's context> get crd servicemonitors.monitoring.coreos.com prometheusrules.monitoring.coreos.com` → two rows. Without them, set `serviceMonitor`, `prometheusRule` and `dashboards` to `enabled: false` (4.1, 5.3). |
| VPC CNI with network policies (`enableNetworkPolicy: "true"`) | VPC CNI ≥ 1.14 | every cluster (the examples enable NetworkPolicies) | `kubectl --context <cluster's context> -n kube-system get ds aws-node -o jsonpath='{.spec.template.spec.containers[*].name}'` → contains `aws-eks-nodeagent` |
| Argo CD (optional, [step 7](#7-argo-cd-instead-of-helm)) | ≥ 2.12 or 3.x | where it runs | `argocd version --short` |

**Metrics.** The hub reads each workload cluster's metrics from a Prometheus-compatible endpoint (a
*datasource*). The simplest layout: every workload cluster's Prometheus remote-writes to one
VictoriaMetrics or Prometheus that the hub cluster reaches at `$METRICS_URL`, with the external label
`cluster=<cluster name>`; that endpoint is the hub's built-in datasource `local`. A per-cluster
Prometheus, Thanos, Mimir or Amazon Managed Prometheus works too: add it in the UI under *Admin →
Metrics sources* and pick it in the cluster wizard. Required series (the readiness checks in
`docs/contracts/readiness.md` test each one): `container_cpu_usage_seconds_total`,
`container_memory_working_set_bytes`, `container_cpu_cfs_*` (cAdvisor), and from kube-state-metrics
`kube_pod_owner`, `kube_replicaset_owner`, `kube_job_owner`, `kube_pod_container_resource_requests/limits`,
`kube_node_labels`. kube-state-metrics must export the node labels pricing uses:

```
--metric-labels-allowlist=nodes=[node.kubernetes.io/instance-type,topology.kubernetes.io/region,topology.kubernetes.io/zone,eks.amazonaws.com/capacityType,karpenter.sh/capacity-type]
```

**Check** (from a pod or port-forward that reaches the endpoint; one workload cluster called `prod-eu1`):
```bash
curl -s "$METRICS_URL/api/v1/query" --data-urlencode 'query=count by (cluster) (kube_pod_owner)' | jq -c '.data.result[] | [.metric.cluster, .value[1]]'
```
Expected: one line per workload cluster, e.g. `["prod-eu1","412"]`.

**Network.** The workload clusters' VPCs must route to the hub cluster's VPC (VPC peering or a transit
gateway): the agents call the hub's internal ALB. Every cluster's nodes need egress to the AWS APIs
(Secrets Manager, ECR, and for pricing the Pricing API and EC2). The hub cluster also pulls
`postgres:16-alpine` from Docker Hub once (step 3.2). Without internet egress, copy that image to your
ECR first and change the `--image` there.

The hub's ALBs are internal. Every `curl https://$HUB_HOST/…` or `https://$UI_HOST/…` in this guide
runs from a machine inside the VPC, or on a VPN or peering into it. The hub ALB admits only the
`alb.ingress.kubernetes.io/inbound-cidrs` of `values-hub.yaml` (default `10.0.0.0/8`): add your VPN's
CIDR there (comma-separated) if it lies outside.

## 2. Images and chart

Build and push the multi-arch images and the OCI charts once per release. `docs/release.md` explains
the build, the immutable tags and the lifecycle policy. Don't paste its `export` lines into this shell:
they set example values for the variables from step 0. ECR does not create repositories on
push, so create them once:

```bash
for repo in hub pricing agent webhook web \
            charts/kerf charts/hub charts/pricing charts/agent charts/webhook charts/web; do
  aws ecr create-repository --region $AWS_REGION --repository-name kerf/$repo \
    --image-tag-mutability IMMUTABLE --image-scanning-configuration scanOnPush=true \
    --query repository.repositoryName --output text \
    || echo "kerf/$repo: not created (RepositoryAlreadyExistsException is fine)"
done
aws ecr get-login-password --region $AWS_REGION | docker login --username AWS --password-stdin $ECR
make release REGISTRY=$REGISTRY VERSION=$VERSION
```

`make release` builds on a clean commit (it refuses uncommitted changes) and needs Docker buildx.
It pushes the umbrella chart and the five component charts. The hub's cluster wizard renders its
install snippets with the umbrella chart.

The chart's `global.imageRegistry` is `$ECR`. The image repositories default to
`kerf/<component>`, and the tags default to the chart version, which is `$VERSION`.

**Check:**
```bash
aws ecr get-login-password --region $AWS_REGION | helm registry login --username AWS --password-stdin $ECR
helm show chart oci://$REGISTRY/charts/kerf --version $VERSION | grep -E '^(name|version|appVersion):'
aws ecr describe-images --region $AWS_REGION --repository-name kerf/hub --image-ids imageTag=$VERSION --query 'imageDetails[0].imageTags' --output text
```
Expected: `name: kerf`, `version: <VERSION>`, `appVersion: <VERSION>`, then `<VERSION>`.
Every node role needs ECR pull access (`AmazonEC2ContainerRegistryReadOnly`, the EKS default).

## 3. AWS resources

### 3.1 Network facts of the hub cluster

```bash
export HUB_VPC_ID=$(aws eks describe-cluster --name $HUB_CLUSTER --query cluster.resourcesVpcConfig.vpcId --output text)
export HUB_VPC_CIDR=$(aws ec2 describe-vpcs --vpc-ids $HUB_VPC_ID --query 'Vpcs[0].CidrBlock' --output text)
export HUB_CLUSTER_SG=$(aws eks describe-cluster --name $HUB_CLUSTER --query cluster.resourcesVpcConfig.clusterSecurityGroupId --output text)
```
**Check:** `echo $HUB_VPC_ID $HUB_VPC_CIDR $HUB_CLUSTER_SG` → `vpc-… 10.…/16 sg-…`.

### 3.2 RDS PostgreSQL

PostgreSQL 16 (12 or later is required). Connect the hub **directly**, or through a **session-mode**
pooler. Never use RDS Proxy or a transaction-mode pooler (PgBouncer `pool_mode=transaction`): the
hubs elect their leader with a Postgres advisory lock and `LISTEN`, and both need a real session.
The hub connects with `sslmode=verify-full` against the RDS CA bundle.

```bash
# Subnets and a security group that admits the hub cluster's pods (VPC CNI: the node security group).
aws rds create-db-subnet-group --db-subnet-group-name kerf \
  --db-subnet-group-description "kerf hub" \
  --subnet-ids $(aws eks describe-cluster --name $HUB_CLUSTER --query 'cluster.resourcesVpcConfig.subnetIds' --output text)
export RDS_SG=$(aws ec2 create-security-group --group-name kerf-rds \
  --description "kerf RDS" --vpc-id $HUB_VPC_ID --query GroupId --output text)
aws ec2 authorize-security-group-ingress --group-id $RDS_SG --protocol tcp --port 5432 --source-group $HUB_CLUSTER_SG

# The instance; RDS keeps the master password in its own Secrets Manager secret.
export PG_VERSION=$(aws rds describe-db-engine-versions --engine postgres \
  --query "DBEngineVersions[?starts_with(EngineVersion, '16.')].EngineVersion | [-1]" --output text)
aws rds create-db-instance --db-instance-identifier ro-hub \
  --engine postgres --engine-version $PG_VERSION --db-instance-class db.t4g.medium \
  --allocated-storage 50 --storage-type gp3 --storage-encrypted --multi-az \
  --master-username kerf_admin --manage-master-user-password \
  --db-subnet-group-name kerf --vpc-security-group-ids $RDS_SG \
  --no-publicly-accessible --backup-retention-period 7 --deletion-protection
aws rds wait db-instance-available --db-instance-identifier ro-hub
export RDS_HOST=$(aws rds describe-db-instances --db-instance-identifier ro-hub --query 'DBInstances[0].Endpoint.Address' --output text)
```

If your node groups use their own security groups instead of the cluster security group, also
run the `authorize-security-group-ingress` line with each of them as `--source-group`.

Now create the application role `kerf` and the database `kerf`, owned by `kerf`. The hub
applies its schema migrations itself. The passwords travel over stdin, never on a command line.

```bash
export APP_PW=$(openssl rand -hex 24)
MASTER_PW=$(aws secretsmanager get-secret-value --query SecretString --output text --secret-id \
  $(aws rds describe-db-instances --db-instance-identifier ro-hub --query 'DBInstances[0].MasterUserSecret.SecretArn' --output text) | jq -r .password)
kubectl --context $HUB_CTX create namespace $NS
kubectl --context $HUB_CTX label namespace $NS elbv2.k8s.aws/pod-readiness-gate-inject=enabled   # ALB: a new pod counts as ready once its target is healthy
{ echo "$MASTER_PW"
  echo "CREATE ROLE kerf LOGIN PASSWORD '$APP_PW';"
  echo "GRANT kerf TO kerf_admin;"
  echo "CREATE DATABASE kerf OWNER kerf;"
} | kubectl --context $HUB_CTX -n $NS run psql-setup --rm -i --restart=Never --image=postgres:16-alpine -- \
  sh -c "read -r PGPASSWORD; export PGPASSWORD; exec psql 'host=$RDS_HOST user=kerf_admin dbname=postgres sslmode=require' -v ON_ERROR_STOP=1"
unset MASTER_PW
```

**Check:** the last command prints `CREATE ROLE`, `GRANT ROLE` and `CREATE DATABASE`, followed by `pod "psql-setup" deleted`.
kubectl may also print *"All commands and output from this session will be recorded in container logs"*.
That is its generic notice for `run -i`: psql reads the passwords from stdin without echoing them,
and `--rm` deletes the pod.
```bash
aws rds describe-db-instances --db-instance-identifier ro-hub --query 'DBInstances[0].[DBInstanceStatus,EngineVersion,PubliclyAccessible]' --output text
```
Expected: `available  16.…  False`.

### 3.3 Secrets Manager

Every secret is a JSON object. The charts read one property of each.

| Secret | Content | Read by | Created |
|---|---|---|---|
| `kerf/hub/db` | `{"username": "kerf", "password": "…"}` (the RDS-managed secret's format) | hub (Secret `hub-db`, built into `DATABASE_URL` with `sslmode=verify-full`) | now |
| `kerf/hub/bootstrap-admin` | `{"password": "…"}` | hub: the first admin, user `admin`, created on an empty database | now |
| `kerf/hub/datasource-key` | `{"key": "<32 random bytes, base64>"}` | hub: encrypts the stored datasource credentials. **Never lose or change it.** | now |
| `kerf/hub/pricing-token` | `{"token": "…"}` | pricing: the `pricing` token (the hub's query proxy) | step 4.5 |
| `kerf/clusters/<cluster>/agent-token` | `{"token": "…"}` | that cluster's agent | step 5.2 |

```bash
aws secretsmanager create-secret --name kerf/hub/db \
  --secret-string "$(jq -cn --arg p "$APP_PW" '{username: "kerf", password: $p}')"
aws secretsmanager create-secret --name kerf/hub/bootstrap-admin \
  --secret-string "$(jq -cn --arg p "$(openssl rand -base64 24)" '{password: $p}')"
aws secretsmanager create-secret --name kerf/hub/datasource-key \
  --secret-string "$(jq -cn --arg k "$(openssl rand -base64 32)" '{key: $k}')"
unset APP_PW
```

**Check:**
```bash
aws secretsmanager list-secrets --filters Key=name,Values=kerf/ --query 'SecretList[].Name' --output text
```
Expected (any order): `kerf/hub/db  kerf/hub/bootstrap-admin  kerf/hub/datasource-key`.

### 3.4 IAM and EKS Pod Identity

Two roles, both trusted by EKS Pod Identity (`deploy/examples/eks/iam/pod-identity-trust.json`):
- `kerf-eso`: the External Secrets Operator on every cluster reads `kerf/*` (`iam/eso-policy.json`).
- `kerf-pricing`: pricing on the hub cluster (`iam/pricing-policy.json`: `pricing:GetProducts`, `ec2:DescribeSpotPriceHistory`).

```bash
E=deploy/examples/eks
sed -e "s|123456789012|$ACCOUNT|g" -e "s|eu-west-1|$AWS_REGION|g" $E/iam/eso-policy.json > $WORK/eso-policy.json
for r in eso pricing; do
  pol=$WORK/eso-policy.json; [ $r = pricing ] && pol=$E/iam/pricing-policy.json
  aws iam create-policy --policy-name kerf-$r --policy-document file://$pol --query Policy.Arn --output text
  aws iam create-role --role-name kerf-$r --assume-role-policy-document file://$E/iam/pod-identity-trust.json --query Role.Arn --output text
  aws iam attach-role-policy --role-name kerf-$r --policy-arn arn:aws:iam::$ACCOUNT:policy/kerf-$r
done

# The hub cluster: ESO and pricing (ServiceAccount `pricing` of release kerf).
aws eks create-pod-identity-association --cluster-name $HUB_CLUSTER --namespace external-secrets \
  --service-account external-secrets --role-arn arn:aws:iam::$ACCOUNT:role/kerf-eso
aws eks create-pod-identity-association --cluster-name $HUB_CLUSTER --namespace $NS \
  --service-account pricing --role-arn arn:aws:iam::$ACCOUNT:role/kerf-pricing
kubectl --context $HUB_CTX -n external-secrets rollout restart deploy/external-secrets
```

ESO already has a Pod Identity association (or IRSA) for other secrets? A ServiceAccount has one
association. Skip the ESO `create-pod-identity-association` and attach `kerf-eso`'s
policy to ESO's existing role instead. Secrets encrypted with your own KMS key also need
`kms:Decrypt` on that key. If you can't use Pod Identity, use IRSA as the fallback: give the roles
an OIDC trust policy and annotate the ServiceAccounts (`pricing.serviceAccount.annotations:
{eks.amazonaws.com/role-arn: …}`).

Then the ClusterSecretStore, on the hub cluster now and on every workload cluster in step 5:

```bash
sed -e "s|eu-west-1|$AWS_REGION|g" $E/external-secrets/cluster-secret-store.yaml > $WORK/cluster-secret-store.yaml
kubectl --context $HUB_CTX apply -f $WORK/cluster-secret-store.yaml
```

**Check:**
```bash
aws eks list-pod-identity-associations --cluster-name $HUB_CLUSTER --query 'associations[].[namespace,serviceAccount]' --output text
kubectl --context $HUB_CTX get clustersecretstore aws-secrets-manager -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\n"}'
```
Expected: the rows `external-secrets  external-secrets` and `kerf  pricing`, then `True`.

### 3.5 ACM certificate and DNS

One public ACM certificate for both names, validated through Route 53. Internal ALBs can use it.

```bash
export CERT_ARN=$(aws acm request-certificate --domain-name $HUB_HOST --subject-alternative-names $UI_HOST \
  --validation-method DNS --query CertificateArn --output text)
until aws acm describe-certificate --certificate-arn $CERT_ARN --output json \
      --query 'Certificate.DomainValidationOptions[].ResourceRecord' | jq -e 'length > 0 and all(. != null)' >/dev/null; do
  sleep 5   # ACM fills in the validation records within a minute
done
aws acm describe-certificate --certificate-arn $CERT_ARN --query 'Certificate.DomainValidationOptions[].ResourceRecord' --output json \
  | jq '{Changes: [unique_by(.Name)[] | {Action: "UPSERT", ResourceRecordSet: {Name: .Name, Type: .Type, TTL: 300, ResourceRecords: [{Value: .Value}]}}]}' \
  > $WORK/acm-validation.json
aws route53 change-resource-record-sets --hosted-zone-id $ZONE_ID --change-batch file://$WORK/acm-validation.json
aws acm wait certificate-validated --certificate-arn $CERT_ARN
```

**Check:** `aws acm describe-certificate --certificate-arn $CERT_ARN --query Certificate.Status --output text` → `ISSUED`.

The two DNS records that point the names at the ALBs follow in step 4.4, once the ALBs exist.

### 3.6 Security groups

| From | To | Port | Why | Done by |
|---|---|---|---|---|
| hub pods | RDS | 5432 | database | step 3.2 |
| ALB | hub and web pods | 8080 | `target-type: ip` | the Load Balancer Controller (it manages these rules) |
| workload clusters (their VPC CIDRs) | hub internal ALB | 443 | agents → hub API | `alb.ingress.kubernetes.io/inbound-cidrs` in `values-hub.yaml` (default `10.0.0.0/8`; narrow it to your workload VPCs) |
| EKS control plane (cluster security group) | workload cluster nodes | **9443** | API server → webhook | below, per workload cluster |

The admission webhook listens on **9443**. The EKS cluster security group allows all traffic between
the control plane and nodes that use it, which is what managed node groups do by default. Nodes with
their own security groups (custom launch templates, self-managed nodes, Karpenter `securityGroupSelectorTerms`)
usually do not allow 9443 from the control plane. Run this for every workload cluster once its
variables are set (step 5):

```bash
CLUSTER_SG=$(aws eks describe-cluster --name $CLUSTER --query cluster.resourcesVpcConfig.clusterSecurityGroupId --output text)
for sg in $(aws ec2 describe-instances --filters "Name=tag-key,Values=kubernetes.io/cluster/$CLUSTER" Name=instance-state-name,Values=running \
            --query 'Reservations[].Instances[].SecurityGroups[].GroupId' --output text | tr '\t' '\n' | sort -u); do
  [ "$sg" = "$CLUSTER_SG" ] && continue
  aws ec2 authorize-security-group-ingress --group-id $sg --protocol tcp --port 9443 --source-group $CLUSTER_SG \
    || echo "$sg: rule not added (InvalidPermission.Duplicate means it is already there)"
done
```

The check is the webhook reachability check in step 5.4.

## 4. Hub cluster

### 4.1 Values file

```bash
sed -e "s|ro-hub.c0ffee0c0ffe.eu-west-1.rds.amazonaws.com|$RDS_HOST|g" \
    -e "s|arn:aws:acm:eu-west-1:123456789012:certificate/00000000-0000-0000-0000-000000000000|$CERT_ARN|g" \
    -e "s|hub.ro.example.com|$HUB_HOST|g" -e "s|ui.ro.example.com|$UI_HOST|g" \
    -e "s|http://vmsingle.monitoring.svc:8428|$METRICS_URL|g" \
    -e "s|10.0.0.0/16|$HUB_VPC_CIDR|g" -e "s|ops-eu1|$HUB_CLUSTER|g" \
    -e "s|123456789012|$ACCOUNT|g" -e "s|eu-west-1|$AWS_REGION|g" \
    deploy/examples/eks/values-hub.yaml > $WORK/values-hub.yaml
```

Read `$WORK/values-hub.yaml` once. Its comments explain every setting, and some choices are yours: the UI
ALB's `scheme` (internal or internet-facing), `inbound-cidrs`, and the monitoring features. Set
`serviceMonitor`, `prometheusRule` and `dashboards` to `enabled: false` if their CRDs or Grafana are
missing. Use `kind: VMServiceScrape` / `VMRule` with the VictoriaMetrics operator.

**Check:**
```bash
grep -nE '123456789012|example\.com|c0ffee|ops-eu1|00000000-0000' $WORK/values-hub.yaml
helm template kerf oci://$REGISTRY/charts/kerf --version $VERSION -n $NS -f $WORK/values-hub.yaml \
  | grep -E '^kind: (Deployment|Ingress|ExternalSecret)$' | sort | uniq -c
```
Expected: no output from `grep` (unless one of your own values is one of those patterns, e.g. `HUB_CLUSTER=ops-eu1`),
then `3 Deployment`, `5 ExternalSecret` and `2 Ingress`. The values schema stops the render if something
required is missing or a secret is inline.

### 4.2 RDS CA bundle

```bash
curl -sSfo $WORK/global-bundle.pem https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem
kubectl --context $HUB_CTX -n $NS create configmap rds-ca-bundle --from-file=global-bundle.pem=$WORK/global-bundle.pem
```
**Check:** `kubectl --context $HUB_CTX -n $NS get configmap rds-ca-bundle -o jsonpath='{.data.global-bundle\.pem}' | grep -c 'BEGIN CERTIFICATE'` → a number above 100.

### 4.3 Install

```bash
helm --kube-context $HUB_CTX upgrade --install kerf oci://$REGISTRY/charts/kerf \
  --version $VERSION -n $NS -f $WORK/values-hub.yaml
kubectl --context $HUB_CTX -n $NS rollout status deploy/hub --timeout=10m
kubectl --context $HUB_CTX -n $NS rollout status deploy/web --timeout=5m
```

The release is named `kerf`, so its objects are named `hub`, `web` and `pricing`.
Any other release name `R` gives `R-hub`, `R-web` and so on. The first hub pod applies the database
migrations before it reports ready (the startup probe allows 10 minutes).

**Check:**
```bash
kubectl --context $HUB_CTX -n $NS get externalsecrets
kubectl --context $HUB_CTX -n $NS get pods -l app.kubernetes.io/instance=kerf
```
Expected: `hub-db`, `hub-bootstrap-admin` and `hub-datasource-key` show `SecretSynced  True`. `pricing-hub-token`
shows `SecretSyncedError` until step 4.5. Two `hub-…` and two `web-…` pods are `Running 1/1`, and
`pricing-…` is `ContainerCreating` until step 4.5, because it waits for its token Secret. That is expected.

Hub readiness and the leader (exactly one replica leads):
```bash
kubectl --context $HUB_CTX get --raw "/api/v1/namespaces/$NS/services/hub:8080/proxy/readyz"
for p in $(kubectl --context $HUB_CTX -n $NS get pods -l app.kubernetes.io/name=hub -o name); do
  kubectl --context $HUB_CTX get --raw "/api/v1/namespaces/$NS/pods/${p#pod/}:8080/proxy/metrics" | grep '^kerf_hub_leader '
done
```
Expected: `ok`, then `kerf_hub_leader 1` once and `kerf_hub_leader 0` once, so the sum is 1. With Prometheus, the query
`sum(kerf_hub_leader)` must return 1, and the alert `KerfNoHubLeader` fires when it doesn't.

### 4.4 DNS for the ALBs

```bash
HUB_ALB=$(kubectl --context $HUB_CTX -n $NS get ingress hub -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
UI_ALB=$(kubectl --context $HUB_CTX -n $NS get ingress web -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')
echo "$HUB_ALB $UI_ALB"
jq -n --arg h $HUB_HOST --arg a "$HUB_ALB" --arg u $UI_HOST --arg b "$UI_ALB" '{Changes: [
  {Action: "UPSERT", ResourceRecordSet: {Name: $h, Type: "CNAME", TTL: 300, ResourceRecords: [{Value: $a}]}},
  {Action: "UPSERT", ResourceRecordSet: {Name: $u, Type: "CNAME", TTL: 300, ResourceRecords: [{Value: $b}]}}]}' > $WORK/dns.json
aws route53 change-resource-record-sets --hosted-zone-id $ZONE_ID --change-batch file://$WORK/dns.json
```
Both ALBs need a minute or two after the install. If `echo` prints an empty name, wait and run the
block again. Run `kubectl --context $HUB_CTX -n $NS describe ingress hub` to see the controller's events. external-dns can
create these records instead.

**Check:** from a machine inside the VPC or on your VPN (the ALBs are internal):
```bash
curl -sS https://$HUB_HOST/readyz
curl -sS -o /dev/null -w '%{http_code}\n' https://$UI_HOST/
```
Expected: `ok`, then `200`.

### 4.5 First sign-in and the pricing token

The first admin is `admin`, with the password from Secrets Manager:
```bash
export ADMIN_PW=$(aws secretsmanager get-secret-value --secret-id kerf/hub/bootstrap-admin --query SecretString --output text | jq -r .password)
CSRF=$(curl -sS -c $WORK/cookies -H 'Content-Type: application/json' \
  -d "$(jq -cn --arg p "$ADMIN_PW" '{username: "admin", password: $p}')" https://$UI_HOST/v1/auth/login | jq -r .csrfToken)
echo $CSRF
```
**Check:** `echo` prints a token, not `null`. You can also sign in at `https://$UI_HOST` in a browser.
Change the password there (the user menu, top right → *Change password*).

Signing in is always required: give everyone who uses Kerf a user under *Admin → Access → Users*
(at least `viewer`). See [Sign-in and access](../auth.md).

Next, create the `pricing` token, store it in Secrets Manager, and let ESO pick it up. You can also
create it in the UI under *Admin → Access → Tokens*. A token is shown once.

```bash
val=$(curl -sS -b $WORK/cookies -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"role": "pricing", "description": "kerf pricing", "reason": "install"}' \
  https://$UI_HOST/v1/admin/tokens | jq -r .value)
aws secretsmanager create-secret --name kerf/hub/pricing-token \
  --secret-string "$(jq -cn --arg t "$val" '{token: $t}')" --query Name --output text
kubectl --context $HUB_CTX -n $NS annotate externalsecret pricing-hub-token force-sync=$(date +%s) --overwrite
```

**Check:**
```bash
kubectl --context $HUB_CTX -n $NS get externalsecrets
kubectl --context $HUB_CTX -n $NS rollout status deploy/pricing --timeout=5m
curl -sS -o /dev/null -w '%{http_code}\n' https://$UI_HOST/v1/workloads
```
Expected: all four `SecretSynced True`, then `deployment "pricing" successfully rolled out`, then
`401`: without a session the UI and the API show nothing.

Pricing (provider `aws`, Pod Identity):
```bash
kubectl --context $HUB_CTX get --raw "/api/v1/namespaces/$NS/services/pricing:9090/proxy/metrics" | grep -E '^kerf_pricing_nodes'
```
Expected (within one `interval`, 5 minutes): series `kerf_pricing_nodes{…status="priced"…}`. If you
see `AccessDenied` or a credentials timeout in `kubectl --context $HUB_CTX -n $NS logs deploy/pricing`, the Pod Identity
association (3.4) is missing or was created after the pod started. The NetworkPolicy must also allow the
Pod Identity Agent (`169.254.170.23:80`, in `values-hub.yaml`). Fix it and run
`kubectl --context $HUB_CTX -n $NS rollout restart deploy/pricing`.

## 5. Each workload cluster

Repeat this section for every workload cluster. Set its variables first:

```bash
export CLUSTER=prod-eu1                           # EKS cluster name; also its name in the hub (a DNS label)
export SPOKE_METRICS_URL=http://prometheus-operated.monitoring.svc:9090   # this cluster's Prometheus, from inside it
aws eks update-kubeconfig --region $AWS_REGION --name $CLUSTER
export SPOKE_CTX=arn:aws:eks:$AWS_REGION:$ACCOUNT:cluster/$CLUSTER
export SPOKE_VPC_CIDR=$(aws ec2 describe-vpcs --query 'Vpcs[0].CidrBlock' --output text \
  --vpc-ids $(aws eks describe-cluster --name $CLUSTER --query cluster.resourcesVpcConfig.vpcId --output text))
```

`SPOKE_METRICS_URL` serves the agent's throttling rollback (`container_cpu_cfs_*`). The example
assumes a Prometheus inside this cluster without a `cluster` label (`health.clusterLabel: ""`).

### 5.1 Prepare the cluster

ESO's Pod Identity association, the ClusterSecretStore and the webhook port. The ESO role exists already (3.4):

```bash
aws eks create-pod-identity-association --cluster-name $CLUSTER --namespace external-secrets \
  --service-account external-secrets --role-arn arn:aws:iam::$ACCOUNT:role/kerf-eso
kubectl --context $SPOKE_CTX -n external-secrets rollout restart deploy/external-secrets
kubectl --context $SPOKE_CTX apply -f $WORK/cluster-secret-store.yaml
```
Then run the security group loop from [3.6](#36-security-groups) (it uses `$CLUSTER`).

**Check:**
```bash
kubectl --context $SPOKE_CTX get clustersecretstore aws-secrets-manager -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\n"}'
kubectl --context $SPOKE_CTX -n cert-manager get deploy
```
Expected: `True`, then three cert-manager Deployments `READY 1/1`.

The agent's token path is `kerf/clusters/<cluster>/*`. To give this cluster's ESO only
its own token, use a copy of `kerf-eso` whose policy `Resource` is
`…:secret:kerf/clusters/<cluster>/*`.

### 5.2 Register the cluster in the hub

In the UI: **Clusters → Add cluster** (`https://$UI_HOST/clusters/new`).
1. **Identify:** name `$CLUSTER` exactly, environment, owner.
2. **Metrics:** the datasource holding this cluster's metrics (`local` = `$METRICS_URL`), label `cluster`,
   value `$CLUSTER`. Click **Test**: it runs the metrics checks against the datasource at once.
3. **Install the agent:** the wizard shows the cluster's **agent token once**. Copy it, then store it:
   ```bash
   read -rs AGENT_TOKEN && echo "read ${#AGENT_TOKEN} characters"   # paste the token, press Enter
   aws secretsmanager create-secret --name kerf/clusters/$CLUSTER/agent-token \
     --secret-string "$(jq -cn --arg t "$AGENT_TOKEN" '{token: $t}')" --query Name --output text
   unset AGENT_TOKEN
   ```
   The wizard's Helm, values and Argo CD snippets install the same chart in mode spoke, with this
   cluster's name, the hub URL and the token from Secrets Manager. 5.3 uses `values-spoke.yaml`
   instead, which adds the production settings (NetworkPolicies, ServiceMonitors, the webhook port,
   the throttling metrics). The wizard's Argo CD `ignoreDifferences` snippet is the one in
   [step 6](#6-turning-on-auto-patch).
4. **Verify:** leave the wizard open. It fills in as the agent connects.

**Check:** the `aws` command prints `kerf/clusters/<cluster>/agent-token`, and the cluster
shows state **Registered** under *Clusters*.

### 5.3 Install

```bash
sed -e "s|hub.ro.example.com|$HUB_HOST|g" -e "s|prod-eu1|$CLUSTER|g" -e "s|10.1.0.0/16|$SPOKE_VPC_CIDR|g" \
    -e "s|http://prometheus-operated.monitoring.svc:9090|$SPOKE_METRICS_URL|g" \
    -e "s|123456789012|$ACCOUNT|g" -e "s|eu-west-1|$AWS_REGION|g" \
    deploy/examples/eks/values-spoke.yaml > $WORK/values-spoke-$CLUSTER.yaml
grep -nE '123456789012|example\.com|prod-eu1' $WORK/values-spoke-$CLUSTER.yaml   # expect no output (unless $CLUSTER is prod-eu1)
helm --kube-context $SPOKE_CTX upgrade --install kerf oci://$REGISTRY/charts/kerf \
  --version $VERSION -n $NS --create-namespace -f $WORK/values-spoke-$CLUSTER.yaml --wait --timeout 10m
```

This installs the Deployments `kerf-agent` (2 replicas, one leader) and `webhook` (2 replicas),
the two CRDs (kept on uninstall), the MutatingWebhookConfiguration `kerf-webhook`, and
the ExternalSecret `kerf-agent-token`. If Argo CD runs **in this cluster**, set
`agent.selfCheck.argocd.force: true` in the values file so the agent may read `argocd-cm`. Without the
Prometheus Operator CRDs on this cluster, set both `serviceMonitor.enabled: false` in the values file first,
or `--wait` fails with `no matches for kind "ServiceMonitor"`.

**Check:**
```bash
kubectl --context $SPOKE_CTX -n $NS get externalsecret kerf-agent-token
kubectl --context $SPOKE_CTX -n $NS get pods -l app.kubernetes.io/instance=kerf
kubectl --context $SPOKE_CTX -n $NS logs deploy/kerf-agent | grep -m1 'starting agent'
```
Expected: `SecretSynced True`, four pods `Running 1/1` (two `kerf-agent-…`, two `webhook-…`), and a
JSON log line with `"msg":"starting agent"`, `"cluster":"<cluster>"` and `"hub":"https://<HUB_HOST>"`.

### 5.4 Readiness

The webhook must be reachable from the control plane on 9443. The API server's service proxy takes the same path:
```bash
kubectl --context $SPOKE_CTX get --raw "/api/v1/namespaces/$NS/services/https:webhook:443/proxy/healthz"
```
Expected: an error that ends in `the server could not find the requested resource`. The webhook
answered with 404, so the port is open. `i/o timeout`, `context deadline exceeded` or
`connection refused` mean the security group (3.6) is wrong.

The cluster's readiness, through the hub API (or open the cluster in the UI):
```bash
curl -sS -b $WORK/cookies https://$UI_HOST/v1/clusters/$CLUSTER/readiness \
  | jq -c '{state, summary, failing: [.checks[] | select(.status != "pass") | {id, severity, status, remediation}]}'
```
Expected: within a few minutes the state reaches `MetricsOK`, then **`ReadyAudit`** once the
recommender has produced recommendations for this cluster. It runs once a day by default. Start a run
now with:
```bash
curl -sS -b $WORK/cookies -H "X-CSRF-Token: $CSRF" -X POST "https://$UI_HOST/v1/admin/recompute?cluster=$CLUSTER" | jq -c .
```
The recommender needs a few minutes of this cluster's metrics before it sees its workloads: if the
run log says `"workloads":0` (or `metrics.recommendations` keeps failing), run the command again
after five minutes. The state then moves to **`AutomationReady`** once every write capability passes. Each failing check
names its fix (`remediation`). The catalog is `docs/contracts/readiness.md`. The readiness states:
`Registered → AgentConnected → MetricsOK → ReadyAudit → AutomationReady`. A session expires after
12 hours. If `curl` returns `401`, sign in again (4.5).

## 6. Turning on auto-patch

Every workload starts in `audit`: kerf recommends but changes nothing. To let it apply changes:

1. **Wait for `AutomationReady`** (5.4). The hub refuses with `409 cluster_not_automation_ready` any
   policy, override or approve-once that newly enables `auto-patch` on a cluster below that state. An admin can
   override with a reason (`overrideReadiness`), which is recorded in the audit log.
2. **Argo CD**, if it syncs the workloads on that cluster. The agent writes `resources` with
   server-side apply under the field manager `kerf`. Argo CD must ignore exactly those fields.
   In `argocd-cm` of every Argo CD instance that syncs workloads on the cluster:
   ```bash
   kubectl --context <the Argo CD cluster's context> -n argocd patch configmap argocd-cm --type merge \
     -p '{"data":{"resource.customizations.ignoreDifferences.all":"managedFieldsManagers:\n- kerf\n"}}'
   ```
   This replaces an existing `resource.customizations.ignoreDifferences.all`. If you already have
   one, add `kerf` to its `managedFieldsManagers` list by hand. Then give those
   Applications the sync options `RespectIgnoreDifferences=true`, and `ServerSideApply=true` is
   recommended. **Check:** the readiness check `integration.argocd` passes.
3. **A policy:** in the UI, *Admin → Policies → New*. Pick the scope (cluster, namespaces, labels),
   set CPU and/or memory mode `auto-patch`, the apply strategy (`in-place` by default, with
   `rollout` as the fallback) and the guardrails. Before you save, the policy page shows an impact
   preview of the workloads the policy would change. You can also commit a `RightsizingPolicy` to
   Git and apply it to the cluster, if the cluster's policy source allows Git.
4. **Watch** *Activity* and the dashboards. Every change is health-watched and rolled back on
   OOM kills, restarts or throttling. *Admin → Automation* pauses a cluster or the whole fleet at once.

## 7. Argo CD instead of helm

`deploy/examples/eks/applicationset.yaml` creates one Application per labelled cluster. It replaces
the `helm upgrade --install` of steps 4.3 and 5.3. Every other step stays the same: the RDS CA
ConfigMap (4.2), the token secrets, and the ClusterSecretStore on each cluster. Here Argo CD runs in the
hub cluster (`$HUB_CTX`), and you are signed in with the CLI (`argocd login <your Argo CD server>`).

1. **Chart credentials.** Argo CD pulls the chart from ECR, and ECR tokens expire after 12 hours.
   `deploy/examples/eks/argocd-ecr-repo.yaml` lets ESO in the Argo CD cluster regenerate the
   repository Secret. Give ESO's role there the ECR pull policy and apply it:
   ```bash
   sed -e "s|123456789012|$ACCOUNT|g" -e "s|eu-west-1|$AWS_REGION|g" deploy/examples/eks/iam/argocd-ecr-pull-policy.json > $WORK/argocd-ecr-pull-policy.json
   aws iam put-role-policy --role-name kerf-eso --policy-name argocd-ecr-pull --policy-document file://$WORK/argocd-ecr-pull-policy.json
   sed -e "s|123456789012|$ACCOUNT|g" -e "s|eu-west-1|$AWS_REGION|g" deploy/examples/eks/argocd-ecr-repo.yaml | kubectl --context $HUB_CTX apply -f -
   ```
   **Check:** `kubectl --context $HUB_CTX -n argocd get secret repo-kerf-charts -o jsonpath='{.metadata.labels}'` → contains `"argocd.argoproj.io/secret-type":"repository"`.
2. **Values in Git.** Commit `$WORK/values-hub.yaml` as `kerf/values-hub.yaml` and one
   `$WORK/values-spoke-<cluster>.yaml` as `kerf/values-spoke.yaml` to your config
   repository. The ApplicationSet sets each cluster's name, token key and VPC CIDR from the cluster
   Secret, so one spoke file serves every workload cluster. Give Argo CD read access to that repository
   if it has none yet, e.g. `argocd repo add $CONFIG_REPO --username <user> --password <token>` (or `--ssh-private-key-path`).
3. **Label the clusters.** The hub cluster needs a cluster Secret for `in-cluster`. Run `argocd cluster add`
   once per workload cluster, each time with that cluster's step 5 variables set:
   ```bash
   kubectl --context $HUB_CTX -n argocd apply -f - <<EOF
   apiVersion: v1
   kind: Secret
   metadata:
     name: cluster-in-cluster
     labels: {argocd.argoproj.io/secret-type: cluster, kerf.getkerf.dev/mode: hub}
     annotations: {kerf.getkerf.dev/cluster-name: $HUB_CLUSTER}
   stringData: {name: in-cluster, server: "https://kubernetes.default.svc"}
   EOF
   argocd cluster add $SPOKE_CTX --name $CLUSTER --label kerf.getkerf.dev/mode=spoke \
     --annotation kerf.getkerf.dev/vpc-cidr=$SPOKE_VPC_CIDR
   ```
   For an existing cluster Secret: `kubectl --context $HUB_CTX -n argocd label secret <its secret> kerf.getkerf.dev/mode=spoke`
   and `kubectl --context $HUB_CTX -n argocd annotate secret <its secret> kerf.getkerf.dev/vpc-cidr=$SPOKE_VPC_CIDR`.
   The annotation `kerf.getkerf.dev/cluster-name` sets the cluster's name in the hub when it
   differs from the Argo CD cluster name. `kerf.getkerf.dev/agent-token-key` overrides the
   token's Secrets Manager key.
4. **The ApplicationSet:**
   ```bash
   export CONFIG_REPO=https://git.example.com/platform/kerf-config.git   # your repo
   sed -e "s|https://git.example.com/platform/kerf-config.git|$CONFIG_REPO|g" \
       -e "s|targetRevision: 0.1.0|targetRevision: $VERSION|" \
       -e "s|123456789012|$ACCOUNT|g" -e "s|eu-west-1|$AWS_REGION|g" \
       deploy/examples/eks/applicationset.yaml | kubectl --context $HUB_CTX apply -f -
   ```
   It syncs with `ServerSideApply=true` and `RespectIgnoreDifferences=true`. It ignores the fields the agent
   (`managedFieldsManagers: [kerf]`) and cert-manager's CA injector (the webhook's `caBundle`) own.
   It keeps the objects when an Application or the ApplicationSet is deleted (`preserveResourcesOnDeletion`, and
   no resources finalizer), because Argo CD does not honour `helm.sh/resource-policy: keep` and the CRDs hold every policy.

**Check:**
```bash
kubectl --context $HUB_CTX -n argocd get applications -l app.kubernetes.io/part-of=kerf
```
Expected: `kerf-in-cluster` and one `kerf-<cluster>` per workload cluster, all
`SYNC STATUS Synced`. Workload clusters are `HEALTH STATUS Healthy` once their agent token exists (5.2).
`kerf-in-cluster` stays `Progressing` until the `pricing` Deployment has its token (4.5),
then turns `Healthy`. Then run the checks of 4.3–4.5 and 5.3–5.4.

The datasource key must come from Secrets Manager under Argo CD, as it does in `values-hub.yaml`.
Argo CD renders without Helm's `lookup`, so a key that the chart generated itself would change on
every sync.

## 8. Upgrade

**Admin → Maintenance** in the web UI shows the version of every component in every cluster and, once a day, checks
for a newer stable Kerf release with its release notes and these commands ([details](../maintenance.md); turn the
check off with `hub.updateCheck.enabled: false`).

Release the new version (step 2), then upgrade the hub cluster first. Upgrade the workload clusters after it,
each with that cluster's step 5 variables set:

```bash
export VERSION=0.2.0
helm --kube-context $HUB_CTX upgrade kerf oci://$REGISTRY/charts/kerf --version $VERSION -n $NS -f $WORK/values-hub.yaml
helm --kube-context $SPOKE_CTX upgrade kerf oci://$REGISTRY/charts/kerf --version $VERSION -n $NS -f $WORK/values-spoke-$CLUSTER.yaml --wait
```
With Argo CD, change `targetRevision` in the ApplicationSet.

- **CRDs upgrade with the chart.** They are templates, so `helm upgrade` updates them. Install the
  umbrella in mode `spoke` once per cluster: a CRD can belong to one release only.
- **The hub rolls with no downtime.** RollingUpdate keeps `maxUnavailable: 0` and the PDB keeps `minAvailable: 1`.
  The ALB pod readiness gate (the namespace label from 3.2) holds the rollout until a new pod is healthy in the
  target group, and `preStopSleepSeconds: 15` (hub and web, in `values-hub.yaml`) keeps a stopping pod serving
  until the ALB has deregistered it. If requests still fail during a roll, raise both `preStopSleepSeconds` and
  `hub.terminationGracePeriodSeconds` (keep it at least 25 s above the sleep).
  A new pod applies the database migrations before it reports ready, under a Postgres lock.
  A replica that stops releases the leader lock at once, so another one takes over. A crash fails over in under 15 seconds.
- **The agent and webhook** roll too: the agent's standby takes the lease, and the webhook's PDB keeps one pod.

**Check:** `kubectl --context $HUB_CTX -n $NS rollout status deploy/hub`, the leader loop of 4.3 (sum 1), and
`helm --kube-context $HUB_CTX -n $NS history kerf` (the new revision `deployed`). To roll back, run
`helm --kube-context $HUB_CTX -n $NS rollback kerf` (or `$SPOKE_CTX`). Database migrations are not rolled back.

### Moving from the component charts or the old `crds/` chart

Clusters installed with the component charts (releases `hub`, `web`, `pricing`, `agent`,
`webhook`, as in development) or with an agent chart that still had a `crds/` directory move once.
The object names stay the same, but the label `app.kubernetes.io/instance` becomes `kerf`.
A Deployment's selector cannot change, so remove the old releases first. Objects marked
`helm.sh/resource-policy: keep` stay: the CRDs with every policy and recommendation, and the
datasource key. Then adopt them with `--take-ownership` (Helm ≥ 3.17).

**Carry the datasource key over first.** The old hub chart generated `hub-datasource-key`, and
`values-hub.yaml` makes ESO write that Secret from `kerf/hub/datasource-key`. Unless
that secret holds the *existing* key, ESO overwrites it with the new random key from 3.3, and every
stored datasource credential becomes unreadable. Run the workload cluster part once per cluster,
with its step 5 variables set:

```bash
# Hub cluster (short API and UI outage).
OLD_KEY=$(kubectl --context $HUB_CTX -n $NS get secret hub-datasource-key -o jsonpath='{.data.key}' | base64 -d)
[ -n "$OLD_KEY" ] && aws secretsmanager put-secret-value --secret-id kerf/hub/datasource-key \
  --secret-string "$(jq -cn --arg k "$OLD_KEY" '{key: $k}')" --query Name --output text
unset OLD_KEY
helm --kube-context $HUB_CTX -n $NS uninstall web pricing hub
helm --kube-context $HUB_CTX upgrade --install kerf oci://$REGISTRY/charts/kerf \
  --version $VERSION -n $NS -f $WORK/values-hub.yaml --take-ownership
# Each workload cluster (the agent applies nothing for a minute).
helm --kube-context $SPOKE_CTX -n $NS uninstall webhook agent
helm --kube-context $SPOKE_CTX upgrade --install kerf oci://$REGISTRY/charts/kerf \
  --version $VERSION -n $NS -f $WORK/values-spoke-$CLUSTER.yaml --take-ownership --wait
```
`--take-ownership` is needed once. Later upgrades don't use it. The `aws` command must print
`kerf/hub/datasource-key` before you uninstall. If it prints nothing, the Secret or its
`key` is missing: stop there. If the old hub release had another name, its key Secret is
`<old release>-datasource-key`. Use that name in the `kubectl get secret` line, or set
`hub.onboarding.secretsKey.existingSecret` to it and `hub.onboarding.secretsKey.externalSecret.enabled: false`.

**Check:**
```bash
kubectl --context $SPOKE_CTX get crd rightsizingpolicies.kerf.getkerf.dev -o jsonpath='{.metadata.annotations.meta\.helm\.sh/release-name}{"\n"}'
```
Expected: `kerf`. Your policies are still there: `kubectl --context $SPOKE_CTX get rsp -A`.

## 9. Uninstall

```bash
helm --kube-context $SPOKE_CTX -n $NS uninstall kerf   # each workload cluster (its step 5 variables set)
helm --kube-context $HUB_CTX -n $NS uninstall kerf     # the hub cluster
```

Two things stay on purpose (`helm.sh/resource-policy: keep`). On workload clusters, the **CRDs** stay,
and with them every `RightsizingPolicy` and `RightsizingRecommendation`. On the hub, the **datasource-key
ExternalSecret** and its Secret stay: without the key, the credentials of the metrics sources stored in
the database cannot be read. Resources that the agent already changed on workloads keep their current
values.

To remove everything:
```bash
# Workload cluster: deletes every policy and recommendation of the cluster.
kubectl --context $SPOKE_CTX delete crd rightsizingpolicies.kerf.getkerf.dev rightsizingrecommendations.kerf.getkerf.dev
kubectl --context $SPOKE_CTX delete namespace $NS
# Hub cluster: only together with the database (the key is useless without it, and vice versa).
kubectl --context $HUB_CTX -n $NS delete externalsecret hub-datasource-key
kubectl --context $HUB_CTX delete namespace $NS
# AWS
aws rds modify-db-instance --db-instance-identifier ro-hub --no-deletion-protection --apply-immediately
aws rds delete-db-instance --db-instance-identifier ro-hub --final-db-snapshot-identifier ro-hub-final
for s in $(aws secretsmanager list-secrets --filters Key=name,Values=kerf/ --query 'SecretList[].Name' --output text); do
  aws secretsmanager delete-secret --secret-id $s        # recoverable for 30 days
done
```
After that, delete the Pod Identity associations (`aws eks delete-pod-identity-association`), the IAM roles and
policies `kerf-eso` and `kerf-pricing`, the certificate, the DNS records,
the security group `kerf-rds` and the DB subnet group.

**Check:** `kubectl --context $SPOKE_CTX get crd | grep kerf.getkerf.dev` and `helm --kube-context $HUB_CTX -n $NS list` → no output.

## 10. Troubleshooting

The commands here leave out `--context`. Run them against the cluster in question
(`kubectl config use-context $HUB_CTX` or `$SPOKE_CTX`, with that cluster's step 5 variables set).

**Webhook timeouts.** Pod creation on a workload cluster is slow by up to `webhook.timeoutSeconds`
(2 s). Events or API server logs show `failed calling webhook "…webhook.kerf.getkerf.dev"…
context deadline exceeded`, and the readiness check `webhook.healthy` warns. Pods still start
(`failurePolicy: Ignore`). They start with their template values, and the agent resizes them
afterwards. Cause: the control plane can't reach the nodes on 9443. Fix: the security group loop in
3.6. Check: the service proxy command in 5.4. With the webhook NetworkPolicy, `webhook.networkPolicy.ingress.apiServerFrom`
must contain the cluster's VPC CIDR.

**No hub leader** (`sum(kerf_hub_leader) == 0`, alert `KerfNoHubLeader`; recommendations
and readiness stop updating, and the API still works). Logs: `kubectl -n $NS logs -l app.kubernetes.io/name=hub | grep leadership`.
Causes: the database URL goes through RDS Proxy or a transaction-mode pooler, where the advisory lock
and `LISTEN` don't hold. Connect to RDS directly (`hub.database.host` is the instance or cluster
endpoint). The database may also be unreachable: `/readyz` is then `not ready` too. A leader that
lost its connection steps down within about 4 s, and another replica takes over within about 13 s.

**Agent offline** (readiness `agent.connected` fails, alert `KerfAgentOffline`). On the workload cluster:
```bash
kubectl -n $NS get externalsecret kerf-agent-token            # SecretSynced True?
kubectl -n $NS logs deploy/kerf-agent | grep -iE 'error|401|x509' | tail
kubectl -n $NS run hub-probe --rm -i --restart=Never --image=curlimages/curl -- -sS https://$HUB_HOST/readyz
```
- `401`: the token is wrong or revoked. Create a new agent token for the cluster (*Admin → Access →
  Tokens*, role `agent`, the cluster) and run `aws secretsmanager put-secret-value --secret-id
  kerf/clusters/$CLUSTER/agent-token --secret-string '{"token":"…"}'`. Then run
  `kubectl -n $NS annotate externalsecret kerf-agent-token force-sync=$(date +%s) --overwrite`.
  The agent re-reads the token on every request.
- `x509: certificate signed by unknown authority`: the hub's certificate is not from ACM or another
  public CA. Set `agent.hub.caBundle`.
- The probe times out: the VPC routing to the hub's VPC, the ALB's `inbound-cidrs`, or the agent
  NetworkPolicy's egress (443) is blocking the connection.

**Stale or missing metrics** (readiness `metrics.cluster-label` or `metrics.freshness` fails, or
cost figures are stale). `metrics.cluster-label` finds no series for the cluster's binding. Its
`details.suggestions` list the `cluster` label values the datasource does have. Make them match:
fix the binding (the cluster's page in *Clusters* → *Edit*) or the Prometheus external label.
`metrics.freshness`: the newest sample is older than 5 minutes (remote-write is broken).
`pricing.coverage` below 100 %: kube-state-metrics lacks the node label allowlist ([1](#1-prerequisites)),
or `kerf_pricing_nodes{status="skipped"}` names the nodes. The alert `KerfPricingStale`
means pricing has not refreshed: check `kubectl -n $NS logs deploy/pricing` on the hub cluster.

**ESO sync errors** (`kubectl get externalsecret -A` shows `SecretSyncedError`; run `kubectl describe externalsecret <name>` for the reason):
- `AccessDeniedException`: the ESO role lacks `kerf/*`, there is no Pod Identity association, or ESO
  started before the association (`kubectl -n external-secrets rollout restart deploy/external-secrets`).
  A customer-managed KMS key also needs `kms:Decrypt`.
- `ResourceNotFoundException` / `Secret does not exist`: the Secrets Manager name (3.3) is wrong or not created yet
  (the `pricing` token before 4.5, an agent token before 5.2).
- `key … does not exist` / property errors: the secret is not the JSON from the table in 3.3.
- `ClusterSecretStore … not ready`: check its region and ESO's credentials (`kubectl describe clustersecretstore aws-secrets-manager`).

After fixing the cause, run `kubectl annotate externalsecret <name> force-sync=$(date +%s) --overwrite`
to sync at once instead of waiting for the next `refreshInterval` (1 h).

**Chart values.** The chart's values reference is `deploy/charts/kerf/README.md`. Each
component's settings are those of its component chart (`deploy/charts/<component>/values.yaml`).
`helm template` with your values file shows schema errors (inline secrets, missing required values)
before anything reaches the cluster.
