package configsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// StateKey is the ConfigMap key holding the persisted state (JSON).
const StateKey = "state.json"

// maxStateBytes keeps the ConfigMap well below the 1 MiB object limit. Larger states are stored
// without the policies and the ETag (the next pull then gets the full config again).
const maxStateBytes = 768 << 10

var (
	configPulls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kerf_agent_config_pulls_total",
		Help: "Hub config pulls by result (ok, not_modified, unsupported, error).",
	}, []string{"result"})
	configPaused = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "kerf_agent_paused",
		Help: "1 while automation of the cluster is paused in the hub config.",
	})
	hubPolicies = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kerf_agent_policies",
		Help: "RightsizingPolicies by source (hub, git) and validity.",
	}, []string{"source", "valid"})
)

func init() {
	metrics.Registry.MustRegister(configPulls, configPaused, hubPolicies)
}

// Hub is the part of the hub client the syncer uses.
type Hub interface {
	GetConfig(ctx context.Context, etag string) (*hubclient.ClusterConfig, string, error)
}

// Syncer pulls the hub config and materialises it. It is driven by the recommendation puller
// (Sync after every pull), so pause and approvals take effect before the reconciles it triggers.
type Syncer struct {
	Hub    Hub
	Client client.Client // RightsizingPolicies (cached reads) and every write
	Reader client.Reader // uncached reads of the state ConfigMap
	State  *State
	// Namespace and ConfigMap name the ConfigMap that persists the state; empty disables it.
	Namespace, ConfigMap string
	// ReadOnly skips materialisation; the inventory is still reported.
	ReadOnly bool
	// Inventory receives the configVersion and the policy inventory after every sync.
	Inventory func(configVersion string, items []hubclient.PolicyInventoryItem)
	// Recheck is called when the hub asks for the self-checks to run again (D-025, WO-25).
	Recheck func(requestedAt time.Time)
	Log     *slog.Logger

	unsupported bool
	recheckSeen time.Time
}

// Load restores the persisted state (pause flag, approvals, last config). Call it once before the
// first Sync; a missing ConfigMap is an empty state.
func (s *Syncer) Load(ctx context.Context) error {
	if s.ConfigMap == "" || s.Namespace == "" {
		return nil
	}
	var cm corev1.ConfigMap
	err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.ConfigMap}, &cm)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load config state: %w", err)
	}
	var p persisted
	if err := json.Unmarshal([]byte(cm.Data[StateKey]), &p); err != nil {
		return fmt.Errorf("config state %s/%s: %w", s.Namespace, s.ConfigMap, err)
	}
	s.State.restore(p)
	if p.Config != nil {
		configPaused.Set(b2f(p.Config.Flags.Paused))
		s.Log.InfoContext(ctx, "restored hub config", "configVersion", p.Config.ConfigVersion,
			"paused", p.Config.Flags.Paused, "approvals", len(p.Config.Approvals))
	}
	return nil
}

// Sync pulls the config, persists it, materialises the policies and reports the inventory. Errors
// are logged: the last config stays in effect while the hub is unreachable.
func (s *Syncer) Sync(ctx context.Context) {
	cfg, etag, err := s.Hub.GetConfig(ctx, s.State.getETag())
	switch {
	case errors.Is(err, hubclient.ErrNotModified):
		configPulls.WithLabelValues("not_modified").Inc()
	case hubclient.IsNotFound(err):
		configPulls.WithLabelValues("unsupported").Inc()
		if !s.unsupported {
			s.unsupported = true
			s.Log.InfoContext(ctx, "hub does not serve the cluster config yet; hub-managed policies, pause and approvals are unavailable", "err", err)
		}
	case err != nil:
		configPulls.WithLabelValues("error").Inc()
		s.Log.WarnContext(ctx, "pull hub config failed; the last config stays in effect", "err", err)
	default:
		configPulls.WithLabelValues("ok").Inc()
		s.unsupported = false
		prev := s.State.Flags()
		s.State.set(cfg, etag)
		configPaused.Set(b2f(cfg.Flags.Paused))
		if prev.Paused != cfg.Flags.Paused {
			s.Log.InfoContext(ctx, "automation pause changed", "paused", cfg.Flags.Paused, "scope", cfg.Flags.PauseScope, "reason", cfg.Flags.PauseReason)
		}
		s.Log.InfoContext(ctx, "pulled hub config", "configVersion", cfg.ConfigVersion, "policies", len(cfg.Policies),
			"approvals", len(cfg.Approvals), "paused", cfg.Flags.Paused, "policySource", cfg.PolicySource)
	}
	if err := s.persist(ctx); err != nil {
		s.Log.WarnContext(ctx, "persist hub config state failed", "err", err)
	}
	current := s.State.Config()
	res, err := Materialise(ctx, s.Client, current, s.ReadOnly, s.Log)
	if err != nil {
		s.Log.WarnContext(ctx, "materialise hub policies", "err", err)
	}
	s.metrics(res.Inventory)
	if s.Inventory != nil {
		s.Inventory(s.State.ConfigVersion(), res.Inventory)
	}
	if f := s.State.Flags(); f.Recheck && f.RecheckRequestedAt != nil && f.RecheckRequestedAt.After(s.recheckSeen) {
		s.recheckSeen = *f.RecheckRequestedAt
		if s.Recheck != nil {
			s.Recheck(s.recheckSeen)
		}
	}
}

func (s *Syncer) metrics(inv []hubclient.PolicyInventoryItem) {
	hubPolicies.Reset()
	for _, it := range inv {
		hubPolicies.WithLabelValues(it.Source, fmt.Sprint(it.Valid)).Inc()
	}
}

// persist writes the state ConfigMap if the state changed.
func (s *Syncer) persist(ctx context.Context) error {
	if s.ConfigMap == "" || s.Namespace == "" {
		return nil
	}
	p, dirty := s.State.snapshot()
	if !dirty {
		return nil
	}
	b, err := json.Marshal(p)
	if err == nil && len(b) > maxStateBytes && p.Config != nil {
		c := *p.Config
		c.Policies = nil
		p.Config, p.ETag = &c, ""
		b, err = json.Marshal(p)
	}
	if err != nil {
		s.State.markDirty()
		return err
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: s.ConfigMap,
		Labels: map[string]string{"app.kubernetes.io/name": "kerf-agent", "app.kubernetes.io/component": "config-state"}},
		Data: map[string]string{StateKey: string(b)}}
	err = s.Client.Update(ctx, cm)
	if apierrors.IsNotFound(err) {
		err = s.Client.Create(ctx, cm)
	}
	if err != nil {
		s.State.markDirty()
	}
	return err
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
