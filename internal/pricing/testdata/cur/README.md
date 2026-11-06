# CUR 2.0 fixture

`fixture.parquet` is a small CUR 2.0 export (the columns rates use, plus `identity_line_item_id`,
`bill_billing_period_start_date` and the `product` map, in row groups of 5). It is generated from
`curFixtureRows` in `cur_fixture_test.go`:

```bash
go test ./internal/pricing -run TestWriteCURFixture -update-cur-fixture
```

Window: October 2026. Hand calculation (`curHandRates` in `cur_test.go`):

| Key | Lines counted | Rate |
|---|---|---|
| eu-west-1 m6i.xlarge on-demand | Usage 100 h $21.40; SavingsPlanCoveredUsage 300 h, effective $45.00; DiscountedUsage 200 h, effective $26.00; RIFee unused $2.00 + $3.00 (no hours) | 97.40 / 600 = 0.16233… |
| eu-west-1 m6i.xlarge spot | Usage (SpotUsage, pricing term Spot) 50 h $4.00 | 0.08 |
| eu-west-1 c6a.2xlarge on-demand | SavingsPlanCoveredUsage 100 h, effective $20.40 | 0.204 |
| us-east-1 m6i.xlarge on-demand | Usage 10 h $1.92 | 0.192 |
| eu-west-1 r6i.xlarge on-demand | RIFee only, no usage | none (list price) |

Excluded: the SavingsPlanNegation and the RI upfront Fee lines, a Windows line (`RunInstances:0002`), an EBS line,
a dedicated-tenancy line and a line that starts before the window. Period: Oct 1 00:00 to Oct 31 08:00 UTC (usage
lines only).
