package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/kerf-dev/kerf/internal/hub/install"
	"github.com/kerf-dev/kerf/internal/recommender"
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
	Onboarding  OnboardingConfig  `json:"onboarding"`
	HA          HAConfig          `json:"ha"`
	UpdateCheck UpdateCheckConfig `json:"updateCheck"`
	Maintenance MaintenanceConfig `json:"maintenance"`
}

// UpdateCheckConfig configures the check for a newer Kerf release (WO-33, Admin -> Maintenance).
// The leader reads the public GitHub releases of kerf-dev/kerf every Interval; nothing about the
// installation is sent. Turn it off for air-gapped sites (chart value hub.updateCheck.enabled).
type UpdateCheckConfig struct {
	// Enabled (default true).
	Enabled *bool `json:"enabled"`
	// Interval between checks (default 24h, minimum 10m); a random delay of up to a tenth of it
	// (at most 1h) is added.
	Interval Duration `json:"interval"`
	// URL is the releases API (default https://api.github.com/repos/kerf-dev/kerf/releases); a
	// mirror or a test server.
	URL string `json:"url"`
}

// On reports whether the check is enabled.
func (u UpdateCheckConfig) On() bool { return u.Enabled == nil || *u.Enabled }

// MaintenanceConfig is set by the hub chart for the Maintenance page and its upgrade commands.
type MaintenanceConfig struct {
	ChartVersion     string `json:"chartVersion"`
	ReleaseName      string `json:"releaseName"`
	ReleaseNamespace string `json:"releaseNamespace"`
}

// HAConfig tunes running several hub replicas on one database (WO-18a, D-028). The defaults
// give a failover of at most 15 s after the leader dies.
type HAConfig struct {
	// LeaderRetry: how often a follower tries to take the leader lock (default 5s).
	LeaderRetry Duration `json:"leaderRetry"`
	// LeaderCheck: how often the lock connection is checked, and the timeout of that check
	// (default 2s). A replica that cannot reach Postgres stops leading within twice this.
	LeaderCheck Duration `json:"leaderCheck"`
	// RecomputePoll: how often the leader looks for queued recompute requests besides being
	// notified of new ones (default 2s).
	RecomputePoll Duration `json:"recomputePoll"`
}

// OnboardingConfig configures datasources, readiness checks and install snippets (D-025).
type OnboardingConfig struct {
	// SecretsKeyFile holds the datasource secrets encryption key(s), one per line, the first
	// encrypts (a mounted Secret; env HUB_DATASOURCE_KEY_FILE). Empty: datasources with a secret
	// (bearer, basic, mTLS) cannot be created.
	SecretsKeyFile string `json:"secretsKeyFile"`
	// DocsBaseURL is the base of the readiness docs links (docsURL = <base>/readiness#<check>).
	DocsBaseURL string `json:"docsBaseURL"`
	// CheckInterval: how often the metrics checks run per cluster (default 1h).
	CheckInterval Duration `json:"checkInterval"`
	// AgentPullInterval is the agents' config pull interval (agent.connected: within 3 of them;
	// default 1m).
	AgentPullInterval Duration `json:"agentPullInterval"`
	// HubVersion and MinAgentVersion are sent to agents for the agent.version check.
	HubVersion      string `json:"hubVersion"`
	MinAgentVersion string `json:"minAgentVersion"`
	// Install snippets of the cluster wizard (internal/hub/install, WO-18f): the hub URL as agents
	// reach it and the umbrella chart kerf (installed with global.mode spoke):
	// oci://registry/path/kerf, <repo URL>|kerf, or a local chart path.
	HubURL string `json:"hubURL"`
	Chart  string `json:"chart"`
	// ChartVersion pins the chart. Empty: hubVersion when it is a release version (the agent
	// matches the hub), else the latest.
	ChartVersion string `json:"chartVersion"`
	// ImageRegistry is the snippets' global.imageRegistry. Empty: the host of an oci:// chart.
	ImageRegistry string `json:"imageRegistry"`
	// AgentTokenExternalSecret: the snippets let the chart build the agent token Secret with the
	// External Secrets Operator (as docs/install/eks.md does). Unset: an existing Secret.
	AgentTokenExternalSecret *TokenExternalSecretConfig `json:"agentTokenExternalSecret,omitempty"`
	// Deprecated (component charts, before WO-18f): Chart defaults to the umbrella chart next to
	// AgentChart (…/agent -> …/kerf). WebhookChart is ignored.
	AgentChart   string `json:"agentChart"`
	WebhookChart string `json:"webhookChart"`
}

// TokenExternalSecretConfig: where the agent tokens are in the secret store (chart values
// global.externalSecrets.secretStoreRef and agent.hub.tokenSecret.externalSecret.remoteRef).
type TokenExternalSecretConfig struct {
	SecretStoreRef struct {
		Kind string `json:"kind"` // default ClusterSecretStore
		Name string `json:"name"`
	} `json:"secretStoreRef"`
	RemoteRef struct {
		// Key: {cluster} is replaced (default kerf/clusters/{cluster}/agent-token).
		Key      string `json:"key"`
		Property string `json:"property"` // default token
	} `json:"remoteRef"`
}

// InstallChart is the umbrella chart reference of the install snippets.
func (o OnboardingConfig) InstallChart() string {
	if o.Chart != "" {
		return o.Chart
	}
	return install.UmbrellaFromComponent(o.AgentChart)
}

var releaseVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// InstallChartVersion is the chart version of the install snippets: ChartVersion, else the hub's
// release version for a chart from a registry (a local chart path has no versions).
func (o OnboardingConfig) InstallChartVersion() string {
	chart := o.InstallChart()
	fromRegistry := strings.HasPrefix(chart, "oci://") || strings.Contains(chart, "|")
	if o.ChartVersion == "" && fromRegistry && releaseVersion.MatchString(o.HubVersion) {
		return o.HubVersion
	}
	return o.ChartVersion
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

	Step *Duration `json:"step"`
	// EndAlignment (dev/e2e): runs use data up to their time truncated to this instead of the
	// step, so a run (or POST /v1/admin/recompute) sees the last minute. Default: the step.
	EndAlignment          *Duration `json:"endAlignment"`
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
		Onboarding:        OnboardingConfig{CheckInterval: Duration(time.Hour), AgentPullInterval: Duration(time.Minute)},
		HA:                HAConfig{LeaderRetry: Duration(5 * time.Second), LeaderCheck: Duration(2 * time.Second), RecomputePoll: Duration(2 * time.Second)},
		UpdateCheck:       UpdateCheckConfig{Interval: Duration(24 * time.Hour)},
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
		"HUB_DATABASE_URL":        &cfg.DatabaseURL,
		"HUB_PROMETHEUS_URL":      &cfg.PrometheusURL,
		"HUB_LISTEN":              &cfg.Listen,
		"HUB_MIGRATIONS_DIR":      &cfg.MigrationsDir,
		"HUB_DEV_TOKENS_DIR":      &cfg.DevSeed.TokensDir,
		"HUB_DATASOURCE_KEY_FILE": &cfg.Onboarding.SecretsKeyFile,
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
	if cfg.UpdateCheck.Interval.D() < 10*time.Minute {
		return cfg, fmt.Errorf("updateCheck.interval must be at least 10m (got %s)", cfg.UpdateCheck.Interval.D())
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
	setD(&out.EndAlignment, r.EndAlignment)
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
