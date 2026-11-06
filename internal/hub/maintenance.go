package hub

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
	"github.com/kerf-dev/kerf/internal/hub/update"
)

// Admin -> Maintenance: the versions of every component and the check for a newer Kerf
// release. Hub replicas record themselves in component_versions (RunReplicaHeartbeat), the pricing
// service through its User-Agent (recordPricing), agents report their webhook pods with the
// status batch. The leader checks for a release every updateCheck.interval (RunMaintenance).

const (
	replicaHeartbeat = 30 * time.Second
	// A hub replica not seen for this long is not shown (it stopped without deleting its row).
	replicaShownFor = 2 * time.Minute
	// Pricing reports every 5 minutes (its discovery interval).
	pricingShownFor = 15 * time.Minute
	// Hub-side rows older than this are deleted by the leader.
	componentRetention = time.Hour
	// "Check now" within this time of the last check returns the stored result.
	checkNowCoolDown = 30 * time.Second
	// Retry after a failed check (or the interval when shorter).
	failedRetry = time.Hour
	// UpgradeDocsURL is the upgrade section of the EKS install guide.
	UpgradeDocsURL = "https://github.com/kerf-dev/kerf/blob/main/docs/install/eks.md#8-upgrade"
)

// UpdateChecker checks for a newer Kerf release and stores the result in Postgres.
type UpdateChecker struct {
	Store  *store.Store
	Client *update.Client
	// Current is this hub's version.
	Current  string
	Enabled  bool
	Interval time.Duration
	Log      *slog.Logger
	Now      func() time.Time

	mu     sync.Mutex // one check at a time on this replica
	cached atomic.Pointer[store.UpdateCheck]
	// Next check due (leader loop); zero = recompute.
	due     time.Time
	dueFrom time.Time // the checked_at due was computed from
}

// NewUpdateChecker returns the checker of the hub configuration.
func NewUpdateChecker(cfg Config, st *store.Store, version string, log *slog.Logger) *UpdateChecker {
	return &UpdateChecker{
		Store:    st,
		Client:   &update.Client{URL: cfg.UpdateCheck.URL, UserAgent: "kerf-hub/" + version},
		Current:  version,
		Enabled:  cfg.UpdateCheck.On(),
		Interval: cfg.UpdateCheck.Interval.D(),
		Log:      log,
	}
}

func (u *UpdateChecker) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *UpdateChecker) log() *slog.Logger {
	if u.Log != nil {
		return u.Log
	}
	return slog.Default()
}

func (u *UpdateChecker) source() string {
	if u.Client != nil && u.Client.URL != "" {
		return u.Client.URL
	}
	return update.DefaultURL
}

// Check reads the releases now and stores the result. A failed check is stored too (state
// failed); the returned error is only a storage error.
func (u *UpdateChecker) Check(ctx context.Context) (store.UpdateCheck, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	prev, had, err := u.Store.GetUpdateCheck(ctx)
	if err != nil {
		return prev, err
	}
	now := u.now().UTC()
	row := store.UpdateCheck{CheckedAt: now, CurrentVersion: u.Current}
	if had {
		row.LatestVersion, row.ReleaseName, row.ReleaseURL, row.PublishedAt = prev.LatestVersion, prev.ReleaseName, prev.ReleaseURL, prev.PublishedAt
		row.LastSuccessAt, row.ETag = prev.LastSuccessAt, prev.ETag
	}
	// The ETag stands for the releases list the stored result was selected from; a 304 is only
	// usable when that selection was for this version and succeeded.
	etag := ""
	if had && prev.Error == "" && prev.CurrentVersion == u.Current && prev.LastSuccessAt != nil {
		etag = prev.ETag
	}
	f, ferr := u.Client.Fetch(ctx, etag)
	switch {
	case ferr != nil:
		row.Error = ferr.Error()
	case f.NotModified:
		row.LastSuccessAt = &now
	default:
		// No matching release yet (e.g. only pre-releases published): a successful check with
		// nothing newer, not a failure.
		rel, _ := update.Select(f.Releases, u.Current)
		row.LatestVersion, row.ReleaseName, row.ReleaseURL, row.PublishedAt = rel.Version(), rel.Name, rel.HTMLURL, rel.PublishedAt
		row.ETag, row.LastSuccessAt = f.ETag, &now
	}
	if err := u.Store.PutUpdateCheck(ctx, row); err != nil {
		return row, err
	}
	u.cached.Store(&row)
	st := u.Status(row, true)
	switch st.State {
	case hubapi.UpdateFailed:
		u.log().WarnContext(ctx, "update check failed", "err", row.Error, "source", u.source())
	case hubapi.UpdateAvailable:
		u.log().InfoContext(ctx, "newer Kerf release available", "current", u.Current, "latest", row.LatestVersion, "notes", row.ReleaseURL)
	default:
		u.log().InfoContext(ctx, "update check done", "state", st.State, "current", u.Current, "latest", row.LatestVersion)
	}
	return row, nil
}

// Status is the API view of a stored check (had = a check ran).
func (u *UpdateChecker) Status(row store.UpdateCheck, had bool) hubapi.UpdateStatus {
	st := hubapi.UpdateStatus{Enabled: u.Enabled, CurrentVersion: u.Current, Interval: shortDuration(u.Interval), Source: u.source()}
	if !u.Enabled {
		st.State = hubapi.UpdateDisabled
		return st
	}
	if !had {
		st.State = hubapi.UpdatePending
		return st
	}
	st.LatestVersion, st.ReleaseName, st.ReleaseURL, st.Error = row.LatestVersion, row.ReleaseName, row.ReleaseURL, row.Error
	st.PublishedAt, st.LastSuccessAt = utcPtr(row.PublishedAt), utcPtr(row.LastSuccessAt)
	checked := row.CheckedAt.UTC()
	st.CheckedAt = &checked
	switch {
	case row.Error != "":
		st.State = hubapi.UpdateFailed
	case !update.IsRelease(u.Current):
		st.State = hubapi.UpdateDevBuild
	default:
		if c, ok := update.Compare(row.LatestVersion, u.Current); ok && c > 0 {
			st.State = hubapi.UpdateAvailable
		} else {
			st.State = hubapi.UpdateUpToDate
		}
	}
	return st
}

// tick runs the check when it is due (leader, once a minute). Next check: the last one plus the
// interval and a random delay of up to a tenth of it (at most 1 h); 0-60 s after taking the lead
// when none ran yet or the last one was for another hub version; a failed check is retried after
// min(interval, 1h).
func (u *UpdateChecker) tick(ctx context.Context) {
	row, had, err := u.Store.GetUpdateCheck(ctx)
	if err != nil {
		if ctx.Err() == nil {
			u.log().WarnContext(ctx, "read update check", "err", err)
		}
		return
	}
	if had {
		u.cached.Store(&row)
	} else {
		u.cached.Store(nil)
	}
	if !u.Enabled {
		return
	}
	now := u.now()
	if u.due.IsZero() || !row.CheckedAt.Equal(u.dueFrom) {
		u.dueFrom = row.CheckedAt
		switch {
		case !had || row.CurrentVersion != u.Current:
			u.due = now.Add(jitter(time.Minute))
		case row.Error != "":
			u.due = row.CheckedAt.Add(min(u.Interval, failedRetry))
		default:
			u.due = row.CheckedAt.Add(u.Interval + jitter(min(u.Interval/10, time.Hour)))
		}
	}
	if now.Before(u.due) {
		return
	}
	u.due = time.Time{}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := u.Check(cctx); err != nil && ctx.Err() == nil {
		u.log().WarnContext(ctx, "store update check", "err", err)
	}
}

func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return rand.N(max)
}

// updateAvailableDesc is kerf_update_available (docs/contracts/metrics.md).
var updateAvailableDesc = prometheus.NewDesc("kerf_update_available",
	"1 when a newer Kerf release than this hub's version exists (latest), 0 when up to date. Absent when the update check is off, never succeeded, or the hub is a development build.",
	[]string{"current", "latest"}, nil)

// Describe implements prometheus.Collector.
func (u *UpdateChecker) Describe(ch chan<- *prometheus.Desc) { ch <- updateAvailableDesc }

// Collect implements prometheus.Collector (registered leader-only: the leader refreshes the stored
// result every minute).
func (u *UpdateChecker) Collect(ch chan<- prometheus.Metric) {
	row := u.cached.Load()
	if !u.Enabled || row == nil || row.LastSuccessAt == nil || row.LatestVersion == "" || !update.IsRelease(u.Current) {
		return
	}
	v := 0.0
	if c, ok := update.Compare(row.LatestVersion, u.Current); ok && c > 0 {
		v = 1
	}
	ch <- prometheus.MustNewConstMetric(updateAvailableDesc, prometheus.GaugeValue, v, u.Current, row.LatestVersion)
}

// RunMaintenance is the leader's maintenance work: the update check and pruning rows of stopped
// hub replicas and pricing instances. It returns when ctx is done.
func (s *Server) RunMaintenance(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	lastPrune := time.Time{}
	for {
		if s.Updates != nil {
			s.Updates.tick(ctx)
		}
		if now := s.now(); now.Sub(lastPrune) >= 10*time.Minute {
			lastPrune = now
			if n, err := s.Store.PruneComponents(ctx, now.Add(-componentRetention)); err != nil && ctx.Err() == nil {
				s.Log.WarnContext(ctx, "prune component versions", "err", err)
			} else if n > 0 {
				s.Log.InfoContext(ctx, "component versions pruned", "rows", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunReplicaHeartbeat records this replica's version every 30 s until ctx is done, then deletes
// its row.
func (s *Server) RunReplicaHeartbeat(ctx context.Context) {
	started := s.Started.UTC()
	beat := func() {
		err := s.Store.UpsertComponent(ctx, store.ComponentVersion{Component: store.ComponentHub, Instance: s.Identity,
			Version: s.Version, StartedAt: &started, SeenAt: s.now().UTC()})
		if err != nil && ctx.Err() == nil {
			s.Log.WarnContext(ctx, "record hub replica version", "err", err)
		}
	}
	beat()
	t := time.NewTicker(replicaHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := s.Store.DeleteComponent(dctx, store.ComponentHub, "", s.Identity); err != nil {
				s.Log.WarnContext(dctx, "remove hub replica version", "err", err)
			}
			return
		case <-t.C:
			beat()
		}
	}
}

// pricingUA is the pricing service's User-Agent: kerf-pricing/<version> (<pod>).
var pricingUA = regexp.MustCompile(`^kerf-pricing/([0-9A-Za-z.+_-]{1,64})(?: \(([0-9A-Za-z.-]{1,253})\))?`)

// recordPricing records the pricing service's version from its User-Agent, at most once a
// minute per instance.
func (s *Server) recordPricing(r *http.Request) {
	m := pricingUA.FindStringSubmatch(r.UserAgent())
	if m == nil {
		return
	}
	instance := m[2]
	if instance == "" {
		instance = "pricing"
	}
	now := s.now()
	key := "pricing/" + instance + "/" + m[1]
	s.mu.Lock()
	if s.componentSeen == nil {
		s.componentSeen = map[string]time.Time{}
	}
	if now.Sub(s.componentSeen[key]) < time.Minute {
		s.mu.Unlock()
		return
	}
	s.componentSeen[key] = now
	s.mu.Unlock()
	err := s.Store.UpsertComponent(r.Context(), store.ComponentVersion{Component: store.ComponentPricing, Instance: instance, Version: m[1], SeenAt: now.UTC()})
	if err != nil {
		s.Log.WarnContext(r.Context(), "record pricing version", "err", err)
	}
}

// recordAgentComponents stores the components an agent reported (absent = unchanged).
func (s *Server) recordAgentComponents(ctx context.Context, cluster string, items []hubapi.AgentComponent) error {
	if items == nil {
		return nil
	}
	rows := make([]store.ComponentVersion, 0, len(items))
	for _, c := range items {
		rows = append(rows, store.ComponentVersion{Component: c.Component, Instance: c.Instance, Version: c.Version})
	}
	return s.Store.ReplaceClusterComponents(ctx, cluster, rows, s.now().UTC())
}

func validateAgentComponents(items []hubapi.AgentComponent) error {
	if len(items) > 100 {
		return errors.New("components: at most 100 items")
	}
	for i, c := range items {
		switch {
		case c.Component != hubapi.ComponentWebhook:
			return fmt.Errorf("components[%d]: unknown component %q", i, c.Component)
		case c.Instance == "" || len(c.Instance) > 253:
			return fmt.Errorf("components[%d]: instance must be 1-253 characters", i)
		case len(c.Version) > 128:
			return fmt.Errorf("components[%d]: version longer than 128 characters", i)
		}
	}
	return nil
}

// compat compares a component's version with the hub's.
func compat(v, hub string) string {
	if v == "" {
		return hubapi.CompatUnknown
	}
	c, ok := update.Compare(v, hub)
	switch {
	case !ok:
		return hubapi.CompatUnknown
	case c < 0:
		return hubapi.CompatOlder
	case c > 0:
		return hubapi.CompatNewer
	}
	return hubapi.CompatSame
}

// getMaintenance serves GET /v1/admin/maintenance.
func (s *Server) getMaintenance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.now()
	rows, err := s.Store.Components(ctx, time.Time{})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	clusters, err := s.Store.Clusters(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	leaderApp, err := s.Store.LeaderApplicationName(ctx)
	if err != nil {
		s.Log.WarnContext(ctx, "find leader", "err", err)
	}
	hubV := s.Version
	out := hubapi.Maintenance{HubVersion: hubV, ChartVersion: s.Cfg.Maintenance.ChartVersion, Components: []hubapi.ComponentVersion{}}

	cv := func(row store.ComponentVersion, component string) hubapi.ComponentVersion {
		seen := row.SeenAt.UTC()
		return hubapi.ComponentVersion{Component: component, Cluster: row.Cluster, Instance: row.Instance, Version: row.Version,
			StartedAt: utcPtr(row.StartedAt), LastSeenAt: &seen, Compat: compat(row.Version, hubV)}
	}
	var hubs, pricing []hubapi.ComponentVersion
	webhooks := map[string][]hubapi.ComponentVersion{}
	hubVersions := map[string]bool{}
	for _, row := range rows {
		switch {
		case row.Cluster == "" && row.Component == store.ComponentHub && now.Sub(row.SeenAt) <= replicaShownFor:
			c := cv(row, hubapi.ComponentHub)
			c.Leader = leaderApp != "" && leaderApp == ElectorApplicationName(row.Instance)
			hubVersions[row.Version] = true
			hubs = append(hubs, c)
		case row.Cluster == "" && row.Component == store.ComponentPricing && now.Sub(row.SeenAt) <= pricingShownFor:
			c := cv(row, hubapi.ComponentPricing)
			c.Message = mismatchMessage(c, hubV)
			pricing = append(pricing, c)
		case row.Cluster != "" && row.Component == store.ComponentWebhook:
			c := cv(row, hubapi.ComponentWebhook)
			c.Message = mismatchMessage(c, hubV)
			webhooks[row.Cluster] = append(webhooks[row.Cluster], c)
		}
	}
	for i := range hubs {
		if len(hubVersions) > 1 {
			hubs[i].Message = "Hub replicas run different versions: a rolling upgrade is in progress (or stuck: kubectl rollout status deploy/hub)."
		}
	}
	out.Components = append(out.Components, hubs...)
	out.Components = append(out.Components, pricing...)
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Name < clusters[j].Name })
	for _, c := range clusters {
		a := hubapi.ComponentVersion{Component: hubapi.ComponentAgent, Cluster: c.Name, Version: c.AgentVersion,
			AgentStatus: "never", Compat: compat(c.AgentVersion, hubV)}
		if c.LastSeenAt != nil {
			t := c.LastSeenAt.UTC()
			a.LastSeenAt, a.AgentStatus = &t, "connected"
			if now.Sub(t) > s.Cfg.AgentStaleAfter.D() {
				a.AgentStatus = "stale"
			}
		}
		a.Message = mismatchMessage(a, hubV)
		if a.AgentStatus == "never" {
			a.Message = "The agent has not reported yet: install the chart in this cluster (Clusters → " + c.Name + " → Install)."
		}
		out.Components = append(out.Components, a)
		out.Components = append(out.Components, webhooks[c.Name]...)
	}

	row, had, err := s.Store.GetUpdateCheck(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	u := s.Updates
	if u == nil {
		u = &UpdateChecker{Current: hubV, Interval: s.Cfg.UpdateCheck.Interval.D(), Enabled: false}
	}
	out.Update = u.Status(row, had)
	// Commands also while the last check failed, if an earlier one found a newer release.
	if c, ok := update.Compare(out.Update.LatestVersion, hubV); ok && c > 0 {
		up := s.upgradeCommands(out.Update.LatestVersion)
		out.Upgrade = &up
	}
	writeJSON(w, out)
}

// mismatchMessage explains a version that differs from the hub's.
func mismatchMessage(c hubapi.ComponentVersion, hub string) string {
	where := "the hub cluster"
	if c.Cluster != "" {
		where = c.Cluster
	}
	switch c.Compat {
	case hubapi.CompatOlder:
		return fmt.Sprintf("Older than the hub (%s): upgrade the Kerf chart in %s.", hub, where)
	case hubapi.CompatNewer:
		return fmt.Sprintf("Newer than the hub (%s): upgrade the hub first, then the workload clusters.", hub)
	}
	return ""
}

// upgradeCommands are the helm upgrade commands to version (docs/install/eks.md section 8).
func (s *Server) upgradeCommands(version string) hubapi.UpgradeCommands {
	chart := s.Cfg.Onboarding.InstallChart()
	if !strings.HasPrefix(chart, "oci://") && !strings.Contains(chart, "|") {
		chart = "oci://<registry>/charts/kerf"
	}
	repoFlag := ""
	if repo, name, ok := strings.Cut(chart, "|"); ok {
		chart, repoFlag = name, " --repo "+repo
	}
	release := cmp.Or(s.Cfg.Maintenance.ReleaseName, "kerf")
	ns := cmp.Or(s.Cfg.Maintenance.ReleaseNamespace, "kerf")
	base := fmt.Sprintf("upgrade %s %s%s --version %s -n %s", release, chart, repoFlag, version, ns)
	return hubapi.UpgradeCommands{
		Version:      version,
		Chart:        chart,
		HubCommand:   "helm --kube-context <hub-cluster> " + base + " -f values-hub.yaml",
		SpokeCommand: "helm --kube-context <workload-cluster> " + base + " -f values-spoke-<cluster>.yaml --wait",
		DocsURL:      UpgradeDocsURL,
	}
}

// checkForUpdate serves POST /v1/admin/maintenance/update-check.
func (s *Server) checkForUpdate(w http.ResponseWriter, r *http.Request) {
	u := s.Updates
	if u == nil || !u.Enabled {
		writeProblem(w, r, http.StatusConflict, hubapi.CodeUpdateCheckDisabled,
			"The update check is off (chart value hub.updateCheck.enabled: false).")
		return
	}
	ctx := r.Context()
	row, had, err := s.Store.GetUpdateCheck(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !had || s.now().Sub(row.CheckedAt) >= checkNowCoolDown || row.CurrentVersion != u.Current {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if row, err = u.Check(cctx); err != nil {
			s.internal(w, r, err)
			return
		}
		had = true
		s.Log.InfoContext(ctx, "update check requested", "by", PrincipalFrom(ctx).Actor().Name)
	}
	writeJSON(w, u.Status(row, had))
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// shortDuration formats d as a Go duration without zero units (24h, not 24h0m0s).
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
