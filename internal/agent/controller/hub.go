package controller

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
)

// Agent metrics (prefix ro_agent_, docs/contracts/metrics.md).
var (
	eventsDropped = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ro_agent_events_dropped_total",
		Help: "Events dropped because the buffer for the hub was full (oldest first).",
	})
	eventsBuffered = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ro_agent_events_buffered",
		Help: "Events waiting to be sent to the hub.",
	})
	hubRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ro_agent_hub_requests_total",
		Help: "Requests to the hub by operation and result (ok, not_modified, error).",
	}, []string{"operation", "result"})
	lastPull = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ro_agent_last_successful_pull_timestamp_seconds",
		Help: "Unix time of the last successful recommendation pull.",
	})
	recommendations = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ro_agent_recommendations",
		Help: "Recommendations currently known to the agent.",
	})
)

func init() {
	metrics.Registry.MustRegister(eventsDropped, eventsBuffered, hubRequests, lastPull, recommendations)
}

// wkey identifies a workload: namespace/Kind/name.
type wkey struct{ Namespace, Kind, Name string }

func (k wkey) String() string { return k.Namespace + "/" + k.Kind + "/" + k.Name }

// Store holds the last recommendations pulled from the hub.
type Store struct {
	mu          sync.RWMutex
	recs        map[wkey]hubclient.WorkloadRecommendation
	etag        string
	lastSuccess time.Time
	attempted   bool
	staleAfter  time.Duration
	now         func() time.Time
}

// NewStore returns an empty store; recommendations older than staleAfter since the last
// successful pull make the hub count as unreachable.
func NewStore(staleAfter time.Duration) *Store {
	return &Store{recs: map[wkey]hubclient.WorkloadRecommendation{}, staleAfter: staleAfter, now: time.Now}
}

// Get returns the recommendation of a workload.
func (s *Store) Get(k wkey) (*hubclient.WorkloadRecommendation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.recs[k]
	if !ok {
		return nil, false
	}
	return &r, true
}

// Fresh reports whether the last successful pull is recent enough to act on.
func (s *Store) Fresh() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.lastSuccess.IsZero() && s.now().Sub(s.lastSuccess) <= s.staleAfter
}

// Replace stores a full result set and returns the keys whose recommendation changed.
func (s *Store) Replace(items []hubclient.WorkloadRecommendation, etag string) []wkey {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make(map[wkey]hubclient.WorkloadRecommendation, len(items))
	var changed []wkey
	for _, it := range items {
		k := wkey{it.Workload.Namespace, it.Workload.Kind, it.Workload.Name}
		next[k] = it
		if old, ok := s.recs[k]; !ok || old.ID != it.ID {
			changed = append(changed, k)
		}
	}
	for k := range s.recs {
		if _, ok := next[k]; !ok {
			changed = append(changed, k)
		}
	}
	s.recs, s.etag, s.lastSuccess = next, etag, s.now()
	recommendations.Set(float64(len(next)))
	lastPull.Set(float64(s.lastSuccess.Unix()))
	return changed
}

// Attempted reports whether at least one pull finished (successfully or not).
func (s *Store) Attempted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.attempted
}

func (s *Store) markAttempted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempted = true
}

func (s *Store) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSuccess = s.now()
	lastPull.Set(float64(s.lastSuccess.Unix()))
}

func (s *Store) getETag() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.etag
}

// Hub is the part of hubclient.Client the agent uses (an interface for tests).
type Hub interface {
	ListRecommendations(ctx context.Context, etag string) ([]hubclient.WorkloadRecommendation, string, error)
	ReportWorkloadStatus(ctx context.Context, agentVersion string, items []hubclient.WorkloadStatus) error
	ReportEvents(ctx context.Context, items []hubclient.Event) (hubclient.BatchResult, error)
}

// Reporter buffers workload statuses and events for the hub and sends them periodically.
// Events are kept in a bounded buffer (oldest dropped first) while the hub is unreachable.
type Reporter struct {
	hub          Hub
	agentVersion string
	maxEvents    int
	log          *slog.Logger

	mu       sync.Mutex
	badIDs   map[string]bool // non-UUID recommendation ids already logged
	statuses map[wkey]hubclient.WorkloadStatus
	dirty    map[wkey]bool
	events   []hubclient.Event
}

// NewReporter returns a reporter with an event buffer of maxEvents.
func NewReporter(hub Hub, agentVersion string, maxEvents int, log *slog.Logger) *Reporter {
	if maxEvents <= 0 {
		maxEvents = 10000
	}
	return &Reporter{hub: hub, agentVersion: agentVersion, maxEvents: maxEvents, log: log,
		statuses: map[wkey]hubclient.WorkloadStatus{}, dirty: map[wkey]bool{}, badIDs: map[string]bool{}}
}

// SetStatus records the status of a workload; it is sent on the next flush if it changed.
func (r *Reporter) SetStatus(k wkey, st hubclient.WorkloadStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st.RecommendationID = r.validID(st.RecommendationID, k.String())
	old, ok := r.statuses[k]
	if ok && statusEqual(old, st) {
		return
	}
	r.statuses[k] = st
	r.dirty[k] = true
}

// DeleteStatus forgets a deleted workload.
func (r *Reporter) DeleteStatus(k wkey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.statuses, k)
	delete(r.dirty, k)
}

func statusEqual(a, b hubclient.WorkloadStatus) bool {
	a.ObservedAt, b.ObservedAt = nil, nil
	return jsonEqual(a, b)
}

// Event queues an event for the hub, filling id and time.
func (r *Reporter) Event(e hubclient.Event) {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if len(e.Message) > 1024 {
		e.Message = e.Message[:1021] + "..."
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e.RecommendationID = r.validID(e.RecommendationID, e.Workload.Namespace+"/"+e.Workload.Kind+"/"+e.Workload.Name)
	r.events = append(r.events, e)
	if over := len(r.events) - r.maxEvents; over > 0 {
		r.events = r.events[over:]
		eventsDropped.Add(float64(over))
	}
	eventsBuffered.Set(float64(len(r.events)))
}

// validID returns id if it is a UUID (the hub API requires one, and rejects a whole batch
// otherwise), else "" — logging each bad id once. RSRs written by hand or by tests may carry
// arbitrary source ids. Callers hold r.mu.
func (r *Reporter) validID(id, workload string) string {
	if id == "" {
		return ""
	}
	if _, err := uuid.Parse(id); err == nil {
		return id
	}
	if !r.badIDs[id] {
		r.badIDs[id] = true
		if r.log != nil {
			r.log.Warn("recommendation id is not a UUID; omitted from hub reports", "workload", workload, "recommendationID", id)
		}
	}
	return ""
}

// PendingEvents returns a copy of the buffered events.
func (r *Reporter) PendingEvents() []hubclient.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hubclient.Event(nil), r.events...)
}

// Flush sends buffered events and changed statuses (all statuses if full).
func (r *Reporter) Flush(ctx context.Context, full bool) error {
	var errs []error
	if err := r.flushEvents(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := r.flushStatuses(ctx, full); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (r *Reporter) flushEvents(ctx context.Context) error {
	for {
		r.mu.Lock()
		n := min(len(r.events), hubclient.MaxBatch)
		batch := append([]hubclient.Event(nil), r.events[:n]...)
		r.mu.Unlock()
		if n == 0 {
			return nil
		}
		if _, err := r.hub.ReportEvents(ctx, batch); err != nil {
			hubRequests.WithLabelValues("reportEvents", "error").Inc()
			return err
		}
		hubRequests.WithLabelValues("reportEvents", "ok").Inc()
		r.mu.Lock()
		// Drop what was sent. Events may have been dropped from the front meanwhile; remove by id.
		sent := map[string]bool{}
		for _, e := range batch {
			sent[e.ID] = true
		}
		kept := r.events[:0]
		for _, e := range r.events {
			if !sent[e.ID] {
				kept = append(kept, e)
			}
		}
		r.events = kept
		eventsBuffered.Set(float64(len(r.events)))
		r.mu.Unlock()
	}
}

func (r *Reporter) flushStatuses(ctx context.Context, full bool) error {
	r.mu.Lock()
	var keys []wkey
	for k := range r.statuses {
		if full || r.dirty[k] {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	now := time.Now().UTC().Truncate(time.Second)
	items := make([]hubclient.WorkloadStatus, 0, len(keys))
	for _, k := range keys {
		st := r.statuses[k]
		st.ObservedAt = &now
		items = append(items, st)
	}
	r.mu.Unlock()
	if len(items) == 0 {
		return nil
	}
	if err := r.hub.ReportWorkloadStatus(ctx, r.agentVersion, items); err != nil {
		hubRequests.WithLabelValues("reportWorkloadStatus", "error").Inc()
		return err
	}
	hubRequests.WithLabelValues("reportWorkloadStatus", "ok").Inc()
	r.mu.Lock()
	for _, k := range keys {
		delete(r.dirty, k)
	}
	r.mu.Unlock()
	return nil
}

// Puller pulls recommendations, triggers reconciles and flushes the reporter.
type Puller struct {
	Hub            Hub
	Store          *Store
	Reporter       *Reporter
	PullInterval   time.Duration
	ReportInterval time.Duration
	// Trigger is called after every pull (successful or not) to re-evaluate every workload: it
	// is the periodic resync and picks up a hub that became (un)reachable.
	Trigger func()
	Log     *slog.Logger
}

// Start implements manager.Runnable.
func (p *Puller) Start(ctx context.Context) error {
	report := time.NewTicker(p.ReportInterval)
	defer report.Stop()
	// After a failed pull retry sooner (5s, doubling) than the pull interval.
	retry := 5 * time.Second
	next := func(ok bool) time.Duration {
		if ok {
			retry = 5 * time.Second
			return p.PullInterval
		}
		d := min(retry, p.PullInterval)
		retry = min(retry*2, p.PullInterval)
		return d
	}
	pull := time.NewTimer(next(p.pullOnce(ctx)))
	defer pull.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pull.C:
			pull.Reset(next(p.pullOnce(ctx)))
		case <-report.C:
			if err := p.Reporter.Flush(ctx, false); err != nil {
				p.Log.DebugContext(ctx, "report to hub failed", "err", err)
			}
		}
	}
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (p *Puller) NeedLeaderElection() bool { return true }

func (p *Puller) pullOnce(ctx context.Context) bool {
	items, etag, err := p.Hub.ListRecommendations(ctx, p.Store.getETag())
	switch {
	case errors.Is(err, hubclient.ErrNotModified):
		hubRequests.WithLabelValues("listRecommendations", "not_modified").Inc()
		p.Store.touch()
	case err != nil:
		hubRequests.WithLabelValues("listRecommendations", "error").Inc()
		p.Log.WarnContext(ctx, "pull recommendations failed; no new changes are applied while the hub is unreachable", "err", err)
	default:
		hubRequests.WithLabelValues("listRecommendations", "ok").Inc()
		changed := p.Store.Replace(items, etag)
		p.Log.InfoContext(ctx, "pulled recommendations", "items", len(items), "changed", len(changed))
	}
	p.Store.markAttempted()
	p.Trigger()
	ok := err == nil || errors.Is(err, hubclient.ErrNotModified)
	if ok {
		// Give the reconciles a moment so the full batch carries their results.
		select {
		case <-ctx.Done():
			return ok
		case <-time.After(2 * time.Second):
		}
		if err := p.Reporter.Flush(ctx, true); err != nil {
			p.Log.WarnContext(ctx, "report to hub failed", "err", err)
		}
	}
	return ok
}
