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

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
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
	ReportWorkloadStatus(ctx context.Context, batch hubclient.WorkloadStatusBatch) error
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

	// configVersion is sent with every status batch, inventory with full ones (D-022); nil
	// inventory = not known yet (omitted).
	configVersion string
	inventory     []hubclient.PolicyInventoryItem

	// Self-checks (D-025): sent with the next batch after a run (pending) and with every full one.
	capabilities []hubclient.AgentCapability
	checks       []hubclient.ReadinessCheck
	checksSet    bool
	checksDirty  bool
}

// SetSelfChecks records a self-check run; the next status batch carries it.
func (r *Reporter) SetSelfChecks(caps []hubclient.AgentCapability, checks []hubclient.ReadinessCheck) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capabilities, r.checks, r.checksSet, r.checksDirty = caps, checks, true, true
}

// Coverage returns the namespaces of the reported workloads and those where every workload has
// both modes off (excluded by policy or label): the policy.namespace-coverage self-check.
func (r *Reporter) Coverage() (namespaces int, excluded []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	off := map[string]bool{}
	for k, st := range r.statuses {
		isOff := st.Modes.CPU == "off" && st.Modes.Memory == "off"
		if v, ok := off[k.Namespace]; !ok {
			off[k.Namespace] = isOff
		} else {
			off[k.Namespace] = v && isOff
		}
	}
	excluded = []string{}
	for ns, o := range off {
		if o {
			excluded = append(excluded, ns)
		}
	}
	sort.Strings(excluded)
	return len(off), excluded
}

// SetConfig records the hub config version the agent materialised and the policy inventory.
func (r *Reporter) SetConfig(configVersion string, inventory []hubclient.PolicyInventoryItem) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.configVersion = configVersion
	r.inventory = inventory
}

// matched counts the workloads each policy takes part in: the winning policy or override of a
// workload, and the hub default layers for every workload. Callers hold r.mu.
func (r *Reporter) matched(inv []hubclient.PolicyInventoryItem) []hubclient.PolicyInventoryItem {
	out := make([]hubclient.PolicyInventoryItem, len(inv))
	counts := map[string]int32{}
	for _, st := range r.statuses {
		counts[st.Policy]++
		if st.Override != "" {
			counts[st.Override]++
		}
	}
	for i, it := range inv {
		out[i] = it
		if it.Generation == 0 {
			continue // not materialised
		}
		n := counts[it.Name]
		if it.Source == hubclient.PolicySourceHub &&
			(it.HubPolicyType == v1alpha1.HubPolicyTypeGlobalDefault || it.HubPolicyType == v1alpha1.HubPolicyTypeClusterDefault) {
			n = int32(len(r.statuses))
		}
		out[i].MatchedWorkloads = &n
	}
	return out
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
	batch := hubclient.WorkloadStatusBatch{AgentVersion: r.agentVersion, ConfigVersion: r.configVersion, Items: items}
	if full && r.inventory != nil {
		batch.PolicyInventory = r.matched(r.inventory)
	}
	sentChecks := r.checksSet && (full || r.checksDirty)
	if sentChecks {
		batch.Capabilities = append([]hubclient.AgentCapability{}, r.capabilities...)
		batch.Checks = append([]hubclient.ReadinessCheck{}, r.checks...)
	}
	r.mu.Unlock()
	if len(items) == 0 && batch.PolicyInventory == nil && !sentChecks {
		return nil
	}
	if err := r.hub.ReportWorkloadStatus(ctx, batch); err != nil {
		hubRequests.WithLabelValues("reportWorkloadStatus", "error").Inc()
		return err
	}
	hubRequests.WithLabelValues("reportWorkloadStatus", "ok").Inc()
	r.mu.Lock()
	for _, k := range keys {
		delete(r.dirty, k)
	}
	if sentChecks {
		r.checksDirty = false
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
	// Config pulls and materialises the hub config after every recommendation pull (D-022), so
	// the reconciles that follow see the current pause flag and approvals; nil disables it.
	Config ConfigSyncer
	Log    *slog.Logger
}

// ConfigSyncer is the hub config side of the pull loop (configsync.Syncer).
type ConfigSyncer interface {
	// Load restores the persisted config before the first pull.
	Load(ctx context.Context) error
	// Sync pulls the config and materialises it.
	Sync(ctx context.Context)
}

// Start implements manager.Runnable.
func (p *Puller) Start(ctx context.Context) error {
	if p.Config != nil {
		if err := p.Config.Load(ctx); err != nil {
			p.Log.WarnContext(ctx, "restore hub config state", "err", err)
		}
	}
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
	// After a successful pull a full status batch follows a moment later, so that it carries the
	// reconciles the pull triggered. It is a timer, not a sleep: changed statuses and events keep
	// flowing every ReportInterval meanwhile.
	var fullFlush <-chan time.Time
	afterPull := func(ok bool) time.Duration {
		if ok && fullFlush == nil { // not re-armed by every pull: short intervals would starve it
			fullFlush = time.After(2 * time.Second)
		}
		return next(ok)
	}
	pull := time.NewTimer(afterPull(p.pullOnce(ctx)))
	defer pull.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pull.C:
			pull.Reset(afterPull(p.pullOnce(ctx)))
		case <-fullFlush:
			fullFlush = nil
			if err := p.Reporter.Flush(ctx, true); err != nil {
				p.Log.WarnContext(ctx, "report to hub failed", "err", err)
			}
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
	if p.Config != nil {
		p.Config.Sync(ctx)
	}
	p.Store.markAttempted()
	p.Trigger()
	return err == nil || errors.Is(err, hubclient.ErrNotModified)
}
