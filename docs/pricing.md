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

The pricing service uses the default credential chain: EKS Pod Identity, as for the price list
([install guide, 3.4](install/eks.md#34-iam-and-eks-pod-identity)). Add one of these statements to the
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
