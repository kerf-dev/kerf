package readiness

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	rd "github.com/tomer1983/resource-optimizer/api/v1alpha1/readiness"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// DefaultInterval is how often the hub re-runs the metrics checks of every cluster.
const DefaultInterval = time.Hour

// AgentConnectedCheck is agent.connected: the agent pulled /config at least once and within 3
// pull intervals.
func AgentConnectedCheck(lastPull *time.Time, pullInterval time.Duration, now time.Time, docsBase string) hubapi.ReadinessCheck {
	if pullInterval <= 0 {
		pullInterval = time.Minute
	}
	rem := "Install the agent with the snippet from GET /v1/admin/clusters/{cluster}/install, and check its log for hub errors (401: token)."
	switch {
	case lastPull == nil:
		return NewCheck(rd.CheckAgentConnected, rd.StatusFail, "the agent has never pulled its config from the hub", rem, hubapi.RemediationText, docsBase, now, nil)
	case now.Sub(*lastPull) > 3*pullInterval:
		return NewCheck(rd.CheckAgentConnected, rd.StatusFail, fmt.Sprintf("last pull %s ago (more than 3 pull intervals)", now.Sub(*lastPull).Truncate(time.Second)),
			rem, hubapi.RemediationText, docsBase, now, map[string]string{"lastPull": lastPull.UTC().Format(time.RFC3339)})
	}
	return NewCheck(rd.CheckAgentConnected, rd.StatusPass, fmt.Sprintf("last pull %s ago", now.Sub(*lastPull).Truncate(time.Second)), "", "", docsBase, now,
		map[string]string{"lastPull": lastPull.UTC().Format(time.RFC3339)})
}

// RecommendationsCheck is metrics.recommendations: recommendations exist for the cluster's live
// workloads.
func RecommendationsCheck(live, recommended int, now time.Time, docsBase string) hubapi.ReadinessCheck {
	d := map[string]string{"workloads": fmt.Sprint(live), "recommended": fmt.Sprint(recommended)}
	if live == 0 || recommended == 0 {
		msg := "no recommendation produced yet"
		if live == 0 {
			msg = "no live workloads found yet"
		}
		return NewCheck(rd.CheckRecommendations, rd.StatusFail, msg+": the recommender runs every cycle once metrics checks pass",
			"Wait for the next recommender run; if it stays, see the metrics checks and the hub log (recommender run errors).", hubapi.RemediationText, docsBase, now, d)
	}
	return NewCheck(rd.CheckRecommendations, rd.StatusPass, fmt.Sprintf("recommendations for %d of %d workloads", recommended, live), "", "", docsBase, now, d)
}

// NotAutomationReady returns the target clusters that are not AutomationReady with their state
// (clusters without stored readiness are Registered). Auto-patch gating (D-025).
func NotAutomationReady(states map[string]rd.State, targets []string) map[string]rd.State {
	out := map[string]rd.State{}
	for _, c := range targets {
		st, ok := states[c]
		if !ok {
			st = rd.StateRegistered
		}
		if !st.AtLeast(rd.StateAutomationReady) {
			out[c] = st
		}
	}
	return out
}

// GateDetail is the problem detail of 409 cluster_not_automation_ready.
func GateDetail(notReady map[string]rd.State) string {
	parts := make([]string, 0, len(notReady))
	for c, st := range notReady {
		parts = append(parts, fmt.Sprintf("%s (%s)", c, st))
	}
	sort.Strings(parts)
	return "auto-patch needs the cluster state AutomationReady: " + strings.Join(parts, ", ") +
		". An admin can override with overrideReadiness: true and a reason."
}

// Runner runs the hub checks of every cluster every Interval, and of one cluster on Trigger
// (registration, re-check, datasource or binding change).
type Runner struct {
	// Clusters lists the registered clusters.
	Clusters func(ctx context.Context) ([]string, error)
	// Check runs the hub checks of one cluster and stores them (e.g. MetricsChecker.Run, then
	// Store.Apply with ReplaceIDs MetricsCheckIDs).
	Check    func(ctx context.Context, cluster string) error
	Interval time.Duration
	Log      *slog.Logger

	once    sync.Once
	trigger chan string
}

func (r *Runner) init() {
	r.once.Do(func() { r.trigger = make(chan string, 64) })
}

// Trigger queues an immediate run for a cluster; it never blocks (a full queue drops the request,
// the hourly run catches up).
func (r *Runner) Trigger(cluster string) bool {
	r.init()
	select {
	case r.trigger <- cluster:
		return true
	default:
		return false
	}
}

// Run blocks until ctx is done. The first full run starts immediately.
func (r *Runner) Run(ctx context.Context) {
	r.init()
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	runOne := func(c string) {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if err := r.Check(cctx, c); err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "readiness checks failed", "cluster", c, "err", err)
		}
	}
	runAll := func() {
		clusters, err := r.Clusters(ctx)
		if err != nil {
			log.WarnContext(ctx, "readiness: list clusters", "err", err)
			return
		}
		for _, c := range clusters {
			if ctx.Err() != nil {
				return
			}
			runOne(c)
		}
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	runAll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runAll()
		case c := <-r.trigger:
			runOne(c)
		}
	}
}
