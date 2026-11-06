package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/notify"
)

type fakeHook struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	code   int
}

func newFakeHook(t *testing.T) *fakeHook {
	t.Helper()
	h := &fakeHook{code: 200}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.bodies = append(h.bodies, b)
		code := h.code
		h.mu.Unlock()
		if code >= 400 {
			http.Error(w, "invalid_token", code)
			return
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *fakeHook) url() string { return h.URL + "/services/T0/B0/HOOKSECRETPATH0123" }

func (h *fakeHook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

func (h *fakeHook) set(code int) {
	h.mu.Lock()
	h.code = code
	h.mu.Unlock()
}

func (h *fakeHook) all() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return string(bytes.Join(h.bodies, []byte("\n")))
}

// notifyEnv is a hub environment with notifications wired and a chart-defined channel.
func newNotifyEnv(t *testing.T) (*testEnv, *fakeHook) {
	t.Helper()
	e := newEnv(t)
	key := make([]byte, datasource.KeySize)
	_, _ = rand.Read(key)
	cipher, err := datasource.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	cfgHook := newFakeHook(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "url"), []byte(cfgHook.url()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.srv.Cfg.Notifications = NotificationsConfig{BaseURL: "https://kerf.example.com", AllowInsecureHTTP: true,
		Channels: []NotificationChannelConfig{{Name: "from-chart", Type: "slack", URLFile: filepath.Join(dir, "url")}}}
	nm := notify.NewMetrics()
	e.srv.Metrics.Registry.MustRegister(nm.Deliveries())
	e.srv.Metrics.LeaderOnly().MustRegister(nm.LastSuccess())
	if err := e.srv.InitNotifications(e.ctx, cipher, nm); err != nil {
		t.Fatal(err)
	}
	return e, cfgHook
}

const notifBase = "/v1/admin/notifications"

func jsonOf(t *testing.T, r resp, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func TestNotificationsAPI(t *testing.T) {
	e, chartHook := newNotifyEnv(t)
	hook := newFakeHook(t)

	// Status and the chart-defined channel.
	var status hubapi.NotificationStatus
	e.getJSON(notifBase+"/status", tokUI, &status)
	if !status.SecretsKey || !status.Links || status.RateLimitPerMinute != 20 || status.RetryWindowSeconds != 3600 {
		t.Errorf("status %+v", status)
	}
	var list hubapi.NotificationChannelList
	e.getJSON(notifBase+"/channels", tokUI, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "from-chart" || list.Items[0].Source != "config" {
		t.Fatalf("channels %+v", list.Items)
	}
	chart := list.Items[0]

	// Only admins write; viewers read.
	in := hubapi.NotificationChannelInput{Name: "platform", Type: "webhook", URL: hook.url(), Secret: "0123456789abcdef-signing", Reason: "alerts for the platform team"}
	e.problem(e.do(http.MethodPost, notifBase+"/channels", tokUI, in), http.StatusForbidden, "role_forbidden")
	e.problem(e.do(http.MethodPost, notifBase+"/channels", "", in), http.StatusUnauthorized, "unauthorized")
	r := e.do(http.MethodPost, notifBase+"/channels", tokAdmin, in)
	expectCode(t, "create channel", r, http.StatusCreated)
	var ch hubapi.NotificationChannel
	jsonOf(t, r, &ch)
	if ch.Version != 1 || r.header.Get("ETag") != `"1"` || !ch.SecretSet || ch.Source != "api" || ch.URL != hook.URL+"/…" {
		t.Errorf("created %+v etag %q", ch, r.header.Get("ETag"))
	}
	for _, leak := range []string{"HOOKSECRETPATH", "0123456789abcdef-signing", "/services"} {
		if strings.Contains(string(r.body), leak) {
			t.Errorf("the create response contains %q: %s", leak, r.body)
		}
	}

	// Validation, conflicts, preconditions.
	bad := in
	bad.Name, bad.URL = "Bad Name", "ftp://x"
	e.problem(e.do(http.MethodPost, notifBase+"/channels", tokAdmin, bad), http.StatusBadRequest, "invalid_notification")
	noReason := in
	noReason.Name, noReason.Reason = "other", ""
	e.problem(e.do(http.MethodPost, notifBase+"/channels", tokAdmin, noReason), http.StatusBadRequest, "invalid_reason")
	e.problem(e.do(http.MethodPost, notifBase+"/channels", tokAdmin, in), http.StatusConflict, hubapi.CodeAlreadyExists)
	upd := hubapi.NotificationChannelInput{Name: "platform-hook", Type: "webhook", Reason: "rename"}
	e.problem(e.do(http.MethodPut, notifBase+"/channels/"+ch.ID, tokAdmin, upd), http.StatusPreconditionRequired, hubapi.CodePreconditionRequired)
	e.problem(e.do(http.MethodPut, notifBase+"/channels/"+ch.ID, tokAdmin, upd, ifMatchHdr(7)...), http.StatusPreconditionFailed, hubapi.CodeVersionConflict)
	r = e.do(http.MethodPut, notifBase+"/channels/"+ch.ID, tokAdmin, upd, ifMatchHdr(1)...)
	expectCode(t, "update channel", r, http.StatusOK)
	jsonOf(t, r, &ch)
	if ch.Name != "platform-hook" || ch.Version != 2 || !ch.SecretSet {
		t.Errorf("updated %+v", ch)
	}
	e.problem(e.do(http.MethodPut, notifBase+"/channels/"+chart.ID, tokAdmin, upd, ifMatchHdr(chart.Version)...), http.StatusConflict, hubapi.CodeChannelReadOnly)
	e.problem(e.do(http.MethodDelete, notifBase+"/channels/"+chart.ID, tokAdmin, hubapi.ChangeReason{Reason: "x"}, ifMatchHdr(chart.Version)...), http.StatusConflict, hubapi.CodeChannelReadOnly)
	e.problem(e.do(http.MethodGet, notifBase+"/channels/not-a-uuid", tokUI, nil), http.StatusNotFound, "not_found")

	// Send test: works, and shows the destination's error when it refuses.
	var tr hubapi.NotificationTestResult
	e.postJSON(notifBase+"/channels/"+ch.ID+"/test", tokAdmin, nil, &tr)
	if !tr.OK || tr.HTTPStatus != 200 || hook.count() != 1 {
		t.Errorf("test result %+v, %d requests", tr, hook.count())
	}
	e.problem(e.do(http.MethodPost, notifBase+"/channels/"+ch.ID+"/test", tokUI, nil), http.StatusForbidden, "role_forbidden")
	hook.set(404)
	e.postJSON(notifBase+"/channels/"+ch.ID+"/test", tokAdmin, nil, &tr)
	if tr.OK || tr.HTTPStatus != 404 || !strings.Contains(tr.Error, "HTTP 404") || !strings.Contains(tr.Error, "invalid_token") || strings.Contains(tr.Error, "HOOKSECRET") {
		t.Errorf("failed test result %+v", tr)
	}
	e.getJSON(notifBase+"/channels/"+ch.ID, tokUI, &ch)
	if ch.LastDelivery == nil || ch.LastDelivery.Status != "failed" || !strings.Contains(ch.LastDelivery.Error, "HTTP 404") {
		t.Errorf("the channel does not show the failed test: %+v", ch.LastDelivery)
	}
	hook.set(200)
	e.postJSON(notifBase+"/channels/"+chart.ID+"/test", tokAdmin, nil, &tr)
	if !tr.OK || chartHook.count() != 1 {
		t.Errorf("test of the chart channel: %+v, %d", tr, chartHook.count())
	}

	// Rules.
	rin := hubapi.NotificationRuleInput{Name: "prod rollbacks", EventTypes: []string{"rolled_back", "failed"}, Clusters: []string{"c*"}, Namespaces: []string{"payments"}, ChannelIDs: []string{ch.ID, chart.ID}, Reason: "route rollbacks"}
	e.problem(e.do(http.MethodPost, notifBase+"/rules", tokUI, rin), http.StatusForbidden, "role_forbidden")
	badRule := rin
	badRule.Name, badRule.EventTypes, badRule.ChannelIDs = "x", []string{"exploded"}, nil
	e.problem(e.do(http.MethodPost, notifBase+"/rules", tokAdmin, badRule), http.StatusBadRequest, "invalid_notification")
	r = e.do(http.MethodPost, notifBase+"/rules", tokAdmin, rin)
	expectCode(t, "create rule", r, http.StatusCreated)
	var rule hubapi.NotificationRule
	jsonOf(t, r, &rule)
	if rule.Version != 1 || !rule.Enabled || len(rule.ChannelIDs) != 2 {
		t.Errorf("rule %+v", rule)
	}
	e.problem(e.do(http.MethodPost, notifBase+"/rules", tokAdmin, rin), http.StatusConflict, hubapi.CodeAlreadyExists)
	// A channel a rule sends to cannot be deleted.
	p := e.problem(e.do(http.MethodDelete, notifBase+"/channels/"+ch.ID, tokAdmin, hubapi.ChangeReason{Reason: "cleanup"}, ifMatchHdr(ch.Version)...), http.StatusConflict, hubapi.CodeChannelInUse)
	if !strings.Contains(p.Detail, "prod rollbacks") {
		t.Errorf("detail %q", p.Detail)
	}
	rin.Namespaces = nil
	r = e.do(http.MethodPut, notifBase+"/rules/"+rule.ID, tokAdmin, rin, ifMatchHdr(1)...)
	expectCode(t, "update rule", r, http.StatusOK)
	jsonOf(t, r, &rule)
	if rule.Version != 2 || len(rule.Namespaces) != 0 {
		t.Errorf("rule %+v", rule)
	}

	// An event from an agent reaches the matching channels through the leader's loop (within a
	// minute), and the destinations get no credentials.
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	if err := e.srv.Notify.PollEvents(ctx); err != nil { // the leader's first round: where to start reading
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); e.srv.Notify.Run(ctx) }()
	defer func() { cancel(); <-done }()
	time.Sleep(50 * time.Millisecond)
	before := hook.count()
	ev := hubapi.Event{ID: "5b1d4c40-0000-4000-8000-000000000001", Type: "rolled_back", Time: time.Now().UTC(), Reason: "OOMKills", Message: "2 OOM kills; token=sup3rs3cretvalue",
		Workload: hubapi.WorkloadRef{Namespace: "payments", Kind: "Deployment", Name: "checkout"}, Strategy: "in-place", Resources: []string{"memory"}}
	r = e.do(http.MethodPost, "/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: []hubapi.Event{ev}})
	expectCode(t, "post event", r, http.StatusOK)
	deadline := time.Now().Add(45 * time.Second)
	for (hook.count() == before || chartHook.count() < 2) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if hook.count() != before+1 || chartHook.count() != 2 {
		t.Fatalf("the rollback did not reach both channels within the deadline: webhook %d (was %d), chart channel %d", hook.count(), before, chartHook.count())
	}
	got := hook.all()
	if !strings.Contains(got, `"type":"rolled_back"`) || !strings.Contains(got, "https://kerf.example.com/workloads/c1/payments/Deployment/checkout") ||
		strings.Contains(got, "sup3rs3cretvalue") || strings.Contains(got, "HOOKSECRET") {
		t.Errorf("webhook body %s", got)
	}

	// History: filterable and paged; the metric counts the attempts.
	var dl hubapi.NotificationDeliveryList
	e.getJSON(notifBase+"/deliveries?channelId="+ch.ID+"&limit=1", tokUI, &dl)
	if len(dl.Items) != 1 || dl.Continue == "" || dl.Items[0].ChannelName != "platform-hook" {
		t.Fatalf("deliveries %+v", dl)
	}
	var dl2 hubapi.NotificationDeliveryList
	e.getJSON(notifBase+"/deliveries?channelId="+ch.ID+"&limit=10&continue="+dl.Continue, tokUI, &dl2)
	if len(dl2.Items) != 2 || dl2.Continue != "" || dl2.Items[0].ID == dl.Items[0].ID {
		t.Errorf("page 2 %+v", dl2)
	}
	var failed hubapi.NotificationDeliveryList
	e.getJSON(notifBase+"/deliveries?status=failed", tokUI, &failed)
	if len(failed.Items) != 1 || failed.Items[0].Kind != "test" || failed.Items[0].HTTPStatus != 404 {
		t.Errorf("failed deliveries %+v", failed)
	}
	e.problem(e.do(http.MethodGet, notifBase+"/deliveries?status=lost", tokUI, nil), http.StatusBadRequest, "invalid_status")
	m := e.metrics()
	if v, ok := m.find("kerf_notification_deliveries_total", map[string]string{"channel_type": "webhook", "result": "success"}); !ok || v != "2" {
		t.Errorf("success counter %q (%v)", v, ok)
	}
	if v, ok := m.find("kerf_notification_deliveries_total", map[string]string{"channel_type": "webhook", "result": "failed"}); !ok || v != "1" {
		t.Errorf("failed counter %q (%v)", v, ok)
	}

	// The configuration audit records the changes without any secret.
	var audit hubapi.ConfigChangeList
	e.getJSON("/v1/audit/config?limit=100", tokUI, &audit)
	actions := map[string]bool{}
	for _, c := range audit.Items {
		if c.Target.Type == hubapi.TargetNotification {
			actions[c.Action] = true
			if bytes.Contains(c.Before, []byte("HOOKSECRET")) || bytes.Contains(c.After, []byte("HOOKSECRET")) || bytes.Contains(c.After, []byte("0123456789abcdef")) {
				t.Errorf("the audit holds a secret: %+v", c)
			}
			if c.Actor.Name == "" || c.Reason == "" {
				t.Errorf("change without actor or reason: %+v", c)
			}
		}
	}
	for _, a := range []string{"NotificationChannelCreated", "NotificationChannelUpdated", "NotificationRuleCreated", "NotificationRuleUpdated"} {
		if !actions[a] {
			t.Errorf("no %s in the audit (%v)", a, actions)
		}
	}
}

func TestNotificationDigestAPI(t *testing.T) {
	e, _ := newNotifyEnv(t)
	hook := newFakeHook(t)
	var ch hubapi.NotificationChannel
	r := e.do(http.MethodPost, notifBase+"/channels", tokAdmin, hubapi.NotificationChannelInput{Name: "finops", Type: "slack", URL: hook.url(), Reason: "weekly summary"})
	expectCode(t, "create channel", r, http.StatusCreated)
	jsonOf(t, r, &ch)

	var d hubapi.NotificationDigest
	rr := e.getJSON(notifBase+"/digest", tokUI, &d)
	if d.Enabled || d.Version != 1 || d.Time != "09:00" || d.Timezone != "UTC" || rr.header.Get("ETag") != `"1"` || d.NextSendAt != nil {
		t.Errorf("default digest %+v", d)
	}
	// Needs a channel before it can be sent or enabled.
	e.problem(e.do(http.MethodPost, notifBase+"/digest/send", tokAdmin, nil), http.StatusConflict, "no_channels")
	in := hubapi.NotificationDigestInput{Enabled: true, Weekday: 1, Time: "08:30", Timezone: "Europe/Berlin", ChannelIDs: []string{ch.ID}, Reason: "weekly digest"}
	e.problem(e.do(http.MethodPut, notifBase+"/digest", tokAdmin, in), http.StatusPreconditionRequired, hubapi.CodePreconditionRequired)
	bad := in
	bad.Time, bad.Timezone = "8:30", "Mars/Base"
	e.problem(e.do(http.MethodPut, notifBase+"/digest", tokAdmin, bad, ifMatchHdr(1)...), http.StatusBadRequest, "invalid_notification")
	e.problem(e.do(http.MethodPut, notifBase+"/digest", tokUI, in, ifMatchHdr(1)...), http.StatusForbidden, "role_forbidden")
	r = e.do(http.MethodPut, notifBase+"/digest", tokAdmin, in, ifMatchHdr(1)...)
	expectCode(t, "update digest", r, http.StatusOK)
	jsonOf(t, r, &d)
	if !d.Enabled || d.Version != 2 || d.NextSendAt == nil || d.NextSendAt.Weekday() != time.Monday || d.NextSendAt.Before(time.Now()) {
		t.Errorf("digest %+v", d)
	}
	e.problem(e.do(http.MethodPut, notifBase+"/digest", tokAdmin, in, ifMatchHdr(1)...), http.StatusPreconditionFailed, hubapi.CodeVersionConflict)

	// "Send now": the leader sends it; it carries the hub's savings and opportunities.
	var q hubapi.NotificationDigestQueued
	r = e.do(http.MethodPost, notifBase+"/digest/send", tokAdmin, nil)
	expectCode(t, "send digest", r, http.StatusAccepted)
	jsonOf(t, r, &q)
	if q.Queued != 1 {
		t.Fatalf("queued %d", q.Queued)
	}
	e.srv.Notify.SendDue(e.ctx)
	if hook.count() != 1 {
		t.Fatalf("%d digests sent", hook.count())
	}
	body := hook.all()
	for _, want := range []string{"Estimated savings", "Top opportunities", "applied, 0 rolled back", "/workloads/c"} {
		if !strings.Contains(body, want) {
			t.Errorf("digest lacks %q: %s", want, body)
		}
	}
	// Without a clean-up of the cluster state the digest names the clusters that need a look; the
	// test clusters never connected an agent.
	if !strings.Contains(body, "the agent has never connected") {
		t.Errorf("no cluster needing attention in %s", body)
	}
	if strings.Contains(body, "Realized") {
		t.Error("realized savings without a source")
	}
}
