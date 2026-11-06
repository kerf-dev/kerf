package notify

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

const (
	pollEvery = 5 * time.Second
	// A row of the audit log can become visible after a later one (concurrent transactions): the
	// leader re-reads this much of the past each time; Enqueue ignores events it already queued.
	overlap       = 2 * time.Minute
	eventBatch    = 500
	claimBatch    = 50
	leaseDuration = 2 * time.Minute
	minBackoff    = 30 * time.Second
	maxBackoff    = 15 * time.Minute
	// One `blocked` message per workload and reason per channel per day, one `failed` per hour:
	// the same de-duplication as the activity log, which the agent applies per run.
	blockedWindow = 24 * time.Hour
	failedWindow  = time.Hour
)

// Run is the leader's notification work: it queues notifications for new events, sends the queue,
// queues the weekly digest and prunes old deliveries. It returns when ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	var lastSlow time.Time
	for {
		if err := s.PollEvents(ctx); err != nil && ctx.Err() == nil {
			s.log().WarnContext(ctx, "notify: read events", "err", err)
		}
		s.SendDue(ctx)
		if now := s.now(); now.Sub(lastSlow) >= 30*time.Second {
			lastSlow = now
			if err := s.digestTick(ctx); err != nil && ctx.Err() == nil {
				s.log().WarnContext(ctx, "notify: weekly digest", "err", err)
			}
			if cs, err := s.Repo.ListChannels(ctx); err == nil {
				s.Metrics.setLastSuccess(cs)
			}
			if n, err := s.Repo.PruneDeliveries(ctx, now.Add(-Retention)); err != nil && ctx.Err() == nil {
				s.log().WarnContext(ctx, "notify: prune deliveries", "err", err)
			} else if n > 0 {
				s.log().InfoContext(ctx, "notify: old deliveries pruned", "deliveries", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// EventMessage builds the message of an audit event.
func (s *Service) eventMessage(e Event) Message {
	em := &EventMessage{
		Type: e.Type, Reason: e.Reason, Cluster: e.Cluster, Namespace: e.Namespace, Strategy: e.Strategy, Resources: e.Resources,
		Changes: ConvertChanges(e.Containers), Message: Redact(e.Message, 500), Details: FilterDetails(e.Details), Actor: e.Actor,
		FromState: e.FromState, ToState: e.ToState, Scope: e.Scope,
	}
	switch {
	case e.Name != "":
		em.Workload = &WorkloadRef{Kind: e.Kind, Name: e.Name}
		em.Link = s.Links.Workload(e.Cluster, e.Namespace, e.Kind, e.Name)
	case e.Cluster != "":
		em.Link = s.Links.Cluster(e.Cluster)
	default:
		em.Link = s.Links.Overview()
	}
	return Message{ID: e.ID, Kind: KindEvent, Time: e.Time.UTC(), Event: em}
}

func dedupe(e Event) (key string, window time.Duration) {
	switch e.Type {
	case hubapi.NotifyBlocked:
		return fmt.Sprintf("blocked/%s/%s/%s/%s/%s", e.Cluster, e.Namespace, e.Kind, e.Name, e.Reason), blockedWindow
	case hubapi.NotifyFailed:
		return fmt.Sprintf("failed/%s/%s/%s/%s/%s", e.Cluster, e.Namespace, e.Kind, e.Name, e.Reason), failedWindow
	}
	return "", 0
}

// PollEvents queues deliveries for the audit events stored since the last call. The first call only
// sets the starting point: nothing is sent for what happened before notifications ran.
func (s *Service) PollEvents(ctx context.Context) error {
	now := s.now()
	cur, floor, ok, err := s.Repo.Cursor(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return s.Repo.ResetCursor(ctx)
	}
	rules, err := s.Repo.ListRules(ctx)
	if err != nil {
		return err
	}
	rules = slices.DeleteFunc(rules, func(r Rule) bool { return !r.Enabled })
	if len(rules) == 0 {
		// Nothing to match: just move on, so a rule added later does not replay the past.
		return s.Repo.ResetCursor(ctx)
	}
	channels, err := s.Repo.ListChannels(ctx)
	if err != nil {
		return err
	}
	enabled := map[string]Channel{}
	for _, c := range channels {
		if c.Enabled {
			enabled[c.ID] = c
		}
	}
	since, newest := cur.Add(-overlap), cur
	if since.Before(floor) {
		since = floor
	}
	for {
		evs, err := s.Repo.EventsSince(ctx, since, eventBatch)
		if err != nil {
			return err
		}
		for _, e := range evs {
			if err := s.queueEvent(ctx, e, rules, enabled, now); err != nil {
				return err
			}
			if e.ReceivedAt.After(newest) {
				newest = e.ReceivedAt
			}
		}
		if len(evs) < eventBatch || !evs[len(evs)-1].ReceivedAt.After(since) {
			break
		}
		since = evs[len(evs)-1].ReceivedAt
	}
	if newest.After(cur) {
		return s.Repo.SetCursor(ctx, newest)
	}
	return nil
}

// queueEvent queues one delivery per channel the matching rules name.
func (s *Service) queueEvent(ctx context.Context, e Event, rules []Rule, channels map[string]Channel, now time.Time) error {
	var targets []string
	for _, r := range rules {
		if !r.Matches(e) {
			continue
		}
		for _, id := range r.ChannelIDs {
			if _, ok := channels[id]; ok && !slices.Contains(targets, id) {
				targets = append(targets, id)
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	m := s.eventMessage(e)
	key, window := dedupe(e)
	for _, id := range targets {
		ok, err := s.Repo.Enqueue(ctx, Delivery{ID: uuid.NewString(), ChannelID: id, Kind: KindEvent, EventID: e.ID, EventType: e.Type, DedupeKey: key,
			Summary: m.Title(), Payload: m, CreatedAt: now}, window)
		if err != nil {
			return err
		}
		if ok {
			s.log().InfoContext(ctx, "notify: queued", "event", e.ID, "type", e.Type, "channel", channels[id].Name)
		}
	}
	return nil
}

// SendDue sends the deliveries that are due, the channels in parallel and each channel's messages in
// order.
func (s *Service) SendDue(ctx context.Context) {
	due, err := s.Repo.Claim(ctx, s.now(), claimBatch, leaseDuration)
	if err != nil {
		if ctx.Err() == nil {
			s.log().WarnContext(ctx, "notify: claim deliveries", "err", err)
		}
		return
	}
	by := map[string][]Delivery{}
	for _, d := range due {
		by[d.ChannelID] = append(by[d.ChannelID], d)
	}
	var wg sync.WaitGroup
	for id, ds := range by {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.sendChannel(ctx, id, ds)
		}()
	}
	wg.Wait()
}

func (s *Service) sendChannel(ctx context.Context, id string, ds []Delivery) {
	c, err := s.Repo.GetChannel(ctx, id)
	if err != nil {
		// Deleted while queued (Repo.DeleteChannel drops the queue, this is the race).
		for _, d := range ds {
			s.settle(ctx, d, hubapi.DeliveryFailed, d.Attempts, s.now(), "channel deleted")
		}
		return
	}
	sec, err := s.open(c)
	for i, d := range ds {
		if ctx.Err() != nil {
			return
		}
		now := s.now()
		switch {
		case err != nil:
			// The key is missing or changed: no retry helps until someone fixes it.
			s.fail(ctx, c, d, "cannot decrypt the channel's URL (secrets key missing or changed); save the channel again")
			continue
		case !c.Enabled && d.Kind != KindTest:
			s.settle(ctx, d, hubapi.DeliveryFailed, d.Attempts, now, "channel disabled")
			continue
		}
		if wait := s.limiter().reserve(c.ID, now); wait > 0 {
			// Over the channel's rate limit: later, without counting an attempt.
			s.settleAt(ctx, d, hubapi.DeliveryPending, d.Attempts, now, now.Add(wait), "")
			for _, rest := range ds[i+1:] {
				s.settleAt(ctx, rest, hubapi.DeliveryPending, rest.Attempts, now, now.Add(wait), "")
			}
			return
		}
		req, rerr := Render(c.Type, d.Payload, sec.Secret, now)
		if rerr != nil {
			s.fail(ctx, c, d, rerr.Error())
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		res := s.sender().Send(sctx, sec.URL, req)
		cancel()
		if res.OK {
			s.Metrics.count(c.Type, true, false)
			s.settle(ctx, d, hubapi.DeliverySent, d.Attempts+1, now, "", res.HTTPStatus)
			if err := s.Repo.RecordResult(ctx, c.ID, hubapi.LastDeliverySent, now, ""); err != nil {
				s.log().WarnContext(ctx, "notify: record result", "channel", c.Name, "err", err)
			}
			continue
		}
		d.Attempts++
		s.failedAttempt(ctx, c, d, res, now)
	}
}

// backoff is the wait before attempt n+1 after n failed attempts: 30 s, 1 min, 2 min, ... at most 15 min.
func backoff(attempts int) time.Duration {
	d := minBackoff
	for i := 1; i < attempts && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

// failedAttempt records a failed attempt (d.Attempts already counts it): retry with backoff while the
// retry window of the delivery is open, otherwise mark it failed.
func (s *Service) failedAttempt(ctx context.Context, c Channel, d Delivery, res Result, now time.Time) {
	deadline := d.CreatedAt.Add(RetryWindow)
	wait := backoff(d.Attempts)
	if res.RetryAfter > wait {
		wait = res.RetryAfter
	}
	next := now.Add(wait)
	if next.After(deadline) {
		next = deadline
	}
	if !res.Retryable || !now.Before(deadline) {
		s.Metrics.count(c.Type, false, false)
		s.log().WarnContext(ctx, "notify: delivery failed", "channel", c.Name, "delivery", d.ID, "attempts", d.Attempts, "err", res.Err)
		s.settleRes(ctx, d, hubapi.DeliveryFailed, d.Attempts, now, now, res.Err, res.HTTPStatus)
		s.record(ctx, c, hubapi.LastDeliveryFailed, now, res.Err)
		return
	}
	s.Metrics.count(c.Type, false, true)
	s.log().WarnContext(ctx, "notify: delivery failed, will retry", "channel", c.Name, "delivery", d.ID, "attempts", d.Attempts, "retryIn", next.Sub(now).Round(time.Second), "err", res.Err)
	s.settleRes(ctx, d, hubapi.DeliveryPending, d.Attempts, now, next, res.Err, res.HTTPStatus)
	s.record(ctx, c, hubapi.LastDeliveryRetrying, now, res.Err)
}

func (s *Service) fail(ctx context.Context, c Channel, d Delivery, msg string) {
	now := s.now()
	d.Attempts++
	s.failedAttempt(ctx, c, d, Result{Err: msg}, now)
}

func (s *Service) record(ctx context.Context, c Channel, status string, at time.Time, errText string) {
	if err := s.Repo.RecordResult(ctx, c.ID, status, at, errText); err != nil {
		s.log().WarnContext(ctx, "notify: record result", "channel", c.Name, "err", err)
	}
}

func (s *Service) settle(ctx context.Context, d Delivery, status string, attempts int, at time.Time, errText string, httpStatus ...int) {
	hs := 0
	if len(httpStatus) > 0 {
		hs = httpStatus[0]
	}
	s.settleRes(ctx, d, status, attempts, at, at, errText, hs)
}

func (s *Service) settleAt(ctx context.Context, d Delivery, status string, attempts int, at, next time.Time, errText string) {
	s.settleRes(ctx, d, status, attempts, at, next, errText, 0)
}

func (s *Service) settleRes(ctx context.Context, d Delivery, status string, attempts int, at, next time.Time, errText string, httpStatus int) {
	if err := s.Repo.Settle(ctx, d.ID, status, attempts, at, next, errText, httpStatus); err != nil {
		s.log().WarnContext(ctx, "notify: store delivery", "delivery", d.ID, "err", err)
	}
}

// limiter returns the service's rate limiter (created on first use from RateLimit).
func (s *Service) limiter() *limiter {
	s.limOnce.Do(func() { s.lim = newLimiter(s.rate()) })
	return s.lim
}

// limiter is a per-channel token bucket: perMinute messages per minute, with a burst of the same.
type limiter struct {
	mu        sync.Mutex
	perMinute float64
	buckets   map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(perMinute int) *limiter {
	return &limiter{perMinute: float64(perMinute), buckets: map[string]*bucket{}}
}

// reserve takes a token of the channel's bucket; it returns 0, or how long to wait for one.
func (l *limiter) reserve(channel string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[channel]
	if b == nil {
		b = &bucket{tokens: l.perMinute, at: now}
		l.buckets[channel] = b
	}
	b.tokens = min(l.perMinute, b.tokens+now.Sub(b.at).Seconds()*l.perMinute/60)
	b.at = now
	if b.tokens >= 1 {
		b.tokens--
		return 0
	}
	return max(time.Second, time.Duration((1-b.tokens)/l.perMinute*60*float64(time.Second)))
}
