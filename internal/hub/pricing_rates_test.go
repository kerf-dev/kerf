package hub

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// ratesProm answers the two pricing-rate queries.
type ratesProm struct {
	bySource, cur []recommender.VectorSample
	err, curErr   error
}

func (p *ratesProm) Instant(_ context.Context, q string, _ time.Time) ([]recommender.VectorSample, error) {
	switch q {
	case pricingRatesBySource:
		return p.bySource, p.err
	case pricingCURStatus:
		return p.cur, p.curErr
	}
	return nil, errors.New("unexpected query " + q)
}

func (p *ratesProm) Range(context.Context, string, time.Time, time.Time, time.Duration) ([]recommender.Series, error) {
	return nil, errors.New("no range queries")
}

func TestPricingRates(t *testing.T) {
	sample := func(v float64, kv ...string) recommender.VectorSample {
		l := recommender.Labels{}
		for i := 0; i < len(kv); i += 2 {
			l[kv[i]] = kv[i+1]
		}
		return recommender.VectorSample{Labels: l, Sample: recommender.Sample{V: v}}
	}
	start, end := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	success := time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)
	bySource := []recommender.VectorSample{
		sample(4, "cluster", "prod", "source", "cur"), sample(1, "cluster", "prod", "source", "list"),
		sample(3, "cluster", "dev", "source", "static"),
	}
	clusters := []hubapi.ClusterPricingRates{
		{Cluster: "dev", Sources: []hubapi.PricingRateSource{{Source: "static", Nodes: 3}}},
		{Cluster: "prod", Sources: []hubapi.PricingRateSource{{Source: "cur", Nodes: 4}, {Source: "list", Nodes: 1}}},
	}
	curSeries := func(ok float64, rates float64) []recommender.VectorSample {
		return []recommender.VectorSample{
			sample(ok, "__name__", "kerf_pricing_cur_last_read_ok"),
			sample(float64(success.Unix()), "__name__", "kerf_pricing_cur_last_success_timestamp_seconds"),
			sample(rates, "__name__", "kerf_pricing_cur_rates"),
			sample(float64(start.Unix()), "__name__", "kerf_pricing_cur_period_start_timestamp_seconds"),
			sample(float64(end.Unix()), "__name__", "kerf_pricing_cur_period_end_timestamp_seconds"),
		}
	}
	tests := []struct {
		name string
		prom *ratesProm
		want *hubapi.PricingRates
	}{
		{
			name: "cur off",
			prom: &ratesProm{bySource: bySource},
			want: &hubapi.PricingRates{Clusters: clusters},
		},
		{
			name: "cur in use",
			prom: &ratesProm{bySource: bySource, cur: curSeries(1, 12)},
			want: &hubapi.PricingRates{Clusters: clusters, CUR: &hubapi.PricingCURStatus{LastReadOK: true, LastSuccess: &success, Rates: 12,
				Period: &hubapi.PricingPeriod{Start: start, End: end}}},
		},
		{
			name: "cur failing after max age: no rates, no period",
			prom: &ratesProm{bySource: bySource, cur: curSeries(0, 0)},
			want: &hubapi.PricingRates{Clusters: clusters, CUR: &hubapi.PricingCURStatus{LastReadOK: false, LastSuccess: &success}},
		},
		{
			name: "cur status query fails",
			prom: &ratesProm{bySource: bySource, curErr: errors.New("timeout")},
			want: &hubapi.PricingRates{Clusters: clusters},
		},
		{
			name: "store down",
			prom: &ratesProm{err: errors.New("connection refused")},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{Prom: tt.prom, Log: discardLog()}
			if got := s.pricingRates(context.Background()); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
