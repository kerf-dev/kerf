package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/realized"
)

type fakeSummarizer struct {
	since   *time.Time
	byFrom  map[time.Time]realized.Amount
	err     error
	calls   []realized.Scope
	buckets []string
}

func (f *fakeSummarizer) Summary(_ context.Context, sc realized.Scope, from, _ time.Time, bucket string) (*realized.Summary, error) {
	f.calls = append(f.calls, sc)
	f.buckets = append(f.buckets, bucket)
	if f.err != nil {
		return nil, f.err
	}
	return &realized.Summary{Since: f.since, InRange: f.byFrom[from]}, nil
}

func TestRealizedSourceForDigest(t *testing.T) {
	end := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	start := end.Add(-7 * 24 * time.Hour)
	prevStart := start.Add(-7 * 24 * time.Hour)
	ago := func(d time.Duration) *time.Time { t := end.Add(-d); return &t }
	week := 7 * 24 * time.Hour

	tests := []struct {
		name      string
		sum       *fakeSummarizer
		wantNil   bool
		wantErr   bool
		wantUSD   float64
		wantPrev  *float64
		wantSince *time.Time
	}{
		{
			name: "savings since before this week: this week and the week before",
			sum: &fakeSummarizer{since: ago(5 * week), byFrom: map[time.Time]realized.Amount{
				start: {CPU: 200, Memory: 110}, prevStart: {CPU: 150, Memory: 130},
			}},
			wantUSD: 310, wantPrev: ptr(280.0), wantSince: ago(5 * week),
		},
		{
			name: "first savings inside this week: no week before to compare",
			sum: &fakeSummarizer{since: ago(2 * 24 * time.Hour), byFrom: map[time.Time]realized.Amount{
				start: {CPU: 12, Memory: 3},
			}},
			wantUSD: 15, wantSince: ago(2 * 24 * time.Hour),
		},
		{name: "nothing released yet: no realized line", sum: &fakeSummarizer{}, wantNil: true},
		{name: "a failing source is an error for the caller to log", sum: &fakeSummarizer{err: errors.New("boom")}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (&realizedSource{sum: tc.sum}).RealizedSavings(context.Background(), start, end)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error: %v", err, tc.wantErr)
			}
			if tc.wantNil || tc.wantErr {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got.PeriodUSD != tc.wantUSD {
				t.Errorf("PeriodUSD = %v, want %v", got.PeriodUSD, tc.wantUSD)
			}
			switch {
			case tc.wantPrev == nil && got.PreviousPeriodUSD != nil:
				t.Errorf("PreviousPeriodUSD = %v, want none", *got.PreviousPeriodUSD)
			case tc.wantPrev != nil && (got.PreviousPeriodUSD == nil || *got.PreviousPeriodUSD != *tc.wantPrev):
				t.Errorf("PreviousPeriodUSD = %v, want %v", got.PreviousPeriodUSD, *tc.wantPrev)
			}
			if got.Since == nil || !got.Since.Equal(*tc.wantSince) {
				t.Errorf("Since = %v, want %v", got.Since, tc.wantSince)
			}
			for i, sc := range tc.sum.calls {
				if len(sc.Clusters) != 0 || len(sc.Namespaces) != 0 || sc.Workload != nil {
					t.Errorf("call %d scope = %+v, want the whole fleet", i, sc)
				}
				if tc.sum.buckets[i] != realized.BucketDay {
					t.Errorf("call %d bucket = %q, want day", i, tc.sum.buckets[i])
				}
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
