// Package agent wires the agent's parts together where they meet.
package agent

import (
	"fmt"
	"log/slog"
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/tomer1983/resource-optimizer/internal/agent/controller"
	"github.com/tomer1983/resource-optimizer/internal/health"
)

// HealthConfig configures the health watch (WO-08).
type HealthConfig struct {
	Enabled bool
	// MetricsURL is the Prometheus-compatible endpoint for the throttling ratio (the hub's
	// VictoriaMetrics); empty disables it.
	MetricsURL       string
	MetricsTokenFile string
	// CadvisorFallback reads throttling from the kubelets (nodes/proxy) when MetricsURL fails.
	CadvisorFallback    bool
	CheckInterval       time.Duration
	MinThrottlingWindow time.Duration
}

// SetupHealth is the single integration point of the health watch with the agent: it registers
// the watcher in the agent's manager, sharing its client, the RSR index (registered by the
// reconciler's SetupWithManager, which must run first) and its event delivery.
func SetupHealth(mgr manager.Manager, r *controller.Reconciler, cfg HealthConfig, log *slog.Logger) error {
	if !cfg.Enabled {
		log.Info("health watch disabled")
		return nil
	}
	var sources []health.ThrottleSource
	if cfg.MetricsURL != "" {
		sources = append(sources, &health.PromSource{URL: cfg.MetricsURL, TokenFile: cfg.MetricsTokenFile})
	}
	if cfg.CadvisorFallback {
		cs, err := kubernetes.NewForConfig(mgr.GetConfig())
		if err != nil {
			return fmt.Errorf("cadvisor client: %w", err)
		}
		sources = append(sources, &health.CadvisorSource{REST: cs.CoreV1().RESTClient()})
	}
	w := &health.Watcher{
		Client:   mgr.GetClient(),
		Reader:   mgr.GetAPIReader(),
		Throttle: health.Sources(sources...),
		Emitter:  r,
		Log:      log,
		Cfg: health.Config{
			ClusterName:         r.Cfg.ClusterName,
			ClusterDefaults:     r.Cfg.ClusterDefaults,
			CheckInterval:       cfg.CheckInterval,
			MinThrottlingWindow: cfg.MinThrottlingWindow,
			ReadOnly:            r.Cfg.ReadOnly,
		},
	}
	return w.SetupWithManager(mgr)
}
