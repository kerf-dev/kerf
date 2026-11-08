# Pricing

Kerf turns a recommendation into dollars with an hourly rate per vCPU and per GiB of memory. The pricing service
(on the hub cluster) prices every node, splits each node's hourly price into a per-vCPU and a per-GiB rate, and
exports them as the `kerf_*_hourly_cost_usd` series ([metrics contract](contracts/metrics.md#2-pricing-series-exported-by-the-pricing-service)).
The hub averages them per cluster for the savings estimates.

## Where a rate comes from

Every node's series carry a `source` label. The workload page ("Why this recommendation" → Savings estimate) and
**Admin → Pricing** show it:

| Source | Rate | When |
|---|---|---|
| `cur` | The effective rate you paid over the last 30 days, from the AWS Cost and Usage Report, with Savings Plans and Reserved Instances amortised. Shown with the period it covers. | The report reader is on and the report has usage for the node's instance type, region and capacity type. |
| `list` | The AWS on-demand list price (Price List API, Linux, shared tenancy). | Provider `aws`, on-demand nodes, and no `cur` rate. |
| `spot` | The current EC2 spot price of the node's zone (or the region's average). | Provider `aws`, spot nodes, and no `cur` rate. |
| `static` | The static price file (`prices` value of the pricing chart). | Provider `static`, and no `cur` rate. |

On top of these rates, **Admin → Pricing** applies a discount or a fixed rate per cluster.

### The static price file

The default `static` file baked into the pricing image holds approximate list prices for development: common x86
families in `eu-west-1`, and the Graviton families (`c7g`, `c8g`, `m7g`, `m8g`, `r7g`, `r8g`, `t4g`) in `eu-west-1` and
`us-east-1`. A node whose instance type or region is not in the file is skipped with the reason `no-price`
(`kerf_pricing_nodes{status="skipped",reason="no-price"}`), and the pricing log names it:

```text
level=WARN msg="no price for node" instance_type=c9g.xlarge key=us-east-1/c9g.xlarge/on-demand
```

Switch to `provider: aws` (below) for real list and spot prices in any region, or set the `prices` value of the
pricing chart to a file in the format of [`dev/prices.yaml`](../dev/prices.yaml) with your types and regions.

## Give the pricing service AWS access

`provider: aws` (list and spot prices) and the Cost and Usage Report readers call AWS with the default credential
chain, so the pricing pod needs an IAM role. The role's policy is
[`deploy/examples/eks/iam/pricing-policy.json`](../deploy/examples/eks/iam/pricing-policy.json) (`pricing:GetProducts`
and `ec2:DescribeSpotPriceHistory`), plus the CUR statements [below](#3-iam) when you use effective rates. There are two
ways to hand the role to the pod:

| | EKS Pod Identity | IRSA (IAM roles for service accounts) |
|---|---|---|
| How | An association between the ServiceAccount and the role, made with the EKS API. Needs the `eks-pod-identity-agent` add-on. | The role trusts the cluster's OIDC provider, and the ServiceAccount carries the `eks.amazonaws.com/role-arn` annotation. |
| Trust policy principal | `pods.eks.amazonaws.com` | The cluster's IAM OIDC provider |
| Choose it when | You can create Pod Identity associations (the default in the [EKS install guide](install/eks.md)). | Your organization blocks Pod Identity associations, or the add-on can't run. |

Some organizations block `eks:CreatePodIdentityAssociation` through a service control policy or a permission
boundary. The call then fails with `Cross-account pass role is not allowed`, and IRSA works as a replacement because it
needs no association. IRSA also works on clusters without the Pod Identity agent.

### With IRSA

1. Find the cluster's OIDC provider. Use its issuer without `https://` as `<OIDC_PROVIDER>`, and create the IAM OIDC
   provider if the second command prints nothing:

   ```bash
   OIDC_ISSUER=$(aws eks describe-cluster --name <cluster> --query cluster.identity.oidc.issuer --output text)
   aws iam list-open-id-connect-providers --query "OpenIDConnectProviderList[?ends_with(Arn, '${OIDC_ISSUER#https://}')]"
   eksctl utils associate-iam-oidc-provider --cluster <cluster> --approve   # only if none is listed
   ```

2. Create the role with this trust policy. `<NAMESPACE>` is the namespace of the release, and `pricing` is the
   ServiceAccount name (the release name of the `pricing` chart, or `pricing` in the `kerf` chart; check it with
   `kubectl -n <namespace> get serviceaccount`):

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Effect": "Allow",
         "Principal": {"Federated": "arn:aws:iam::<ACCOUNT_ID>:oidc-provider/<OIDC_PROVIDER>"},
         "Action": "sts:AssumeRoleWithWebIdentity",
         "Condition": {
           "StringEquals": {
             "<OIDC_PROVIDER>:aud": "sts.amazonaws.com",
             "<OIDC_PROVIDER>:sub": "system:serviceaccount:<NAMESPACE>:pricing"
           }
         }
       }
     ]
   }
   ```

   ```bash
   aws iam create-role --role-name kerf-pricing --assume-role-policy-document file://irsa-trust.json
   aws iam put-role-policy --role-name kerf-pricing --policy-name kerf-pricing --policy-document file://pricing-policy.json
   ```

3. Annotate the ServiceAccount in the `kerf` chart's values (or the `pricing` chart's, without the `pricing:` level),
   and upgrade the release:

   ```yaml
   pricing:
     provider: aws
     serviceAccount:
       annotations:
         eks.amazonaws.com/role-arn: arn:aws:iam::<ACCOUNT_ID>:role/kerf-pricing
   ```

4. Check that the pricing pod assumed the role and prices every node:

   ```bash
   kubectl -n <namespace> rollout status deploy/pricing
   kubectl -n <namespace> logs deploy/pricing | grep "pricing refreshed"
   ```

   Expected: `"pricing refreshed"` with `"priced"` equal to `"nodes"`. `AccessDenied`, `WebIdentityErr` or a
   credentials timeout in the log means the trust policy's `sub` (namespace or ServiceAccount name) or the
   annotation does not match, or the pod started before the annotation (restart it).

With Pod Identity, make no annotation and create the association instead; see
[the EKS install guide](install/eks.md#34-iam-and-eks-pod-identity). Do not use both for the same ServiceAccount.

## Effective rates from the AWS Cost and Usage Report

List prices overstate the savings of teams that pay less, through Savings Plans, Reserved Instances or a
negotiated discount. With the report reader on, the pricing service reads your Cost and Usage Report (CUR 2.0,
exported by AWS Data Exports as Parquet in S3) once a day. It computes the rate you actually paid per instance type,
region and capacity type, and prices each node at it.

### The formula

For each instance type, region (`product_region_code`) and capacity type, over the EC2 usage whose
`line_item_usage_start_date` falls in the last 30 days:

```
effective hourly rate = sum(amortised cost) / sum(usage hours)
```

Only EC2 Linux instance usage counts: `line_item_product_code = 'AmazonEC2'`, `line_item_operation = 'RunInstances'`,
a `line_item_usage_type` containing `BoxUsage`, `SpotUsage` or `HeavyUsage`, and a `product_instance_type`. A line
is spot when `pricing_term = 'Spot'` or its usage type contains `SpotUsage`; everything else, including hours
covered by Savings Plans and Reserved Instances, is on-demand.

The amortised cost of a line follows AWS's amortized cost, by `line_item_line_item_type`:

| Line item type | Amortised cost | Usage hours (`line_item_usage_amount`) |
|---|---|---|
| `Usage` (on-demand, spot) | `line_item_unblended_cost` | yes |
| `SavingsPlanCoveredUsage` | `savings_plan_savings_plan_effective_cost` | yes |
| `DiscountedUsage` (covered by a Reserved Instance) | `reservation_effective_cost` | yes |
| `RIFee` (unused Reserved Instance hours) | `reservation_unused_amortized_upfront_fee_for_billing_period` + `reservation_unused_recurring_fee` | no |

Other line item types are left out. `SavingsPlanNegation` lines cancel the on-demand cost of covered hours, which
the effective cost already replaces. Upfront fees (`Fee`, `SavingsPlanUpfrontFee`) and recurring Savings Plan fees
are already spread into the effective costs. Unused Savings Plan commitment belongs to no instance type, so it is
not added to any rate. Credits, refunds, taxes and discount lines are left out too.

The rate uses the unblended and effective-cost columns, not the `net_` ones, so an Enterprise Discount Program (EDP)
or private pricing discount is **not** in it. Keep entering those as a discount in **Admin → Pricing**. Don't enter
Savings Plans or Reserved Instances there any more: they are already in `cur` rates, and you would count them twice.

Example: in one month, an `m6i.xlarge` in `eu-west-1` ran 100 on-demand hours ($21.40), 300 hours under a
Savings Plan (effective cost $45.00) and 200 hours under a Reserved Instance (effective cost $26.00). The
Reserved Instance also left unused hours worth $2.00 of amortised upfront fee and $3.00 of recurring fee. The rate
is (21.40 + 45.00 + 26.00 + 2.00 + 3.00) / (100 + 300 + 200) = $0.1623 per hour, against a list price of $0.214.
This is the fixture the tests check (`internal/pricing/testdata/cur`).

### Fallback and freshness

- An instance type, region and capacity type without usage in the window keeps its list (or spot, or static)
  price. A Reserved Instance with no usage at all gives no rate.
- The report is read every `cur.interval` (24 h). A failed read is retried after 15 minutes. The last good rates
  are kept for `cur.maxAge` (72 h); after that, every node falls back to its list price until a read succeeds.
- A failed read shows as a warning on the **`pricing.coverage`** readiness check of every cluster, and on
  **Admin → Pricing**.
- Series of the pricing service (with the hub's `cluster` label): `kerf_pricing_cur_last_read_ok` (1/0),
  `kerf_pricing_cur_last_success_timestamp_seconds`, `kerf_pricing_cur_rates` (keys with an effective rate),
  `kerf_pricing_cur_period_start_timestamp_seconds`, `kerf_pricing_cur_period_end_timestamp_seconds`, and
  `kerf_pricing_cur_refreshes_total{result}`. Alert on stale reads, for example
  `time() - kerf_pricing_cur_last_success_timestamp_seconds > 2 * 86400`.
- Only the aggregated rates are kept, in the pricing service's memory. Line items are never copied into Kerf's
  database or metrics. The S3 reader spools one Parquet file at a time to an `emptyDir` and deletes it.

## Set it up

### 1. Create the CUR 2.0 export

In the AWS Billing and Cost Management console, **Data Exports → Create**:

| Setting | Value |
|---|---|
| Export type | Standard data export |
| Table | CUR 2.0 |
| Columns | All columns (the default). At least the columns named in [the formula](#the-formula) plus `line_item_usage_end_date`. |
| Time granularity | Hourly or Daily (not Monthly: the 30-day window is by usage start) |
| Compression and file format | Parquet |
| File versioning | Overwrite existing data export file |
| S3 bucket and path prefix | For example `s3://<bucket>/cur`, with an export named `kerf`. The data lands in `cur/kerf/data/BILLING_PERIOD=YYYY-MM/`. |

Export from the **management (payer) account** to see the usage of every linked account. A member account's export
covers only that account. The first data arrives within 24 hours; AWS refreshes the current month's files up to
several times a day.

### 2. Choose a reader

- **Athena** (recommended). Create a Glue table over the export's `data/` path, for example with a Glue crawler on
  `s3://<bucket>/cur/kerf/data/` (it adds the `billing_period` partition), and an Athena workgroup with a query
  result location. The query aggregates in Athena; one row per instance type comes back.
- **S3** (small accounts). The pricing service lists the `.parquet` files of the billing periods in the window and
  reads the needed columns itself. Every read downloads the whole window, so use it when the export is a few
  hundred MB at most, and raise `resources.limits.memory` and `cur.s3.tmpSizeLimit` with its size.

### 3. IAM

The pricing service uses the default credential chain, with the same role as for the price list: EKS Pod Identity
or [IRSA](#give-the-pricing-service-aws-access). Add one of these statements to the
`kerf-pricing` role's policy, next to `pricing:GetProducts` and `ec2:DescribeSpotPriceHistory`.

Athena (replace the region, account, workgroup, database, table and buckets):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "KerfCurAthena",
      "Effect": "Allow",
      "Action": ["athena:StartQueryExecution", "athena:GetQueryExecution", "athena:GetQueryResults", "athena:GetWorkGroup"],
      "Resource": "arn:aws:athena:<region>:<account>:workgroup/<workgroup>"
    },
    {
      "Sid": "KerfCurGlue",
      "Effect": "Allow",
      "Action": ["glue:GetDatabase", "glue:GetTable", "glue:GetPartition", "glue:GetPartitions"],
      "Resource": [
        "arn:aws:glue:<region>:<account>:catalog",
        "arn:aws:glue:<region>:<account>:database/<database>",
        "arn:aws:glue:<region>:<account>:table/<database>/<table>"
      ]
    },
    {
      "Sid": "KerfCurData",
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:ListBucket", "s3:GetBucketLocation"],
      "Resource": ["arn:aws:s3:::<cur-bucket>", "arn:aws:s3:::<cur-bucket>/cur/kerf/data/*"]
    },
    {
      "Sid": "KerfCurQueryResults",
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject", "s3:AbortMultipartUpload", "s3:ListBucket", "s3:GetBucketLocation"],
      "Resource": ["arn:aws:s3:::<results-bucket>", "arn:aws:s3:::<results-bucket>/<results-prefix>/*"]
    }
  ]
}
```

S3:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "KerfCurList",
      "Effect": "Allow",
      "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::<cur-bucket>",
      "Condition": {"StringLike": {"s3:prefix": ["cur/kerf/data/*"]}}
    },
    {
      "Sid": "KerfCurRead",
      "Effect": "Allow",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::<cur-bucket>/cur/kerf/data/*"
    }
  ]
}
```

When the export lives in another account (the management account) than the hub cluster, also allow the
`kerf-pricing` role in that bucket's policy (S3 reader), or create the Athena table and workgroup in the hub's
account over a bucket that trusts it. If the bucket uses a customer-managed KMS key, add `kms:Decrypt` on that key.

### 4. Turn it on

In the `kerf` chart's values (or the `pricing` chart's, without the `pricing:` level):

```yaml
pricing:
  provider: aws
  cur:
    source: athena                 # or s3
    region: us-east-1              # where the workgroup or bucket is
    athena:
      database: kerf_cur
      table: data
      workgroup: kerf
      outputLocation: ""           # s3://<results-bucket>/<results-prefix>/ unless the workgroup sets one
    # s3:
    #   bucket: <cur-bucket>
    #   prefix: cur/kerf/data/
```

Other values: `cur.interval` (24h), `cur.window` (720h), `cur.maxAge` (72h). With a NetworkPolicy, the pricing
service needs HTTPS (443) to Athena and S3, which `pricing.networkPolicy.egress.ports` allows by default.

### 5. Check

```bash
kubectl -n kerf logs deploy/pricing | grep -i "cost and usage report"
kubectl -n kerf get --raw "/api/v1/namespaces/kerf/services/pricing:9090/proxy/metrics" | grep -E '^kerf_pricing_cur_|source="cur"' | head
```

Expected: a log line `cost and usage report read` with the number of rates and the period, then
`kerf_pricing_cur_last_read_ok 1` and cost series with `source="cur"`. **Admin → Pricing** shows "Effective rates
from the AWS Cost and Usage Report" with the period, and each cluster's nodes by rate source. An `AccessDenied`,
`TABLE_NOT_FOUND` or `no .parquet object` error in the log names what to fix; nodes keep their list prices meanwhile.
