package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/recommender"
)

func TestAPIAuth(t *testing.T) {
	e := newEnv(t)
	// A revoked and an expired token.
	for _, tok := range []string{"revoked", "expired"} {
		if err := e.st.EnsureToken(e.ctx, tok, hubapi.RoleUI, "", tok); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE api_tokens SET revoked_at = now() WHERE description = 'revoked'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE api_tokens SET expires_at = now() - interval '1 minute' WHERE description = 'expired'`); err != nil {
		t.Fatal(err)
	}
	const recs = "/v1/clusters/c1/recommendations"
	cases := []struct {
		name, method, path, token string
		status                    int
		code                      string
	}{
		{"no token", "GET", recs, "", 401, "unauthorized"},
		{"bad token", "GET", recs, "nope", 401, "invalid_token"},
		{"revoked token", "GET", "/v1/summary", "revoked", 401, "invalid_token"},
		{"expired token", "GET", "/v1/summary", "expired", 401, "invalid_token"},
		{"agent of other cluster", "GET", recs, tokC2, 403, "cluster_forbidden"},
		{"agent of other cluster POST status", "POST", "/v1/clusters/c1/workload-status", tokC2, 403, "cluster_forbidden"},
		{"agent of other cluster POST events", "POST", "/v1/clusters/c1/events", tokC2, 403, "cluster_forbidden"},
		{"agent on unknown cluster", "GET", "/v1/clusters/zz/recommendations", tokC1, 403, "cluster_forbidden"},
		{"ui on agent endpoint", "GET", recs, tokUI, 403, "role_forbidden"},
		{"ui POST events", "POST", "/v1/clusters/c1/events", tokUI, 403, "role_forbidden"},
		{"admin unknown cluster", "GET", "/v1/clusters/zz/recommendations", tokAdmin, 404, "cluster_not_found"},
		{"admin unknown cluster POST", "POST", "/v1/clusters/zz/workload-status", tokAdmin, 404, "cluster_not_found"},
		{"agent on summary", "GET", "/v1/summary", tokC1, 403, "role_forbidden"},
		{"agent on workloads", "GET", "/v1/workloads", tokC1, 403, "role_forbidden"},
		{"agent on clusters", "GET", "/v1/clusters", tokC1, 403, "role_forbidden"},
		{"agent on trend", "GET", "/v1/savings/trend", tokC1, 403, "role_forbidden"},
		{"agent on workload detail", "GET", wlPath(kCheckout, ""), tokC1, 403, "role_forbidden"},
		{"agent audit other cluster", "GET", "/v1/audit?cluster=c2", tokC1, 403, "cluster_forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := e.do(tc.method, tc.path, tc.token, nil)
			p := e.problem(r, tc.status, tc.code)
			if u, _ := url.Parse(tc.path); p.Instance != u.Path {
				t.Errorf("instance = %q, want %q", p.Instance, u.Path)
			}
		})
	}
	ok := []struct{ name, path, token string }{
		{"agent own cluster", recs, tokC1},
		{"admin agent endpoint", recs, tokAdmin},
		{"admin summary", "/v1/summary", tokAdmin},
		{"ui summary", "/v1/summary", tokUI},
		{"ui audit", "/v1/audit", tokUI},
		{"agent audit", "/v1/audit", tokC1},
		{"agent audit own cluster", "/v1/audit?cluster=c1", tokC1},
		{"admin audit", "/v1/audit?cluster=c2", tokAdmin},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if r := e.do("GET", tc.path, tc.token, nil); r.code != 200 {
				t.Errorf("%d %s", r.code, r.body)
			}
		})
	}
	// Request metrics are labelled by route pattern, not by raw path.
	ms := e.metrics()
	if v, ok := ms.find("kerf_hub_http_requests_total", map[string]string{"route": "GET /v1/clusters/{cluster}/recommendations", "code": "403"}); !ok || v == "0" {
		t.Errorf("kerf_hub_http_requests_total for the recommendations route not found")
	}
	if r := e.do("GET", "/readyz", "", nil); r.code != 200 {
		t.Errorf("readyz: %d", r.code)
	}
}

func TestAgentRecommendations(t *testing.T) {
	e := newEnv(t)
	const path = "/v1/clusters/c1/recommendations"
	var all hubapi.RecommendationList
	r := e.getJSON(path, tokC1, &all)
	if all.Cluster != "c1" || all.Continue != "" {
		t.Errorf("list: cluster %q continue %q", all.Cluster, all.Continue)
	}
	// c1 has 5 workloads; report has no usage data, so no recommendation.
	var names []string
	for _, it := range all.Items {
		names = append(names, it.Workload.Namespace+"/"+it.Workload.Name)
		for _, c := range it.Containers {
			if c.Explanation != nil {
				t.Errorf("%s/%s: explanation present in agent list", it.Workload.Name, c.Name)
			}
		}
		if it.ID == "" || it.GeneratedAt.IsZero() || len(it.Containers) == 0 || it.RiskFlags == nil || it.Settings.Profile == "" {
			t.Errorf("incomplete recommendation %+v", it)
		}
		if !it.GeneratedAt.Equal(e.now.Truncate(fakeStep)) {
			t.Errorf("%s: generatedAt %s, want latest run %s", it.Workload.Name, it.GeneratedAt, e.now.Truncate(fakeStep))
		}
	}
	if want := []string{"payments/checkout", "payments/ledger", "web/frontend", "web/legacy"}; !slices.Equal(names, want) {
		t.Errorf("items = %v, want %v", names, want)
	}
	etag1 := r.header.Get("ETag")
	if !strings.HasPrefix(etag1, `"`) || !strings.HasSuffix(etag1, `"`) || len(etag1) < 10 {
		t.Errorf("ETag %q", etag1)
	}

	// namespace filter
	var web hubapi.RecommendationList
	e.getJSON(path+"?namespace=web", tokC1, &web)
	if len(web.Items) != 2 || web.Items[0].Workload.Name != "frontend" || web.Items[1].Workload.Name != "legacy" {
		t.Errorf("namespace=web: %+v", web.Items)
	}
	var none hubapi.RecommendationList
	e.getJSON(path+"?namespace=nothing", tokC1, &none)
	if none.Items == nil || len(none.Items) != 0 {
		t.Errorf("namespace=nothing: items %v (must be [])", none.Items)
	}

	// limit + continue covers every item exactly once.
	for _, limit := range []int{1, 2, 3, 4, 5} {
		seen := map[string]int{}
		var order []string
		next := ""
		pages := 0
		for {
			p := fmt.Sprintf("%s?limit=%d", path, limit)
			if next != "" {
				p += "&continue=" + url.QueryEscape(next)
			}
			var page hubapi.RecommendationList
			e.getJSON(p, tokC1, &page)
			pages++
			if len(page.Items) > limit {
				t.Fatalf("limit %d: page has %d items", limit, len(page.Items))
			}
			for _, it := range page.Items {
				seen[it.ID]++
				order = append(order, it.Workload.Namespace+"/"+it.Workload.Name)
			}
			if page.Continue == "" {
				break
			}
			if pages > 10 {
				t.Fatal("paging does not terminate")
			}
			next = page.Continue
		}
		if !slices.Equal(order, names) {
			t.Errorf("limit %d: paged order %v, want %v", limit, order, names)
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("limit %d: item %s seen %d times", limit, id, n)
			}
		}
		if wantPages := (len(names) + limit - 1) / limit; pages != wantPages {
			t.Errorf("limit %d: %d pages, want %d", limit, pages, wantPages)
		}
	}
	for _, bad := range []string{"?limit=0", "?limit=5001", "?limit=x"} {
		e.problem(e.do("GET", path+bad, tokC1, nil), 400, "invalid_limit")
	}
	e.problem(e.do("GET", path+"?continue=%21%21", tokC1, nil), 400, "invalid_continue")

	// Conditional requests.
	r = e.do("GET", path, tokC1, nil, "If-None-Match", etag1)
	if r.code != http.StatusNotModified || len(r.body) != 0 || r.header.Get("ETag") != etag1 {
		t.Errorf("If-None-Match current: %d %q etag %q", r.code, r.body, r.header.Get("ETag"))
	}
	if r = e.do("GET", path, tokC1, nil, "If-None-Match", `W/`+etag1); r.code != http.StatusNotModified {
		t.Errorf("weak If-None-Match: %d", r.code)
	}
	if r = e.do("GET", path, tokC1, nil, "If-None-Match", `"other"`); r.code != 200 {
		t.Errorf("If-None-Match other: %d", r.code)
	}
	if r = e.do("GET", path+"?namespace=web", tokC1, nil, "If-None-Match", etag1); r.code != 200 {
		t.Errorf("other query with the same ETag: %d", r.code)
	}
	// A new run gives new ids and a new ETag.
	e.advance(10 * time.Minute)
	e.run(e.now, false)
	r = e.do("GET", path, tokC1, nil, "If-None-Match", etag1)
	if r.code != 200 {
		t.Fatalf("after new run: %d", r.code)
	}
	if etag2 := r.header.Get("ETag"); etag2 == "" || etag2 == etag1 {
		t.Errorf("ETag unchanged after a new run: %q", etag2)
	}
	if r = e.do("GET", path, tokC1, nil, "If-None-Match", r.header.Get("ETag")); r.code != http.StatusNotModified {
		t.Errorf("new ETag not honoured: %d", r.code)
	}

	// Admin may read any cluster; c2 list is independent.
	var c2 hubapi.RecommendationList
	e.getJSON("/v1/clusters/c2/recommendations", tokAdmin, &c2)
	if c2.Cluster != "c2" || len(c2.Items) != 1 || c2.Items[0].Workload.Name != "api" {
		t.Errorf("c2: %+v", c2)
	}
}

func checkoutStatus() hubapi.WorkloadStatus {
	return hubapi.WorkloadStatus{
		Workload: kCheckout.Ref(),
		Policy:   "payments-dev",
		Modes:    hubapi.EffectiveModes{CPU: hubapi.ModeAutoPatch, Memory: hubapi.ModeAudit, CPUSource: "policy", MemorySource: "cluster-default"},
		Strategy: "in-place", Phase: "Applied", Reason: "Applied",
		Settings: &hubapi.ValueSettings{Profile: hubapi.ProfileDev},
		Live:     []hubapi.ContainerResources{{Name: "app", Requests: &hubapi.ResourceValues{CPU: "250m", Memory: "256Mi"}}},
	}
}

func TestAgentWorkloadStatus(t *testing.T) {
	e := newEnv(t)
	const path = "/v1/clusters/c1/workload-status"

	// Before any agent request, both clusters were never seen.
	var cl hubapi.ClusterList
	e.getJSON("/v1/clusters", tokUI, &cl)
	for _, c := range cl.Items {
		if c.AgentStatus != "never" || c.LastHeartbeatAt != nil {
			t.Errorf("%s before agent requests: %+v", c.Name, c)
		}
	}

	ledger := hubapi.WorkloadStatus{
		Workload: kLedger.Ref(), Modes: hubapi.EffectiveModes{CPU: hubapi.ModeOff, Memory: hubapi.ModeOff},
		Phase: "Blocked", Reason: "PDBViolation", Message: "would violate PDB",
	}
	var res hubapi.BatchResult
	e.postJSON(path, tokC1, hubapi.WorkloadStatusBatch{AgentVersion: "1.2.3", Items: []hubapi.WorkloadStatus{checkoutStatus(), ledger}}, &res)
	if res.Accepted != 2 || res.Duplicates != nil {
		t.Errorf("result %+v", res)
	}

	// Errors.
	big := hubapi.WorkloadStatusBatch{}
	for i := 0; i < 1001; i++ {
		s := checkoutStatus()
		s.Workload.Name = fmt.Sprintf("w%d", i)
		big.Items = append(big.Items, s)
	}
	e.problem(e.do("POST", path, tokC1, big), 413, "batch_too_large")
	bad := checkoutStatus()
	bad.Phase = "Done"
	p := e.problem(e.do("POST", path, tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{checkoutStatus(), bad}}), 400, "invalid_item")
	if !strings.Contains(p.Detail, "items[1]") {
		t.Errorf("detail %q does not name the item", p.Detail)
	}
	e.problem(e.do("POST", path, tokC1, `{"items": [`), 400, "invalid_json")
	e.problem(e.do("POST", path, tokC1, `{"items": "x"}`), 400, "invalid_json")
	// Unknown fields are ignored.
	e.postJSON(path, tokC1, `{"items": [], "future": true}`, &res)
	if res.Accepted != 0 {
		t.Errorf("empty batch: %+v", res)
	}

	// Detail shows the status; listing shows modes and phase.
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Status == nil || d.Status.Phase != "Applied" || d.Status.Policy != "payments-dev" || d.Status.Settings == nil ||
		d.Status.Settings.Profile != "dev" || len(d.Status.Live) != 1 || d.Status.ObservedAt == nil {
		t.Errorf("status: %+v", d.Status)
	}
	if d.Modes.CPU != hubapi.ModeAutoPatch || d.Modes.CPUSource != "policy" || d.Phase != "Applied" {
		t.Errorf("modes/phase: %+v %s", d.Modes, d.Phase)
	}

	// agentVersion and heartbeat.
	e.getJSON("/v1/clusters", tokUI, &cl)
	byName := map[string]hubapi.ClusterInfo{}
	for _, c := range cl.Items {
		byName[c.Name] = c
	}
	c1 := byName["c1"]
	if c1.AgentVersion != "1.2.3" || c1.AgentStatus != "connected" || c1.LastHeartbeatAt == nil {
		t.Errorf("c1: %+v", c1)
	}
	if c1.Counts.AutoPatch != 1 || c1.Counts.Blocked != 1 || c1.Counts.Workloads != 5 || c1.Counts.WithRecommendation != 4 {
		t.Errorf("c1 counts: %+v", c1.Counts)
	}
	if c2 := byName["c2"]; c2.AgentStatus != "never" || c2.AgentVersion != "" || c2.Counts.Workloads != 1 {
		t.Errorf("c2: %+v", c2)
	}
	// A later batch without agentVersion keeps the version.
	e.postJSON(path, tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{checkoutStatus()}}, &res)
	e.getJSON("/v1/clusters", tokUI, &cl)
	if cl.Items[0].AgentVersion != "1.2.3" {
		t.Errorf("agentVersion lost: %+v", cl.Items[0])
	}

	// The reported profile is used by the next run.
	e.advance(10 * time.Minute)
	e.run(e.now, false)
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Recommendation == nil {
		t.Fatal("no recommendation")
	}
	if d.Recommendation.Settings.Profile != hubapi.ProfileDev {
		t.Errorf("checkout settings.profile = %q, want dev", d.Recommendation.Settings.Profile)
	}
	for _, c := range d.Recommendation.Containers {
		if c.Explanation == nil || c.Explanation.Profile != hubapi.ProfileDev || c.Explanation.Window != "168h0m0s" {
			t.Errorf("checkout/%s explanation %+v, want profile dev window 168h0m0s", c.Name, c.Explanation)
		}
	}
	if d.Recommendation.DataCoverage.Window != "168h0m0s" {
		t.Errorf("dataCoverage.window %q", d.Recommendation.DataCoverage.Window)
	}
	// ledger reported no settings: default profile.
	e.getJSON(wlPath(kLedger, ""), tokUI, &d)
	if d.Recommendation.Settings.Profile != hubapi.ProfileProduction || d.Recommendation.Containers[0].Explanation.Window != "336h0m0s" {
		t.Errorf("ledger: %+v", d.Recommendation.Settings)
	}
	// The agent sees the settings echoed in its list.
	var list hubapi.RecommendationList
	e.getJSON("/v1/clusters/c1/recommendations?namespace=payments", tokC1, &list)
	if list.Items[0].Workload.Name != "checkout" || list.Items[0].Settings.Profile != hubapi.ProfileDev {
		t.Errorf("agent list settings: %+v", list.Items[0].Settings)
	}
}

func event(i int, typ string, k hubapi.WorkloadKey, at time.Time, resources ...string) hubapi.Event {
	return hubapi.Event{
		ID: newUUID(i), Type: typ, Time: at, Workload: k.Ref(), Reason: "Reason" + strings.ToUpper(typ[:1]) + typ[1:2],
		Strategy: "in-place", Resources: resources, Message: fmt.Sprintf("event %d", i),
		Details:    map[string]string{"n": fmt.Sprint(i)},
		Containers: []hubapi.ContainerChange{{Name: "app", To: &hubapi.Resources{Requests: &hubapi.ResourceValues{CPU: "100m"}}}},
	}
}

func TestAgentEventsAndAudit(t *testing.T) {
	e := newEnv(t)
	base := e.now.Add(-6 * time.Hour).Truncate(time.Second)
	c1Events := []hubapi.Event{
		event(1, "applied", kCheckout, base.Add(1*time.Hour), "cpu", "memory"),
		event(2, "blocked", kLedger, base.Add(2*time.Hour), "memory"),
		event(3, "skipped", kCheckout, base.Add(3*time.Hour)),
		event(4, "applied", kFrontend, base.Add(4*time.Hour), "cpu"),
		// Same time as event 4: ordered by id.
		event(5, "failed", kCheckout, base.Add(4*time.Hour), "cpu"),
		// Unknown workload: stored without a workload_id link.
		event(6, "skipped", key("c1", "ghost", hubapi.KindDaemonSet, "nothing"), base.Add(5*time.Hour)),
	}
	c1Events[2].Strategy = ""
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: c1Events}, &res)
	if res.Accepted != 6 || res.Duplicates == nil || *res.Duplicates != 0 {
		t.Errorf("first post: %+v", res)
	}
	// Idempotent: posting again stores nothing.
	e.postJSON("/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: c1Events[:1]}, &res)
	if res.Accepted != 0 || res.Duplicates == nil || *res.Duplicates != 1 {
		t.Errorf("second post: %+v", res)
	}
	e.postJSON("/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: c1Events}, &res)
	if res.Accepted != 0 || *res.Duplicates != 6 {
		t.Errorf("third post: %+v", res)
	}
	e.postJSON("/v1/clusters/c2/events", tokC2, hubapi.EventBatch{Items: []hubapi.Event{event(7, "applied", kAPI, base.Add(90*time.Minute), "cpu")}}, &res)
	if res.Accepted != 1 {
		t.Errorf("c2 post: %+v", res)
	}
	bad := event(8, "applied", kCheckout, base)
	bad.Reason = "lowercase"
	e.problem(e.do("POST", "/v1/clusters/c1/events", tokC1, hubapi.EventBatch{Items: []hubapi.Event{bad}}), 400, "invalid_item")
	var tooMany hubapi.EventBatch
	for i := 0; i < 1001; i++ {
		tooMany.Items = append(tooMany.Items, event(100+i, "applied", kCheckout, base))
	}
	e.problem(e.do("POST", "/v1/clusters/c1/events", tokC1, tooMany), 413, "batch_too_large")
	e.problem(e.do("POST", "/v1/clusters/c1/events", tokC1, "nope"), 400, "invalid_json")

	// kerf_actions_total counts stored events once per resource, with the workload labels.
	ms := e.metrics()
	wl := func(k hubapi.WorkloadKey, l map[string]string) map[string]string {
		l["cluster"], l["namespace"], l["workload_kind"], l["workload"] = k.Cluster, k.Namespace, k.Kind, k.Name
		return l
	}
	for _, want := range []struct {
		labels map[string]string
		value  string
	}{
		{wl(kCheckout, map[string]string{"type": "applied", "resource": "cpu", "strategy": "in-place"}), "1"},
		{wl(kFrontend, map[string]string{"type": "applied", "resource": "cpu", "strategy": "in-place"}), "1"},
		{wl(kCheckout, map[string]string{"type": "applied", "resource": "memory"}), "1"},
		{wl(kLedger, map[string]string{"type": "blocked", "resource": "memory"}), "1"},
		{wl(kCheckout, map[string]string{"type": "skipped", "resource": "", "strategy": ""}), "1"},
		{wl(key("c1", "ghost", hubapi.KindDaemonSet, "nothing"), map[string]string{"type": "skipped"}), "1"},
		{wl(kAPI, map[string]string{"type": "applied", "resource": "cpu"}), "1"},
	} {
		if v, ok := ms.find("kerf_actions_total", want.labels); !ok || v != want.value {
			t.Errorf("kerf_actions_total%v = %q (found %v), want %s", want.labels, v, ok, want.value)
		}
	}

	ids := func(items []hubapi.AuditEvent) []int {
		var out []int
		for _, it := range items {
			var n int
			_, _ = fmt.Sscanf(it.ID[len(it.ID)-12:], "%d", &n)
			out = append(out, n)
		}
		return out
	}
	audit := func(token, query string) hubapi.AuditEventList {
		t.Helper()
		var l hubapi.AuditEventList
		e.getJSON("/v1/audit"+query, token, &l)
		return l
	}
	all := audit(tokUI, "")
	if got := ids(all.Items); !slices.Equal(got, []int{1, 7, 2, 3, 4, 5, 6}) {
		t.Errorf("all events asc = %v", got)
	}
	first := all.Items[0]
	if first.Cluster != "c1" || first.WorkloadID != kCheckout.ID() || first.ReceivedAt.IsZero() || !first.Time.Equal(c1Events[0].Time) ||
		first.Strategy != "in-place" || !slices.Equal(first.Resources, []string{"cpu", "memory"}) || first.Details["n"] != "1" ||
		len(first.Containers) != 1 || first.Message != "event 1" {
		t.Errorf("first event: %+v", first)
	}
	if ghost := all.Items[6]; ghost.WorkloadID != "c1/ghost/DaemonSet/nothing" || ghost.Resources != nil {
		t.Errorf("ghost event: %+v", ghost)
	}
	cases := []struct {
		name, token, query string
		want               []int
	}{
		{"type repeatable", tokUI, "?type=applied&type=failed", []int{1, 7, 4, 5}},
		{"type single", tokUI, "?type=blocked", []int{2}},
		{"cluster", tokUI, "?cluster=c2", []int{7}},
		{"workload", tokUI, "?workload=checkout", []int{1, 3, 5}},
		{"namespace+kind", tokUI, "?namespace=payments&kind=StatefulSet", []int{2}},
		{"from inclusive", tokUI, "?from=" + url.QueryEscape(base.Add(3*time.Hour).Format(time.RFC3339)), []int{3, 4, 5, 6}},
		{"to exclusive", tokUI, "?to=" + url.QueryEscape(base.Add(3*time.Hour).Format(time.RFC3339)), []int{1, 7, 2}},
		{"from+to", tokUI, "?from=" + url.QueryEscape(base.Add(2*time.Hour).Format(time.RFC3339Nano)) + "&to=" + url.QueryEscape(base.Add(4*time.Hour).Format(time.RFC3339)), []int{2, 3}},
		{"desc", tokUI, "?order=desc", []int{6, 5, 4, 3, 2, 7, 1}},
		{"asc explicit", tokUI, "?order=asc&type=skipped", []int{3, 6}},
		{"agent implicit cluster", tokC1, "", []int{1, 2, 3, 4, 5, 6}},
		{"agent own cluster", tokC1, "?cluster=c1&type=applied", []int{1, 4}},
		{"agent c2", tokC2, "", []int{7}},
		{"admin", tokAdmin, "?cluster=c1&workload=ledger", []int{2}},
		{"no match", tokUI, "?workload=nothing-here", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := audit(tc.token, tc.query)
			if got := ids(l.Items); !slices.Equal(got, tc.want) {
				t.Errorf("ids = %v, want %v", got, tc.want)
			}
			if l.Items == nil {
				t.Error("items is null")
			}
			if l.Continue != "" {
				t.Errorf("unexpected continue %q", l.Continue)
			}
		})
	}
	// Paging with limit+continue, both orders, with filters kept.
	for _, tc := range []struct {
		query string
		want  []int
	}{
		{"", []int{1, 7, 2, 3, 4, 5, 6}},
		{"&order=desc", []int{6, 5, 4, 3, 2, 7, 1}},
		{"&cluster=c1&type=applied&type=failed&type=skipped", []int{1, 3, 4, 5, 6}},
	} {
		for _, limit := range []int{1, 2, 3, 7} {
			var got []int
			next := ""
			for i := 0; ; i++ {
				q := fmt.Sprintf("?limit=%d%s", limit, tc.query)
				if next != "" {
					q += "&continue=" + url.QueryEscape(next)
				}
				l := audit(tokUI, q)
				if len(l.Items) > limit {
					t.Fatalf("page larger than limit")
				}
				got = append(got, ids(l.Items)...)
				if l.Continue == "" {
					break
				}
				if i > 10 {
					t.Fatal("paging does not terminate")
				}
				next = l.Continue
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("paging %q limit %d: %v, want %v", tc.query, limit, got, tc.want)
			}
		}
	}
	for _, tc := range []struct{ query, code string }{
		{"?type=deleted", "invalid_type"},
		{"?kind=Pod", "invalid_kind"},
		{"?cluster=Bad_Name", "invalid_cluster"},
		{"?order=up", "invalid_order"},
		{"?limit=0", "invalid_limit"},
		{"?limit=1001", "invalid_limit"},
		{"?from=yesterday", "invalid_from"},
		{"?to=2026-01-01", "invalid_to"},
		{"?continue=xyz", "invalid_continue"},
		{"?continue=" + encodeCursor(cursor{Offset: 3}), "invalid_continue"},
	} {
		e.problem(e.do("GET", "/v1/audit"+tc.query, tokUI, nil), 400, tc.code)
	}

	// History: recommendations and events newest first.
	var h hubapi.WorkloadHistory
	e.getJSON(wlPath(kCheckout, "/history"), tokUI, &h)
	if h.WorkloadID != kCheckout.ID() {
		t.Errorf("workloadId %q", h.WorkloadID)
	}
	if got := ids(h.Events); !slices.Equal(got, []int{5, 3, 1}) {
		t.Errorf("history events = %v, want [5 3 1]", got)
	}
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if len(h.Recommendations) != 3 {
		t.Fatalf("history has %d recommendations, want 3 (2 backfill + 1)", len(h.Recommendations))
	}
	if h.Recommendations[0].ID != d.Recommendation.ID {
		t.Errorf("newest history item %s is not the current recommendation %s", h.Recommendations[0].ID, d.Recommendation.ID)
	}
	wantTimes := []time.Time{e.now.Truncate(fakeStep), e.backfillAsOf[1], e.backfillAsOf[0]}
	for i, rec := range h.Recommendations {
		if !rec.GeneratedAt.Equal(wantTimes[i]) {
			t.Errorf("history[%d].generatedAt = %s, want %s", i, rec.GeneratedAt, wantTimes[i])
		}
		for _, c := range rec.Containers {
			if c.Explanation != nil {
				t.Errorf("history[%d] has an explanation", i)
			}
		}
	}
	// limit applies to both lists; from/to window.
	e.getJSON(wlPath(kCheckout, "/history?limit=1"), tokUI, &h)
	if len(h.Recommendations) != 1 || len(h.Events) != 1 || h.Events[0].ID != newUUID(5) {
		t.Errorf("limit=1: %d recs, events %v", len(h.Recommendations), ids(h.Events))
	}
	from := url.QueryEscape(e.backfillAsOf[1].Add(-time.Minute).Format(time.RFC3339))
	to := url.QueryEscape(base.Add(2 * time.Hour).Format(time.RFC3339))
	e.getJSON(wlPath(kCheckout, "/history?from="+from+"&to="+to), tokUI, &h)
	if len(h.Recommendations) != 1 || !h.Recommendations[0].GeneratedAt.Equal(e.backfillAsOf[1]) || !slices.Equal(ids(h.Events), []int{1}) {
		t.Errorf("window: %d recs, events %v", len(h.Recommendations), ids(h.Events))
	}
	e.problem(e.do("GET", wlPath(kCheckout, "/history?from="+to+"&to="+from), tokUI, nil), 400, "invalid_time_range")
	e.problem(e.do("GET", wlPath(kCheckout, "/history?limit=0"), tokUI, nil), 400, "invalid_limit")
	e.problem(e.do("GET", "/v1/workloads/c1%2Fx%2FDeployment%2Fnope/history", tokUI, nil), 404, "workload_not_found")
	var hr hubapi.WorkloadHistory
	e.getJSON(wlPath(kReport, "/history"), tokUI, &hr)
	if hr.Recommendations == nil || hr.Events == nil || len(hr.Recommendations) != 0 {
		t.Errorf("report history: %+v (lists must be [])", hr)
	}
}

func TestUISummaryAndClusters(t *testing.T) {
	e := newEnv(t)
	var sum hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &sum)
	if !sum.GeneratedAt.Equal(e.now) {
		t.Errorf("generatedAt %s", sum.GeneratedAt)
	}
	if len(sum.Clusters) != 2 || sum.Clusters[0].Cluster != "c1" || sum.Clusters[1].Cluster != "c2" {
		t.Fatalf("clusters: %+v", sum.Clusters)
	}
	c1, c2, f := sum.Clusters[0].Summary, sum.Clusters[1].Summary, sum.Fleet
	if c1.Workloads != 5 || c2.Workloads != 1 || f.Workloads != 6 {
		t.Errorf("workloads %d %d %d", c1.Workloads, c2.Workloads, f.Workloads)
	}
	// Never reported: audit / Pending (global default).
	for name, s := range map[string]hubapi.Summary{"fleet": f, "c1": c1, "c2": c2} {
		if s.WorkloadsByMode.CPU.Audit != s.Workloads || s.WorkloadsByMode.Memory.Audit != s.Workloads ||
			s.WorkloadsByMode.CPU.Off+s.WorkloadsByMode.CPU.AutoPatch != 0 || s.WorkloadsByPhase["Pending"] != s.Workloads {
			t.Errorf("%s modes/phases: %+v %v", name, s.WorkloadsByMode, s.WorkloadsByPhase)
		}
		if s.LastRecommenderRunAt == nil {
			t.Errorf("%s: lastRecommenderRunAt missing", name)
		}
		if s.MonthlySavingsUSD.Total <= 0 || !approx(s.MonthlySavingsUSD.Total, s.MonthlySavingsUSD.CPU+s.MonthlySavingsUSD.Memory, 0.011) {
			t.Errorf("%s savings %+v", name, s.MonthlySavingsUSD)
		}
		if !(s.CPU.Requested > s.CPU.Recommended && s.CPU.Recommended > 0 && s.CPU.Used > 0) ||
			!(s.Memory.Requested > s.Memory.Recommended && s.Memory.Recommended > 0 && s.Memory.Used > 0) {
			t.Errorf("%s totals cpu %+v memory %+v", name, s.CPU, s.Memory)
		}
	}
	// Fleet = sum of clusters.
	if !approx(f.MonthlySavingsUSD.Total, c1.MonthlySavingsUSD.Total+c2.MonthlySavingsUSD.Total, 0.011) ||
		!approx(f.CPU.Requested, c1.CPU.Requested+c2.CPU.Requested, 0.002) ||
		!approx(f.CPU.Recommended, c1.CPU.Recommended+c2.CPU.Recommended, 0.002) ||
		!approx(f.CPU.Used, c1.CPU.Used+c2.CPU.Used, 0.002) ||
		!approx(f.Memory.Requested, c1.Memory.Requested+c2.Memory.Requested, 2) {
		t.Errorf("fleet != sum of clusters: fleet %+v c1 %+v c2 %+v", f, c1, c2)
	}
	for flag, n := range f.WorkloadsByRiskFlag {
		if c1.WorkloadsByRiskFlag[flag]+c2.WorkloadsByRiskFlag[flag] != n {
			t.Errorf("risk flag %s: fleet %d", flag, n)
		}
	}
	if f.WorkloadsByRiskFlag[hubapi.FlagOOMKilled] != 1 || f.WorkloadsByRiskFlag[hubapi.FlagLowConfidence] != 1 {
		t.Errorf("risk flags %v", f.WorkloadsByRiskFlag)
	}
	// used = 24 h usage summed over pods (fake: mean usage x replicas).
	wantUsedCPU := 0.2*3 + 0.4*1 + 0.05*2 + 0.1*1
	if !approx(c1.CPU.Used, wantUsedCPU, 0.002) {
		t.Errorf("c1 cpu used %v, want %v", c1.CPU.Used, wantUsedCPU)
	}
	// Savings = sum over the latest recommendations.
	var list hubapi.WorkloadList
	e.getJSON("/v1/workloads?limit=500", tokUI, &list)
	total := 0.0
	for _, r := range list.Items {
		if r.MonthlySavingsUSD != nil {
			total += r.MonthlySavingsUSD.Total
		}
	}
	if !approx(f.MonthlySavingsUSD.Total, total, 0.05) {
		t.Errorf("fleet savings %v, sum of rows %v", f.MonthlySavingsUSD.Total, total)
	}

	// lastRecommenderRunAt is the latest non-backfill run; a later backfill run does not change it.
	lastRun := *f.LastRecommenderRunAt
	if !c1.LastRecommenderRunAt.Equal(lastRun) && !c2.LastRecommenderRunAt.Equal(lastRun) {
		t.Errorf("fleet lastRecommenderRunAt %s not the latest cluster run (%s, %s)", lastRun, c1.LastRecommenderRunAt, c2.LastRecommenderRunAt)
	}
	e.advance(time.Second)
	e.run(e.now.Add(-72*time.Hour), true)
	var backfillFinished time.Time
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT max(rc.finished_at) FROM recommender_run_clusters rc JOIN recommender_runs r ON r.id = rc.run_id WHERE r.backfill`).Scan(&backfillFinished); err != nil {
		t.Fatal(err)
	}
	if !backfillFinished.After(lastRun) {
		t.Fatalf("test setup: backfill finished %s not after %s", backfillFinished, lastRun)
	}
	e.getJSON("/v1/summary", tokUI, &sum)
	if !sum.Fleet.LastRecommenderRunAt.Equal(lastRun) {
		t.Errorf("backfill run changed lastRecommenderRunAt: %s -> %s", lastRun, ptrStr(sum.Fleet.LastRecommenderRunAt))
	}
	// A regular run moves it.
	e.advance(10 * time.Minute)
	e.run(e.now, false)
	e.getJSON("/v1/summary", tokUI, &sum)
	if !sum.Fleet.LastRecommenderRunAt.After(lastRun) {
		t.Errorf("regular run did not move lastRecommenderRunAt: %s", ptrStr(sum.Fleet.LastRecommenderRunAt))
	}

	// A cluster without runs or workloads has an empty summary.
	if err := e.st.EnsureCluster(e.ctx, "c3", ""); err != nil {
		t.Fatal(err)
	}
	e.getJSON("/v1/summary", tokUI, &sum)
	if c3 := sum.Clusters[2]; c3.Cluster != "c3" || c3.Workloads != 0 || c3.LastRecommenderRunAt != nil || c3.WorkloadsByRiskFlag == nil {
		t.Errorf("c3 summary: %+v", c3)
	}

	// Clusters.
	var cl hubapi.ClusterList
	e.getJSON("/v1/clusters", tokUI, &cl)
	if len(cl.Items) != 3 {
		t.Fatalf("clusters: %+v", cl.Items)
	}
	want := map[string]hubapi.ClusterCounts{"c1": {Workloads: 5, WithRecommendation: 4}, "c2": {Workloads: 1, WithRecommendation: 1}, "c3": {}}
	for i, c := range cl.Items {
		if c.Name != []string{"c1", "c2", "c3"}[i] {
			t.Errorf("order: %s at %d", c.Name, i)
		}
		if c.Counts != want[c.Name] || c.AgentStatus != "never" || c.CreatedAt == nil {
			t.Errorf("%s: %+v", c.Name, c)
		}
	}
	if cl.Items[0].Description != "test cluster c1" {
		t.Errorf("description %q", cl.Items[0].Description)
	}
	// Stale heartbeat.
	if _, err := e.st.Pool.Exec(e.ctx, `UPDATE clusters SET last_seen_at = $1 WHERE name = 'c2'`, e.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.do("GET", "/v1/clusters/c1/recommendations", tokC1, nil)
	e.getJSON("/v1/clusters", tokUI, &cl)
	if cl.Items[0].AgentStatus != "connected" || cl.Items[1].AgentStatus != "stale" || cl.Items[2].AgentStatus != "never" {
		t.Errorf("agentStatus: %s %s %s", cl.Items[0].AgentStatus, cl.Items[1].AgentStatus, cl.Items[2].AgentStatus)
	}
}

func rowNames(items []hubapi.WorkloadRow) []string {
	var out []string
	for _, r := range items {
		out = append(out, r.Name)
	}
	return out
}

func TestUIWorkloads(t *testing.T) {
	e := newEnv(t)
	// checkout: auto-patch CPU.
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{checkoutStatus()}}, &res)

	var all hubapi.WorkloadList
	e.getJSON("/v1/workloads", tokUI, &all)
	if all.Total != 6 || len(all.Items) != 6 || all.Continue != "" {
		t.Fatalf("all: total %d items %d", all.Total, len(all.Items))
	}
	// Default sort: savings desc, rows without a recommendation last.
	last := all.Items[len(all.Items)-1]
	if last.Name != "report" || last.RecommendationID != "" || last.MonthlySavingsUSD != nil || last.Confidence != nil || last.GeneratedAt != nil {
		t.Errorf("last row %+v, want report without recommendation fields", last)
	}
	savings := map[string]float64{}
	for i, r := range all.Items[:5] {
		if r.MonthlySavingsUSD == nil {
			t.Fatalf("%s has no savings", r.Name)
		}
		savings[r.Name] = r.MonthlySavingsUSD.Total
		if i > 0 && r.MonthlySavingsUSD.Total > all.Items[i-1].MonthlySavingsUSD.Total {
			t.Errorf("not sorted by savings desc: %v", rowNames(all.Items))
		}
		for _, c := range r.Containers {
			if c.Explanation != nil {
				t.Errorf("%s: explanation in list", r.Name)
			}
		}
		if r.ID != r.Cluster+"/"+r.Namespace+"/"+r.Kind+"/"+r.Name {
			t.Errorf("id %q", r.ID)
		}
	}
	var asc hubapi.WorkloadList
	e.getJSON("/v1/workloads?order=asc", tokUI, &asc)
	if n := rowNames(asc.Items); n[len(n)-1] != "report" {
		t.Errorf("savings asc: no-recommendation row must stay last: %v", n)
	}
	for _, r := range all.Items {
		switch r.Name {
		case "checkout":
			if r.Modes.CPU != hubapi.ModeAutoPatch || r.Phase != "Applied" {
				t.Errorf("checkout modes %+v phase %s", r.Modes, r.Phase)
			}
		default:
			if r.Modes != hubapi.DefaultModes() || r.Phase != "Pending" {
				t.Errorf("%s modes %+v phase %s", r.Name, r.Modes, r.Phase)
			}
		}
	}
	// minSavings threshold between ledger's and checkout's savings.
	thr := (savings["checkout"] + savings["api"]) / 2
	if savings["checkout"] <= savings["api"] {
		t.Fatalf("test data: checkout savings %v <= api %v", savings["checkout"], savings["api"])
	}
	cases := []struct {
		name, query string
		want        []string
	}{
		{"cluster", "?cluster=c2", []string{"api"}},
		{"cluster repeat", "?cluster=c2&cluster=c1&sort=name", []string{"api", "checkout", "frontend", "ledger", "legacy", "report"}},
		{"namespace", "?namespace=web&sort=name", []string{"frontend", "legacy"}},
		{"kind", "?kind=StatefulSet", []string{"ledger"}},
		{"kind repeat", "?kind=CronJob&kind=StatefulSet&sort=name", []string{"ledger", "report"}},
		{"mode auto-patch", "?mode=auto-patch", []string{"checkout"}},
		{"mode off", "?mode=off", nil},
		{"mode audit (cpu or memory)", "?mode=audit&sort=name", []string{"api", "checkout", "frontend", "ledger", "legacy", "report"}},
		{"riskFlag", "?riskFlag=oom-killed", []string{"ledger"}},
		{"riskFlag any", "?riskFlag=oom-killed&riskFlag=low-confidence&sort=name", []string{"frontend", "ledger"}},
		{"minSavings", fmt.Sprintf("?minSavings=%v", thr), []string{"checkout"}},
		{"minSavings negative keeps all with recommendation", "?minSavings=-1000000&sort=name", []string{"api", "checkout", "frontend", "ledger", "legacy"}},
		{"q namespace case-insensitive", "?q=PAY&sort=name", []string{"api", "checkout", "ledger"}},
		{"q name", "?q=ront", []string{"frontend"}},
		{"q nothing", "?q=zzz", nil},
		{"combined", "?cluster=c1&namespace=payments&kind=Deployment", []string{"checkout"}},
		{"sort name", "?sort=name", []string{"api", "checkout", "frontend", "ledger", "legacy", "report"}},
		{"sort name desc", "?sort=name&order=desc", []string{"report", "legacy", "ledger", "frontend", "checkout", "api"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var l hubapi.WorkloadList
			e.getJSON("/v1/workloads"+tc.query, tokUI, &l)
			if got := rowNames(l.Items); !slices.Equal(got, tc.want) {
				t.Errorf("rows = %v, want %v", got, tc.want)
			}
			if l.Total != len(tc.want) || l.Items == nil {
				t.Errorf("total %d, items nil=%v", l.Total, l.Items == nil)
			}
		})
	}
	// Confidence sort.
	var byConf hubapi.WorkloadList
	e.getJSON("/v1/workloads?sort=confidence", tokUI, &byConf)
	for i := 1; i < 5; i++ {
		if *byConf.Items[i].Confidence > *byConf.Items[i-1].Confidence {
			t.Errorf("confidence not desc: %v", rowNames(byConf.Items))
		}
	}
	if byConf.Items[4].Name != "frontend" || byConf.Items[5].Name != "report" {
		t.Errorf("confidence order %v (frontend has the least data)", rowNames(byConf.Items))
	}

	// Paging.
	for _, sortQ := range []string{"sort=name", "sort=savings", "sort=confidence&order=asc"} {
		var full hubapi.WorkloadList
		e.getJSON("/v1/workloads?"+sortQ, tokUI, &full)
		var got []string
		next := ""
		for i := 0; ; i++ {
			q := "/v1/workloads?limit=4&" + sortQ
			if next != "" {
				q += "&continue=" + url.QueryEscape(next)
			}
			var page hubapi.WorkloadList
			e.getJSON(q, tokUI, &page)
			if page.Total != 6 || len(page.Items) > 4 {
				t.Errorf("%s page %d: total %d items %d", sortQ, i, page.Total, len(page.Items))
			}
			got = append(got, rowNames(page.Items)...)
			if page.Continue == "" {
				break
			}
			next = page.Continue
			if i > 5 {
				t.Fatal("paging does not terminate")
			}
		}
		if !slices.Equal(got, rowNames(full.Items)) {
			t.Errorf("%s paged %v, want %v", sortQ, got, rowNames(full.Items))
		}
	}
	var p1 hubapi.WorkloadList
	e.getJSON("/v1/workloads?limit=2&sort=name", tokUI, &p1)
	if p1.Continue == "" {
		t.Fatal("no continue")
	}
	// A different limit is fine, changed filters are not.
	var p2 hubapi.WorkloadList
	e.getJSON("/v1/workloads?limit=3&sort=name&continue="+url.QueryEscape(p1.Continue), tokUI, &p2)
	if got := rowNames(p2.Items); !slices.Equal(got, []string{"frontend", "ledger", "legacy"}) {
		t.Errorf("page 2 with limit 3: %v", got)
	}
	for _, q := range []string{"limit=2&sort=name&cluster=c1", "limit=2&sort=savings", "limit=2&sort=name&order=desc", "limit=2"} {
		e.problem(e.do("GET", "/v1/workloads?"+q+"&continue="+url.QueryEscape(p1.Continue), tokUI, nil), 400, "invalid_continue")
	}
	for _, tc := range []struct{ query, code string }{
		{"?cluster=C1", "invalid_cluster"},
		{"?kind=Pod", "invalid_kind"},
		{"?mode=manual", "invalid_mode"},
		{"?riskFlag=scary", "invalid_risk_flag"},
		{"?minSavings=lots", "invalid_min_savings"},
		{"?minSavings=NaN", "invalid_min_savings"},
		{"?q=" + strings.Repeat("a", 254), "invalid_q"},
		{"?sort=size", "invalid_sort"},
		{"?order=random", "invalid_order"},
		{"?limit=0", "invalid_limit"},
		{"?limit=501", "invalid_limit"},
		{"?continue=@@", "invalid_continue"},
	} {
		e.problem(e.do("GET", "/v1/workloads"+tc.query, tokUI, nil), 400, tc.code)
	}

	// Detail with a %2F-escaped id.
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.ID != kCheckout.ID() || d.Cluster != "c1" || d.Namespace != "payments" || d.Kind != "Deployment" || d.Name != "checkout" ||
		d.FirstSeenAt == nil || d.LastSeenAt == nil || d.Status == nil || d.Recommendation == nil {
		t.Fatalf("detail: %+v", d)
	}
	if !d.FirstSeenAt.Equal(e.backfillAsOf[0]) {
		t.Errorf("firstSeenAt %s, want first backfill %s", d.FirstSeenAt, e.backfillAsOf[0])
	}
	if !d.LastSeenAt.Equal(e.now) {
		t.Errorf("lastSeenAt %s, want %s", d.LastSeenAt, e.now)
	}
	for _, c := range d.Recommendation.Containers {
		x := c.Explanation
		if x == nil || x.Profile != hubapi.ProfileProduction || x.StrategyName != "percentile" || x.Window != "336h0m0s" || x.CPU == nil || x.Memory == nil {
			t.Errorf("container %s explanation %+v", c.Name, x)
		}
	}
	if !strings.Contains(wlPath(kCheckout, ""), "%2F") {
		t.Fatal("id not escaped")
	}
	var dl hubapi.WorkloadDetail
	e.getJSON(wlPath(kLedger, ""), tokUI, &dl)
	if dl.Status != nil || dl.Modes != hubapi.DefaultModes() || dl.Phase != "Pending" || !slices.Contains(dl.Recommendation.RiskFlags, hubapi.FlagOOMKilled) {
		t.Errorf("ledger detail: %+v", dl)
	}
	var dr hubapi.WorkloadDetail
	e.getJSON(wlPath(kReport, ""), tokUI, &dr)
	if dr.Recommendation != nil {
		t.Errorf("report has a recommendation")
	}
	e.problem(e.do("GET", wlPath(key("c1", "payments", "Deployment", "nope"), ""), tokUI, nil), 404, "workload_not_found")
	e.problem(e.do("GET", wlPath(key("c9", "payments", "Deployment", "checkout"), ""), tokUI, nil), 404, "workload_not_found")
	e.problem(e.do("GET", "/v1/workloads/garbage", tokUI, nil), 400, "invalid_workload_id")
	e.problem(e.do("GET", "/v1/workloads/c1%2Fpayments%2FPod%2Fx", tokUI, nil), 400, "invalid_workload_id")
	// Unescaped slashes do not match the route at all.
	e.problem(e.do("GET", "/v1/workloads/c1/payments/Deployment/checkout", tokUI, nil), 404, "not_found")
}

func TestWorkloadUsage(t *testing.T) {
	e := newEnv(t)
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	recCPU := mustParseCPU(t, d.Recommendation.Containers[0].Recommended.Requests.CPU)

	var u hubapi.UsageSeries
	e.getJSON(wlPath(kCheckout, "/usage?resource=cpu"), tokUI, &u)
	if u.WorkloadID != kCheckout.ID() || u.Resource != "cpu" || u.Unit != "cores" || u.Step != "15m0s" ||
		!u.To.Equal(e.now) || !u.From.Equal(e.now.Add(-7*24*time.Hour)) {
		t.Errorf("header: %+v", u)
	}
	if len(u.Containers) != 1 || u.Containers[0].Name != "app" {
		t.Fatalf("containers: %+v", u.Containers)
	}
	c := u.Containers[0]
	check := func(name string, pts []hubapi.Point, want float64) {
		t.Helper()
		if len(pts) == 0 {
			t.Errorf("%s: no points", name)
		}
		for _, p := range pts {
			if p[1] != want {
				t.Errorf("%s: value %v, want %v (foreign series leaked?)", name, p[1], want)
				return
			}
		}
	}
	check("usage", c.Usage, 0.3)
	check("usageAvg", c.UsageAvg, 0.15)
	check("request", c.Request, 1)
	check("limit", c.Limit, 2)
	if len(c.Recommendation) == 0 {
		t.Fatal("no recommendation points")
	}
	firstRec := time.Unix(int64(c.Recommendation[0][0]), 0)
	if firstRec.Before(e.backfillAsOf[0]) || firstRec.After(e.backfillAsOf[0].Add(15*time.Minute)) {
		t.Errorf("first recommendation point at %s, want the first step after the first backfill %s", firstRec, e.backfillAsOf[0])
	}
	if lastV := c.Recommendation[len(c.Recommendation)-1][1]; lastV != recCPU {
		t.Errorf("last recommendation point %v, want current recommended request %v", lastV, recCPU)
	}
	// The backfill runs recommended from the same usage: the step function exists across all runs.
	if n := len(c.Recommendation); n < 2*24*4 {
		t.Errorf("only %d recommendation points over 48 h at 15 m", n)
	}
	for _, q := range e.prom.queries {
		if !strings.Contains(q, `cluster="c1"`) || !strings.Contains(q, `namespace="payments"`) {
			t.Errorf("query not restricted to the workload's cluster and namespace: %s", q)
			break
		}
	}
	if len(e.prom.queries) != 6 {
		t.Errorf("%d queries, want 6", len(e.prom.queries))
	}

	// memory, explicit window and step, container filter.
	from := e.now.Add(-2 * time.Hour).Truncate(time.Minute)
	q := fmt.Sprintf("/usage?resource=memory&container=app&step=5m&from=%s&to=%s", url.QueryEscape(from.Format(time.RFC3339)), url.QueryEscape(e.now.Format(time.RFC3339)))
	e.getJSON(wlPath(kCheckout, q), tokUI, &u)
	if u.Unit != "bytes" || u.Step != "5m0s" || len(u.Containers) != 1 {
		t.Fatalf("memory: %+v", u)
	}
	check("memory request", u.Containers[0].Request, 1*gi)
	if len(u.Containers[0].Limit) != 0 || u.Containers[0].Limit == nil {
		t.Errorf("memory limit: %v (fake has no memory limit series; must be [])", u.Containers[0].Limit)
	}
	e.getJSON(wlPath(kCheckout, "/usage?resource=cpu&container=sidecar"), tokUI, &u)
	if len(u.Containers) != 0 {
		t.Errorf("container=sidecar: %+v", u.Containers)
	}

	// A workload without recommendation and without matching series.
	e.getJSON(wlPath(kReport, "/usage?resource=cpu"), tokUI, &u)
	if u.Containers == nil || len(u.Containers) != 0 {
		t.Errorf("report: %+v", u.Containers)
	}

	for _, tc := range []struct{ query, code string }{
		{"?resource=bogus", "invalid_resource"},
		{"", "invalid_resource"},
		{"?resource=cpu&step=1s", "invalid_step"},
		{"?resource=cpu&from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z", "invalid_time_range"},
		{"?resource=cpu&to=now", "invalid_time_range"},
	} {
		e.problem(e.do("GET", wlPath(kCheckout, "/usage"+tc.query), tokUI, nil), 400, tc.code)
	}
	e.problem(e.do("GET", wlPath(key("c1", "x", "Deployment", "y"), "/usage?resource=cpu"), tokUI, nil), 404, "workload_not_found")
	e.prom.err = errors.New("connection refused")
	e.problem(e.do("GET", wlPath(kCheckout, "/usage?resource=cpu"), tokUI, nil), 502, "metrics_unavailable")
}

// TestUsageInitContainerSeries exercises init-container series, which are written from concurrent
// queries (regression test for a map race; run with -race). The fake init container has requests
// but no usage and no recommendation, so it is left out of the response (no empty charts).
func TestUsageInitContainerSeries(t *testing.T) {
	e := newEnv(t)
	e.prom.initSeries = true
	for i := 0; i < 20; i++ {
		var u hubapi.UsageSeries
		e.getJSON(wlPath(kCheckout, "/usage?resource=cpu"), tokUI, &u)
		if len(u.Containers) != 1 || u.Containers[0].Name != "app" || len(u.Containers[0].Usage) == 0 {
			t.Fatalf("containers %+v", u.Containers)
		}
	}
}

func TestSavingsTrend(t *testing.T) {
	e := newEnv(t)
	// Oracle: per-cluster run totals as stored, effective from the end of a regular run or the
	// as-of time of a backfill run.
	type sample struct {
		at       time.Time
		cpu, mem float64
	}
	runs := map[string][]sample{}
	rows, err := e.st.Pool.Query(e.ctx, `SELECT rc.cluster, CASE WHEN r.backfill THEN rc.as_of ELSE rc.finished_at END AS at,
		rc.monthly_savings_cpu_usd, rc.monthly_savings_memory_usd FROM recommender_run_clusters rc
		JOIN recommender_runs r ON r.id = rc.run_id WHERE rc.status = 'succeeded' ORDER BY at`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		var s sample
		if err := rows.Scan(&c, &s.at, &s.cpu, &s.mem); err != nil {
			t.Fatal(err)
		}
		runs[c] = append(runs[c], s)
	}
	rows.Close()
	if len(runs["c1"]) != 3 || len(runs["c2"]) != 3 {
		t.Fatalf("runs: %+v", runs)
	}
	// The regular run's total is exactly the summary's cluster total.
	var sum hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &sum)
	for _, cs := range sum.Clusters {
		latest := runs[cs.Cluster][len(runs[cs.Cluster])-1]
		if got := round2(latest.cpu + latest.mem); got != cs.MonthlySavingsUSD.Total {
			t.Errorf("%s: run total %v != summary %v", cs.Cluster, got, cs.MonthlySavingsUSD.Total)
		}
		if approx(runs[cs.Cluster][0].cpu+runs[cs.Cluster][0].mem, latest.cpu+latest.mem, 0.01) {
			t.Errorf("%s: test data: backfill and regular totals are equal", cs.Cluster)
		}
	}
	expect := func(clusters []string, at time.Time) float64 {
		v := 0.0
		for _, c := range clusters {
			var last *sample
			for i := range runs[c] {
				if !runs[c][i].at.After(at) {
					last = &runs[c][i]
				}
			}
			if last != nil {
				v += last.cpu + last.mem
			}
		}
		return v
	}

	from := e.backfillAsOf[0].Add(-3*time.Hour - 20*time.Minute).Truncate(time.Second) // RFC 3339 on the wire; not on the step grid
	to := e.now.Add(time.Hour)
	q := fmt.Sprintf("from=%s&to=%s&step=1h", url.QueryEscape(from.Format(time.RFC3339)), url.QueryEscape(to.Format(time.RFC3339)))
	var tr hubapi.SavingsTrend
	e.getJSON("/v1/savings/trend?"+q, tokUI, &tr)
	if tr.Step != "1h0m0s" || !tr.From.Equal(from) || !tr.To.Equal(to) || len(tr.Series) != 1 || tr.Series[0].Cluster != "" {
		t.Fatalf("trend header: %+v", tr)
	}
	pts := tr.Series[0].Points
	first := from.Truncate(time.Hour) // first point: the first full hour at or after from
	if first.Before(from) {
		first = first.Add(time.Hour)
	}
	if want := int((to.Sub(first) + time.Hour - 1) / time.Hour); len(pts) != want {
		t.Errorf("%d points, want %d", len(pts), want)
	}
	bucketEnd := func(t time.Time) time.Time {
		if e := t.Add(time.Hour); e.Before(to) {
			return e
		}
		return to
	}
	distinct := map[float64]bool{}
	for i, p := range pts {
		if !p.Time.Equal(first.Add(time.Duration(i) * time.Hour)) {
			t.Errorf("point %d at %s, want aligned to the hour from %s", i, p.Time, first)
		}
		want := expect([]string{"c1", "c2"}, bucketEnd(p.Time))
		if !approx(p.Total, want, 0.011) || !approx(p.Total, p.CPU+p.Memory, 0.011) {
			t.Errorf("point %s: total %v (cpu %v mem %v), want %v", p.Time, p.Total, p.CPU, p.Memory, want)
		}
		distinct[p.Total] = true
	}
	if pts[0].Total != 0 {
		t.Errorf("before the first run: %v", pts[0].Total)
	}
	if len(distinct) != 4 {
		t.Errorf("want 4 levels (none, backfill 1, backfill 2, regular), got %v", distinct)
	}

	// Default range (to = now): the last point equals the summary, per cluster and for the fleet.
	var def hubapi.SavingsTrend
	e.getJSON("/v1/savings/trend?groupBy=cluster", tokUI, &def)
	for i, cs := range sum.Clusters {
		last := def.Series[i].Points[len(def.Series[i].Points)-1]
		if def.Series[i].Cluster != cs.Cluster || last.Total != cs.MonthlySavingsUSD.Total || last.CPU != cs.MonthlySavingsUSD.CPU {
			t.Errorf("%s: last trend point %+v != summary %+v", cs.Cluster, last, cs.MonthlySavingsUSD)
		}
	}
	var fleet hubapi.SavingsTrend
	e.getJSON("/v1/savings/trend", tokUI, &fleet)
	if last := fleet.Series[0].Points[len(fleet.Series[0].Points)-1]; last.Total != sum.Fleet.MonthlySavingsUSD.Total {
		t.Errorf("fleet: last trend point %v != summary %v", last.Total, sum.Fleet.MonthlySavingsUSD.Total)
	}

	// groupBy=cluster: one series per cluster, summing to the fleet series.
	var byC hubapi.SavingsTrend
	e.getJSON("/v1/savings/trend?groupBy=cluster&"+q, tokUI, &byC)
	if len(byC.Series) != 2 || byC.Series[0].Cluster != "c1" || byC.Series[1].Cluster != "c2" {
		t.Fatalf("groupBy=cluster: %+v", byC.Series)
	}
	for i := range pts {
		a, b := byC.Series[0].Points[i], byC.Series[1].Points[i]
		if !approx(a.Total, expect([]string{"c1"}, bucketEnd(a.Time)), 0.011) || !approx(b.Total, expect([]string{"c2"}, bucketEnd(b.Time)), 0.011) {
			t.Errorf("per-cluster point %s: %v %v", a.Time, a.Total, b.Total)
		}
		if !approx(a.Total+b.Total, pts[i].Total, 0.021) {
			t.Errorf("point %s: c1+c2 %v != fleet %v", a.Time, a.Total+b.Total, pts[i].Total)
		}
	}
	// Cluster filter.
	var c2 hubapi.SavingsTrend
	e.getJSON("/v1/savings/trend?cluster=c2&"+q, tokUI, &c2)
	for _, p := range c2.Series[0].Points {
		if !approx(p.Total, expect([]string{"c2"}, bucketEnd(p.Time)), 0.011) {
			t.Errorf("cluster=c2 point %s: %v", p.Time, p.Total)
		}
	}
	// A from after all runs still carries the latest run before it.
	late := e.now.Add(-time.Minute)
	var tl hubapi.SavingsTrend
	e.getJSON(fmt.Sprintf("/v1/savings/trend?from=%s&to=%s&step=10s", url.QueryEscape(late.Format(time.RFC3339)), url.QueryEscape(e.now.Format(time.RFC3339))), tokUI, &tl)
	lateFirst := late.Truncate(10 * time.Second)
	if lateFirst.Before(late) {
		lateFirst = lateFirst.Add(10 * time.Second)
	}
	if p := tl.Series[0].Points; len(p) != int((e.now.Sub(lateFirst)+10*time.Second-1)/(10*time.Second)) ||
		!approx(p[0].Total, expect([]string{"c1", "c2"}, lateFirst.Add(10*time.Second)), 0.011) || p[0].Total == 0 {
		t.Errorf("late window: %+v", p)
	}
	// Default window and step.
	if fleet.Step != "15m0s" || !fleet.To.Equal(e.now) || len(fleet.Series[0].Points) != 672 {
		t.Errorf("defaults: step %s to %s points %d", fleet.Step, fleet.To, len(fleet.Series[0].Points))
	}
	if last := fleet.Series[0].Points[671]; !approx(last.Total, expect([]string{"c1", "c2"}, e.now), 0.011) {
		t.Errorf("default last point %v", last)
	}
	// Unknown cluster: empty series, no error.
	var unk hubapi.SavingsTrend
	e.getJSON("/v1/savings/trend?cluster=nope&"+q, tokUI, &unk)
	for _, p := range unk.Series[0].Points {
		if p.Total != 0 {
			t.Fatalf("unknown cluster has savings %v", p)
		}
	}
	var unkG hubapi.SavingsTrend
	e.getJSON("/v1/savings/trend?groupBy=cluster&cluster=nope&"+q, tokUI, &unkG)
	if unkG.Series == nil || len(unkG.Series) != 0 {
		t.Errorf("groupBy=cluster unknown: %+v", unkG.Series)
	}
	for _, tc := range []struct{ query, code string }{
		{"?groupBy=namespace", "invalid_group_by"},
		{"?step=1s", "invalid_step"},
		{"?step=1m&from=2020-01-01T00:00:00Z", "invalid_step"},
		{"?from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z", "invalid_time_range"},
		{"?cluster=Nope", "invalid_cluster"},
	} {
		e.problem(e.do("GET", "/v1/savings/trend"+tc.query, tokUI, nil), 400, tc.code)
	}
}

func TestInventoryDeletion(t *testing.T) {
	e := newEnv(t)
	listed := func() []string {
		var l hubapi.WorkloadList
		e.getJSON("/v1/workloads?sort=name", tokUI, &l)
		return rowNames(l.Items)
	}
	all := []string{"api", "checkout", "frontend", "ledger", "legacy", "report"}
	if got := listed(); !slices.Equal(got, all) {
		t.Fatalf("initial: %v", got)
	}
	e.loader.hide(kLegacy.ID(), true)
	// A backfill run never deletes.
	e.run(e.now.Add(-12*time.Hour), true)
	if got := listed(); !slices.Equal(got, all) {
		t.Errorf("after backfill: %v", got)
	}
	// An empty inventory (e.g. missing metrics) never deletes either.
	e.loader.setEmpty("c1", true)
	e.run(e.now, false)
	if got := listed(); !slices.Equal(got, all) {
		t.Errorf("after empty inventory: %v", got)
	}
	e.loader.setEmpty("c1", false)
	// A regular run deletes workloads missing from the inventory.
	e.run(e.now, false)
	want := []string{"api", "checkout", "frontend", "ledger", "report"}
	if got := listed(); !slices.Equal(got, want) {
		t.Errorf("after regular run: %v, want %v", got, want)
	}
	var recs hubapi.RecommendationList
	e.getJSON("/v1/clusters/c1/recommendations?namespace=web", tokC1, &recs)
	if len(recs.Items) != 1 || recs.Items[0].Workload.Name != "frontend" {
		t.Errorf("agent list still has the deleted workload: %+v", recs.Items)
	}
	var cl hubapi.ClusterList
	e.getJSON("/v1/clusters", tokUI, &cl)
	if cl.Items[0].Counts.Workloads != 4 {
		t.Errorf("c1 counts %+v", cl.Items[0].Counts)
	}
	var sum hubapi.FleetSummary
	e.getJSON("/v1/summary", tokUI, &sum)
	if sum.Fleet.Workloads != 5 {
		t.Errorf("summary workloads %d", sum.Fleet.Workloads)
	}
	if v, ok := e.metrics().find("kerf_workload_phase", map[string]string{"workload": "legacy"}); ok {
		t.Errorf("deleted workload still exported: %v", v)
	}
	// The workload comes back when it reappears.
	e.loader.hide(kLegacy.ID(), false)
	e.run(e.now, false)
	if got := listed(); !slices.Equal(got, all) {
		t.Errorf("after reappearing: %v", got)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	e := newEnv(t)
	ms := e.metrics()
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	wl := map[string]string{"cluster": "c1", "namespace": "payments", "workload_kind": "Deployment", "workload": "checkout"}
	with := func(extra ...string) map[string]string {
		m := map[string]string{}
		for k, v := range wl {
			m[k] = v
		}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i]] = extra[i+1]
		}
		return m
	}
	rec := d.Recommendation.Containers[0]
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"kerf_recommendation_cpu_cores", with("container", "app", "type", "request", "source", "recommended"), mustParseCPU(t, rec.Recommended.Requests.CPU)},
		{"kerf_recommendation_cpu_cores", with("container", "app", "type", "request", "source", "current"), 1},
		{"kerf_recommendation_cpu_cores", with("container", "app", "type", "limit", "source", "current"), 2},
		{"kerf_recommendation_memory_bytes", with("container", "app", "type", "request", "source", "current"), 1 * gi},
		{"kerf_recommendation_confidence", wl, d.Recommendation.Confidence},
		{"kerf_recommendation_average_replicas", wl, 3},
		{"kerf_recommendation_generated_timestamp_seconds", wl, float64(e.now.Truncate(fakeStep).Unix())},
		{"kerf_recommendation_monthly_savings_usd", with("container", "app", "resource", "cpu"), rec.MonthlySavingsUSD.CPU},
		{"kerf_workload_mode", with("resource", "cpu", "mode", "audit", "source", "global-default"), 1},
		{"kerf_workload_phase", with("phase", "Pending"), 1},
		{"kerf_workload_mode", map[string]string{"cluster": "c1", "namespace": "batch", "workload_kind": "CronJob", "workload": "report", "resource": "memory", "mode": "audit", "source": "global-default"}, 1},
		{"kerf_recommendation_risk_flag", map[string]string{"workload": "ledger", "flag": "oom-killed"}, 1},
		{"kerf_recommender_recommendations", map[string]string{"cluster": "c1"}, 4},
		{"kerf_recommender_pricing_fallback", map[string]string{"cluster": "c1"}, 0},
		{"kerf_recommender_runs_total", map[string]string{"status": "succeeded"}, 3},
	} {
		v, ok := ms.find(tc.name, tc.labels)
		if !ok {
			t.Errorf("%s%v missing", tc.name, tc.labels)
			continue
		}
		var got float64
		_, _ = fmt.Sscan(v, &got)
		if !approx(got, tc.want, 1e-6*max(1, tc.want)) {
			t.Errorf("%s%v = %v, want %v", tc.name, tc.labels, got, tc.want)
		}
	}
	if _, ok := ms.find("kerf_recommendation_confidence", map[string]string{"workload": "report"}); ok {
		t.Error("report has no recommendation but exports confidence")
	}
	if _, ok := ms.find("kerf_recommender_last_success_timestamp_seconds", map[string]string{"cluster": "c2"}); !ok {
		t.Error("last success timestamp missing")
	}
	// Status reports are reflected after the next refresh.
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{checkoutStatus()}}, &res)
	if err := e.srv.Metrics.Refresh(e.ctx, e.st); err != nil {
		t.Fatal(err)
	}
	ms = e.metrics()
	if _, ok := ms.find("kerf_workload_mode", with("resource", "cpu", "mode", "auto-patch", "source", "policy")); !ok {
		t.Error("reported mode not exported")
	}
	if _, ok := ms.find("kerf_workload_mode", with("resource", "cpu", "mode", "audit")); ok {
		t.Error("stale default mode still exported")
	}
	// Series names and label sets are unique (the collector must not emit duplicates).
	seen := map[string]bool{}
	for _, m := range ms {
		var parts []string
		for k, v := range m.labels {
			parts = append(parts, k+"="+v)
		}
		sort.Strings(parts)
		id := m.name + "{" + strings.Join(parts, ",") + "}"
		if seen[id] {
			t.Errorf("duplicate series %s", id)
		}
		seen[id] = true
	}
}

// TestRecommendationsPagingCollation runs the agent list paging on a database whose default
// collation is not byte order (as on glibc/RDS en_US.UTF-8, where punctuation is ignored at the
// first level). Known bug: the keyset cursor compares in Go byte order while the rows come back
// in the database collation order.
func TestRecommendationsPagingCollation(t *testing.T) {
	st := newTestStore(t, "TEMPLATE template0 LOCALE_PROVIDER icu ICU_LOCALE 'en-US-u-ka-shifted' LOCALE 'C'")
	ctx := context.Background()
	if err := st.EnsureCluster(ctx, "c1", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureToken(ctx, tokC1, hubapi.RoleAgent, "c1", ""); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	recCfg, _ := cfg.RecommenderConfig()
	now := time.Now().UTC().Add(time.Second)
	spec := func(name string) wlSpec {
		return wlSpec{key: key("c1", "ns", hubapi.KindDeployment, name), replicas: 1, days: 1,
			containers: []ctrSpec{{name: "app", cpu: 0.1, mem: 64 * mi, curCPU: 0.5, curMem: 256 * mi}}}
	}
	loader := &fakeLoader{now: now, workloads: map[string][]wlSpec{"c1": {spec("ab"), spec("a-c"), spec("ad")}}}
	m := NewMetrics()
	job := &Job{Store: st, Metrics: m, Cfg: cfg, Log: discardLog(), Engine: &recommender.Engine{Loader: loader, Cfg: recCfg, Strategy: recommender.PercentileStrategy{}}}
	if err := job.RunOnce(ctx, now, false); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Cfg: cfg, Store: st, RecCfg: recCfg, Metrics: m, Log: discardLog()}
	e := &testEnv{t: t, ctx: ctx, st: st, srv: srv}
	e.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(e.ts.Close)
	var got []string
	next := ""
	for i := 0; i < 5; i++ {
		p := "/v1/clusters/c1/recommendations?limit=1"
		if next != "" {
			p += "&continue=" + url.QueryEscape(next)
		}
		var page hubapi.RecommendationList
		e.getJSON(p, tokC1, &page)
		for _, it := range page.Items {
			got = append(got, it.Workload.Name)
		}
		if next = page.Continue; next == "" {
			break
		}
	}
	sort.Strings(got)
	if !slices.Equal(got, []string{"a-c", "ab", "ad"}) {
		t.Errorf("paging with limit=1 returned %v, want all of [a-c ab ad] once", got)
	}
}

// TestRerunSameAsOf: a second run whose generatedAt equals the previous one (hub restart with
// runOnStart within the same 5 m step) must become the latest recommendation. Known bug: the
// latest recommendation is picked by generated_at alone, so ties return the older row.
func TestRerunSameAsOf(t *testing.T) {
	e := newEnv(t)
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{checkoutStatus()}}, &res)
	e.advance(time.Second) // same 5 m step
	e.run(e.now, false)
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Recommendation.Settings.Profile != hubapi.ProfileDev {
		t.Errorf("latest recommendation has profile %q after a re-run with the reported profile dev", d.Recommendation.Settings.Profile)
	}
	var newest string
	if err := e.st.Pool.QueryRow(e.ctx, `SELECT r.id::text FROM recommendations r JOIN recommender_runs rr ON rr.id = r.run_id
		JOIN workloads w ON w.id = r.workload_id WHERE w.name = 'checkout' ORDER BY rr.started_at DESC LIMIT 1`).Scan(&newest); err != nil {
		t.Fatal(err)
	}
	if d.Recommendation.ID != newest {
		t.Errorf("latest recommendation %s is not the one of the newest run %s", d.Recommendation.ID, newest)
	}
}

// TestDeferredStatus (WO-10): an agent reports phase Deferred with the next maintenance window;
// the hub stores both and returns them on the workload.
func TestDeferredStatus(t *testing.T) {
	e := newEnv(t)
	next := time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC)
	st := checkoutStatus()
	st.Strategy, st.Phase, st.Reason, st.NextWindow = "deferred", "Deferred", "OutsideMaintenanceWindow", &next
	var res hubapi.BatchResult
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{st}}, &res)
	if res.Accepted != 1 {
		t.Fatalf("result %+v", res)
	}
	var d hubapi.WorkloadDetail
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Status == nil || d.Status.Phase != "Deferred" || d.Status.NextWindow == nil || !d.Status.NextWindow.Equal(next) || d.Phase != "Deferred" {
		t.Fatalf("status %+v phase %s", d.Status, d.Phase)
	}
	// Leaving Deferred clears it.
	st.Phase, st.NextWindow = "Applied", nil
	e.postJSON("/v1/clusters/c1/workload-status", tokC1, hubapi.WorkloadStatusBatch{Items: []hubapi.WorkloadStatus{st}}, &res)
	d = hubapi.WorkloadDetail{}
	e.getJSON(wlPath(kCheckout, ""), tokUI, &d)
	if d.Status == nil || d.Status.NextWindow != nil {
		t.Fatalf("nextWindow kept: %v", d.Status.NextWindow)
	}
}
