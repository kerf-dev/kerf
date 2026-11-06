package pricing

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	athenatypes "github.com/aws/aws-sdk-go-v2/service/athena/types"
)

// AthenaAPI is the part of the Athena client the CUR reader uses.
type AthenaAPI interface {
	StartQueryExecution(ctx context.Context, in *athena.StartQueryExecutionInput, opts ...func(*athena.Options)) (*athena.StartQueryExecutionOutput, error)
	GetQueryExecution(ctx context.Context, in *athena.GetQueryExecutionInput, opts ...func(*athena.Options)) (*athena.GetQueryExecutionOutput, error)
	GetQueryResults(ctx context.Context, in *athena.GetQueryResultsInput, opts ...func(*athena.Options)) (*athena.GetQueryResultsOutput, error)
}

// CURAthena reads the report through an Athena table over the CUR 2.0 export (created by the
// export's CloudFormation template or a Glue crawler). The query aggregates in Athena, so only one
// row per (region, instance type, capacity type, line item type) comes back.
type CURAthena struct {
	Athena    AthenaAPI
	Database  string
	Table     string
	Catalog   string // default AwsDataCatalog
	Workgroup string // default primary
	// OutputLocation is the s3:// URL for query results; empty = the workgroup's setting.
	OutputLocation string
	// PollInterval between GetQueryExecution calls (default 2s); Timeout of one query (default 10m).
	PollInterval time.Duration
	Timeout      time.Duration
}

func (r *CURAthena) Name() string { return "athena" }

// athenaIdentRE accepts Glue database and table names (lower case letters, digits, underscores).
var athenaIdentRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)

// curAthenaQuery is the query the Athena reader runs; database and table are validated identifiers,
// the window bounds are execution parameters. The filters and groups match curInstanceUsage,
// curCapacityType and CURGroup.
const curAthenaQuery = `SELECT
  product_region_code,
  product_instance_type,
  CASE WHEN pricing_term = 'Spot' OR line_item_usage_type LIKE '%%SpotUsage%%' THEN 'spot' ELSE 'on-demand' END AS capacity_type,
  line_item_line_item_type,
  min(line_item_usage_start_date),
  max(line_item_usage_end_date),
  sum(line_item_usage_amount),
  sum(line_item_unblended_cost),
  sum(savings_plan_savings_plan_effective_cost),
  sum(reservation_effective_cost),
  sum(reservation_unused_amortized_upfront_fee_for_billing_period),
  sum(reservation_unused_recurring_fee)
FROM "%s"."%s"
WHERE line_item_product_code = 'AmazonEC2'
  AND line_item_operation = 'RunInstances'
  AND product_instance_type <> ''
  AND (line_item_usage_type LIKE '%%BoxUsage%%' OR line_item_usage_type LIKE '%%SpotUsage%%' OR line_item_usage_type LIKE '%%HeavyUsage%%')
  AND line_item_line_item_type IN ('Usage', 'SavingsPlanCoveredUsage', 'DiscountedUsage', 'RIFee')
  AND line_item_usage_start_date >= ?
  AND line_item_usage_start_date < ?
GROUP BY 1, 2, 3, 4`

// Query returns the SQL and its execution parameters for the window [from, to).
func (r *CURAthena) Query(from, to time.Time) (string, []string, error) {
	if !athenaIdentRE.MatchString(r.Database) || !athenaIdentRE.MatchString(r.Table) {
		return "", nil, fmt.Errorf("cur athena: invalid database %q or table %q (letters, digits and _ only)", r.Database, r.Table)
	}
	ts := func(t time.Time) string { return "TIMESTAMP '" + t.UTC().Format("2006-01-02 15:04:05.000") + "'" }
	return fmt.Sprintf(curAthenaQuery, r.Database, r.Table), []string{ts(from), ts(to)}, nil
}

func (r *CURAthena) Groups(ctx context.Context, from, to time.Time) ([]CURGroup, error) {
	sql, params, err := r.Query(from, to)
	if err != nil {
		return nil, err
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	in := &athena.StartQueryExecutionInput{
		QueryString:           aws.String(sql),
		ExecutionParameters:   params,
		QueryExecutionContext: &athenatypes.QueryExecutionContext{Database: aws.String(r.Database)},
	}
	if r.Catalog != "" {
		in.QueryExecutionContext.Catalog = aws.String(r.Catalog)
	}
	if r.Workgroup != "" {
		in.WorkGroup = aws.String(r.Workgroup)
	}
	if r.OutputLocation != "" {
		in.ResultConfiguration = &athenatypes.ResultConfiguration{OutputLocation: aws.String(r.OutputLocation)}
	}
	start, err := r.Athena.StartQueryExecution(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("cur athena: StartQueryExecution: %w", err)
	}
	id := aws.ToString(start.QueryExecutionId)
	if err := r.wait(ctx, id); err != nil {
		return nil, err
	}
	return r.results(ctx, id)
}

func (r *CURAthena) wait(ctx context.Context, id string) error {
	poll := r.PollInterval
	if poll <= 0 {
		poll = 2 * time.Second
	}
	for {
		out, err := r.Athena.GetQueryExecution(ctx, &athena.GetQueryExecutionInput{QueryExecutionId: aws.String(id)})
		if err != nil {
			return fmt.Errorf("cur athena: GetQueryExecution %s: %w", id, err)
		}
		var st athenatypes.QueryExecutionStatus
		if out.QueryExecution != nil && out.QueryExecution.Status != nil {
			st = *out.QueryExecution.Status
		}
		switch st.State {
		case athenatypes.QueryExecutionStateSucceeded:
			return nil
		case athenatypes.QueryExecutionStateFailed, athenatypes.QueryExecutionStateCancelled:
			reason := aws.ToString(st.StateChangeReason)
			if st.AthenaError != nil && st.AthenaError.ErrorMessage != nil && reason == "" {
				reason = aws.ToString(st.AthenaError.ErrorMessage)
			}
			return fmt.Errorf("cur athena: query %s %s: %s", id, strings.ToLower(string(st.State)), reason)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cur athena: query %s: %w", id, ctx.Err())
		case <-time.After(poll):
		}
	}
}

func (r *CURAthena) results(ctx context.Context, id string) ([]CURGroup, error) {
	var groups []CURGroup
	in := &athena.GetQueryResultsInput{QueryExecutionId: aws.String(id)}
	header := true
	for p := athena.NewGetQueryResultsPaginator(r.Athena, in); p.HasMorePages(); {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cur athena: GetQueryResults %s: %w", id, err)
		}
		if out.ResultSet == nil {
			continue
		}
		for _, row := range out.ResultSet.Rows {
			if header { // the first row of the first page holds the column names
				header = false
				continue
			}
			g, err := parseAthenaRow(row.Data)
			if err != nil {
				return nil, fmt.Errorf("cur athena: query %s: %w", id, err)
			}
			groups = append(groups, g)
		}
	}
	return groups, nil
}

func parseAthenaRow(d []athenatypes.Datum) (CURGroup, error) {
	if len(d) != 12 {
		return CURGroup{}, fmt.Errorf("result row has %d columns, want 12", len(d))
	}
	s := func(i int) string { return aws.ToString(d[i].VarCharValue) }
	var errs []error
	f := func(i int) float64 {
		v := s(i)
		if v == "" { // NULL: no line item of the group has the column
			return 0
		}
		x, err := strconv.ParseFloat(v, 64)
		if err != nil {
			errs = append(errs, fmt.Errorf("column %d: %w", i+1, err))
		}
		return x
	}
	t := func(i int) time.Time {
		v := s(i)
		if v == "" {
			return time.Time{}
		}
		for _, layout := range []string{"2006-01-02 15:04:05.000", "2006-01-02 15:04:05", time.RFC3339Nano} {
			if ts, err := time.Parse(layout, v); err == nil {
				return ts.UTC()
			}
		}
		errs = append(errs, fmt.Errorf("column %d: timestamp %q", i+1, v))
		return time.Time{}
	}
	ct, err := ParseCapacityType(s(2))
	if err != nil {
		errs = append(errs, err)
	}
	g := CURGroup{
		Region: s(0), InstanceType: s(1), CapacityType: ct, LineItemType: s(3),
		Start: t(4), End: t(5),
		UsageAmount: f(6), UnblendedCost: f(7), SavingsPlanEffectiveCost: f(8), ReservationEffectiveCost: f(9),
		ReservationUnusedAmortizedFee: f(10), ReservationUnusedRecurringFee: f(11),
	}
	return g, errors.Join(errs...)
}
