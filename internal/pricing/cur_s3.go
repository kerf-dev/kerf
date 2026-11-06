package pricing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/deprecated"
	"github.com/parquet-go/parquet-go/format"
)

// S3API is the part of the S3 client the CUR reader uses.
type S3API interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// CURS3 reads a CUR 2.0 export's Parquet files directly from S3, for accounts whose report is small
// enough to scan on every read (Athena is the reader for large ones). Only the columns rates need are
// read; the files are spooled to TempDir one at a time and removed.
type CURS3 struct {
	S3     S3API
	Bucket string
	// Prefix is the export's data path, <S3 path prefix>/<export name>/data/. Every .parquet object
	// under it is read, except BILLING_PERIOD=YYYY-MM partitions that end before the window.
	Prefix  string
	TempDir string
}

func (r *CURS3) Name() string { return "s3" }

var billingPeriodRE = regexp.MustCompile(`(?:^|/)BILLING_PERIOD=(\d{4})-(\d{2})(?:/|$)`)

// inWindow reports whether an object can hold usage starting in [from, to), from its
// BILLING_PERIOD=YYYY-MM partition (objects without one are always read).
func inWindow(key string, from, to time.Time) bool {
	m := billingPeriodRE.FindStringSubmatch(key)
	if m == nil {
		return true
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	start := time.Date(y, time.Month(mo), 1, 0, 0, 0, 0, time.UTC)
	return start.Before(to) && start.AddDate(0, 1, 0).After(from)
}

func (r *CURS3) Groups(ctx context.Context, from, to time.Time) ([]CURGroup, error) {
	if r.Bucket == "" {
		return nil, errors.New("cur s3: no bucket")
	}
	var keys []string
	in := &s3.ListObjectsV2Input{Bucket: aws.String(r.Bucket), Prefix: aws.String(r.Prefix)}
	for p := s3.NewListObjectsV2Paginator(r.S3, in); p.HasMorePages(); {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("cur s3: list s3://%s/%s: %w", r.Bucket, r.Prefix, err)
		}
		for _, o := range out.Contents {
			k := aws.ToString(o.Key)
			if strings.HasSuffix(k, ".parquet") && inWindow(k, from, to) {
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("cur s3: no .parquet object in the window under s3://%s/%s", r.Bucket, r.Prefix)
	}
	sort.Strings(keys)
	var agg curAggregator
	for _, k := range keys {
		if err := r.readObject(ctx, k, from, to, &agg); err != nil {
			return nil, fmt.Errorf("cur s3: s3://%s/%s: %w", r.Bucket, k, err)
		}
	}
	return agg.result(), nil
}

func (r *CURS3) readObject(ctx context.Context, key string, from, to time.Time, agg *curAggregator) error {
	out, err := r.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.Bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	f, err := os.CreateTemp(r.TempDir, "kerf-cur-*.parquet")
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	n, err := io.Copy(f, out.Body)
	if err != nil {
		return err
	}
	return ReadCURParquet(f, n, from, to, agg.add)
}

// curColumns are the CUR 2.0 columns rates are computed from.
var curColumns = []string{
	"line_item_product_code", "line_item_operation", "line_item_usage_type", "line_item_line_item_type",
	"pricing_term", "product_instance_type", "product_region_code",
	"line_item_usage_start_date", "line_item_usage_end_date",
	"line_item_usage_amount", "line_item_unblended_cost", "savings_plan_savings_plan_effective_cost",
	"reservation_effective_cost", "reservation_unused_amortized_upfront_fee_for_billing_period",
	"reservation_unused_recurring_fee",
}

// curOptionalColumns may be missing: an export without Savings Plans or Reserved Instances columns
// reads them as zero.
var curOptionalColumns = map[string]bool{
	"pricing_term": true, "savings_plan_savings_plan_effective_cost": true, "reservation_effective_cost": true,
	"reservation_unused_amortized_upfront_fee_for_billing_period": true, "reservation_unused_recurring_fee": true,
}

// ReadCURParquet reads the line items of a CUR 2.0 Parquet file and passes those whose usage starts in
// [from, to) to add (which filters the EC2 instance usage). Only the curColumns are read.
func ReadCURParquet(r io.ReaderAt, size int64, from, to time.Time, add func(CURLineItem, time.Time, time.Time)) error {
	f, err := parquet.OpenFile(r, size, parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
	if err != nil {
		return err
	}
	cols := make([]*parquet.Column, len(curColumns))
	for i, name := range curColumns {
		c := f.Root().Column(name)
		if c == nil || !c.Leaf() {
			if curOptionalColumns[name] {
				continue
			}
			return fmt.Errorf("column %s missing: is this a CUR 2.0 export?", name)
		}
		cols[i] = c
	}
	for _, rg := range f.RowGroups() {
		n := int(rg.NumRows())
		vals := make([][]parquet.Value, len(cols))
		for i, c := range cols {
			if c == nil {
				continue
			}
			v, err := readColumn(rg.ColumnChunks()[c.Index()], n)
			if err != nil {
				return fmt.Errorf("column %s: %w", curColumns[i], err)
			}
			vals[i] = v
		}
		str := func(i, row int) string {
			if vals[i] == nil || vals[i][row].IsNull() {
				return ""
			}
			return string(vals[i][row].ByteArray())
		}
		num := func(i, row int) float64 {
			if vals[i] == nil {
				return 0
			}
			return toFloat(vals[i][row])
		}
		ts := func(i, row int) time.Time {
			if vals[i] == nil {
				return time.Time{}
			}
			return toTime(vals[i][row], cols[i].Type())
		}
		for row := 0; row < n; row++ {
			add(CURLineItem{
				ProductCode: str(0, row), Operation: str(1, row), UsageType: str(2, row), LineItemType: str(3, row),
				PricingTerm: str(4, row), InstanceType: str(5, row), Region: str(6, row),
				UsageStart: ts(7, row), UsageEnd: ts(8, row),
				UsageAmount: num(9, row), UnblendedCost: num(10, row), SavingsPlanEffectiveCost: num(11, row),
				ReservationEffectiveCost: num(12, row), ReservationUnusedAmortizedFee: num(13, row),
				ReservationUnusedRecurringFee: num(14, row),
			}, from, to)
		}
	}
	return nil
}

// readColumn reads the n values of a flat (non-repeated) column chunk, nulls included.
func readColumn(cc parquet.ColumnChunk, n int) ([]parquet.Value, error) {
	pages := cc.Pages()
	defer pages.Close()
	out := make([]parquet.Value, 0, n)
	buf := make([]parquet.Value, 1024)
	for {
		p, err := pages.ReadPage()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		vr := p.Values()
		for {
			k, err := vr.ReadValues(buf)
			for _, v := range buf[:k] {
				out = append(out, v.Clone())
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				parquet.Release(p)
				return nil, err
			}
			if k == 0 {
				break
			}
		}
		parquet.Release(p)
	}
	if len(out) != n {
		return nil, fmt.Errorf("read %d values, want %d (a nested column?)", len(out), n)
	}
	return out, nil
}

func toFloat(v parquet.Value) float64 {
	if v.IsNull() {
		return 0
	}
	switch v.Kind() {
	case parquet.Double:
		return v.Double()
	case parquet.Float:
		return float64(v.Float())
	case parquet.Int64:
		return float64(v.Int64())
	case parquet.Int32:
		return float64(v.Int32())
	case parquet.ByteArray, parquet.FixedLenByteArray:
		f, err := strconv.ParseFloat(strings.TrimSpace(string(v.ByteArray())), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0
		}
		return f
	}
	return 0
}

// toTime converts a timestamp value: INT64 with a TIMESTAMP logical (or converted) type, a legacy
// INT96 timestamp, or an RFC 3339 string.
func toTime(v parquet.Value, t parquet.Type) time.Time {
	if v.IsNull() {
		return time.Time{}
	}
	switch v.Kind() {
	case parquet.Int64:
		unit := time.Millisecond
		if lt := t.LogicalType(); lt != nil {
			if ts, ok := lt.Value.(*format.TimestampType); ok && ts.Unit.Value != nil {
				unit = ts.Unit.Value.Duration()
			}
		} else if ct := t.ConvertedType(); ct != nil && *ct == deprecated.TimestampMicros {
			unit = time.Microsecond
		}
		return time.Unix(0, 0).Add(time.Duration(v.Int64()) * unit).UTC()
	case parquet.Int96:
		i := v.Int96()
		nanos := int64(i[1])<<32 | int64(i[0])
		julianDay := int64(i[2])
		const unixEpochJulianDay = 2440588
		return time.Unix((julianDay-unixEpochJulianDay)*86400, nanos).UTC()
	case parquet.ByteArray:
		s := string(v.ByteArray())
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.000", "2006-01-02 15:04:05"} {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC()
			}
		}
	}
	return time.Time{}
}
