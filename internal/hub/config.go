package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/tomer1983/resource-optimizer/internal/recommender"
)

// Duration is a time.Duration that (un)marshals as a Go duration string ("5m").
type Duration time.Duration

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"5m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the hub configuration (YAML file, e.g. dev/hub.yaml).
type Config struct {
	// Listen is the HTTP listen address of the API, /metrics and health endpoints.
	Listen string `json:"listen"`
	// DatabaseURL is the Postgres URL (env HUB_DATABASE_URL overrides).
	DatabaseURL string `json:"databaseURL"`
	// PrometheusURL is the Prometheus/VictoriaMetrics base URL (env HUB_PROMETHEUS_URL overrides).
	PrometheusURL string `json:"prometheusURL"`
	// PrometheusTimeout bounds each query.
	PrometheusTimeout Duration `json:"prometheusTimeout"`
	// MigrationsDir holds the goose migrations (db/migrations); applied on startup.
	MigrationsDir string `json:"migrationsDir"`
	// CORSOrigins are allowed browser origins (dev: the UI dev server http://localhost:5173).
	CORSOrigins []string `json:"corsOrigins"`
	// AgentStaleAfter: agentStatus is "stale" when the last heartbeat is older.
	AgentStaleAfter Duration `json:"agentStaleAfter"`
	// TokenCacheTTL caches bearer-token lookups.
	TokenCacheTTL Duration `json:"tokenCacheTTL"`

	DevSeed     DevSeed           `json:"devSeed"`
	Recommender RecommenderConfig `json:"recommender"`
	History     HistoryConfig     `json:"history"`
}

// DevSeed registers clusters and tokens at startup. Development only.
type DevSeed struct {
	Enabled bool `json:"enabled"`
	// Clusters to register.
	Clusters []string `json:"clusters"`
	// TokensDir holds one file per token: "<cluster>" (agent token of that cluster), "ui", "admin"
	// (the mounted Secret hub-dev-tokens). Env HUB_DEV_TOKENS_DIR overrides.
	TokensDir string `json:"tokensDir"`
	// Tokens are inline tokens by the same keys (used when TokensDir is empty, e.g. go run).
	Tokens map[string]string `json:"tokens"`
}

// HistoryConfig controls recommendation history pruning.
type HistoryConfig struct {
	// FullResolution keeps every run's recommendations this long; older ones are thinned to the
	// newest per workload and day.
	FullResolution Duration `json:"fullResolution"`
	// Retention deletes recommendations older than this (the latest per workload is always kept).
	Retention Duration `json:"retention"`
}

// RecommenderConfig configures the scheduler and overrides recommender.DefaultConfig().
type RecommenderConfig struct {
	// Interval between runs (production: 24h; dev: 5m).
	Interval Duration `json:"interval"`
	// RunOnStart runs once at startup.
	RunOnStart *bool `json:"runOnStart"`
	// Strategy name (default "percentile").
	Strategy string `json:"strategy"`
	// Backfill (dev): compute history from the metrics already stored, one run as of each
	// multiple of Every (UTC) in the Runs intervals before the first regular run, so history and
	// the savings trend have data. Missing ones are filled at every start, between regular runs.
	Backfill struct {
		Runs  int      `json:"runs"`
		Every Duration `json:"every"`
	} `json:"backfill"`

	Step                  *Duration `json:"step"`
	RateWindow            *Duration `json:"rateWindow"`
	RiskWindow            *Duration `json:"riskWindow"`
	InventoryLookback     *Duration `json:"inventoryLookback"`
	ThrottleThreshold     *float64  `json:"throttleThreshold"`
	RestartThreshold      *float64  `json:"restartThreshold"`
	ThrottleIncrease      *float64  `json:"throttleIncrease"`
	OOMIncrease           *float64  `json:"oomIncrease"`
	MinCPU                string    `json:"minCPU"`
	MinMemory             string    `json:"minMemory"`
	LowConfidence         *float64  `json:"lowConfidence"`
	FullConfidenceSamples *int      `json:"fullConfidenceSamples"`
	SidecarContainers     []string  `json:"sidecarContainers"`
	DefaultProfile        string    `json:"defaultProfile"`
	// DefaultCPUCoreHourUSD / DefaultMemoryGiBHourUSD are the fallback rates when no pricing series
	// exist for a cluster (the recommendation's explanation then says so).
	DefaultCPUCoreHourUSD   *float64                 `json:"defaultCPUCoreHourUSD"`
	DefaultMemoryGiBHourUSD *float64                 `json:"defaultMemoryGiBHourUSD"`
	Profiles                map[string]ProfileConfig `json:"profiles"`
}

// ProfileConfig overrides a profile; unset fields keep the built-in value.
type ProfileConfig struct {
	Window           *Duration `json:"window"`
	CPUPercentile    *float64  `json:"cpuPercentile"`
	MemoryPercentile *float64  `json:"memoryPercentile"`
	MemoryPeakWindow *Duration `json:"memoryPeakWindow"`
	CPUHeadroom      *float64  `json:"cpuHeadroom"`
	MemoryBuffer     *float64  `json:"memoryBuffer"`
}

// DefaultConfig returns the built-in configuration.
func DefaultConfig() Config {
	return Config{
		Listen:            ":8080",
		PrometheusURL:     "http://localhost:8428",
		PrometheusTimeout: Duration(2 * time.Minute),
		MigrationsDir:     "db/migrations",
		AgentStaleAfter:   Duration(10 * time.Minute),
		TokenCacheTTL:     Duration(30 * time.Second),
		Recommender:       RecommenderConfig{Interval: Duration(24 * time.Hour), Strategy: "percentile"},
		History:           HistoryConfig{FullResolution: Duration(7 * 24 * time.Hour), Retention: Duration(90 * 24 * time.Hour)},
	}
}

// LoadConfig reads a YAML file (empty path: defaults) and applies env overrides.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return cfg, err
		}
		if err := yaml.UnmarshalStrict(b, &cfg); err != nil {
			return cfg, fmt.Errorf("%s: %w", path, err)
		}
	}
	for env, dst := range map[string]*string{
		"HUB_DATABASE_URL":   &cfg.DatabaseURL,
		"HUB_PROMETHEUS_URL": &cfg.PrometheusURL,
		"HUB_LISTEN":         &cfg.Listen,
		"HUB_MIGRATIONS_DIR": &cfg.MigrationsDir,
		"HUB_DEV_TOKENS_DIR": &cfg.DevSeed.TokensDir,
	} {
		if v := os.Getenv(env); v != "" {
			*dst = v
		}
	}
	if v := os.Getenv("HUB_CORS_ORIGINS"); v != "" {
		cfg.CORSOrigins = strings.Split(v, ",")
	}
	if cfg.DatabaseURL == "" {
		return cfg, fmt.Errorf("databaseURL (or HUB_DATABASE_URL) is required")
	}
	if cfg.Recommender.Interval.D() <= 0 {
		return cfg, fmt.Errorf("recommender.interval must be positive")
	}
	if _, err := cfg.RecommenderConfig(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// RecommenderConfig returns the recommender configuration with the overrides applied.
func (c Config) RecommenderConfig() (recommender.Config, error) {
	r := c.Recommender
	out := recommender.DefaultConfig()
	setD := func(dst *time.Duration, v *Duration) {
		if v != nil {
			*dst = v.D()
		}
	}
	setF := func(dst *float64, v *float64) {
		if v != nil {
			*dst = *v
		}
	}
	setD(&out.Step, r.Step)
	setD(&out.RateWindow, r.RateWindow)
	setD(&out.RiskWindow, r.RiskWindow)
	setD(&out.InventoryLookback, r.InventoryLookback)
	setF(&out.ThrottleThreshold, r.ThrottleThreshold)
	setF(&out.RestartThreshold, r.RestartThreshold)
	setF(&out.ThrottleIncrease, r.ThrottleIncrease)
	setF(&out.OOMIncrease, r.OOMIncrease)
	setF(&out.LowConfidence, r.LowConfidence)
	setF(&out.DefaultCPUCoreHourUSD, r.DefaultCPUCoreHourUSD)
	setF(&out.DefaultMemoryGiBHourUSD, r.DefaultMemoryGiBHourUSD)
	if r.FullConfidenceSamples != nil {
		out.FullConfidenceSamples = *r.FullConfidenceSamples
	}
	if r.MinCPU != "" {
		v, err := recommender.ParseCPU(r.MinCPU)
		if err != nil {
			return out, fmt.Errorf("recommender.minCPU: %w", err)
		}
		out.MinCPUCores = v
	}
	if r.MinMemory != "" {
		v, err := recommender.ParseMemory(r.MinMemory)
		if err != nil {
			return out, fmt.Errorf("recommender.minMemory: %w", err)
		}
		out.MinMemoryBytes = v
	}
	if r.SidecarContainers != nil {
		out.SidecarContainers = r.SidecarContainers
	}
	if r.DefaultProfile != "" {
		out.DefaultSettings.Profile = r.DefaultProfile
	}
	for name, pc := range r.Profiles {
		p, ok := out.Profiles[name]
		if !ok {
			return out, fmt.Errorf("recommender.profiles: unknown profile %q (profiles are dev, production, high-availability)", name)
		}
		setD(&p.Window, pc.Window)
		setD(&p.MemoryPeakWindow, pc.MemoryPeakWindow)
		setF(&p.CPUPercentile, pc.CPUPercentile)
		setF(&p.MemoryPercentile, pc.MemoryPercentile)
		setF(&p.CPUHeadroom, pc.CPUHeadroom)
		setF(&p.MemoryBuffer, pc.MemoryBuffer)
		out.Profiles[name] = p
	}
	if _, ok := recommender.Strategies[r.Strategy]; !ok && r.Strategy != "" {
		return out, fmt.Errorf("recommender.strategy: unknown strategy %q", r.Strategy)
	}
	return out, out.Validate()
}
