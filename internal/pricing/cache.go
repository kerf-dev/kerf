package pricing

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Cached wraps a Provider with a TTL cache. Prices are refreshed after TTL (daily by default); if a
// refresh fails with a transient error, the last known price is served until it succeeds.
// Not-found results are cached for NegativeTTL so unknown instance types are not re-queried on every
// refresh.
type Cached struct {
	Provider    Provider
	TTL         time.Duration
	NegativeTTL time.Duration
	Log         *slog.Logger
	Now         func() time.Time

	mu      sync.Mutex
	entries map[Key]cacheEntry
}

type cacheEntry struct {
	price     Price
	err       error // ErrNotFound-wrapped, for negative entries
	fetchedAt time.Time
}

// NewCached returns a cache with the given TTL and a negative TTL of min(TTL, 1h).
func NewCached(p Provider, ttl time.Duration, log *slog.Logger) *Cached {
	return &Cached{Provider: p, TTL: ttl, NegativeTTL: min(ttl, time.Hour), Log: log}
}

func (c *Cached) Name() string { return c.Provider.Name() }

func (c *Cached) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cached) Price(ctx context.Context, k Key) (Price, error) {
	k = k.normalized()
	now := c.now()
	c.mu.Lock()
	e, ok := c.entries[k]
	c.mu.Unlock()
	if ok {
		ttl := c.TTL
		if e.err != nil {
			ttl = c.NegativeTTL
		}
		if now.Sub(e.fetchedAt) < ttl {
			return e.price, e.err
		}
	}

	p, err := c.Provider.Price(ctx, k)
	switch {
	case err == nil:
		c.store(k, cacheEntry{price: p, fetchedAt: now})
		return p, nil
	case errors.Is(err, ErrNotFound):
		c.store(k, cacheEntry{err: err, fetchedAt: now})
		return Price{}, err
	case ok && e.err == nil:
		if c.Log != nil {
			c.Log.WarnContext(ctx, "price refresh failed, serving stale price", "key", k.String(), "age", now.Sub(e.fetchedAt).String(), "err", err)
		}
		return e.price, nil
	default:
		return Price{}, err
	}
}

func (c *Cached) store(k Key, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[Key]cacheEntry{}
	}
	c.entries[k] = e
}
