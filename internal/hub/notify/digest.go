package notify

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// DigestSource provides the parts of the weekly digest that come from the hub's recommendations.
// The hub implements it (internal/hub/notifications.go).
type DigestSource interface {
	// EstimatedSavings returns the fleet's estimated monthly savings now and as of previous (ok is
	// false when there is no sample from that long ago).
	EstimatedSavings(ctx context.Context, now, previous time.Time) (current, before float64, ok bool, err error)
	// TopOpportunities returns the n workloads with the largest estimated monthly saving.
	TopOpportunities(ctx context.Context, n int) ([]Opportunity, error)
	// Attention returns the clusters that need a look: readiness below AutomationReady, a silent
	// agent, paused automation.
	Attention(ctx context.Context) ([]Attention, error)
}

// RealizedSavingsSource is the hook for realized savings: how much the applied changes actually
// saved in a period. It is optional (the Service field may be nil, and a source may return nil when
// it has nothing for the period); the digest then simply has no realized-savings line. A source
// plugs in with its own implementation, no change to this package is needed.
type RealizedSavingsSource interface {
	RealizedSavings(ctx context.Context, from, to time.Time) (*RealizedSavings, error)
}

// DigestPeriod is the length of a digest's period.
const DigestPeriod = 7 * 24 * time.Hour

// BuildDigest builds the digest of the week that ends at end.
func (s *Service) BuildDigest(ctx context.Context, end time.Time) (Message, error) {
	if s.Digest == nil {
		return Message{}, errors.New("no digest source")
	}
	start := end.Add(-DigestPeriod)
	d := &DigestMessage{PeriodStart: start, PeriodEnd: end, Link: s.Links.Overview(), TopOpportunities: []Opportunity{}, Attention: []Attention{}}
	cur, before, ok, err := s.Digest.EstimatedSavings(ctx, end, start)
	if err != nil {
		return Message{}, fmt.Errorf("estimated savings: %w", err)
	}
	d.EstimatedSavings = EstimatedSavings{CurrentUSD: cur}
	if ok {
		change := cur - before
		d.EstimatedSavings.PreviousUSD, d.EstimatedSavings.ChangeUSD = &before, &change
	}
	if s.Realized != nil {
		r, err := s.Realized.RealizedSavings(ctx, start, end)
		if err != nil {
			// The hook is a nicety: a failure there does not hold back the digest.
			s.log().WarnContext(ctx, "notify: realized savings for the digest", "err", err)
		} else {
			d.RealizedSavings = r
		}
	}
	if d.TopOpportunities, err = s.Digest.TopOpportunities(ctx, 5); err != nil {
		return Message{}, fmt.Errorf("top opportunities: %w", err)
	}
	for i := range d.TopOpportunities {
		o := &d.TopOpportunities[i]
		o.Link = s.Links.Workload(o.Cluster, o.Namespace, o.Kind, o.Name)
	}
	if d.Changes, err = s.Repo.ChangeCounts(ctx, start, end); err != nil {
		return Message{}, fmt.Errorf("changes: %w", err)
	}
	if d.Attention, err = s.Digest.Attention(ctx); err != nil {
		return Message{}, fmt.Errorf("clusters needing attention: %w", err)
	}
	for i := range d.Attention {
		d.Attention[i].Link = s.Links.Cluster(d.Attention[i].Cluster)
	}
	if d.TopOpportunities == nil {
		d.TopOpportunities = []Opportunity{}
	}
	if d.Attention == nil {
		d.Attention = []Attention{}
	}
	return Message{ID: uuid.NewString(), Kind: KindDigest, Time: end, Digest: d}, nil
}

var timeOfDayRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

// ValidateSchedule checks a digest's day, time and time zone.
func ValidateSchedule(weekday int, timeOfDay, tz string) []string {
	var p []string
	if weekday < 0 || weekday > 6 {
		p = append(p, "weekday must be 0 (Sunday) to 6 (Saturday)")
	}
	if !timeOfDayRE.MatchString(timeOfDay) {
		p = append(p, "time must be HH:MM (24 hours)")
	}
	if _, err := time.LoadLocation(tz); err != nil || tz == "" || strings.EqualFold(tz, "local") {
		p = append(p, "timezone must be an IANA time zone, e.g. Europe/Berlin")
	}
	return p
}

// scheduleOn returns the digest's scheduled instant on the local calendar day of t.
func scheduleOn(d DigestSettings, loc *time.Location, day time.Time) time.Time {
	var h, m int
	fmt.Sscanf(d.TimeOfDay, "%d:%d", &h, &m) //nolint:errcheck // validated on write
	y, mo, dd := day.In(loc).Date()
	return time.Date(y, mo, dd, h, m, 0, 0, loc).UTC()
}

// NextSend returns the first scheduled digest time after t.
func NextSend(d DigestSettings, t time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	for i := 0; i <= 8; i++ {
		day := t.In(loc).AddDate(0, 0, i)
		if int(day.Weekday()) != d.Weekday {
			continue
		}
		if at := scheduleOn(d, loc, day); at.After(t) {
			return at, nil
		}
	}
	return time.Time{}, errors.New("no schedule found")
}

// LastScheduled returns the latest scheduled digest time at or before t.
func LastScheduled(d DigestSettings, t time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(d.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	for i := 0; i <= 8; i++ {
		day := t.In(loc).AddDate(0, 0, -i)
		if int(day.Weekday()) != d.Weekday {
			continue
		}
		if at := scheduleOn(d, loc, day); !at.After(t) {
			return at, nil
		}
	}
	return time.Time{}, errors.New("no schedule found")
}

// DigestAPI converts the stored settings to their API form.
func DigestAPI(d DigestSettings, now time.Time) hubapi.NotificationDigest {
	out := hubapi.NotificationDigest{Enabled: d.Enabled, Weekday: d.Weekday, Time: d.TimeOfDay, Timezone: d.Timezone, ChannelIDs: nz(d.ChannelIDs),
		LastSentAt: d.LastPeriod, Version: d.Version, UpdatedAt: d.UpdatedAt, UpdatedBy: d.UpdatedBy}
	if d.Enabled {
		if next, err := NextSend(d, now); err == nil {
			out.NextSendAt = &next
		}
	}
	return out
}

// GetDigest returns the digest settings.
func (s *Service) GetDigest(ctx context.Context) (hubapi.NotificationDigest, error) {
	d, err := s.Repo.GetDigest(ctx)
	return DigestAPI(d, s.now()), err
}

// UpdateDigest stores the digest settings; it returns the settings before and after.
func (s *Service) UpdateDigest(ctx context.Context, version int, in hubapi.NotificationDigestInput, by hubapi.Actor) (before, after hubapi.NotificationDigest, err error) {
	prev, err := s.Repo.GetDigest(ctx)
	if err != nil {
		return before, after, err
	}
	before = DigestAPI(prev, s.now())
	p := ValidateSchedule(in.Weekday, in.Time, in.Timezone)
	ids, pi := uniqueIDs(in.ChannelIDs)
	p = append(p, pi...)
	if len(pi) == 0 {
		pc, err := s.checkChannels(ctx, ids)
		if err != nil {
			return before, after, err
		}
		p = append(p, pc...)
	}
	if in.Enabled && len(ids) == 0 {
		p = append(p, "channelIds: choose at least one channel for the digest")
	}
	if len(ids) > maxRuleEntries {
		p = append(p, fmt.Sprintf("channelIds: at most %d channels", maxRuleEntries))
	}
	if len(p) > 0 {
		return before, after, &ValidationError{p}
	}
	d := DigestSettings{Enabled: in.Enabled, Weekday: in.Weekday, TimeOfDay: in.Time, Timezone: in.Timezone, ChannelIDs: ids}
	if err := s.Repo.UpdateDigest(ctx, d, version, by); err != nil {
		return before, after, err
	}
	got, err := s.Repo.GetDigest(ctx)
	return before, DigestAPI(got, s.now()), err
}

// QueueDigest queues the digest for the configured channels now (the leader sends it) and returns
// the number of deliveries.
func (s *Service) QueueDigest(ctx context.Context) (int, error) {
	d, err := s.Repo.GetDigest(ctx)
	if err != nil {
		return 0, err
	}
	return s.queueDigest(ctx, d, s.now())
}

func (s *Service) queueDigest(ctx context.Context, d DigestSettings, end time.Time) (int, error) {
	channels, err := s.Repo.ListChannels(ctx)
	if err != nil {
		return 0, err
	}
	var targets []Channel
	for _, c := range channels {
		if c.Enabled && slices.Contains(d.ChannelIDs, c.ID) {
			targets = append(targets, c)
		}
	}
	if len(targets) == 0 {
		return 0, ErrNoChannels
	}
	m, err := s.BuildDigest(ctx, end)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range targets {
		ok, err := s.Repo.Enqueue(ctx, Delivery{ID: uuid.NewString(), ChannelID: c.ID, Kind: KindDigest, Summary: m.Title(), Payload: m, CreatedAt: s.now()}, 0)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

// digestTick queues the weekly digest when its time has come. A digest is queued once per scheduled
// time, whichever replica leads; one that is more than catchUp late (the hub was down) is skipped,
// and so is the time that passed before the settings were last saved.
func (s *Service) digestTick(ctx context.Context) error {
	const catchUp = 12 * time.Hour
	d, err := s.Repo.GetDigest(ctx)
	if err != nil || !d.Enabled {
		return err
	}
	now := s.now()
	due, err := LastScheduled(d, now)
	if err != nil {
		return err
	}
	if !due.After(d.UpdatedAt) || now.Sub(due) > catchUp {
		return nil
	}
	claimed, err := s.Repo.ClaimDigestPeriod(ctx, due)
	if err != nil || !claimed {
		return err
	}
	n, err := s.queueDigest(ctx, d, due)
	switch {
	case errors.Is(err, ErrNoChannels):
		s.log().WarnContext(ctx, "notify: the weekly digest has no enabled channel")
		return nil
	case err != nil:
		return err
	}
	s.log().InfoContext(ctx, "notify: weekly digest queued", "scheduledFor", due, "deliveries", n)
	return nil
}
