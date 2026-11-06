package pricing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// curHandRates is the hand calculation of testdata/cur/README.md.
var curHandRates = map[Key]float64{
	// (21.40 on-demand + 45.00 SP effective + 26.00 RI effective + 2.00 + 3.00 unused RI) / (100 + 300 + 200) h
	{Region: "eu-west-1", InstanceType: "m6i.xlarge", CapacityType: OnDemand}: 97.40 / 600,
	// 4.00 / 50 h
	{Region: "eu-west-1", InstanceType: "m6i.xlarge", CapacityType: Spot}: 0.08,
	// 20.40 SP effective / 100 h
	{Region: "eu-west-1", InstanceType: "c6a.2xlarge", CapacityType: OnDemand}: 0.204,
	// 1.92 / 10 h
	{Region: "us-east-1", InstanceType: "m6i.xlarge", CapacityType: OnDemand}: 0.192,
}

var (
	curHandStart = oct(1, 0)
	curHandEnd   = oct(31, 8)
)

func checkHandRates(t *testing.T, got CURRates) {
	t.Helper()
	if len(got.Rates) != len(curHandRates) {
		t.Errorf("got %d rates, want %d: %+v", len(got.Rates), len(curHandRates), got.Rates)
	}
	for k, want := range curHandRates {
		r, ok := got.Rates[k]
		if !ok {
			t.Errorf("%s: no rate", k)
			continue
		}
		if math.Abs(r.HourlyUSD-want) > 1e-9 {
			t.Errorf("%s: rate %.9f, want %.9f (cost %v / %v h)", k, r.HourlyUSD, want, r.CostUSD, r.Hours)
		}
	}
	if !got.Start.Equal(curHandStart) || !got.End.Equal(curHandEnd) {
		t.Errorf("period %s – %s, want %s – %s", got.Start, got.End, curHandStart, curHandEnd)
	}
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(curFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureGroups(t *testing.T) []CURGroup {
	t.Helper()
	b := readFixture(t)
	var agg curAggregator
	if err := ReadCURParquet(bytes.NewReader(b), int64(len(b)), curFixtureFrom, curFixtureTo, agg.add); err != nil {
		t.Fatal(err)
	}
	return agg.result()
}

func TestCURRatesMatchHandCalculation(t *testing.T) {
	checkHandRates(t, ComputeCURRates(fixtureGroups(t)))
}

func TestCURFixtureIsCurrent(t *testing.T) {
	// The committed Parquet file holds exactly curFixtureRows (regenerate with -update-cur-fixture).
	b := readFixture(t)
	var n int
	if err := ReadCURParquet(bytes.NewReader(b), int64(len(b)), time.Time{}, time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC), func(CURLineItem, time.Time, time.Time) { n++ }); err != nil {
		t.Fatal(err)
	}
	if want := len(curFixtureRows()); n != want {
		t.Fatalf("fixture has %d rows, curFixtureRows %d: run go test ./internal/pricing -run TestWriteCURFixture -update-cur-fixture", n, want)
	}
}

func TestComputeCURRates(t *testing.T) {
	k := Key{Region: "eu-west-1", InstanceType: "m6i.large", CapacityType: OnDemand}
	g := func(typ string, hours, unblended, sp, ri, unusedA, unusedR float64) CURGroup {
		return CURGroup{Region: k.Region, InstanceType: k.InstanceType, CapacityType: OnDemand, LineItemType: typ,
			Start: oct(1, 0), End: oct(2, 0), UsageAmount: hours, UnblendedCost: unblended,
			SavingsPlanEffectiveCost: sp, ReservationEffectiveCost: ri, ReservationUnusedAmortizedFee: unusedA, ReservationUnusedRecurringFee: unusedR}
	}
	tests := []struct {
		name   string
		groups []CURGroup
		want   float64 // 0 = no rate
	}{
		{"on-demand only", []CURGroup{g(LineItemUsage, 10, 1.07, 0, 0, 0, 0)}, 0.107},
		{"savings plan uses the effective cost, not the unblended", []CURGroup{g(LineItemSavingsPlanCoveredUsage, 10, 1.07, 0.70, 0, 0, 0)}, 0.07},
		{"reserved instance uses the effective cost", []CURGroup{g(LineItemDiscountedUsage, 10, 0, 0, 0.60, 0, 0)}, 0.06},
		{"unused RI adds cost but no hours", []CURGroup{g(LineItemDiscountedUsage, 10, 0, 0, 0.60, 0, 0), g(LineItemRIFee, 744, 44.64, 0, 0, 0.10, 0.30)}, 0.10},
		{"unused RI alone gives no rate", []CURGroup{g(LineItemRIFee, 744, 44.64, 0, 0, 0.10, 0.30)}, 0},
		{"other line item types are ignored", []CURGroup{g(LineItemUsage, 10, 1.07, 0, 0, 0, 0), g("SavingsPlanNegation", 0, -5, 0, 0, 0, 0), g("Credit", 0, -1, 0, 0, 0, 0)}, 0.107},
		{"no cost gives no rate", []CURGroup{g(LineItemUsage, 10, 0, 0, 0, 0, 0)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := ComputeCURRates(tt.groups).Rates[k]
			if tt.want == 0 {
				if ok {
					t.Fatalf("got rate %v, want none", r.HourlyUSD)
				}
				return
			}
			if !ok || math.Abs(r.HourlyUSD-tt.want) > 1e-9 {
				t.Fatalf("got %v (ok=%v), want %v", r.HourlyUSD, ok, tt.want)
			}
		})
	}
}

// fakeS3 serves objects from memory.
type fakeS3 struct {
	objects map[string][]byte
	listErr error
	gets    []string
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := &s3.ListObjectsV2Output{}
	for k, b := range f.objects {
		if strings.HasPrefix(k, aws.ToString(in.Prefix)) {
			out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k), Size: aws.Int64(int64(len(b)))})
		}
	}
	return out, nil
}

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	k := aws.ToString(in.Key)
	f.gets = append(f.gets, k)
	b, ok := f.objects[k]
	if !ok {
		return nil, fmt.Errorf("NoSuchKey: %s", k)
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func TestCURS3(t *testing.T) {
	prefix := "cur/kerf/data/"
	fixture := readFixture(t)
	tests := []struct {
		name     string
		objects  map[string][]byte
		listErr  error
		wantErr  string
		wantGets int
	}{
		{
			name: "reads the window's partitions only",
			objects: map[string][]byte{
				prefix + "BILLING_PERIOD=2026-10/kerf-00001.snappy.parquet": fixture,
				prefix + "BILLING_PERIOD=2026-07/kerf-00001.snappy.parquet": []byte("not parquet: outside the window, never read"),
				prefix + "metadata/kerf-Manifest.json":                      []byte("{}"),
				"other/BILLING_PERIOD=2026-10/x.parquet":                    []byte("another prefix"),
			},
			wantGets: 1,
		},
		{
			name:    "no parquet object",
			objects: map[string][]byte{prefix + "metadata/kerf-Manifest.json": []byte("{}")},
			wantErr: "no .parquet object",
		},
		{
			name:    "unreadable object",
			objects: map[string][]byte{prefix + "BILLING_PERIOD=2026-10/broken.parquet": []byte("truncated")},
			wantErr: "broken.parquet",
		},
		{
			name:    "access denied",
			listErr: errors.New("AccessDenied: s3:ListBucket"),
			wantErr: "AccessDenied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeS3{objects: tt.objects, listErr: tt.listErr}
			r := &CURS3{S3: f, Bucket: "billing", Prefix: prefix, TempDir: t.TempDir()}
			groups, err := r.Groups(context.Background(), curFixtureFrom, curFixtureTo)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(f.gets) != tt.wantGets {
				t.Errorf("read %d objects (%v), want %d", len(f.gets), f.gets, tt.wantGets)
			}
			checkHandRates(t, ComputeCURRates(groups))
		})
	}
}

func TestInWindow(t *testing.T) {
	from, to := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	for key, want := range map[string]bool{
		"d/BILLING_PERIOD=2026-09/a.parquet": false,
		"d/BILLING_PERIOD=2026-10/a.parquet": true,
		"d/BILLING_PERIOD=2026-11/a.parquet": true,
		"d/BILLING_PERIOD=2026-12/a.parquet": false,
		"d/a.parquet":                        true,
	} {
		if got := inWindow(key, from, to); got != want {
			t.Errorf("%s: %v, want %v", key, got, want)
		}
	}
}

// fakeAthena runs the reader's query against the fixture: it checks the SQL and parameters, then
// returns the fixture's groups as Athena string results, over two pages.
type fakeAthena struct {
	t        *testing.T
	groups   []CURGroup
	states   []athenatypes.QueryExecutionState
	failMsg  string
	started  *athena.StartQueryExecutionInput
	polls    int
	startErr error
}

func (f *fakeAthena) StartQueryExecution(_ context.Context, in *athena.StartQueryExecutionInput, _ ...func(*athena.Options)) (*athena.StartQueryExecutionOutput, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.started = in
	return &athena.StartQueryExecutionOutput{QueryExecutionId: aws.String("q-1")}, nil
}

func (f *fakeAthena) GetQueryExecution(_ context.Context, in *athena.GetQueryExecutionInput, _ ...func(*athena.Options)) (*athena.GetQueryExecutionOutput, error) {
	st := f.states[min(f.polls, len(f.states)-1)]
	f.polls++
	status := &athenatypes.QueryExecutionStatus{State: st}
	if st == athenatypes.QueryExecutionStateFailed {
		status.StateChangeReason = aws.String(f.failMsg)
	}
	return &athena.GetQueryExecutionOutput{QueryExecution: &athenatypes.QueryExecution{QueryExecutionId: in.QueryExecutionId, Status: status}}, nil
}

func (f *fakeAthena) GetQueryResults(_ context.Context, in *athena.GetQueryResultsInput, _ ...func(*athena.Options)) (*athena.GetQueryResultsOutput, error) {
	str := func(s string) athenatypes.Datum { return athenatypes.Datum{VarCharValue: aws.String(s)} }
	num := func(v float64) athenatypes.Datum {
		if v == 0 {
			return athenatypes.Datum{} // NULL, as for a group without SP/RI columns
		}
		return str(strconv.FormatFloat(v, 'f', -1, 64))
	}
	ts := func(t time.Time) athenatypes.Datum { return str(t.Format("2006-01-02 15:04:05.000")) }
	var rows []athenatypes.Row
	header := athenatypes.Row{}
	for range 12 {
		header.Data = append(header.Data, str("col"))
	}
	rows = append(rows, header)
	for _, g := range f.groups {
		rows = append(rows, athenatypes.Row{Data: []athenatypes.Datum{
			str(g.Region), str(g.InstanceType), str(string(g.CapacityType)), str(g.LineItemType), ts(g.Start), ts(g.End),
			num(g.UsageAmount), num(g.UnblendedCost), num(g.SavingsPlanEffectiveCost), num(g.ReservationEffectiveCost),
			num(g.ReservationUnusedAmortizedFee), num(g.ReservationUnusedRecurringFee),
		}})
	}
	half := len(rows) / 2
	if in.NextToken == nil {
		return &athena.GetQueryResultsOutput{ResultSet: &athenatypes.ResultSet{Rows: rows[:half]}, NextToken: aws.String("page-2")}, nil
	}
	return &athena.GetQueryResultsOutput{ResultSet: &athenatypes.ResultSet{Rows: rows[half:]}}, nil
}

func TestCURAthena(t *testing.T) {
	running := []athenatypes.QueryExecutionState{athenatypes.QueryExecutionStateQueued, athenatypes.QueryExecutionStateRunning}
	tests := []struct {
		name    string
		states  []athenatypes.QueryExecutionState
		failMsg string
		table   string
		wantErr string
	}{
		{name: "succeeded", states: append(running, athenatypes.QueryExecutionStateSucceeded), table: "data"},
		{name: "failed", states: append(running, athenatypes.QueryExecutionStateFailed), failMsg: "TABLE_NOT_FOUND: Table kerf.data does not exist", table: "data", wantErr: "TABLE_NOT_FOUND"},
		{name: "injection in the table name", table: `data" UNION SELECT`, wantErr: "invalid database"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAthena{t: t, groups: fixtureGroups(t), states: tt.states, failMsg: tt.failMsg}
			r := &CURAthena{Athena: f, Database: "kerf_cur", Table: tt.table, Workgroup: "kerf", OutputLocation: "s3://billing/athena/", PollInterval: time.Millisecond}
			groups, err := r.Groups(context.Background(), curFixtureFrom, curFixtureTo)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			in := f.started
			if got := in.ExecutionParameters; len(got) != 2 || got[0] != "TIMESTAMP '2026-10-01 00:00:00.000'" || got[1] != "TIMESTAMP '2026-11-01 00:00:00.000'" {
				t.Errorf("execution parameters %q", got)
			}
			if aws.ToString(in.WorkGroup) != "kerf" || aws.ToString(in.ResultConfiguration.OutputLocation) != "s3://billing/athena/" || aws.ToString(in.QueryExecutionContext.Database) != "kerf_cur" {
				t.Errorf("query context %+v %+v", in.QueryExecutionContext, in.ResultConfiguration)
			}
			sql := aws.ToString(in.QueryString)
			for _, s := range []string{`FROM "kerf_cur"."data"`, "savings_plan_savings_plan_effective_cost", "reservation_unused_amortized_upfront_fee_for_billing_period", "line_item_usage_start_date >= ?", "LIKE '%SpotUsage%'"} {
				if !strings.Contains(sql, s) {
					t.Errorf("query lacks %q:\n%s", s, sql)
				}
			}
			checkHandRates(t, ComputeCURRates(groups))
		})
	}
}

// fakeReader returns fixed groups or an error.
type fakeReader struct {
	groups []CURGroup
	err    error
}

func (f *fakeReader) Name() string { return "fake" }
func (f *fakeReader) Groups(context.Context, time.Time, time.Time) ([]CURGroup, error) {
	return f.groups, f.err
}

func TestCURProvider(t *testing.T) {
	static, err := LoadStatic("../../dev/prices.yaml")
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeReader{groups: fixtureGroups(t)}
	now := oct(31, 12)
	c := NewCUR(reader, static, nil)
	c.Now = func() time.Time { return now }
	reg := prometheus.NewRegistry()
	if err := c.Register(reg); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m6i := Key{Region: "eu-west-1", Zone: "eu-west-1a", InstanceType: "m6i.xlarge", CapacityType: OnDemand}
	m6iSpot := Key{Region: "eu-west-1", Zone: "eu-west-1b", InstanceType: "m6i.xlarge", CapacityType: Spot}
	r6i := Key{Region: "eu-west-1", InstanceType: "r6i.xlarge", CapacityType: OnDemand}

	check := func(step string, k Key, wantSource string, wantUSD float64) {
		t.Helper()
		p, err := c.Price(ctx, k)
		if err != nil {
			t.Fatalf("%s: %s: %v", step, k, err)
		}
		if p.Source != wantSource || math.Abs(p.HourlyUSD-wantUSD) > 1e-9 {
			t.Errorf("%s: %s = %v (%s), want %v (%s)", step, k, p.HourlyUSD, p.Source, wantUSD, wantSource)
		}
		if wantSource == SourceCUR && (!p.PeriodStart.Equal(curHandStart) || !p.PeriodEnd.Equal(curHandEnd) || p.Shape.VCPU != 4) {
			t.Errorf("%s: %s period %s – %s shape %+v", step, k, p.PeriodStart, p.PeriodEnd, p.Shape)
		}
	}
	gauge := func(name string) float64 {
		t.Helper()
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() == name {
				return mf.GetMetric()[0].GetGauge().GetValue()
			}
		}
		t.Fatalf("no %s", name)
		return 0
	}

	// Before the first read every key falls back.
	check("before the first read", m6i, SourceStatic, 0.214)

	// A failed first read: still the fallback, with the failure visible.
	reader.err = errors.New("AccessDenied")
	if err := c.Refresh(ctx); err == nil {
		t.Fatal("refresh succeeded with a reader error")
	}
	check("unreadable report", m6i, SourceStatic, 0.214)
	if v := gauge("kerf_pricing_cur_last_read_ok"); v != 0 {
		t.Errorf("last_read_ok %v after a failure", v)
	}
	if v := gauge("kerf_pricing_cur_last_success_timestamp_seconds"); v != 0 {
		t.Errorf("last success %v before any success", v)
	}

	// A good read: effective rates where the report has usage, list prices elsewhere.
	reader.err = nil
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	check("after a read", m6i, SourceCUR, 97.40/600)
	check("after a read", m6iSpot, SourceCUR, 0.08)
	check("no usage in the report", r6i, SourceStatic, 0.282)
	if v := gauge("kerf_pricing_cur_last_success_timestamp_seconds"); v != float64(now.Unix()) {
		t.Errorf("last success %v, want %v", v, now.Unix())
	}
	if v := gauge("kerf_pricing_cur_rates"); v != 4 {
		t.Errorf("rates gauge %v, want 4", v)
	}
	if got := testutil.ToFloat64(c.refreshes.WithLabelValues("success")); got != 1 {
		t.Errorf("success count %v", got)
	}

	// A later failure keeps the last rates for MaxAge...
	reader.err = errors.New("throttled")
	now = now.Add(48 * time.Hour)
	_ = c.Refresh(ctx)
	check("failure within MaxAge", m6i, SourceCUR, 97.40/600)
	if st := c.Status(); !st.InUse || st.LastError == nil {
		t.Errorf("status %+v", st)
	}
	// ...and then everything falls back.
	now = now.Add(25 * time.Hour)
	_ = c.Refresh(ctx)
	check("failure after MaxAge", m6i, SourceStatic, 0.214)
	if v := gauge("kerf_pricing_cur_rates"); v != 0 {
		t.Errorf("rates gauge %v after MaxAge, want 0", v)
	}

	// An empty report is a failure too.
	reader.err, reader.groups = nil, nil
	if err := c.Refresh(ctx); err == nil || !strings.Contains(err.Error(), "no EC2 instance usage") {
		t.Fatalf("empty report: %v", err)
	}
}

func TestExporterSourceLabel(t *testing.T) {
	static, err := LoadStatic("../../dev/prices.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c := NewCUR(&fakeReader{groups: fixtureGroups(t)}, static, nil)
	c.Now = func() time.Time { return oct(31, 12) }
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	nodes := &fakeNodes{nodes: []Node{
		{Cluster: "c1", Name: "a", InstanceType: "m6i.xlarge", CapacityType: OnDemand, Region: "eu-west-1", Zone: "eu-west-1a", AllocatableCores: 4, AllocatableGiB: 15},
		{Cluster: "c1", Name: "b", InstanceType: "r6i.xlarge", CapacityType: OnDemand, Region: "eu-west-1", Zone: "eu-west-1a", AllocatableCores: 4, AllocatableGiB: 30},
	}}
	e := NewExporter(nodes, c, &Splitter{Provider: static}, nil)
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, nc := range e.Costs() {
		got[nc.Name] = nc.Source
	}
	if got["a"] != SourceCUR || got["b"] != SourceStatic {
		t.Fatalf("sources %v, want a=cur b=static", got)
	}
}
