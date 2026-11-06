package pricing

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
)

var updateCURFixture = flag.Bool("update-cur-fixture", false, "rewrite testdata/cur/fixture.parquet from curFixtureRows")

// curFixtureRow is a CUR 2.0 line item with the columns rates use, plus a few others (identity, the
// product map) so the reader has to skip columns. Pointers are nullable columns.
type curFixtureRow struct {
	IdentityLineItemID   string            `parquet:"identity_line_item_id"`
	BillBillingPeriod    time.Time         `parquet:"bill_billing_period_start_date,timestamp(millisecond)"`
	ProductCode          string            `parquet:"line_item_product_code"`
	Operation            string            `parquet:"line_item_operation"`
	UsageType            string            `parquet:"line_item_usage_type"`
	LineItemType         string            `parquet:"line_item_line_item_type"`
	PricingTerm          *string           `parquet:"pricing_term,optional"`
	InstanceType         string            `parquet:"product_instance_type"`
	Region               string            `parquet:"product_region_code"`
	Product              map[string]string `parquet:"product"`
	UsageStart           time.Time         `parquet:"line_item_usage_start_date,timestamp(millisecond)"`
	UsageEnd             time.Time         `parquet:"line_item_usage_end_date,timestamp(millisecond)"`
	UsageAmount          float64           `parquet:"line_item_usage_amount"`
	UnblendedCost        float64           `parquet:"line_item_unblended_cost"`
	SPEffectiveCost      *float64          `parquet:"savings_plan_savings_plan_effective_cost,optional"`
	RIEffectiveCost      *float64          `parquet:"reservation_effective_cost,optional"`
	RIUnusedAmortizedFee *float64          `parquet:"reservation_unused_amortized_upfront_fee_for_billing_period,optional"`
	RIUnusedRecurringFee *float64          `parquet:"reservation_unused_recurring_fee,optional"`
}

func fp(v float64) *float64 { return &v }
func sp(v string) *string   { return &v }

// The fixture's window is October 2026.
var (
	curFixtureFrom = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	curFixtureTo   = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
)

func oct(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC) }

// curFixtureRows: see testdata/cur/README.md for the hand calculation.
func curFixtureRows() []curFixtureRow {
	period := curFixtureFrom
	row := func(id, op, usageType, typ, term, it, region string, start, end time.Time, hours, unblended float64) curFixtureRow {
		r := curFixtureRow{
			IdentityLineItemID: id, BillBillingPeriod: period, ProductCode: "AmazonEC2", Operation: op,
			UsageType: usageType, LineItemType: typ, InstanceType: it, Region: region,
			Product:    map[string]string{"instance_type": it, "region": region, "servicecode": "AmazonEC2"},
			UsageStart: start, UsageEnd: end, UsageAmount: hours, UnblendedCost: unblended,
		}
		if term != "" {
			r.PricingTerm = sp(term)
		}
		return r
	}
	rows := []curFixtureRow{
		// m6i.xlarge on-demand in eu-west-1: on-demand, Savings Plan and RI hours, and unused RI.
		row("od-1", "RunInstances", "EUW1-BoxUsage:m6i.xlarge", "Usage", "OnDemand", "m6i.xlarge", "eu-west-1", oct(1, 0), oct(5, 4), 100, 21.40),
		withSP(row("sp-1", "RunInstances", "EUW1-BoxUsage:m6i.xlarge", "SavingsPlanCoveredUsage", "OnDemand", "m6i.xlarge", "eu-west-1", oct(2, 0), oct(14, 12), 300, 64.20), 45.00),
		row("sp-neg-1", "RunInstances", "EUW1-BoxUsage:m6i.xlarge", "SavingsPlanNegation", "OnDemand", "m6i.xlarge", "eu-west-1", oct(2, 0), oct(14, 12), 0, -64.20),
		withRI(row("ri-1", "RunInstances", "EUW1-BoxUsage:m6i.xlarge", "DiscountedUsage", "Reserved", "m6i.xlarge", "eu-west-1", oct(3, 0), oct(31, 8), 200, 0), 26.00),
		withRIFee(row("rifee-1", "RunInstances", "EUW1-HeavyUsage:m6i.xlarge", "RIFee", "Reserved", "m6i.xlarge", "eu-west-1", oct(1, 0), curFixtureTo, 744, 18.00), 2.00, 3.00),
		row("fee-1", "RunInstances", "EUW1-HeavyUsage:m6i.xlarge", "Fee", "Reserved", "m6i.xlarge", "eu-west-1", oct(1, 0), curFixtureTo, 1, 500),
		// m6i.xlarge spot.
		row("spot-1", "RunInstances", "EUW1-SpotUsage:m6i.xlarge", "Usage", "Spot", "m6i.xlarge", "eu-west-1", oct(10, 0), oct(12, 2), 50, 4.00),
		// c6a.2xlarge fully covered by a Savings Plan.
		withSP(row("sp-2", "RunInstances", "EUW1-BoxUsage:c6a.2xlarge", "SavingsPlanCoveredUsage", "OnDemand", "c6a.2xlarge", "eu-west-1", oct(1, 0), oct(5, 4), 100, 30.60), 20.40),
		// r6i.xlarge: an unused RI and no usage, so no rate.
		withRIFee(row("rifee-2", "RunInstances", "EUW1-HeavyUsage:r6i.xlarge", "RIFee", "Reserved", "r6i.xlarge", "eu-west-1", oct(1, 0), curFixtureTo, 744, 50.00), 10.00, 40.00),
		// m6i.xlarge in us-east-1 (no region prefix in the usage type).
		row("od-2", "RunInstances", "BoxUsage:m6i.xlarge", "Usage", "OnDemand", "m6i.xlarge", "us-east-1", oct(20, 0), oct(20, 10), 10, 1.92),
		// Excluded: Windows, EBS, dedicated tenancy, before the window.
		row("win-1", "RunInstances:0002", "EUW1-BoxUsage:m6i.xlarge", "Usage", "OnDemand", "m6i.xlarge", "eu-west-1", oct(6, 0), oct(6, 10), 10, 10.00),
		row("ebs-1", "CreateVolume-Gp3", "EUW1-EBS:VolumeUsage.gp3", "Usage", "OnDemand", "", "eu-west-1", oct(6, 0), oct(7, 0), 24, 3.00),
		row("ded-1", "RunInstances", "EUW1-DedicatedUsage:m6i.xlarge", "Usage", "OnDemand", "m6i.xlarge", "eu-west-1", oct(6, 0), oct(6, 10), 10, 5.00),
		row("old-1", "RunInstances", "EUW1-BoxUsage:m6i.xlarge", "Usage", "OnDemand", "m6i.xlarge", "eu-west-1", time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC), curFixtureFrom, 1, 100),
	}
	return rows
}

func withSP(r curFixtureRow, effective float64) curFixtureRow {
	r.SPEffectiveCost = fp(effective)
	return r
}

func withRI(r curFixtureRow, effective float64) curFixtureRow {
	r.RIEffectiveCost = fp(effective)
	return r
}

func withRIFee(r curFixtureRow, unusedAmortized, unusedRecurring float64) curFixtureRow {
	r.RIUnusedAmortizedFee, r.RIUnusedRecurringFee = fp(unusedAmortized), fp(unusedRecurring)
	return r
}

const curFixturePath = "testdata/cur/fixture.parquet"

// TestWriteCURFixture regenerates the fixture with -update-cur-fixture. Small row groups make the
// reader cross row-group boundaries.
func TestWriteCURFixture(t *testing.T) {
	if !*updateCURFixture {
		t.Skip("run with -update-cur-fixture to rewrite " + curFixturePath)
	}
	if err := os.MkdirAll(filepath.Dir(curFixturePath), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(curFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := parquet.NewGenericWriter[curFixtureRow](f, parquet.MaxRowsPerRowGroup(5))
	if _, err := w.Write(curFixtureRows()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
