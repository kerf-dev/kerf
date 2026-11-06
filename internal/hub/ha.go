package hub

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/store"
)

// Hub HA (WO-18a, D-028). Every replica serves the whole API from the database. One replica, the
// leader (Elector), runs the background work in RunLeader. The replicas tell each other about
// changes through Postgres notifications (HandleNotify).

// NewElector returns the elector of this replica; RunLeader is its leader work.
func (s *Server) NewElector(identity string) (*Elector, error) {
	cc, err := ElectorConnConfig(s.Cfg.DatabaseURL, identity)
	if err != nil {
		return nil, err
	}
	e := &Elector{
		ConnConfig: cc, Key: store.LeaderLockKey, Retry: s.Cfg.HA.LeaderRetry.D(), Check: s.Cfg.HA.LeaderCheck.D(),
		Identity: identity, Log: s.Log, Metrics: s.Metrics, Lead: s.RunLeader, OnNotify: s.HandleNotify,
		OnListen: func(bool) { s.dropTokenCache() }, // a revocation may have been missed
	}
	s.Coord = e
	if s.Metrics != nil {
		s.Metrics.SetLeaderFunc(e.IsLeader)
	}
	return e, nil
}

// HandleNotify handles a notification from another replica (or this one). It does not block.
func (s *Server) HandleNotify(payload string) {
	leader := s.Coord == nil || s.Coord.IsLeader()
	switch {
	case payload == store.NotifyTokens:
		s.dropTokenCache()
	case payload == store.NotifyRecompute:
		if leader && s.Job != nil {
			s.Job.Wake()
		}
	case payload == store.NotifySettings:
		if leader && s.Job != nil {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				s.Job.ReloadInterval(ctx)
			}()
		}
	case strings.HasPrefix(payload, store.NotifyReadinessPrefix):
		if leader && s.Onb != nil {
			s.Onb.Runner.Trigger(strings.TrimPrefix(payload, store.NotifyReadinessPrefix))
		}
	}
}

// RunLeader is the leader's work: the recommender schedule and backfill, recompute requests, the
// readiness checks (metrics checks, agent checks, re-check expiry), the update check, the
// notification queue and the metrics snapshot. It returns once ctx is done and all of it stopped.
func (s *Server) RunLeader(ctx context.Context) {
	if s.Job != nil {
		if err := s.Job.Takeover(ctx); err != nil && ctx.Err() == nil {
			s.Log.WarnContext(ctx, "leader takeover", "err", err)
		}
	}
	if s.Metrics != nil {
		if err := s.Metrics.Refresh(ctx, s.Store); err != nil && ctx.Err() == nil {
			s.Log.WarnContext(ctx, "metrics refresh", "err", err)
		}
	}
	var wg sync.WaitGroup
	start := func(f func(context.Context)) {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	if s.Job != nil {
		start(s.Job.Loop)
		start(s.Job.RecomputeLoop)
	}
	start(s.RunReadiness)
	start(s.RunMaintenance)
	if s.Notify != nil {
		start(s.Notify.Run)
	}
	if s.Metrics != nil {
		start(func(ctx context.Context) { s.Metrics.RefreshLoop(ctx, s.Store, time.Minute, s.Log) })
	}
	wg.Wait()
}
