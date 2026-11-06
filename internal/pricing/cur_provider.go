package pricing

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Defaults of the CUR provider.
const (
	DefaultCURWindow   = 30 * 24 * time.Hour
	DefaultCURInterval = 24 * time.Hour
	// DefaultCURMaxAge is how long the last rates are served while the report cannot be read.
	DefaultCURMaxAge = 72 * time.Hour
	// CURRetryInterval is the wait after a failed read.
	CURRetryInterval = 15 * time.Minute
)

// CUR prices a key at its effective rate from the Cost and Usage Report when the report has usage for
// it, and through Fallback (the list or static prices) otherwise. Rates are read by Run (daily by
// default) and kept in memory; only the aggregated rates are kept. When the report cannot be read,
// the last rates are served for MaxAge and then every key falls back.
type CUR struct {
	Reader   CURReader
	Fallback Provider
	Window   time.Duration
	MaxAge   time.Duration
	Log      *slog.Logger
	Now      func() time.Time

	mu      sync.RWMutex
	rates   *CURRates
	readAt  time.Time
	lastErr error

	refreshes   *prometheus.CounterVec
	lastSuccess prometheus.Gauge
	lastOK      prometheus.Gauge
	rateCount   prometheus.Gauge
	periodStart prometheus.Gauge
	periodEnd   prometheus.Gauge
}

// NewCUR wires the provider. Register it with Register.
func NewCUR(r CURReader, fallback Provider, log *slog.Logger) *CUR {
	if log == nil {
		log = slog.Default()
	}
	return &CUR{
		Reader: r, Fallback: fallback, Window: DefaultCURWindow, MaxAge: DefaultCURMaxAge, Log: log,
		refreshes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kerf_pricing_cur_refreshes_total", Help: "Cost and Usage Report reads by result (success|error).",
		}, []string{"result"}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kerf_pricing_cur_last_success_timestamp_seconds", Help: "Unix time of the last successful Cost and Usage Report read (0 = none yet).",
		}),
		lastOK: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kerf_pricing_cur_last_read_ok", Help: "1 if the last Cost and Usage Report read succeeded, 0 if it failed or none ran yet.",
		}),
		rateCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kerf_pricing_cur_rates", Help: "Instance type, region and capacity type keys with an effective rate in use.",
		}),
		periodStart: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kerf_pricing_cur_period_start_timestamp_seconds", Help: "Start of the usage the effective rates in use were computed from.",
		}),
		periodEnd: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "kerf_pricing_cur_period_end_timestamp_seconds", Help: "End of the usage the effective rates in use were computed from.",
		}),
	}
}

// Register registers the kerf_pricing_cur_* series (on the ops registry).
func (c *CUR) Register(reg prometheus.Registerer) error {
	for _, m := range []prometheus.Collector{c.refreshes, c.lastSuccess, c.lastOK, c.rateCount, c.periodStart, c.periodEnd} {
		if err := reg.Register(m); err != nil {
			return err
		}
	}
	return nil
}

func (c *CUR) Name() string { return c.Fallback.Name() }

func (c *CUR) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// current returns the rates in use: nil before the first read and after MaxAge without a read.
func (c *CUR) current() *CURRates {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.rates == nil || (c.MaxAge > 0 && c.now().Sub(c.readAt) > c.MaxAge) {
		return nil
	}
	return c.rates
}

func (c *CUR) Price(ctx context.Context, k Key) (Price, error) {
	fb, fbErr := c.Fallback.Price(ctx, k)
	rates := c.current()
	if rates == nil {
		return fb, fbErr
	}
	r, ok := rates.Rates[Key{Region: k.Region, InstanceType: k.InstanceType, CapacityType: k.CapacityType}]
	if !ok {
		return fb, fbErr
	}
	// The shape comes from the list price when there is one; the rate does not need it.
	return Price{HourlyUSD: r.HourlyUSD, Shape: fb.Shape, Source: SourceCUR, PeriodStart: rates.Start, PeriodEnd: rates.End}, nil
}

// Status is the state of the report reads, for logs and tests.
type CURStatus struct {
	Rates       int
	Start, End  time.Time
	LastSuccess time.Time
	LastError   error
	InUse       bool
}

// Status returns the state of the last reads.
func (c *CUR) Status() CURStatus {
	inUse := c.current()
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := CURStatus{LastSuccess: c.readAt, LastError: c.lastErr, InUse: inUse != nil}
	if inUse != nil {
		s.Rates, s.Start, s.End = len(inUse.Rates), inUse.Start, inUse.End
	}
	return s
}

// Refresh reads the report once.
func (c *CUR) Refresh(ctx context.Context) error {
	now := c.now()
	window := c.Window
	if window <= 0 {
		window = DefaultCURWindow
	}
	start := time.Now()
	groups, err := c.Reader.Groups(ctx, now.Add(-window), now)
	if err == nil && len(groups) == 0 {
		err = errEmptyCUR
	}
	if err != nil {
		c.mu.Lock()
		c.lastErr = err
		c.mu.Unlock()
		c.refreshes.WithLabelValues("error").Inc()
		c.lastOK.Set(0)
		c.updateGauges()
		return err
	}
	rates := ComputeCURRates(groups)
	c.mu.Lock()
	c.rates, c.readAt, c.lastErr = &rates, now, nil
	c.mu.Unlock()
	c.refreshes.WithLabelValues("success").Inc()
	c.lastOK.Set(1)
	c.lastSuccess.Set(float64(now.Unix()))
	c.updateGauges()
	c.Log.InfoContext(ctx, "cost and usage report read", "reader", c.Reader.Name(), "groups", len(groups), "rates", len(rates.Rates),
		"period_start", rates.Start.Format(time.RFC3339), "period_end", rates.End.Format(time.RFC3339), "took", time.Since(start).Round(time.Millisecond).String())
	return nil
}

var errEmptyCUR = errors.New("the cost and usage report has no EC2 instance usage in the window")

// updateGauges sets the rate gauges to the rates in use (0 when every key falls back).
func (c *CUR) updateGauges() {
	r := c.current()
	if r == nil {
		c.rateCount.Set(0)
		c.periodStart.Set(0)
		c.periodEnd.Set(0)
		return
	}
	c.rateCount.Set(float64(len(r.Rates)))
	c.periodStart.Set(float64(r.Start.Unix()))
	c.periodEnd.Set(float64(r.End.Unix()))
}

// Run reads the report now and then every interval until ctx is done; after a failed read it retries
// after CURRetryInterval.
func (c *CUR) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultCURInterval
	}
	for {
		wait := interval
		if err := c.Refresh(ctx); err != nil && ctx.Err() == nil {
			st := c.Status()
			if st.InUse {
				c.Log.WarnContext(ctx, "cannot read the cost and usage report; keeping the last effective rates", "reader", c.Reader.Name(), "last_success", st.LastSuccess.Format(time.RFC3339), "err", err)
			} else {
				c.Log.ErrorContext(ctx, "cannot read the cost and usage report; using list prices", "reader", c.Reader.Name(), "err", err)
			}
			wait = min(interval, CURRetryInterval)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			c.updateGauges() // MaxAge may have expired the rates
		}
	}
}
