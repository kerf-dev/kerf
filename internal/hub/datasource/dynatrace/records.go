package dynatrace

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kerf-dev/kerf/internal/recommender"
)

// str returns a string field of a record ("" when absent or not a string).
func str(rec map[string]json.RawMessage, field string) string {
	raw, ok := rec[field]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// num returns a numeric field of a record; ok is false when absent or null. The query API may
// send longs as strings: both forms are read.
func num(rec map[string]json.RawMessage, field string) (float64, bool) {
	raw, ok := rec[field]
	if !ok {
		return 0, false
	}
	return parseNum(raw)
}

func parseNum(raw json.RawMessage) (float64, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return 0, false
	}
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		p, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		f = p
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// series reads a timeseries field (array of numbers or nulls) with the record's timeframe and
// interval. Each sample is stamped with the end of its bucket, like a Prometheus range query
// whose sample at t covers the step before t; null buckets are left out. scale converts the unit.
func series(rec map[string]json.RawMessage, field string, scale float64) ([]recommender.Sample, error) {
	raw, ok := rec[field]
	if !ok {
		return nil, nil
	}
	var vals []json.RawMessage
	if err := json.Unmarshal(raw, &vals); err != nil {
		return nil, fmt.Errorf("field %s: not a timeseries: %w", field, err)
	}
	start, step, err := frame(rec)
	if err != nil {
		return nil, err
	}
	out := make([]recommender.Sample, 0, len(vals))
	for i, raw := range vals {
		v, ok := parseNum(raw)
		if !ok {
			continue
		}
		out = append(out, recommender.Sample{T: start.Add(time.Duration(i+1) * step), V: v * scale})
	}
	return out, nil
}

// frame returns the start of the first bucket and the bucket size of a timeseries record.
func frame(rec map[string]json.RawMessage) (time.Time, time.Duration, error) {
	var tf struct {
		Start string `json:"start"`
	}
	if err := json.Unmarshal(rec["timeframe"], &tf); err != nil || tf.Start == "" {
		return time.Time{}, 0, fmt.Errorf("timeseries record without timeframe")
	}
	start, err := time.Parse(time.RFC3339Nano, tf.Start)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("timeframe start %q: %w", tf.Start, err)
	}
	step, err := interval(rec["interval"])
	if err != nil {
		return time.Time{}, 0, err
	}
	return start.UTC(), step, nil
}

// interval parses the interval of a timeseries record: nanoseconds as a string or number.
func interval(raw json.RawMessage) (time.Duration, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("timeseries interval %q", s)
		}
		return time.Duration(n), nil
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("timeseries record without interval")
	}
	return time.Duration(n), nil
}

// Unit kinds of the mapped metrics.
type unitKind int

const (
	unitCPU unitKind = iota
	unitBytes
	unitRatio
	unitCount
)

// scale returns the factor that converts a value in unit to Kerf's unit of the kind: cores,
// bytes, a 0..1 ratio, or a plain count. known is false when the unit was not recognised (the
// default is then used: millicores for CPU, bytes, percent for ratios).
func scale(kind unitKind, unit string) (factor float64, known bool) {
	u := strings.ToLower(strings.TrimSpace(unit))
	u = strings.NewReplacer(" ", "", "_", "", "-", "").Replace(u)
	switch kind {
	case unitCPU:
		switch u {
		case "millicore", "millicores", "mcpu", "millicpu", "m":
			return 1e-3, true
		case "core", "cores", "cpu", "cpus", "vcpu":
			return 1, true
		case "microcore", "microcores":
			return 1e-6, true
		case "nanocore", "nanocores":
			return 1e-9, true
		}
		return 1e-3, false
	case unitBytes:
		switch u {
		case "byte", "bytes", "b":
			return 1, true
		case "kibibyte", "kibibytes", "kib":
			return 1 << 10, true
		case "mebibyte", "mebibytes", "mib":
			return 1 << 20, true
		case "gibibyte", "gibibytes", "gib":
			return 1 << 30, true
		case "kilobyte", "kilobytes", "kb":
			return 1e3, true
		case "megabyte", "megabytes", "mb":
			return 1e6, true
		case "gigabyte", "gigabytes", "gb":
			return 1e9, true
		}
		return 1, false
	case unitRatio:
		switch u {
		case "%", "percent", "percents":
			return 0.01, true
		case "ratio", "fraction", "unspecified", "unitless", "":
			if u == "" {
				return 0.01, false
			}
			return 1, true
		}
		return 0.01, false
	}
	return 1, true
}
