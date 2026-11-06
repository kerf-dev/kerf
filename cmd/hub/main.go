// Command hub runs the Kerf hub: API, recommender scheduler and metrics (WO-04).
// Several replicas may run on one database (WO-18a): see internal/hub/leader.go.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kerf-dev/kerf/internal/hub"
	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/notify"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/recommender"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	configPath := flag.String("config", os.Getenv("HUB_CONFIG"), "path to the hub YAML config (e.g. dev/hub.yaml)")
	devSeed := flag.Bool("dev-seed", false, "register dev clusters and tokens at startup (development only; also devSeed.enabled)")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	flag.Parse()

	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(*logLevel))
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(log)

	if err := run(*configPath, *devSeed, log); err != nil {
		log.Error("hub stopped", "err", err)
		os.Exit(1)
	}
}

func run(configPath string, devSeed bool, log *slog.Logger) error {
	cfg, err := hub.LoadConfig(configPath)
	if err != nil {
		return err
	}
	recCfg, err := cfg.RecommenderConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := openStore(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer st.Close()
	// Migrations run under goose's session locker: replicas starting together apply each one once.
	applied, err := st.Migrate(ctx, os.DirFS(cfg.MigrationsDir))
	if err != nil {
		return err
	}
	log.Info("migrations applied", "dir", cfg.MigrationsDir, "applied", applied)

	prom := recommender.NewPromClient(cfg.PrometheusURL, cfg.PrometheusTimeout.D())
	metrics := hub.NewMetrics()
	identity := hub.Identity()
	srv := &hub.Server{Cfg: cfg, Store: st, Prom: prom, RecCfg: recCfg, Metrics: metrics, Log: log,
		Version: version, Identity: identity, Started: time.Now()}
	// The check for a newer Kerf release (WO-33): run by the leader, stored in Postgres.
	srv.Updates = hub.NewUpdateChecker(cfg, st, version, log.With("component", "update-check"))
	metrics.LeaderOnly().MustRegister(srv.Updates)
	if !cfg.UpdateCheck.On() {
		log.Info("update check disabled (updateCheck.enabled: false)")
	}
	// Notifications (docs/notifications.md): the counter is per replica, the last-success gauge is
	// read from the database by the leader.
	nmetrics := notify.NewMetrics()
	metrics.Registry.MustRegister(nmetrics.Deliveries())
	metrics.LeaderOnly().MustRegister(nmetrics.LastSuccess())
	var cipher *datasource.Cipher
	if cfg.Onboarding.SecretsKeyFile != "" {
		if cipher, err = datasource.LoadCipher(cfg.Onboarding.SecretsKeyFile); err != nil {
			return err
		}
	} else {
		log.Warn("no datasource secrets key (onboarding.secretsKeyFile): datasources with bearer, basic or mTLS auth cannot be created")
	}
	// The other start-up writes, one replica at a time (WO-18a).
	if err := st.WithInitLock(ctx, func() error {
		if devSeed || cfg.DevSeed.Enabled {
			if err := hub.RunDevSeed(ctx, st, cfg.DevSeed, log); err != nil {
				return err
			}
		}
		if err := hub.BootstrapUsers(ctx, st, cfg, log); err != nil {
			return err
		}
		if n, err := st.PruneSessions(ctx, time.Now().Add(-7*24*time.Hour)); err != nil {
			log.Warn("prune sessions", "err", err)
		} else if n > 0 {
			log.Info("old sessions pruned", "sessions", n)
		}
		if err := srv.InitOnboarding(ctx, cipher); err != nil {
			return err
		}
		var sealer notify.Sealer
		if cipher != nil { // a nil *Cipher in the interface would not be nil
			sealer = cipher
		}
		return srv.InitNotifications(ctx, sealer, nmetrics)
	}); err != nil {
		return err
	}

	strategy := recommender.Strategies[cfg.Recommender.Strategy]
	if strategy == nil {
		strategy = recommender.PercentileStrategy{}
	}
	source := &recommender.Source{Q: prom, Cfg: recCfg, Binder: srv.Onb.Bind.ForRecommender}
	srv.Job = &hub.Job{
		Store:   st,
		Engine:  &recommender.Engine{Loader: source, Cfg: recCfg, Strategy: strategy},
		Metrics: metrics, Cfg: cfg, Log: log,
	}
	// Leader election (WO-18a, D-028): every replica serves the API; the leader runs the
	// recommender, recompute requests, readiness checks and sweeps (srv.RunLeader).
	elector, err := srv.NewElector(identity)
	if err != nil {
		return err
	}
	// Ready once migrations are done; /readyz also pings Postgres. Leadership plays no part.
	srv.SetReady()

	httpSrv := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "version", version, "addr", cfg.Listen, "prometheus", cfg.PrometheusURL, "interval", cfg.Recommender.Interval.D(), "pod", identity)
		errc <- httpSrv.ListenAndServe()
	}()
	electorCtx, stopElector := context.WithCancel(ctx)
	electorDone := make(chan struct{})
	go func() { defer close(electorDone); elector.Run(electorCtx) }()
	// This replica's version for the Maintenance page (WO-33); its row is removed on shutdown.
	beatCtx, stopBeat := context.WithCancel(ctx)
	beatDone := make(chan struct{})
	go func() { defer close(beatDone); srv.RunReplicaHeartbeat(beatCtx) }()

	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	// Step down first (stops the leader work, releases the lock and tells the followers), then
	// drain the API.
	stopElector()
	select {
	case <-electorDone:
	case <-time.After(15 * time.Second):
		log.Warn("leader work did not stop within 15s")
	}
	stopBeat()
	<-beatDone
	if serveErr != nil {
		return serveErr
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdown)
}

// openStore retries while Postgres starts.
func openStore(ctx context.Context, url string, log *slog.Logger) (*store.Store, error) {
	var lastErr error
	for i := 0; i < 30; i++ {
		st, err := store.Open(ctx, url)
		if err == nil {
			return st, nil
		}
		lastErr = err
		log.WarnContext(ctx, "waiting for postgres", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, lastErr
}
