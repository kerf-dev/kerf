package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/notify"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// Notifications (docs/notifications.md): Slack, Microsoft Teams and signed webhook channels, rules,
// the weekly digest and the delivery history. The leader sends (RunLeader starts Notify.Run).

// NotificationsConfig configures notifications (chart values `notifications`).
type NotificationsConfig struct {
	// BaseURL is the web UI's address; messages link to <baseURL>/workloads/... Empty: no links.
	BaseURL string `json:"baseURL"`
	// AllowInsecureHTTP accepts http:// channel URLs (a receiver inside the cluster, tests).
	AllowInsecureHTTP bool `json:"allowInsecureHTTP"`
	// RateLimitPerMinute is the per-channel limit (default 20).
	RateLimitPerMinute int `json:"rateLimitPerMinute"`
	// Channels are channels defined in the chart values. They appear read-only in the UI; their URL
	// (and signing secret) are read from the files of a mounted Secret.
	Channels []NotificationChannelConfig `json:"channels"`
}

// NotificationChannelConfig is a chart-defined channel.
type NotificationChannelConfig struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// URLFile holds the webhook URL; SecretFile (webhook channels, optional) the signing secret.
	URLFile    string `json:"urlFile"`
	SecretFile string `json:"secretFile"`
	Enabled    *bool  `json:"enabled"`
}

// InitNotifications wires notifications and syncs the chart-defined channels. cipher may be nil:
// channels (their URL is a secret) cannot be stored then.
func (s *Server) InitNotifications(ctx context.Context, cipher notify.Sealer, m *notify.Metrics) error {
	svc := &notify.Service{
		Repo: &notify.PG{DB: s.Store.Pool}, Sealer: cipher,
		Sender:    &notify.Sender{UserAgent: "kerf-hub/" + s.Version},
		Links:     notify.Links{Base: strings.TrimRight(s.Cfg.Notifications.BaseURL, "/")},
		AllowHTTP: s.Cfg.Notifications.AllowInsecureHTTP, RateLimit: s.Cfg.Notifications.RateLimitPerMinute,
		Metrics: m, Log: s.Log.With("component", "notify"), Now: s.Now,
		Digest: &digestSource{s: s},
	}
	s.Notify = svc
	return s.syncConfigChannels(ctx, svc)
}

func (s *Server) syncConfigChannels(ctx context.Context, svc *notify.Service) error {
	var want []notify.Channel
	for _, c := range s.Cfg.Notifications.Channels {
		raw, err := os.ReadFile(c.URLFile)
		if err != nil {
			return fmt.Errorf("notifications.channels %q: read url: %w", c.Name, err)
		}
		var secret []byte
		if c.SecretFile != "" {
			if secret, err = os.ReadFile(c.SecretFile); err != nil {
				return fmt.Errorf("notifications.channels %q: read secret: %w", c.Name, err)
			}
		}
		ch, err := svc.ConfigChannel(c.Name, c.Type, strings.TrimSpace(string(raw)), strings.TrimSpace(string(secret)), c.Enabled == nil || *c.Enabled)
		if err != nil {
			return fmt.Errorf("notifications.channels %q: %w", c.Name, err)
		}
		want = append(want, ch)
	}
	// Also with nothing configured: channels that used to be are retired.
	return svc.Repo.SyncConfigChannels(ctx, want)
}

// notifyOn answers 501 when notifications are not wired (tests that do not need them).
func (s *Server) notifyOn(w http.ResponseWriter, r *http.Request) bool {
	if s.Notify == nil {
		writeProblem(w, r, http.StatusNotImplemented, hubapi.CodeNotImplemented, "Notifications are not available.")
		return false
	}
	return true
}

func (s *Server) notifyError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *notify.ValidationError
	var iu *notify.InUseError
	switch {
	case errors.As(err, &ve):
		badRequest(w, r, "invalid_notification", ve.Error())
	case errors.As(err, &iu):
		writeProblem(w, r, http.StatusConflict, hubapi.CodeChannelInUse, "The channel is "+iu.Error()+"; remove it there first.")
	case errors.Is(err, notify.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not_found", "Unknown notification object.")
	case errors.Is(err, notify.ErrVersionConflict):
		writeProblem(w, r, http.StatusPreconditionFailed, hubapi.CodeVersionConflict, "It was changed by someone else; reload it.")
	case errors.Is(err, notify.ErrNameTaken):
		writeProblem(w, r, http.StatusConflict, hubapi.CodeAlreadyExists, "An object with this name already exists.")
	case errors.Is(err, notify.ErrSecretsKeyMissing):
		writeProblem(w, r, http.StatusConflict, hubapi.CodeSecretsKeyMissing, "The hub has no secrets encryption key (onboarding.secretsKey), so webhook URLs cannot be stored.")
	case errors.Is(err, notify.ErrReadOnly):
		writeProblem(w, r, http.StatusConflict, hubapi.CodeChannelReadOnly, "The channel is defined in the chart values; change it there.")
	case errors.Is(err, notify.ErrNoChannels):
		writeProblem(w, r, http.StatusConflict, "no_channels", "Choose at least one enabled channel for the digest first.")
	default:
		s.internal(w, r, err)
	}
}

func (s *Server) notifyChange(r *http.Request, action, reason, id, name string, version int, before, after any) store.Change {
	c := s.change(r, action, reason)
	c.Target = hubapi.ConfigTarget{Type: hubapi.TargetNotification, ID: id, Name: name}
	c.Version, c.Before, c.After = version, before, after
	return c
}

func (s *Server) recordNotifyChange(r *http.Request, c store.Change) {
	if err := s.Store.RecordChange(r.Context(), c); err != nil {
		s.Log.WarnContext(r.Context(), "config audit", "err", err)
	}
}

// getNotificationStatus serves GET /v1/admin/notifications/status.
func (s *Server) getNotificationStatus(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	writeJSON(w, s.Notify.Status())
}

// listNotificationChannels serves GET /v1/admin/notifications/channels.
func (s *Server) listNotificationChannels(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	items, err := s.Notify.ListChannels(r.Context())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	writeJSON(w, hubapi.NotificationChannelList{Items: items})
}

// getNotificationChannel serves GET /v1/admin/notifications/channels/{channelId}.
func (s *Server) getNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	c, err := s.Notify.GetChannel(r.Context(), r.PathValue("channelId"))
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	setETag(w, c.Version)
	writeJSON(w, c)
}

// createNotificationChannel serves POST /v1/admin/notifications/channels.
func (s *Server) createNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	var in hubapi.NotificationChannelInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	c, err := s.Notify.CreateChannel(r.Context(), in, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	s.recordNotifyChange(r, s.notifyChange(r, hubapi.ActionNotificationChannelCreated, in.Reason, c.ID, c.Name, c.Version, nil, c))
	setETag(w, c.Version)
	writeJSONStatus(w, http.StatusCreated, c)
}

// updateNotificationChannel serves PUT /v1/admin/notifications/channels/{channelId}.
func (s *Server) updateNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.NotificationChannelInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	before, after, err := s.Notify.UpdateChannel(r.Context(), r.PathValue("channelId"), ver, in, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	s.recordNotifyChange(r, s.notifyChange(r, hubapi.ActionNotificationChannelUpdated, in.Reason, after.ID, after.Name, after.Version, before, after))
	setETag(w, after.Version)
	writeJSON(w, after)
}

// deleteNotificationChannel serves DELETE /v1/admin/notifications/channels/{channelId}.
func (s *Server) deleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.ChangeReason
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	c, err := s.Notify.DeleteChannel(r.Context(), r.PathValue("channelId"), ver, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	s.recordNotifyChange(r, s.notifyChange(r, hubapi.ActionNotificationChannelDeleted, in.Reason, c.ID, c.Name, c.Version+1, c, nil))
	w.WriteHeader(http.StatusNoContent)
}

// testNotificationChannel serves POST /v1/admin/notifications/channels/{channelId}/test.
func (s *Server) testNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	res, err := s.Notify.TestChannel(r.Context(), r.PathValue("channelId"))
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	writeJSON(w, res)
}

// listNotificationRules serves GET /v1/admin/notifications/rules.
func (s *Server) listNotificationRules(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	items, err := s.Notify.ListRules(r.Context())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	writeJSON(w, hubapi.NotificationRuleList{Items: items})
}

// createNotificationRule serves POST /v1/admin/notifications/rules.
func (s *Server) createNotificationRule(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	var in hubapi.NotificationRuleInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	rule, err := s.Notify.CreateRule(r.Context(), in, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	s.recordNotifyChange(r, s.notifyChange(r, hubapi.ActionNotificationRuleCreated, in.Reason, rule.ID, rule.Name, rule.Version, nil, rule))
	setETag(w, rule.Version)
	writeJSONStatus(w, http.StatusCreated, rule)
}

// updateNotificationRule serves PUT /v1/admin/notifications/rules/{ruleId}.
func (s *Server) updateNotificationRule(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.NotificationRuleInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	before, after, err := s.Notify.UpdateRule(r.Context(), r.PathValue("ruleId"), ver, in, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	s.recordNotifyChange(r, s.notifyChange(r, hubapi.ActionNotificationRuleUpdated, in.Reason, after.ID, after.Name, after.Version, before, after))
	setETag(w, after.Version)
	writeJSON(w, after)
}

// deleteNotificationRule serves DELETE /v1/admin/notifications/rules/{ruleId}.
func (s *Server) deleteNotificationRule(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.ChangeReason
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	rule, err := s.Notify.DeleteRule(r.Context(), r.PathValue("ruleId"), ver, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	s.recordNotifyChange(r, s.notifyChange(r, hubapi.ActionNotificationRuleDeleted, in.Reason, rule.ID, rule.Name, rule.Version+1, rule, nil))
	w.WriteHeader(http.StatusNoContent)
}

// getNotificationDigest serves GET /v1/admin/notifications/digest.
func (s *Server) getNotificationDigest(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	d, err := s.Notify.GetDigest(r.Context())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	setETag(w, d.Version)
	writeJSON(w, d)
}

// updateNotificationDigest serves PUT /v1/admin/notifications/digest.
func (s *Server) updateNotificationDigest(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	ver, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var in hubapi.NotificationDigestInput
	if !decodeBody(w, r, &in) || !validReason(w, r, in.Reason) {
		return
	}
	before, after, err := s.Notify.UpdateDigest(r.Context(), ver, in, PrincipalFrom(r.Context()).Actor())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	s.recordNotifyChange(r, s.notifyChange(r, hubapi.ActionNotificationDigestUpdated, in.Reason, "digest", "weekly digest", after.Version, before, after))
	setETag(w, after.Version)
	writeJSON(w, after)
}

// sendNotificationDigest serves POST /v1/admin/notifications/digest/send.
func (s *Server) sendNotificationDigest(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	n, err := s.Notify.QueueDigest(r.Context())
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, hubapi.NotificationDigestQueued{Queued: n})
}

// listNotificationDeliveries serves GET /v1/admin/notifications/deliveries.
func (s *Server) listNotificationDeliveries(w http.ResponseWriter, r *http.Request) {
	if !s.notifyOn(w, r) {
		return
	}
	q := r.URL.Query()
	f := notify.DeliveryFilter{ChannelID: q.Get("channelId"), Status: q.Get("status")}
	if f.ChannelID != "" {
		if _, err := uuid.Parse(f.ChannelID); err != nil {
			badRequest(w, r, "invalid_channel", "channelId must be a UUID.")
			return
		}
	}
	if f.Status != "" && !slices.Contains(hubapi.NotificationDeliveryStatuses, f.Status) {
		badRequest(w, r, "invalid_status", "status must be pending, sent or failed.")
		return
	}
	var err error
	if f.Limit, err = intParam(q.Get("limit"), 50, 200); err != nil {
		badRequest(w, r, "invalid_limit", "limit "+err.Error())
		return
	}
	if c := q.Get("continue"); c != "" {
		cur, err := decodeCursor(c)
		if err != nil || cur.Offset < 1 {
			badRequest(w, r, "invalid_continue", "Invalid continue token.")
			return
		}
		f.Offset = cur.Offset
	}
	items, more, err := s.Notify.Repo.ListDeliveries(r.Context(), f)
	if err != nil {
		s.notifyError(w, r, err)
		return
	}
	out := hubapi.NotificationDeliveryList{Items: make([]hubapi.NotificationDelivery, 0, len(items))}
	for _, d := range items {
		out.Items = append(out.Items, notify.DeliveryAPI(d))
	}
	if more {
		out.Continue = encodeCursor(cursor{Offset: f.Offset + f.Limit})
	}
	writeJSON(w, out)
}

// ---- the digest's data ----

// digestSource gives the weekly digest the hub's recommendation data.
type digestSource struct{ s *Server }

func (d *digestSource) EstimatedSavings(ctx context.Context, now, previous time.Time) (cur, before float64, ok bool, err error) {
	views, err := d.s.Store.LoadWorkloads(ctx, store.WorkloadFilter{})
	if err != nil {
		return 0, 0, false, err
	}
	perCluster := map[string][]*store.WorkloadView{}
	for _, v := range views {
		perCluster[v.Key.Cluster] = append(perCluster[v.Key.Cluster], v)
	}
	// As GET /v1/summary: the sum of the rounded cluster totals.
	for _, vs := range perCluster {
		cur += clusterSavings(vs).Total
	}
	samples, err := d.s.Store.SavingsSamples(ctx, nil, previous.Add(-24*time.Hour), previous)
	if err != nil {
		return cur, 0, false, err
	}
	// The total of each cluster as it was at `previous`: its last sample at or before then.
	last := map[string]store.SavingsSample{}
	for _, x := range samples {
		if !x.At.After(previous) {
			last[x.Cluster] = x
		}
	}
	for _, x := range last {
		before += round2(round2(x.CPU) + round2(x.Memory))
	}
	return cur, round2(before), len(last) > 0, nil
}

func (d *digestSource) TopOpportunities(ctx context.Context, n int) ([]notify.Opportunity, error) {
	views, err := d.s.Store.LoadWorkloads(ctx, store.WorkloadFilter{})
	if err != nil {
		return nil, err
	}
	var out []notify.Opportunity
	for _, v := range views {
		if v.Rec == nil || v.Rec.MonthlySavingsUSD.Total <= 0 {
			continue
		}
		out = append(out, notify.Opportunity{Cluster: v.Key.Cluster, Namespace: v.Key.Namespace, Kind: v.Key.Kind, Name: v.Key.Name,
			MonthlySavingsUSD: round2(v.Rec.MonthlySavingsUSD.Total)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MonthlySavingsUSD != out[j].MonthlySavingsUSD {
			return out[i].MonthlySavingsUSD > out[j].MonthlySavingsUSD
		}
		return out[i].Cluster+out[i].Namespace+out[i].Name < out[j].Cluster+out[j].Namespace+out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

func (d *digestSource) Attention(ctx context.Context) ([]notify.Attention, error) {
	clusters, err := d.s.Store.ClusterRows(ctx, false)
	if err != nil {
		return nil, err
	}
	automation, err := d.s.Store.Automations(ctx)
	if err != nil {
		return nil, err
	}
	var states map[string]rd.State
	if d.s.Onb != nil {
		if states, err = d.s.Onb.Readiness.States(ctx); err != nil {
			return nil, err
		}
	}
	now := d.s.now()
	var out []notify.Attention
	for _, c := range clusters {
		var why []string
		switch {
		case c.LastSeenAt == nil:
			why = append(why, "the agent has never connected")
		case now.Sub(*c.LastSeenAt) > d.s.Cfg.AgentStaleAfter.D():
			why = append(why, "the agent has been silent for "+strconv.Itoa(int(now.Sub(*c.LastSeenAt).Hours()))+" h")
		}
		if st, ok := states[c.Name]; ok && !st.AtLeast(rd.StateAutomationReady) {
			why = append(why, "readiness is "+string(st))
		}
		if a := automation[c.Name]; a.Paused {
			why = append(why, "automation is paused")
		} else if f := automation["*"]; f.Paused {
			why = append(why, "automation is paused fleet-wide")
		}
		if len(why) > 0 {
			out = append(out, notify.Attention{Cluster: c.Name, Reasons: why})
		}
	}
	return out, nil
}
