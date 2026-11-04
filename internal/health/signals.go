// Package health is the health watch after every automatic change (WO-08).
//
// After a RightsizingRecommendation reaches phase Applied, the workload is watched for
// guardrails.rollback.watchDuration. OOM kills and container restarts come from pod status (pods
// are watched, so a breach is acted on immediately); the CPU throttling ratio comes from PromQL
// against the hub's metrics endpoint, or from the kubelets' cAdvisor endpoints when that is not
// reachable (optional). On a breach the previous values are restored with the same strategy, the
// phase becomes RolledBack, a rolled_back event is emitted, and further decreases of the restored
// resource are blocked until the cooldown ends (the agent's guardrails read RolledBackAt and the
// HealthWatch condition reason). Nothing here depends on the hub being reachable.
package health

import (
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Signals are the pod-status counters of one workload since the change.
type Signals struct {
	OOMKills int
	Restarts int
	// Pods is the number of pods observed.
	Pods int
}

// tracker remembers, per watch, the restart counts the containers had when the watch started
// (pods that existed before the change, e.g. resized in place) and every OOM kill seen, so that
// several kills of one container are counted although the pod status only keeps the last one.
type tracker struct {
	mu      sync.Mutex
	watches map[types.UID]*watchState
	// restored and restoreFailed are keyed by RSR and hold the rolledBackAt they refer to.
	restored      map[types.UID]time.Time
	restoreFailed map[types.UID]time.Time
}

type watchState struct {
	appliedAt time.Time
	baseline  map[string]int32    // podUID/container -> restartCount at first observation
	ooms      map[string]struct{} // podUID/container/finishedAt
	throttle  map[string]counter  // cAdvisor baselines: pod/container -> counters
	watching  bool
}

type counter struct{ throttled, periods float64 }

func newTracker() *tracker {
	return &tracker{watches: map[types.UID]*watchState{}, restored: map[types.UID]time.Time{}, restoreFailed: map[types.UID]time.Time{}}
}

// state returns the state of a watch, resetting it when the change it belongs to changed.
func (t *tracker) state(uid types.UID, appliedAt time.Time) *watchState {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.watches[uid]
	if !ok || !s.appliedAt.Equal(appliedAt) {
		s = &watchState{appliedAt: appliedAt, baseline: map[string]int32{}, ooms: map[string]struct{}{}, throttle: map[string]counter{}}
		t.watches[uid] = s
	}
	return s
}

func (t *tracker) forget(uid types.UID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.watches, uid)
}

// observe counts OOM kills and restarts of the pods since appliedAt and updates the state.
// Containers that started after appliedAt count from zero; containers of older pods count from
// the restart count of the first observation (the agent restarting mid-watch re-baselines them).
func (s *watchState) observe(pods []corev1.Pod) Signals {
	sig := Signals{Pods: len(pods)}
	for _, p := range pods {
		newPod := !p.CreationTimestamp.Time.Before(s.appliedAt)
		statuses := append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
		for _, cs := range statuses {
			key := string(p.UID) + "/" + cs.Name
			base, seen := s.baseline[key]
			if !seen {
				if newPod {
					base = 0
				} else {
					base = cs.RestartCount
				}
				s.baseline[key] = base
			}
			if d := int(cs.RestartCount - base); d > 0 {
				sig.Restarts += d
			}
			for _, term := range []*corev1.ContainerStateTerminated{cs.LastTerminationState.Terminated, cs.State.Terminated} {
				if term == nil || term.Reason != "OOMKilled" || term.FinishedAt.Time.Before(s.appliedAt) {
					continue
				}
				s.ooms[fmt.Sprintf("%s/%d", key, term.FinishedAt.Unix())] = struct{}{}
			}
		}
	}
	sig.OOMKills = len(s.ooms)
	return sig
}
