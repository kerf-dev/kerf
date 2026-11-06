package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

func TestSignAndVerify(t *testing.T) {
	body := []byte(`{"version":1}`)
	sig := Sign("0123456789abcdef", body)
	if !strings.HasPrefix(sig, "sha256=") || len(sig) != len("sha256=")+64 {
		t.Fatalf("signature %q", sig)
	}
	if !Verify("0123456789abcdef", body, sig) {
		t.Error("a valid signature does not verify")
	}
	if Verify("0123456789abcdef", append(body, ' '), sig) || Verify("another-secret-xx", body, sig) {
		t.Error("a signature of another body or key verifies")
	}
}

func sampleEvent() Message {
	return Message{ID: "6f1c2a8e-0d7b-4c55-a0b1-8f9e2f0d1c3a", Kind: KindEvent, Time: time.Date(2026, 11, 4, 10, 15, 0, 0, time.UTC), Event: &EventMessage{
		Type: hubapi.NotifyRolledBack, Reason: "OOMKills", Cluster: "prod-eu", Namespace: "payments", Workload: &WorkloadRef{Kind: "Deployment", Name: "checkout"},
		Strategy: "in-place", Resources: []string{"memory"}, Message: "2 OOM kills within 30m after change",
		Changes: []Change{{Container: "app", Resource: "memory", Field: "requests", From: "384Mi", To: "1Gi"}},
		Link:    "https://kerf.example.com/workloads/prod-eu/payments/Deployment/checkout",
	}}
}

func TestRenderWebhookIsSignedOverTheRawBody(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	req, err := Render(hubapi.NotificationWebhook, sampleEvent(), "0123456789abcdef", now)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify("0123456789abcdef", req.Body, req.Headers[HeaderSignature]) {
		t.Fatalf("signature header %q does not verify over the body", req.Headers[HeaderSignature])
	}
	if req.Headers[HeaderTimestamp] != "1790000000" || req.Headers[HeaderDelivery] != sampleEvent().ID || req.Headers[HeaderEvent] != "rolled_back" {
		t.Errorf("headers %v", req.Headers)
	}
	var got map[string]any
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatal(err)
	}
	// The signed body carries the same timestamp as the header, so it cannot be replayed with a new one.
	if got["version"] != float64(PayloadVersion) || got["timestamp"] != float64(1790000000) || got["kind"] != "event" || got["id"] != sampleEvent().ID {
		t.Errorf("envelope %v", got)
	}
	ev := got["event"].(map[string]any)
	if ev["type"] != "rolled_back" || ev["cluster"] != "prod-eu" || ev["link"] == "" {
		t.Errorf("event %v", ev)
	}
	// Unsigned without a secret.
	req, _ = Render(hubapi.NotificationWebhook, sampleEvent(), "", now)
	if _, ok := req.Headers[HeaderSignature]; ok {
		t.Error("signature header without a secret")
	}
}

func TestRenderSlackAndTeams(t *testing.T) {
	now := time.Now()
	req, err := Render(hubapi.NotificationSlack, sampleEvent(), "", now)
	if err != nil {
		t.Fatal(err)
	}
	var slack struct {
		Text   string `json:"text"`
		Blocks []struct {
			Type     string           `json:"type"`
			Elements []map[string]any `json:"elements"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(req.Body, &slack); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(slack.Text, "Rolled back payments/checkout on prod-eu (OOMKills)") || len(slack.Blocks) < 3 || slack.Blocks[0].Type != "header" {
		t.Errorf("slack message %s", req.Body)
	}
	last := slack.Blocks[len(slack.Blocks)-1]
	if last.Type != "actions" || last.Elements[0]["url"] != sampleEvent().Event.Link {
		t.Errorf("slack message has no link button: %s", req.Body)
	}
	if !strings.Contains(string(req.Body), "384Mi → 1Gi") {
		t.Errorf("slack message lacks the change: %s", req.Body)
	}

	req, err = Render(hubapi.NotificationTeams, sampleEvent(), "", now)
	if err != nil {
		t.Fatal(err)
	}
	var teams struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Type    string           `json:"type"`
				Version string           `json:"version"`
				Body    []map[string]any `json:"body"`
				Actions []map[string]any `json:"actions"`
			} `json:"content"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(req.Body, &teams); err != nil {
		t.Fatal(err)
	}
	a := teams.Attachments[0]
	if teams.Type != "message" || a.ContentType != "application/vnd.microsoft.card.adaptive" || a.Content.Type != "AdaptiveCard" || a.Content.Version != "1.4" {
		t.Errorf("teams message %s", req.Body)
	}
	if len(a.Content.Actions) != 1 || a.Content.Actions[0]["type"] != "Action.OpenUrl" || a.Content.Actions[0]["url"] != sampleEvent().Event.Link {
		t.Errorf("teams card has no link action: %s", req.Body)
	}
}

func TestRenderDigestAndTest(t *testing.T) {
	prev, change := 1000.0, 234.0
	d := Message{ID: "d1", Kind: KindDigest, Time: time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC), Digest: &DigestMessage{
		PeriodStart: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC),
		EstimatedSavings: EstimatedSavings{CurrentUSD: 1234, PreviousUSD: &prev, ChangeUSD: &change},
		RealizedSavings:  &RealizedSavings{PeriodUSD: 310},
		TopOpportunities: []Opportunity{{Cluster: "prod-eu", Namespace: "payments", Kind: "Deployment", Name: "checkout", MonthlySavingsUSD: 410, Link: "https://k/w"}},
		Changes:          ChangeCounts{Applied: 12, RolledBack: 1}, Attention: []Attention{{Cluster: "dev-1", Reasons: []string{"automation is paused"}}}, Link: "https://k/",
	}}
	for _, typ := range []string{hubapi.NotificationSlack, hubapi.NotificationTeams} {
		req, err := Render(typ, d, "", time.Now())
		if err != nil {
			t.Fatal(typ, err)
		}
		s := string(req.Body)
		for _, want := range []string{"$1,234", "+$234", "Realized savings", "$310", "payments/checkout", "dev-1", "12 applied, 1 rolled back"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s digest lacks %q: %s", typ, want, s)
			}
		}
	}
	// The webhook carries the data, not display text.
	req, err := Render(hubapi.NotificationWebhook, d, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var wh struct {
		Digest DigestMessage `json:"digest"`
	}
	if err := json.Unmarshal(req.Body, &wh); err != nil || wh.Digest.EstimatedSavings.CurrentUSD != 1234 || *wh.Digest.EstimatedSavings.ChangeUSD != 234 ||
		wh.Digest.RealizedSavings.PeriodUSD != 310 || wh.Digest.Changes.Applied != 12 || wh.Digest.TopOpportunities[0].Name != "checkout" || wh.Digest.Attention[0].Cluster != "dev-1" {
		t.Errorf("webhook digest: %v %s", err, req.Body)
	}
	d.Digest.RealizedSavings = nil
	req, _ = Render(hubapi.NotificationSlack, d, "", time.Now())
	if strings.Contains(string(req.Body), "Realized") {
		t.Error("the realized-savings line shows without a source")
	}
	for _, typ := range hubapi.NotificationChannelTypes {
		req, err := Render(typ, Message{ID: "t1", Kind: KindTest, Time: time.Now().UTC(), Test: &TestMessage{Channel: "ops"}}, "", time.Now())
		if err != nil || !strings.Contains(string(req.Body), "ops") || (typ != hubapi.NotificationWebhook && !strings.Contains(string(req.Body), "test message")) {
			t.Errorf("%s test message: %v %s", typ, err, req.Body)
		}
	}
	if _, err := Render("pager", d, "", time.Now()); err == nil {
		t.Error("an unknown channel type renders")
	}
}

func TestRedact(t *testing.T) {
	tests := []struct{ name, in, notWant string }{
		{"slack url", "post to https://hooks.slack.com/services/T000/B000/XXXXXXXXXXXXXXXXXXXX failed", "XXXXXXXX"},
		{"teams url", "https://prod-12.westeurope.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke?sig=Zm9vYmFy&sp=x", "Zm9vYmFy"},
		{"userinfo", "dial https://admin:hunter2@registry.example.com/x", "hunter2"},
		{"bearer", "Authorization: Bearer abcDEF123456.tokenvalue", "tokenvalue"},
		{"param", "request failed: token=s3cr3tValue&x=1", "s3cr3tValue"},
		{"password json", `{"password": "p4ssw0rd!"}`, "p4ssw0rd"},
		{"jwt", "got eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJl", "c2lnbmF0dXJl"},
		{"aws key", "key AKIAABCDEFGHIJKLMNOP in env", "AKIAABCDEFGHIJKLMNOP"},
		{"opaque", "secret 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 here", "9f86d081884c7d659a2f"},
		{"github", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "abcdefghijklmnopqrstuvwxyz0123"},
	}
	for _, tt := range tests {
		got := Redact(tt.in, 500)
		if strings.Contains(got, tt.notWant) {
			t.Errorf("%s: %q still contains %q", tt.name, got, tt.notWant)
		}
	}
	if got := Redact("2 OOM kills within 30m after change (threshold 1)", 100); got != "2 OOM kills within 30m after change (threshold 1)" {
		t.Errorf("plain text changed: %q", got)
	}
	if got := Redact(strings.Repeat("word ", 100), 20); len([]rune(got)) != 20 || !strings.HasSuffix(got, "…") {
		t.Errorf("not shortened: %q", got)
	}
	if got := FilterDetails(map[string]string{"oomKills": "2", "error": "token=abc12345", "secret": "x"}); len(got) != 1 || got["oomKills"] != "2" {
		t.Errorf("details %v", got)
	}
}

func TestLinks(t *testing.T) {
	l := Links{Base: "https://kerf.example.com/"}
	if got := l.Workload("prod-eu", "pay ments", "Deployment", "checkout"); got != "https://kerf.example.com/workloads/prod-eu/pay%20ments/Deployment/checkout" {
		t.Error(got)
	}
	if got := l.Cluster("prod-eu"); got != "https://kerf.example.com/clusters/prod-eu" {
		t.Error(got)
	}
	if (Links{}).Workload("a", "b", "c", "d") != "" || (Links{}).Overview() != "" {
		t.Error("links without a base URL")
	}
}

func TestRuleMatches(t *testing.T) {
	rule := func(types, clusters, namespaces []string) Rule {
		return Rule{Enabled: true, EventTypes: types, Clusters: clusters, Namespaces: namespaces}
	}
	rb := Event{Type: "rolled_back", Cluster: "prod-eu", Namespace: "payments"}
	paused := Event{Type: "automation_paused", Cluster: "prod-eu"}
	fleet := Event{Type: "automation_paused"}
	tests := []struct {
		name string
		r    Rule
		e    Event
		want bool
	}{
		{"type and any scope", rule([]string{"rolled_back"}, nil, nil), rb, true},
		{"other type", rule([]string{"applied"}, nil, nil), rb, false},
		{"cluster glob", rule([]string{"rolled_back"}, []string{"prod-*"}, nil), rb, true},
		{"cluster glob miss", rule([]string{"rolled_back"}, []string{"dev-*"}, nil), rb, false},
		{"namespace", rule([]string{"rolled_back"}, nil, []string{"payments"}), rb, true},
		{"other namespace", rule([]string{"rolled_back"}, nil, []string{"payments"}), Event{Type: "rolled_back", Cluster: "prod-eu", Namespace: "search"}, false},
		{"any of several", rule([]string{"rolled_back"}, []string{"dev-*", "prod-eu"}, []string{"x", "pay*"}), rb, true},
		{"namespace rule never matches a cluster event", rule([]string{"automation_paused"}, nil, []string{"payments"}), paused, false},
		{"cluster event, cluster rule", rule([]string{"automation_paused"}, []string{"prod-*"}, nil), paused, true},
		{"fleet event matches an unscoped rule", rule([]string{"automation_paused"}, nil, nil), fleet, true},
		{"fleet event does not match a cluster rule", rule([]string{"automation_paused"}, []string{"prod-*"}, nil), fleet, false},
		{"disabled", Rule{EventTypes: []string{"rolled_back"}}, rb, false},
	}
	for _, tt := range tests {
		if got := tt.r.Matches(tt.e); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(6)
	now := time.Now()
	for i := range 6 {
		if w := l.reserve("a", now); w != 0 {
			t.Fatalf("message %d waits %v within the burst", i, w)
		}
	}
	if w := l.reserve("a", now); w <= 0 {
		t.Error("the seventh message is not delayed")
	}
	if w := l.reserve("b", now); w != 0 {
		t.Error("another channel shares the limit")
	}
	if w := l.reserve("a", now.Add(11*time.Second)); w != 0 {
		t.Errorf("no token after 11 s at 6/min: %v", w)
	}
}

func TestSender(t *testing.T) {
	var calls atomic.Int32
	var gotHeaders http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("ok"))
		case "/gone":
			http.Error(w, "no_service", http.StatusNotFound)
		case "/boom":
			http.Error(w, "internal error", http.StatusInternalServerError)
		case "/limited":
			w.Header().Set("Retry-After", "120")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusFound)
		}
	}))
	defer srv.Close()
	s := &Sender{UserAgent: "kerf-hub/test"}
	req := Request{Body: []byte(`{"a":1}`), Headers: map[string]string{"Content-Type": "application/json", HeaderSignature: "sha256=x"}}
	r := s.Send(context.Background(), srv.URL+"/ok", req)
	if !r.OK || r.HTTPStatus != 200 || gotHeaders.Get("User-Agent") != "kerf-hub/test" || gotHeaders.Get(HeaderSignature) != "sha256=x" || string(gotBody) != `{"a":1}` {
		t.Errorf("ok: %+v %v", r, gotHeaders)
	}
	tests := []struct {
		path      string
		retryable bool
		errHas    string
		after     time.Duration
	}{
		{"/gone", false, "HTTP 404 Not Found: no_service", 0},
		{"/boom", true, "HTTP 500", 0},
		{"/limited", true, "HTTP 429", 2 * time.Minute},
		{"/moved", false, "redirects are not followed", 0},
	}
	for _, tt := range tests {
		r := s.Send(context.Background(), srv.URL+tt.path, req)
		if r.OK || r.Retryable != tt.retryable || !strings.Contains(r.Err, tt.errHas) || r.RetryAfter != tt.after {
			t.Errorf("%s: %+v", tt.path, r)
		}
	}
	// The path of a webhook URL is its credential: transport errors never show it.
	closed := httptest.NewServer(http.NotFoundHandler())
	secretURL := closed.URL + "/services/T000/B000/SUPERSECRETPATH?sig=QUERYSECRET"
	closed.Close()
	r = s.Send(context.Background(), secretURL, req)
	if r.OK || !r.Retryable || r.Err == "" || strings.Contains(r.Err, "SUPERSECRETPATH") || strings.Contains(r.Err, "QUERYSECRET") || strings.Contains(r.Err, "/services/") {
		t.Errorf("transport error leaks the URL: %+v", r)
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		url       string
		allowHTTP bool
		ok        bool
	}{
		{"https://hooks.slack.com/services/T/B/X", false, true},
		{"http://hooks.example.com/x", false, false},
		{"http://127.0.0.1:9000/x", true, true},
		{"ftp://example.com/x", true, false},
		{"https://user:pw@example.com/x", false, false},
		{"https://example.com/x#frag", false, false},
		{"/relative", false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		if err := ValidateURL(tt.url, tt.allowHTTP); (err == nil) != tt.ok {
			t.Errorf("ValidateURL(%q, %v) = %v", tt.url, tt.allowHTTP, err)
		}
	}
	if got := MaskURL("https://hooks.slack.com/services/T000/B000/XXXX"); got != "https://hooks.slack.com/…" {
		t.Error(got)
	}
}

func TestSchedule(t *testing.T) {
	d := DigestSettings{Enabled: true, Weekday: 1, TimeOfDay: "09:00", Timezone: "Europe/Berlin"}
	// Monday 2026-11-09 09:00 in Berlin (UTC+1) = 08:00 UTC.
	mon := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	next, err := NextSend(d, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	if err != nil || !next.Equal(mon) {
		t.Errorf("next = %v, %v; want %v", next, err, mon)
	}
	if next, _ := NextSend(d, mon); !next.Equal(mon.AddDate(0, 0, 7)) {
		t.Errorf("next after the digest = %v", next)
	}
	last, err := LastScheduled(d, time.Date(2026, 11, 9, 8, 30, 0, 0, time.UTC))
	if err != nil || !last.Equal(mon) {
		t.Errorf("last = %v, %v", last, err)
	}
	if last, _ := LastScheduled(d, time.Date(2026, 11, 9, 7, 59, 0, 0, time.UTC)); !last.Equal(mon.AddDate(0, 0, -7)) {
		t.Errorf("last before the time of day = %v", last)
	}
	// Daylight saving: the same local time is a different UTC time in summer.
	summer, _ := NextSend(d, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	if summer.Hour() != 7 || summer.Weekday() != time.Monday {
		t.Errorf("summer digest at %v (want 07:00 UTC on a Monday)", summer)
	}
	if p := ValidateSchedule(7, "9:00", "Mars/Olympus"); len(p) != 3 {
		t.Errorf("problems %v", p)
	}
	if p := ValidateSchedule(0, "23:59", "UTC"); len(p) != 0 {
		t.Errorf("problems %v", p)
	}
}

func TestConvertChanges(t *testing.T) {
	cs := ConvertChanges([]hubapi.ContainerChange{{
		Name: "app",
		From: &hubapi.Resources{Requests: &hubapi.ResourceValues{CPU: "500m", Memory: "384Mi"}, Limits: &hubapi.ResourceValues{Memory: "384Mi"}},
		To:   &hubapi.Resources{Requests: &hubapi.ResourceValues{CPU: "500m", Memory: "1Gi"}, Limits: &hubapi.ResourceValues{Memory: "1Gi"}},
	}})
	want := []Change{{"app", "memory", "requests", "384Mi", "1Gi"}, {"app", "memory", "limits", "384Mi", "1Gi"}}
	if len(cs) != len(want) {
		t.Fatalf("changes %+v", cs)
	}
	for i := range want {
		if cs[i] != want[i] {
			t.Errorf("change %d = %+v, want %+v", i, cs[i], want[i])
		}
	}
}

func TestMetricsNilSafe(t *testing.T) {
	var m *Metrics
	m.count("slack", true, false)
	m.setLastSuccess(nil)
}
