// Package configsync is the agent side of the hub-managed configuration (D-022, D-024, WO-21):
// it pulls GET /v1/clusters/{c}/config, materialises the hub policies as RightsizingPolicy CRs
// labelled managed-by=hub, keeps the cluster flags (pause) and approve-once grants for the
// reconciler, persists them so a restart without the hub keeps them, and builds the policy
// inventory reported to the hub.
package configsync

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// State is the last configuration received from the hub. The reconciler reads the flags and
// approvals from it; it is safe for concurrent use.
type State struct {
	mu   sync.RWMutex
	cfg  *hubclient.ClusterConfig // nil until the first config (pulled or restored)
	etag string
	// consumed holds approvals the agent applied (id -> when), until the hub stops sending them.
	consumed map[string]time.Time
	// held holds approvals whose change is in flight (id -> approval), until it is consumed or
	// expires: a recommender run may supersede them in the hub meanwhile (WO-29).
	held  map[string]hubclient.ConfigApproval
	dirty bool
}

// NewState returns an empty state: not paused, no approvals, no config.
func NewState() *State {
	return &State{consumed: map[string]time.Time{}, held: map[string]hubclient.ConfigApproval{}}
}

// Config returns the last config (nil if none). It must not be modified.
func (s *State) Config() *hubclient.ClusterConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// ConfigVersion returns the configVersion of the last config ("" if none).
func (s *State) ConfigVersion() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg == nil {
		return ""
	}
	return s.cfg.ConfigVersion
}

// Flags returns the last cluster flags (zero value if none).
func (s *State) Flags() hubclient.ConfigFlags {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cfg == nil {
		return hubclient.ConfigFlags{}
	}
	return s.cfg.Flags
}

// Approval returns the approve-once grant for the workload's recommendation recID, if there is
// one that is not expired and not consumed yet: one the hub sends, or one held for a change in
// flight.
func (s *State) Approval(w hubclient.WorkloadRef, recID string, now time.Time) *hubclient.ConfigApproval {
	if recID == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	usable := func(a hubclient.ConfigApproval) bool {
		if a.Workload != w || a.RecommendationID != recID {
			return false
		}
		_, done := s.consumed[a.ID]
		return !done && !expired(a, now)
	}
	if s.cfg != nil {
		for _, a := range s.cfg.Approvals {
			if usable(a) {
				return &a
			}
		}
	}
	for _, a := range s.held {
		if usable(a) {
			return &a
		}
	}
	return nil
}

func expired(a hubclient.ConfigApproval, now time.Time) bool {
	return !a.ExpiresAt.IsZero() && !now.Before(a.ExpiresAt)
}

// Hold keeps an approval whose change is in flight (an in-place resize): Approval returns it
// until it is consumed or expires, also after the hub stops sending it because a recommender run
// superseded it. The agent acted on it, so the change finishes and its applied event consumes it.
func (s *State) Hold(a hubclient.ConfigApproval) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.held[a.ID]; !ok {
		s.held[a.ID] = a
		s.dirty = true
	}
}

// Consume marks an approval applied: it is never applied again, even while the hub still sends
// it (until it stores the applied/ApprovedOnce event).
func (s *State) Consume(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.consumed[id]; !ok {
		s.consumed[id] = now.UTC()
		s.dirty = true
	}
	if _, ok := s.held[id]; ok {
		delete(s.held, id)
		s.dirty = true
	}
}

// set replaces the config. Consumed approvals the hub no longer sends are forgotten, and so are
// held approvals that expired.
func (s *State) set(cfg *hubclient.ClusterConfig, etag string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg, s.etag, s.dirty = cfg, etag, true
	for id := range s.consumed {
		if !slices.ContainsFunc(cfg.Approvals, func(a hubclient.ConfigApproval) bool { return a.ID == id }) {
			delete(s.consumed, id)
		}
	}
	for id, a := range s.held {
		if expired(a, now) {
			delete(s.held, id)
		}
	}
}

func (s *State) getETag() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.etag
}

// snapshot returns what is persisted and clears the dirty flag (restored by markDirty on failure).
func (s *State) snapshot() (persisted, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := persisted{ETag: s.etag, Config: s.cfg, Consumed: map[string]time.Time{}}
	for k, v := range s.consumed {
		p.Consumed[k] = v
	}
	for _, a := range s.held {
		p.Held = append(p.Held, a)
	}
	slices.SortFunc(p.Held, func(a, b hubclient.ConfigApproval) int { return strings.Compare(a.ID, b.ID) })
	dirty := s.dirty
	s.dirty = false
	return p, dirty
}

func (s *State) markDirty() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty = true
}

func (s *State) restore(p persisted) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg, s.etag = p.Config, p.ETag
	s.consumed = map[string]time.Time{}
	for k, v := range p.Consumed {
		s.consumed[k] = v
	}
	s.held = map[string]hubclient.ConfigApproval{}
	for _, a := range p.Held {
		s.held[a.ID] = a
	}
}

// persisted is the form stored in the state ConfigMap.
type persisted struct {
	ETag     string                   `json:"etag,omitempty"`
	Config   *hubclient.ClusterConfig `json:"config,omitempty"`
	Consumed map[string]time.Time     `json:"consumed,omitempty"`
	// Held: approvals whose change is in flight (WO-29).
	Held []hubclient.ConfigApproval `json:"held,omitempty"`
}
