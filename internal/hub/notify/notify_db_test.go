package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/kerf-dev/kerf/internal/hub/datasource"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/store"
)

// These tests need Postgres: KERF_TEST_DATABASE_URL is a superuser URL, as for the other hub tests;
// each test creates and drops its own database.

const signingSecret = "0123456789abcdef-signing"

var admin = hubapi.Actor{Type: "user", Name: "tester", Role: "admin"}

// receiver is a fake Slack, Teams or webhook endpoint. Its path is the credential.
type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	hdrs   []http.Header
	status []int // answers in order; the last one repeats
	n      int
}

func newReceiver(t *testing.T, status ...int) *receiver {
	t.Helper()
	r := &receiver{status: status}
	if len(status) == 0 {
		r.status = []int{200}
	}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, b)
		r.hdrs = append(r.hdrs, req.Header.Clone())
		code := r.status[min(r.n, len(r.status)-1)]
		r.n++
		r.mu.Unlock()
		if code >= 400 {
			http.Error(w, "nope", code)
			return
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) url() string { return r.URL + "/services/T0000/B0000/PATHSECRET0123456789" }

func (r *receiver) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *receiver) last() ([]byte, http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bodies[len(r.bodies)-1], r.hdrs[len(r.hdrs)-1]
}

type env struct {
	t     *testing.T
	st    *store.Store
	svc   *Service
	ctx   context.Context
	clock *fakeClock
	met   *Metrics
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeDigest struct{ attention []Attention }

func (fakeDigest) EstimatedSavings(context.Context, time.Time, time.Time) (float64, float64, bool, error) {
	return 1234, 1000, true, nil
}

func (fakeDigest) TopOpportunities(context.Context, int) ([]Opportunity, error) {
	return []Opportunity{{Cluster: "prod-eu", Namespace: "payments", Kind: "Deployment", Name: "checkout", MonthlySavingsUSD: 410}}, nil
}

func (f fakeDigest) Attention(context.Context) ([]Attention, error) { return f.attention, nil }

func newEnv(t *testing.T) *env {
	t.Helper()
	base := os.Getenv("KERF_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("KERF_TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "kerf_hub_test_" + hex.EncodeToString(b)
	adm, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adm.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adm.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		adm.Close(context.Background())
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if _, err := st.Migrate(ctx, os.DirFS("../../../db/migrations")); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, datasource.KeySize)
	_, _ = rand.Read(key)
	cipher, err := datasource.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Now().UTC().Truncate(time.Second)}
	met := NewMetrics()
	e := &env{t: t, st: st, ctx: ctx, clock: clock, met: met}
	e.svc = &Service{Repo: &PG{DB: st.Pool}, Sealer: cipher, AllowHTTP: true, Metrics: met, Now: clock.Now,
		Links: Links{Base: "https://kerf.example.com"}, Digest: fakeDigest{}, RateLimit: 1000}
	for _, c := range []string{"prod-eu", "dev-1"} {
		if err := st.EnsureCluster(ctx, c, ""); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *env) channel(name, typ, rawURL, secret string) hubapi.NotificationChannel {
	e.t.Helper()
	c, err := e.svc.CreateChannel(e.ctx, hubapi.NotificationChannelInput{Name: name, Type: typ, URL: rawURL, Secret: secret, Reason: "test"}, admin)
	if err != nil {
		e.t.Fatalf("create channel %s: %v", name, err)
	}
	return c
}

func (e *env) rule(name string, types, clusters, namespaces []string, channels ...hubapi.NotificationChannel) hubapi.NotificationRule {
	e.t.Helper()
	var ids []string
	for _, c := range channels {
		ids = append(ids, c.ID)
	}
	r, err := e.svc.CreateRule(e.ctx, hubapi.NotificationRuleInput{Name: name, EventTypes: types, Clusters: clusters, Namespaces: namespaces, ChannelIDs: ids, Reason: "test"}, admin)
	if err != nil {
		e.t.Fatalf("create rule %s: %v", name, err)
	}
	return r
}

var evSeq int

func (e *env) emit(typ, cluster, ns, name, reason string) string {
	e.t.Helper()
	evSeq++
	id := "00000000-0000-4000-8000-" + strings.Repeat("0", 12-len(hex.EncodeToString([]byte{byte(evSeq)}))) + hex.EncodeToString([]byte{byte(evSeq)})
	ev := hubapi.Event{ID: id, Type: typ, Time: e.clock.Now(), Reason: reason, Message: "something happened with token=hunter2hunter2",
		Workload: hubapi.WorkloadRef{Namespace: ns, Kind: "Deployment", Name: name}, Strategy: "in-place", Resources: []string{"memory"},
		Containers: []hubapi.ContainerChange{{Name: "app",
			From: &hubapi.Resources{Requests: &hubapi.ResourceValues{Memory: "384Mi"}}, To: &hubapi.Resources{Requests: &hubapi.ResourceValues{Memory: "1Gi"}}}},
		Details: map[string]string{"oomKills": "2", "error": "password=hunter2"}}
	if _, _, err := e.st.InsertEvents(e.ctx, cluster, []hubapi.Event{ev}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// poll runs one round of the leader: read new events, send what is due.
func (e *env) poll() {
	e.t.Helper()
	if err := e.svc.PollEvents(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	e.svc.SendDue(e.ctx)
}

func (e *env) deliveries(channelID string) []hubapi.NotificationDelivery {
	e.t.Helper()
	ds, _, err := e.svc.Repo.ListDeliveries(e.ctx, DeliveryFilter{ChannelID: channelID, Limit: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []hubapi.NotificationDelivery
	for _, d := range ds {
		out = append(out, DeliveryAPI(d))
	}
	return out
}

// start sets the read position of the leader, as its first round does.
func (e *env) start() {
	e.t.Helper()
	if err := e.svc.PollEvents(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func TestEventsReachTheMatchingChannels(t *testing.T) {
	e := newEnv(t)
	slack, teams, hook := newReceiver(t), newReceiver(t), newReceiver(t)
	cSlack := e.channel("slack-alerts", hubapi.NotificationSlack, slack.url(), "")
	cTeams := e.channel("teams-payments", hubapi.NotificationTeams, teams.url(), "")
	cHook := e.channel("pagerduty", hubapi.NotificationWebhook, hook.url(), signingSecret)
	e.rule("rollbacks", []string{"rolled_back", "failed"}, []string{"prod-*"}, nil, cSlack, cHook)
	e.rule("payments applied", []string{"applied"}, nil, []string{"payments"}, cTeams)
	e.start()

	// Nothing is sent for what happened before.
	e.emit("applied", "prod-eu", "search", "indexer", "RecommendationApplied") // other namespace: no rule
	rbID := e.emit("rolled_back", "prod-eu", "payments", "checkout", "OOMKills")
	e.emit("rolled_back", "dev-1", "payments", "checkout", "OOMKills") // other cluster: no rule
	e.emit("applied", "prod-eu", "payments", "checkout", "RecommendationApplied")
	e.poll()

	if slack.calls() != 1 || hook.calls() != 1 || teams.calls() != 1 {
		t.Fatalf("deliveries: slack %d, webhook %d, teams %d; want 1 each", slack.calls(), hook.calls(), teams.calls())
	}
	body, _ := slack.last()
	var sm struct{ Text string }
	_ = json.Unmarshal(body, &sm)
	if !strings.Contains(sm.Text, "Rolled back payments/checkout on prod-eu (OOMKills)") {
		t.Errorf("slack text %q", sm.Text)
	}
	body, h := hook.last()
	if !Verify(signingSecret, body, h.Get(HeaderSignature)) {
		t.Errorf("the webhook signature does not verify: %q", h.Get(HeaderSignature))
	}
	if h.Get(HeaderDelivery) != rbID {
		t.Errorf("delivery header %q, want the event id %s", h.Get(HeaderDelivery), rbID)
	}
	var env struct {
		Version int `json:"version"`
		Event   struct {
			Type, Reason, Cluster, Namespace, Link, Message string
			Details                                         map[string]string
		} `json:"event"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Version != 1 || env.Event.Type != "rolled_back" || env.Event.Link != "https://kerf.example.com/workloads/prod-eu/payments/Deployment/checkout" {
		t.Errorf("payload %s", body)
	}
	// Messages never carry credentials from event text or details.
	for _, b := range [][]byte{body, mustLast(slack), mustLast(teams)} {
		if bytes.Contains(b, []byte("hunter2")) {
			t.Errorf("a message leaks event text: %s", b)
		}
	}
	if env.Event.Details["error"] != "" || env.Event.Details["oomKills"] != "2" {
		t.Errorf("details %v", env.Event.Details)
	}

	// Polling again (the overlap re-reads events) sends nothing twice.
	e.clock.Advance(time.Minute)
	e.poll()
	if slack.calls() != 1 || hook.calls() != 1 || teams.calls() != 1 {
		t.Errorf("repeated delivery: slack %d, webhook %d, teams %d", slack.calls(), hook.calls(), teams.calls())
	}

	// The channel list shows the status and nothing secret; the history holds no URL either.
	cs, err := e.svc.ListChannels(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.LastDelivery == nil || c.LastDelivery.Status != "sent" || c.LastSuccessAt == nil || c.Pending != 0 {
			t.Errorf("channel %s: %+v", c.Name, c)
		}
		raw, _ := json.Marshal(c)
		if bytes.Contains(raw, []byte("PATHSECRET")) || bytes.Contains(raw, []byte(signingSecret)) || strings.Contains(c.URL, "/services") {
			t.Errorf("channel %s exposes a secret: %s", c.Name, raw)
		}
	}
	if !cs[0].SecretSet && cs[1].Name == "teams-payments" {
		t.Log("webhook secret flag checked below")
	}
	for _, c := range []hubapi.NotificationChannel{cSlack, cTeams, cHook} {
		for _, d := range e.deliveries(c.ID) {
			raw, _ := json.Marshal(d)
			if d.Status != "sent" || d.Attempts != 1 || bytes.Contains(raw, []byte("PATHSECRET")) {
				t.Errorf("delivery %+v", d)
			}
		}
	}
	var payloads string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT string_agg(payload::text, ' ') FROM notification_deliveries`).Scan(&payloads); err != nil || strings.Contains(payloads, "PATHSECRET") || strings.Contains(payloads, "hunter2") {
		t.Errorf("stored payloads hold a secret (%v)", err)
	}
	// Encrypted at rest.
	var blob []byte
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT secret_enc FROM notification_channels WHERE name = 'pagerduty'`).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("PATHSECRET")) || bytes.Contains(blob, []byte(signingSecret)) || len(blob) < 30 {
		t.Error("the channel's URL and secret are not encrypted at rest")
	}
	if got := testutil.ToFloat64(e.met.deliveries.WithLabelValues("slack", "success")); got != 1 {
		t.Errorf("success counter %v", got)
	}
}

func mustLast(r *receiver) []byte { b, _ := r.last(); return b }

func TestRetriesBackOffAndGiveUpAfterAnHour(t *testing.T) {
	e := newEnv(t)
	flaky := newReceiver(t, 500, 503, 200)
	c := e.channel("flaky", hubapi.NotificationWebhook, flaky.url(), "")
	e.rule("all", []string{"rolled_back"}, nil, nil, c)
	e.start()
	e.emit("rolled_back", "prod-eu", "payments", "checkout", "OOMKills")

	e.poll() // attempt 1 fails
	ch, _ := e.svc.GetChannel(e.ctx, c.ID)
	if flaky.calls() != 1 || ch.LastDelivery == nil || ch.LastDelivery.Status != "retrying" || !strings.Contains(ch.LastDelivery.Error, "HTTP 500") || ch.Pending != 1 {
		t.Fatalf("after attempt 1: calls %d, channel %+v", flaky.calls(), ch)
	}
	ds := e.deliveries(c.ID)
	if len(ds) != 1 || ds[0].Status != "pending" || ds[0].Attempts != 1 || ds[0].NextAttemptAt == nil || ds[0].NextAttemptAt.Sub(e.clock.Now()) != 30*time.Second {
		t.Fatalf("deliveries %+v", ds)
	}
	e.clock.Advance(10 * time.Second)
	e.poll()
	if flaky.calls() != 1 {
		t.Error("retried before the backoff passed")
	}
	e.clock.Advance(21 * time.Second) // 31 s: attempt 2 fails, next in 1 min
	e.poll()
	if flaky.calls() != 2 {
		t.Fatalf("calls %d", flaky.calls())
	}
	e.clock.Advance(59 * time.Second)
	e.poll()
	if flaky.calls() != 2 {
		t.Error("retried before the second backoff passed")
	}
	e.clock.Advance(2 * time.Second) // attempt 3 succeeds
	e.poll()
	ds = e.deliveries(c.ID)
	if flaky.calls() != 3 || ds[0].Status != "sent" || ds[0].Attempts != 3 {
		t.Fatalf("after the third attempt: calls %d, %+v", flaky.calls(), ds)
	}
	ch, _ = e.svc.GetChannel(e.ctx, c.ID)
	if ch.LastDelivery.Status != "sent" || ch.LastDelivery.Error != "" {
		t.Errorf("channel %+v", ch.LastDelivery)
	}
	for res, want := range map[string]float64{"success": 1, "retry": 2, "failed": 0} {
		if got := testutil.ToFloat64(e.met.deliveries.WithLabelValues("webhook", res)); got != want {
			t.Errorf("counter %s = %v, want %v", res, got, want)
		}
	}

	// A destination that never recovers: retried for an hour, then failed.
	down := newReceiver(t, 503)
	c2 := e.channel("down", hubapi.NotificationSlack, down.url(), "")
	e.rule("down", []string{"failed"}, nil, nil, c2)
	e.emit("failed", "prod-eu", "payments", "checkout", "ApplyError")
	start := e.clock.Now()
	for e.clock.Now().Sub(start) <= 70*time.Minute {
		e.poll()
		e.clock.Advance(time.Minute)
	}
	ds = e.deliveries(c2.ID)
	if len(ds) != 1 || ds[0].Status != "failed" || ds[0].Attempts < 6 || !strings.Contains(ds[0].LastError, "HTTP 503") || ds[0].NextAttemptAt != nil {
		t.Fatalf("after an hour: %+v", ds)
	}
	calls := down.calls()
	e.clock.Advance(time.Hour)
	e.poll()
	if down.calls() != calls {
		t.Error("a failed delivery is retried again")
	}
	ch2, _ := e.svc.GetChannel(e.ctx, c2.ID)
	if ch2.LastDelivery.Status != "failed" || testutil.ToFloat64(e.met.deliveries.WithLabelValues("slack", "failed")) != 1 {
		t.Errorf("channel %+v", ch2.LastDelivery)
	}
}

func TestRevokedWebhookFailsAtOnceWithoutTheURL(t *testing.T) {
	e := newEnv(t)
	gone := newReceiver(t, 404)
	c := e.channel("gone", hubapi.NotificationSlack, gone.url(), "")
	e.rule("all", []string{"blocked"}, nil, nil, c)
	e.start()
	e.emit("blocked", "prod-eu", "payments", "checkout", "FreezeWindow")
	e.poll()
	ds := e.deliveries(c.ID)
	if gone.calls() != 1 || len(ds) != 1 || ds[0].Status != "failed" || ds[0].HTTPStatus != 404 || strings.Contains(ds[0].LastError, "PATHSECRET") {
		t.Fatalf("calls %d, %+v", gone.calls(), ds)
	}
}

func TestBlockedIsSentOncePerWorkloadAndReason(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	c := e.channel("ops", hubapi.NotificationSlack, r.url(), "")
	e.rule("blocked", []string{"blocked"}, nil, nil, c)
	e.start()
	for range 3 { // the recommender keeps hitting the same guardrail
		e.emit("blocked", "prod-eu", "payments", "checkout", "FreezeWindow")
	}
	e.emit("blocked", "prod-eu", "payments", "checkout", "Cooldown")    // another reason
	e.emit("blocked", "prod-eu", "payments", "billing", "FreezeWindow") // another workload
	e.poll()
	if r.calls() != 3 {
		t.Fatalf("%d messages, want 3 (one per workload and reason)", r.calls())
	}
	// A day later the same block is news again.
	e.clock.Advance(25 * time.Hour)
	e.emit("blocked", "prod-eu", "payments", "checkout", "FreezeWindow")
	e.poll()
	if r.calls() != 4 {
		t.Errorf("%d messages, want 4", r.calls())
	}
}

func TestRateLimitDelaysWithoutDropping(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	c := e.channel("busy", hubapi.NotificationSlack, r.url(), "")
	e.rule("all", []string{"applied"}, nil, nil, c)
	e.start()
	for i := range 5 {
		e.emit("applied", "prod-eu", "payments", "wl-"+string(rune('a'+i)), "RecommendationApplied")
	}
	e.svc.RateLimit = 2
	e.poll()
	if r.calls() != 2 {
		t.Fatalf("%d messages through a limit of 2 per minute, want 2", r.calls())
	}
	for _, d := range e.deliveries(c.ID) {
		if d.Status == "pending" && d.Attempts != 0 {
			t.Errorf("a delayed delivery counts an attempt: %+v", d)
		}
	}
	for range 4 {
		e.clock.Advance(31 * time.Second)
		e.poll()
	}
	if r.calls() != 5 {
		t.Errorf("%d messages after waiting, want all 5", r.calls())
	}
}

func TestSendTest(t *testing.T) {
	e := newEnv(t)
	ok := newReceiver(t)
	bad := newReceiver(t, 400)
	cOK := e.channel("ok", hubapi.NotificationTeams, ok.url(), "")
	cBad := e.channel("bad", hubapi.NotificationSlack, bad.url(), "")
	res, err := e.svc.TestChannel(e.ctx, cOK.ID)
	if err != nil || !res.OK || res.HTTPStatus != 200 || ok.calls() != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = e.svc.TestChannel(e.ctx, cBad.ID)
	if err != nil || res.OK || res.HTTPStatus != 400 || !strings.Contains(res.Error, "HTTP 400") || strings.Contains(res.Error, "PATHSECRET") {
		t.Fatalf("%+v %v", res, err)
	}
	ch, _ := e.svc.GetChannel(e.ctx, cBad.ID)
	if ch.LastDelivery == nil || ch.LastDelivery.Status != "failed" || ch.LastDelivery.Error == "" {
		t.Errorf("the failed test is not shown on the channel: %+v", ch.LastDelivery)
	}
	if ds := e.deliveries(cBad.ID); len(ds) != 1 || ds[0].Kind != "test" || ds[0].Status != "failed" {
		t.Errorf("history %+v", ds)
	}
	if _, err := e.svc.TestChannel(e.ctx, "00000000-0000-4000-8000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown channel: %v", err)
	}
	// An unreachable destination reports why, without its URL.
	dead := newReceiver(t)
	cDead := e.channel("dead", hubapi.NotificationWebhook, dead.url(), "")
	dead.Close()
	res, err = e.svc.TestChannel(e.ctx, cDead.ID)
	if err != nil || res.OK || res.Error == "" || strings.Contains(res.Error, "PATHSECRET") {
		t.Errorf("%+v %v", res, err)
	}
}

func TestChannelAndRuleManagement(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	c := e.channel("ops", hubapi.NotificationWebhook, r.url(), signingSecret)
	if !c.SecretSet || c.Version != 1 || c.Source != "api" || !c.Enabled || c.URL != "http://"+strings.TrimPrefix(r.URL, "http://")+"/…" {
		t.Errorf("channel %+v", c)
	}
	if _, err := e.svc.CreateChannel(e.ctx, hubapi.NotificationChannelInput{Name: "ops", Type: "slack", URL: r.url(), Reason: "x"}, admin); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name: %v", err)
	}
	for name, in := range map[string]hubapi.NotificationChannelInput{
		"bad name":      {Name: "Ops Team", Type: "slack", URL: r.url()},
		"bad type":      {Name: "x", Type: "pager", URL: r.url()},
		"no url":        {Name: "x", Type: "slack"},
		"short secret":  {Name: "x", Type: "webhook", URL: r.url(), Secret: "short"},
		"slack secret":  {Name: "x", Type: "slack", URL: r.url(), Secret: signingSecret},
		"credentials":   {Name: "x", Type: "slack", URL: "http://u:p@example.com/x"},
		"both secret":   {Name: "x", Type: "webhook", URL: r.url(), Secret: signingSecret, ClearSecret: true},
		"relative url":  {Name: "x", Type: "slack", URL: "/hooks/x"},
		"unknown field": {Name: "x", Type: "teams", URL: "ftp://x.example.com"},
	} {
		var ve *ValidationError
		if _, err := e.svc.CreateChannel(e.ctx, in, admin); !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Update keeps the URL and the secret unless they are sent.
	disabled := false
	before, after, err := e.svc.UpdateChannel(e.ctx, c.ID, 1, hubapi.NotificationChannelInput{Name: "ops-hook", Type: "webhook", Enabled: &disabled, Reason: "rename"}, admin)
	if err != nil || before.Name != "ops" || after.Name != "ops-hook" || after.Enabled || !after.SecretSet || after.Version != 2 || after.URL != c.URL {
		t.Fatalf("update: %+v %+v %v", before, after, err)
	}
	if _, _, err := e.svc.UpdateChannel(e.ctx, c.ID, 1, hubapi.NotificationChannelInput{Name: "x", Type: "webhook", Reason: "r"}, admin); !errors.Is(err, ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if _, _, err := e.svc.UpdateChannel(e.ctx, c.ID, 2, hubapi.NotificationChannelInput{Name: "ops-hook", Type: "slack", Reason: "r"}, admin); err == nil {
		t.Error("the type of a channel changes")
	}
	_, after, err = e.svc.UpdateChannel(e.ctx, c.ID, 2, hubapi.NotificationChannelInput{Name: "ops-hook", Type: "webhook", ClearSecret: true, Reason: "r"}, admin)
	if err != nil || after.SecretSet {
		t.Errorf("clear secret: %+v %v", after, err)
	}
	// The URL still works after an update (re-sealed with the same id).
	if res, err := e.svc.TestChannel(e.ctx, c.ID); err != nil || !res.OK {
		t.Errorf("test after update: %+v %v", res, err)
	}

	// Rules.
	bad := hubapi.NotificationRuleInput{Name: "r", EventTypes: []string{"exploded"}, Clusters: []string{"[x"}, ChannelIDs: []string{"nope"}, Reason: "x"}
	var ve *ValidationError
	if _, err := e.svc.CreateRule(e.ctx, bad, admin); !errors.As(err, &ve) || len(ve.Problems) < 3 {
		t.Errorf("invalid rule: %v", err)
	}
	if _, err := e.svc.CreateRule(e.ctx, hubapi.NotificationRuleInput{Name: "r", EventTypes: []string{"applied"}, ChannelIDs: []string{"00000000-0000-4000-8000-000000000000"}, Reason: "x"}, admin); !errors.As(err, &ve) {
		t.Errorf("unknown channel: %v", err)
	}
	rule := e.rule("rollbacks", []string{"rolled_back"}, []string{"prod-*"}, []string{"pay*"}, c)
	if rule.Version != 1 || len(rule.ChannelIDs) != 1 || rule.Namespaces[0] != "pay*" {
		t.Errorf("rule %+v", rule)
	}
	// A channel in use cannot be deleted.
	var inUse *InUseError
	if _, err := e.svc.DeleteChannel(e.ctx, c.ID, 3, admin); !errors.As(err, &inUse) || !strings.Contains(inUse.Error(), "rollbacks") {
		t.Errorf("delete a channel in use: %v", err)
	}
	if _, err := e.svc.DeleteRule(e.ctx, rule.ID, 1, admin); err != nil {
		t.Fatal(err)
	}
	// Queue something, delete the channel: the queue goes with it.
	e.start()
	e.rule("again", []string{"applied"}, nil, nil, c)
	rules, _ := e.svc.ListRules(e.ctx)
	if _, err := e.svc.DeleteRule(e.ctx, rules[0].ID, rules[0].Version, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DeleteChannel(e.ctx, c.ID, 3, admin); err != nil {
		t.Fatal(err)
	}
	if cs, _ := e.svc.ListChannels(e.ctx); len(cs) != 0 {
		t.Errorf("channels %+v", cs)
	}
	if _, err := e.svc.GetChannel(e.ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted channel: %v", err)
	}
}

func TestConfigChannelsAreReadOnlyAndSynced(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	ch, err := e.svc.ConfigChannel("from-chart", "slack", r.url(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Repo.SyncConfigChannels(e.ctx, []Channel{ch}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Repo.SyncConfigChannels(e.ctx, []Channel{ch}); err != nil { // a restart
		t.Fatal(err)
	}
	got, err := e.svc.GetChannel(e.ctx, ch.ID)
	if err != nil || got.Source != "config" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, _, err := e.svc.UpdateChannel(e.ctx, ch.ID, got.Version, hubapi.NotificationChannelInput{Name: "x", Type: "slack", Reason: "r"}, admin); !errors.Is(err, ErrReadOnly) {
		t.Errorf("update: %v", err)
	}
	if _, err := e.svc.DeleteChannel(e.ctx, ch.ID, got.Version, admin); !errors.Is(err, ErrReadOnly) {
		t.Errorf("delete: %v", err)
	}
	if res, err := e.svc.TestChannel(e.ctx, ch.ID); err != nil || !res.OK {
		t.Errorf("test: %+v %v", res, err)
	}
	// An API channel cannot take the name.
	if _, err := e.svc.CreateChannel(e.ctx, hubapi.NotificationChannelInput{Name: "from-chart", Type: "slack", URL: r.url(), Reason: "x"}, admin); !errors.Is(err, ErrNameTaken) {
		t.Errorf("name clash: %v", err)
	}
	// Removed from the values: gone.
	if err := e.svc.Repo.SyncConfigChannels(e.ctx, nil); err != nil {
		t.Fatal(err)
	}
	if cs, _ := e.svc.ListChannels(e.ctx); len(cs) != 0 {
		t.Errorf("channels %+v", cs)
	}
	// Without an encryption key a URL cannot be stored.
	e.svc.Sealer = nil
	if _, err := e.svc.CreateChannel(e.ctx, hubapi.NotificationChannelInput{Name: "x", Type: "slack", URL: r.url(), Reason: "x"}, admin); !errors.Is(err, ErrSecretsKeyMissing) {
		t.Errorf("no key: %v", err)
	}
	if e.svc.Status().SecretsKey {
		t.Error("status says there is a key")
	}
}

func TestOtherEventKinds(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	c := e.channel("ops", hubapi.NotificationWebhook, r.url(), "")
	e.rule("fleet", []string{"automation_paused", "automation_resumed", "readiness_degraded"}, nil, nil, c)
	e.start()
	record := func(action, targetType, id string, before, after any) {
		t.Helper()
		err := e.st.RecordChange(e.ctx, store.Change{Actor: hubapi.Actor{Type: "user", Name: "alice"}, Action: action, Reason: "maintenance window", Time: time.Now().UTC(), // the hub stamps these rows itself, like the DB does

			Target: hubapi.ConfigTarget{Type: targetType, ID: id, Cluster: ifCluster(targetType, id)}, Before: before, After: after})
		if err != nil {
			t.Fatal(err)
		}
	}
	record(hubapi.ActionAutomationPaused, hubapi.TargetAutomation, "prod-eu", nil, map[string]any{"paused": true})
	record(hubapi.ActionAutomationPaused, hubapi.TargetAutomation, "*", nil, map[string]any{"paused": true})
	record(hubapi.ActionAutomationResumed, hubapi.TargetAutomation, "prod-eu", nil, map[string]any{"paused": false})
	record(hubapi.ActionReadinessChanged, hubapi.TargetReadiness, "prod-eu", map[string]string{"state": "AutomationReady"}, map[string]string{"state": "MetricsOK"})
	record(hubapi.ActionReadinessChanged, hubapi.TargetReadiness, "dev-1", map[string]string{"state": "MetricsOK"}, map[string]string{"state": "AutomationReady"}) // an improvement: no message
	record(hubapi.ActionSettingsUpdated, hubapi.TargetSettings, "recommender", nil, nil)                                                                           // not notifiable
	e.poll()
	if r.calls() != 4 {
		t.Fatalf("%d messages, want 4 (pause cluster, pause fleet, resume, readiness drop)", r.calls())
	}
	var titles []string
	r.mu.Lock()
	for _, b := range r.bodies {
		var m struct{ Event EventMessage }
		_ = json.Unmarshal(b, &m)
		titles = append(titles, m.Event.Title())
	}
	r.mu.Unlock()
	joined := strings.Join(titles, "\n")
	for _, want := range []string{"Automation paused for cluster prod-eu", "Automation paused for the whole fleet", "Automation resumed for cluster prod-eu", "Cluster prod-eu is less ready: AutomationReady → MetricsOK"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no message %q in:\n%s", want, joined)
		}
	}
}

func ifCluster(targetType, id string) string {
	if id != "*" && (targetType == hubapi.TargetAutomation || targetType == hubapi.TargetReadiness) {
		return id
	}
	return ""
}

func TestWeeklyDigest(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	c := e.channel("finops", hubapi.NotificationSlack, r.url(), "")
	e.svc.Digest = fakeDigest{attention: []Attention{{Cluster: "dev-1", Reasons: []string{"automation is paused"}}}}

	// Settings are validated and need a channel to be enabled.
	d, err := e.svc.GetDigest(e.ctx)
	if err != nil || d.Enabled || d.Version != 1 || d.NextSendAt != nil {
		t.Fatalf("%+v %v", d, err)
	}
	var ve *ValidationError
	if _, _, err := e.svc.UpdateDigest(e.ctx, 1, hubapi.NotificationDigestInput{Enabled: true, Weekday: 1, Time: "09:00", Timezone: "UTC", Reason: "x"}, admin); !errors.As(err, &ve) {
		t.Errorf("enabled without a channel: %v", err)
	}
	if _, _, err := e.svc.UpdateDigest(e.ctx, 1, hubapi.NotificationDigestInput{Weekday: 9, Time: "25:00", Timezone: "Mars/Base", ChannelIDs: []string{c.ID}, Reason: "x"}, admin); !errors.As(err, &ve) || len(ve.Problems) != 3 {
		t.Errorf("invalid schedule: %v", err)
	}
	// "Send now" needs a channel too.
	if _, err := e.svc.QueueDigest(e.ctx); !errors.Is(err, ErrNoChannels) {
		t.Errorf("queue without channels: %v", err)
	}

	// Schedule it for an hour after it is saved; three hours later the leader's tick queues it, once.
	saved := time.Now().UTC()
	at := saved.Add(time.Hour).Truncate(time.Minute).Add(time.Minute)
	_, d, err = e.svc.UpdateDigest(e.ctx, 1, hubapi.NotificationDigestInput{Enabled: true, Weekday: int(at.Weekday()), Time: at.Format("15:04"), Timezone: "UTC", ChannelIDs: []string{c.ID}, Reason: "weekly"}, admin)
	if err != nil || d.Version != 2 || d.NextSendAt == nil || !d.NextSendAt.Equal(at) {
		t.Fatalf("%+v %v (want next %v)", d, err, at)
	}
	e.clock.t = at.Add(-time.Minute)
	if err := e.svc.digestTick(e.ctx); err != nil || r.calls() != 0 {
		t.Fatalf("early tick: %v", err)
	}
	e.svc.SendDue(e.ctx)
	if r.calls() != 0 {
		t.Fatal("the digest is sent before its time")
	}
	e.clock.t = at.Add(2 * time.Minute)
	if err := e.svc.digestTick(e.ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.digestTick(e.ctx); err != nil { // another replica, or the next tick
		t.Fatal(err)
	}
	e.svc.SendDue(e.ctx)
	if r.calls() != 1 {
		t.Fatalf("%d digests, want 1", r.calls())
	}
	body := string(mustLast(r))
	for _, want := range []string{"$1,234", "+$234", "payments/checkout", "dev-1", "weekly digest", "0 applied, 0 rolled back, 0 failed"} {
		if !strings.Contains(body, want) {
			t.Errorf("digest lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "Realized") {
		t.Error("realized savings show without a source")
	}
	if got, _ := e.svc.GetDigest(e.ctx); got.LastSentAt == nil || !got.LastSentAt.Equal(at) {
		t.Errorf("last sent %v, want %v", got.LastSentAt, at)
	}
	// A week later it is due again.
	e.clock.Advance(7 * 24 * time.Hour)
	if err := e.svc.digestTick(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.svc.SendDue(e.ctx)
	if r.calls() != 2 {
		t.Errorf("%d digests after a week, want 2", r.calls())
	}
	// A digest the hub slept through (more than 12 hours late) is skipped.
	e.clock.Advance(7*24*time.Hour + 13*time.Hour)
	if err := e.svc.digestTick(e.ctx); err != nil {
		t.Fatal(err)
	}
	e.svc.SendDue(e.ctx)
	if r.calls() != 2 {
		t.Errorf("a stale digest was sent: %d", r.calls())
	}

	// Realized savings plug in through the hook.
	e.svc.Realized = realizedFunc(func(context.Context, time.Time, time.Time) (*RealizedSavings, error) {
		return &RealizedSavings{PeriodUSD: 310}, nil
	})
	if n, err := e.svc.QueueDigest(e.ctx); err != nil || n != 1 {
		t.Fatalf("send now: %d %v", n, err)
	}
	e.svc.SendDue(e.ctx)
	if body := string(mustLast(r)); !strings.Contains(body, "Realized savings this week: $310") {
		t.Errorf("no realized savings: %s", body)
	}
	// A failing hook does not hold the digest back.
	e.svc.Realized = realizedFunc(func(context.Context, time.Time, time.Time) (*RealizedSavings, error) { return nil, errors.New("boom") })
	if n, err := e.svc.QueueDigest(e.ctx); err != nil || n != 1 {
		t.Fatalf("send now with a failing hook: %d %v", n, err)
	}
}

type realizedFunc func(ctx context.Context, from, to time.Time) (*RealizedSavings, error)

func (f realizedFunc) RealizedSavings(ctx context.Context, from, to time.Time) (*RealizedSavings, error) {
	return f(ctx, from, to)
}

func TestNothingIsSentForTheOldPast(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	c := e.channel("ops", hubapi.NotificationSlack, r.url(), "")
	e.emit("rolled_back", "prod-eu", "payments", "checkout", "OOMKills") // before anything runs
	time.Sleep(20 * time.Millisecond)
	e.rule("all", []string{"rolled_back"}, nil, nil, c)
	e.start()
	e.poll()
	if r.calls() != 0 {
		t.Errorf("%d messages for an old event", r.calls())
	}
	e.emit("rolled_back", "prod-eu", "payments", "checkout", "OOMKills")
	e.poll()
	if r.calls() != 1 {
		t.Errorf("%d messages for a new event", r.calls())
	}
	// Old finished deliveries are pruned.
	e.clock.Advance(Retention + time.Hour)
	if n, err := e.svc.Repo.PruneDeliveries(e.ctx, e.clock.Now().Add(-Retention)); err != nil || n != 1 {
		t.Errorf("pruned %d, %v", n, err)
	}
}
