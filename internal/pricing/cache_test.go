package pricing

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCached(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	m6i := Key{Region: "eu-west-1", Zone: "eu-west-1a", InstanceType: "m6i.xlarge", CapacityType: OnDemand}
	f := &fakeProvider{prices: map[Key]Price{m6i.normalized(): {0.214, Shape{4, 16}}}, errs: map[Key]error{}}
	c := NewCached(f, 24*time.Hour, nil)
	c.Now = func() time.Time { return now }
	ctx := context.Background()
	get := func(k Key) (Price, error) { return c.Price(ctx, k) }

	if c.Name() != "fake" {
		t.Fatalf("name %q", c.Name())
	}
	// Zone is ignored for on-demand: both lookups hit one entry.
	if _, err := get(m6i); err != nil {
		t.Fatal(err)
	}
	other := m6i
	other.Zone = "eu-west-1b"
	if _, err := get(other); err != nil {
		t.Fatal(err)
	}
	if n := f.calls[m6i.normalized()]; n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}

	// After the TTL a transient error serves the stale price.
	now = now.Add(25 * time.Hour)
	f.errs[m6i.normalized()] = errors.New("throttled")
	if p, err := get(m6i); err != nil || p.HourlyUSD != 0.214 {
		t.Fatalf("stale: %+v %v", p, err)
	}
	// Once AWS answers again, the new price replaces it.
	delete(f.errs, m6i.normalized())
	f.prices[m6i.normalized()] = Price{0.22, Shape{4, 16}}
	if p, _ := get(m6i); p.HourlyUSD != 0.22 {
		t.Fatalf("refreshed price %v", p.HourlyUSD)
	}

	// Not found is cached for NegativeTTL only.
	unknown := Key{Region: "eu-west-1", InstanceType: "x9.large", CapacityType: OnDemand}
	for range 2 {
		if _, err := get(unknown); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	}
	if n := f.calls[unknown]; n != 1 {
		t.Fatalf("negative lookups = %d, want 1", n)
	}
	now = now.Add(2 * time.Hour)
	_, _ = get(unknown)
	if n := f.calls[unknown]; n != 2 {
		t.Fatalf("negative lookups after NegativeTTL = %d, want 2", n)
	}

	// A transient error with nothing cached is returned as is and not cached.
	flaky := Key{Region: "eu-west-1", InstanceType: "c6a.2xlarge", CapacityType: OnDemand}
	f.errs[flaky] = errors.New("boom")
	if _, err := get(flaky); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	_, _ = get(flaky)
	if n := f.calls[flaky]; n != 2 {
		t.Fatalf("transient errors cached: calls = %d", n)
	}
}
