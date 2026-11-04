// Command hub runs the resource-optimizer hub: API, recommender scheduler and metrics (WO-04).
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

	"github.com/tomer1983/resource-optimizer/internal/hub"
	"github.com/tomer1983/resource-optimizer/internal/hub/datasource"
	"github.com/tomer1983/resource-optimizer/internal/hub/store"
	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

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
	applied, err := st.Migrate(ctx, os.DirFS(cfg.MigrationsDir))
	if err != nil {
		return err
	}
	log.Info("migrations applied", "dir", cfg.MigrationsDir, "applied", applied)
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

	prom := recommender.NewPromClient(cfg.PrometheusURL, cfg.PrometheusTimeout.D())
	metrics := hub.NewMetrics()
	srv := &hub.Server{Cfg: cfg, Store: st, Prom: prom, RecCfg: recCfg, Metrics: metrics, Log: log}
	strategy := recommender.Strategies[cfg.Recommender.Strategy]
	if strategy == nil {
		strategy = recommender.PercentileStrategy{}
	}
	var cipher *datasource.Cipher
	if cfg.Onboarding.SecretsKeyFile != "" {
		if cipher, err = datasource.LoadCipher(cfg.Onboarding.SecretsKeyFile); err != nil {
			return err
		}
	} else {
		log.Warn("no datasource secrets key (onboarding.secretsKeyFile): datasources with bearer, basic or mTLS auth cannot be created")
	}
	if err := srv.InitOnboarding(ctx, cipher); err != nil {
		return err
	}
	source := &recommender.Source{Q: prom, Cfg: recCfg, Binder: srv.Onb.Bind.ForRecommender}
	job := &hub.Job{
		Store:   st,
		Engine:  &recommender.Engine{Loader: source, Cfg: recCfg, Strategy: strategy},
		Metrics: metrics, Cfg: cfg, Log: log,
	}
	srv.Job = job
	if err := metrics.Refresh(ctx, st); err != nil {
		log.Warn("initial metrics refresh", "err", err)
	}
	srv.SetReady()

	httpSrv := &http.Server{Addr: cfg.Listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "prometheus", cfg.PrometheusURL, "interval", cfg.Recommender.Interval.D())
		errc <- httpSrv.ListenAndServe()
	}()
	go job.Loop(ctx)
	go srv.RunReadiness(ctx)
	go metrics.RefreshLoop(ctx, st, time.Minute, log)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
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
