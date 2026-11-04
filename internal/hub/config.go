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
	Auth        AuthConfig        `json:"auth"`
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
	// Users are local users created when missing (passwords from the chart's dev values).
	Users []DevUser `json:"users"`
	// UsersFile is a YAML list of more DevUser (the chart mounts Secret hub-dev-users there).
	UsersFile string `json:"usersFile"`
}

// DevUser is a seeded local user (development only).
type DevUser struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Password    string `json:"password"`
}

// AuthConfig configures local users and browser sessions (D-023).
type AuthConfig struct {
	// CookieSecure sets the Secure attribute of the session cookie (default true; false only for
	// plain-HTTP development).
	CookieSecure *bool `json:"cookieSecure"`
	// SessionTTL is the lifetime of a browser session (default 12h).
	SessionTTL Duration `json:"sessionTTL"`
	// BcryptCost of new password hashes (minimum and default 12).
	BcryptCost int `json:"bcryptCost"`
	// MaxFailedLogins locks an account after that many consecutive failures (default 5, 0 = never)
	// for LockoutDuration (default 15m).
	MaxFailedLogins *int     `json:"maxFailedLogins"`
	LockoutDuration Duration `json:"lockoutDuration"`
	// LoginRateLimit is the number of login attempts per minute per client address and per
	// username (default 10).
	LoginRateLimit int `json:"loginRateLimit"`
	// BootstrapAdmin is created on a database without users (D-023). The password comes from
	// PasswordFile (a mounted Secret) or env HUB_BOOTSTRAP_ADMIN_PASSWORD.
	BootstrapAdmin BootstrapAdmin `json:"bootstrapAdmin"`
}

// BootstrapAdmin is the initial admin.
type BootstrapAdmin struct {
	Username     string `json:"username"`
	PasswordFile string `json:"passwordFile"`
}

func (a AuthConfig) cookieSecure() bool { return a.CookieSecure == nil || *a.CookieSecure }

func (a AuthConfig) sessionTTL() time.Duration {
	if a.SessionTTL.D() <= 0 {
		return 12 * time.Hour
	}
	return a.SessionTTL.D()
}

func (a AuthConfig) maxFailedLogins() int {
	if a.MaxFailedLogins == nil {
		return 5
	}
	return *a.MaxFailedLogins
}

func (a AuthConfig) lockoutDuration() time.Duration {
	if a.LockoutDuration.D() <= 0 {
		return 15 * time.Minute
	}
	return a.LockoutDuration.D()
}

func (a AuthConfig) loginRateLimit() int {
	if a.LoginRateLimit <= 0 {
		return 10
	}
	return a.LoginRateLimit
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
	// IncompleteMetricsGrace: a regular run whose inventory, or number of workloads with request
	// series, fell below half of the previous one is treated as incomplete metrics and not stored
	// for up to this long (the previous results stay current); after that the drop is accepted
	// as real (WO-04d). Default 30m; negative disables the check.
	IncompleteMetricsGrace Duration `json:"incompleteMetricsGrace"`

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
