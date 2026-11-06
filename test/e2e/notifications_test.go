//go:build e2e

package e2e

// Notifications: the hub delivers to a fake receiver the suite runs on the host. The hub (in the k3d
// hub cluster) reaches it through host.k3d.internal (E2E_NOTIFY_HOST overrides), which needs the dev
// hub values `notifications.allowInsecureHTTP: true` (deploy/charts/hub/values-dev.yaml).
//
// The events are posted to the hub with spoke-1's agent token (the Secret of its agent) for a namespace of their own
// (e2e-notify-*), like the agent reports them; the audit log is append-only, so they stay there.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
	"github.com/kerf-dev/kerf/internal/hub/notify"
)

const notifySecret = "e2e-signing-secret-0123456789"

// receiver is a fake webhook endpoint that verifies the signature of what it receives.
type notifyReceiver struct {
	mu     sync.Mutex
	srv    *http.Server
	port   int
	got    []notifyReceived
	secret string
}

type notifyReceived struct {
	Header http.Header
	Body   []byte
	Valid  bool
	Msg    struct {
		Version   int    `json:"version"`
		Timestamp int64  `json:"timestamp"`
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Event     struct {
			Type      string `json:"type"`
			Reason    string `json:"reason"`
			Cluster   string `json:"cluster"`
			Namespace string `json:"namespace"`
			Workload  struct{ Kind, Name string }
			Link      string `json:"link"`
			Message   string `json:"message"`
		} `json:"event"`
	}
}

func startNotifyReceiver(t *testing.T, secret string) *notifyReceiver {
	t.Helper()
	l, err := net.Listen("tcp", "0.0.0.0:0")
	must(t, err)
	r := &notifyReceiver{port: l.Addr().(*net.TCPAddr).Port, secret: secret}
	r.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		x := notifyReceived{Header: req.Header.Clone(), Body: b}
		x.Valid = notify.Verify(secret, b, req.Header.Get(notify.HeaderSignature))
		_ = json.Unmarshal(b, &x.Msg)
		r.mu.Lock()
		r.got = append(r.got, x)
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = r.srv.Serve(l) }()
	t.Cleanup(func() { _ = r.srv.Close() })
	return r
}

// url is the receiver's address as the hub reaches it. The path is the part a real webhook URL keeps
// secret.
func (r *notifyReceiver) url() string {
	host := getenv("E2E_NOTIFY_HOST", "host.k3d.internal")
	return fmt.Sprintf("http://%s:%d/hooks/e2e-%s", host, r.port, randSuffix())
}

func (r *notifyReceiver) events(typ, ns string) []notifyReceived {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []notifyReceived
	for _, x := range r.got {
		if x.Msg.Kind == "event" && x.Msg.Event.Type == typ && x.Msg.Event.Namespace == ns {
			out = append(out, x)
		}
	}
	return out
}

func (r *notifyReceiver) kind(kind string) []notifyReceived {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []notifyReceived
	for _, x := range r.got {
		if x.Msg.Kind == kind {
			out = append(out, x)
		}
	}
	return out
}

func (r *notifyReceiver) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

// postEvent reports an event the way the agent does.
func postEvent(t *testing.T, token, cluster string, e hubapi.Event) {
	t.Helper()
	b, _ := json.Marshal(hubapi.EventBatch{Items: []hubapi.Event{e}})
	req, err := http.NewRequestWithContext(ctx(t), http.MethodPost, env.Hub.Base+"/v1/clusters/"+url.PathEscape(cluster)+"/events", strings.NewReader(string(b)))
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post event: %s: %s", resp.Status, body)
	}
}

func notifyID(t *testing.T) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffff, time.Now().UnixNano()&0xffffffffffff)
}

// TestNotifications: a signed webhook channel with a rule gets the rollback of its namespace within a
// minute, nothing for another namespace, one message for a repeated block; "Send test" works and
// shows the error of a channel that cannot be reached; secrets never come back from the API.
func TestNotifications(t *testing.T) {
	t.Parallel()
	adm := as(t, admin)
	rcv := startNotifyReceiver(t, notifySecret)
	ns := "e2e-notify-" + randSuffix()
	other := "e2e-notify-other-" + randSuffix()
	cl := env.Spoke1

	// The channel, as an admin does it.
	chURL := rcv.url()
	var ch hubapi.NotificationChannel
	adm.call(t, http.MethodPost, "/v1/admin/notifications/channels", hubapi.NotificationChannelInput{
		Name: "e2e-" + randSuffix(), Type: hubapi.NotificationWebhook, URL: chURL, Secret: notifySecret, Reason: reason(t, "channel")}, nil, http.StatusCreated, &ch)
	t.Cleanup(func() { deleteNotifyObjects(t, ch.ID, "") })
	if raw, _ := json.Marshal(ch); strings.Contains(string(raw), "/hooks/e2e-") || strings.Contains(string(raw), notifySecret) || !ch.SecretSet {
		t.Errorf("the channel as returned: %s", raw)
	}

	// Send test: arrives signed, and the result is shown.
	var res hubapi.NotificationTestResult
	adm.call(t, http.MethodPost, "/v1/admin/notifications/channels/"+ch.ID+"/test", nil, nil, http.StatusOK, &res)
	if !res.OK || res.HTTPStatus != http.StatusNoContent {
		t.Fatalf("send test: %+v", res)
	}
	tests := rcv.kind("test")
	if len(tests) != 1 || !tests[0].Valid || tests[0].Msg.Version != 1 || tests[0].Header.Get(notify.HeaderEvent) != "test" {
		t.Fatalf("the test message: %+v", tests)
	}

	// A channel nobody listens on: the error is shown, without the URL.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	deadPort := dead.Addr().(*net.TCPAddr).Port
	dead.Close()
	var bad hubapi.NotificationChannel
	adm.call(t, http.MethodPost, "/v1/admin/notifications/channels", hubapi.NotificationChannelInput{
		Name: "e2e-dead-" + randSuffix(), Type: hubapi.NotificationSlack, URL: fmt.Sprintf("http://127.0.0.1:%d/services/T0/B0/DEADSECRETPATH", deadPort), Reason: reason(t, "dead channel")}, nil, http.StatusCreated, &bad)
	t.Cleanup(func() { deleteNotifyObjects(t, bad.ID, "") })
	adm.call(t, http.MethodPost, "/v1/admin/notifications/channels/"+bad.ID+"/test", nil, nil, http.StatusOK, &res)
	if res.OK || res.Error == "" || strings.Contains(res.Error, "DEADSECRETPATH") {
		t.Errorf("test of an unreachable channel: %+v", res)
	}
	adm.get(t, "/v1/admin/notifications/channels/"+bad.ID, &bad)
	if bad.LastDelivery == nil || bad.LastDelivery.Status != hubapi.LastDeliveryFailed || bad.LastDelivery.Error == "" {
		t.Errorf("the channel does not show why its last delivery failed: %+v", bad.LastDelivery)
	}

	// The rule: rollbacks and blocks of one namespace of spoke-1 to the channel.
	var rule hubapi.NotificationRule
	adm.call(t, http.MethodPost, "/v1/admin/notifications/rules", hubapi.NotificationRuleInput{
		Name: "e2e-" + randSuffix(), EventTypes: []string{hubapi.NotifyRolledBack, hubapi.NotifyBlocked}, Clusters: []string{cl.Name}, Namespaces: []string{ns},
		ChannelIDs: []string{ch.ID}, Reason: reason(t, "rule")}, nil, http.StatusCreated, &rule)
	t.Cleanup(func() { deleteNotifyObjects(t, "", rule.ID) })
	sleep(t, 6*time.Second, "the leader's next read of the audit log starts after the rule exists")

	token := agentToken(t, cl)
	if strings.HasPrefix(token, "error: ") || token == "" {
		t.Fatalf("read the agent token of %s: %s", cl.Name, token)
	}
	now := time.Now().UTC()
	wl := func(n string) hubapi.WorkloadRef {
		return hubapi.WorkloadRef{Namespace: n, Kind: "Deployment", Name: "checkout"}
	}
	postEvent(t, token, cl.Name, hubapi.Event{ID: notifyID(t), Type: "rolled_back", Time: now, Reason: "OOMKills", Workload: wl(ns), Strategy: "in-place", Resources: []string{"memory"},
		Message: "2 OOM kills within 30m after change; Authorization: Bearer abcdefghijkl123456"})
	postEvent(t, token, cl.Name, hubapi.Event{ID: notifyID(t), Type: "rolled_back", Time: now, Reason: "OOMKills", Workload: wl(other), Strategy: "in-place", Resources: []string{"memory"}})
	for range 3 { // the same block, reported again and again
		postEvent(t, token, cl.Name, hubapi.Event{ID: notifyID(t), Type: "blocked", Time: now, Reason: "FreezeWindow", Workload: wl(ns)})
	}

	// Within a minute: the rollback of the rule's namespace, signed, with a link to the workload page.
	eventually(t, time.Minute, time.Second, "the rollback reached the receiver", func() (bool, string) {
		return len(rcv.events("rolled_back", ns)) >= 1 && len(rcv.events("blocked", ns)) >= 1, fmt.Sprintf("%d messages", rcv.total())
	})
	rb := rcv.events("rolled_back", ns)[0]
	if !rb.Valid || rb.Msg.Version != 1 || rb.Msg.Event.Cluster != cl.Name || rb.Msg.Event.Reason != "OOMKills" || rb.Msg.Event.Workload.Name != "checkout" {
		t.Errorf("rollback message: %+v", rb.Msg)
	}
	if !strings.HasSuffix(rb.Msg.Event.Link, "/workloads/"+cl.Name+"/"+ns+"/Deployment/checkout") {
		t.Errorf("link %q does not point to the workload page", rb.Msg.Event.Link)
	}
	if strings.Contains(string(rb.Body), "abcdefghijkl123456") || strings.Contains(string(rb.Body), "Bearer") && !strings.Contains(string(rb.Body), "[redacted]") {
		t.Errorf("the message carries a credential from the event text: %s", rb.Body)
	}
	if got := rb.Header.Get(notify.HeaderDelivery); got != rb.Msg.ID || rb.Header.Get(notify.HeaderTimestamp) == "" {
		t.Errorf("headers %v", rb.Header)
	}

	// Nothing for the other namespace, one message for the repeated block, nothing twice.
	consistently(t, 20*time.Second, 2*time.Second, "no other messages", func() (bool, string) {
		if n := len(rcv.events("rolled_back", other)); n != 0 {
			return false, fmt.Sprintf("%d rollbacks of the namespace outside the rule", n)
		}
		if n := len(rcv.events("blocked", ns)); n != 1 {
			return false, fmt.Sprintf("%d blocked messages for 3 identical blocks, want 1", n)
		}
		if n := len(rcv.events("rolled_back", ns)); n != 1 {
			return false, fmt.Sprintf("%d rollback messages, want 1", n)
		}
		return true, ""
	})

	// The channel shows the delivery, the history lists it, the metric counts it.
	adm.get(t, "/v1/admin/notifications/channels/"+ch.ID, &ch)
	if ch.LastDelivery == nil || ch.LastDelivery.Status != hubapi.LastDeliverySent || ch.LastSuccessAt == nil {
		t.Errorf("channel status %+v", ch.LastDelivery)
	}
	var dl hubapi.NotificationDeliveryList
	adm.get(t, "/v1/admin/notifications/deliveries?channelId="+ch.ID+"&limit=50", &dl)
	sent := 0
	for _, d := range dl.Items {
		if d.Status == hubapi.DeliverySent && d.Kind == hubapi.DeliveryKindEvent {
			sent++
		}
		if raw, _ := json.Marshal(d); strings.Contains(string(raw), "/hooks/e2e-") {
			t.Errorf("a delivery shows the URL: %s", raw)
		}
	}
	if sent != 2 {
		t.Errorf("%d sent event deliveries in the history, want 2: %+v", sent, dl.Items)
	}
	resp, err := http.Get(env.Hub.Base + "/metrics")
	must(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `kerf_notification_deliveries_total{channel_type="webhook",result="success"}`) {
		t.Error("the hub does not export kerf_notification_deliveries_total")
	}
}

// deleteNotifyObjects removes a rule and/or a channel at the end of a test (rules first: a channel in
// use cannot be deleted); problems are only logged.
func deleteNotifyObjects(t *testing.T, channelID, ruleID string) {
	s := sessionOf(admin)
	if s == nil || env.Keep {
		return
	}
	c, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	del := func(path string) {
		var obj struct {
			Version int `json:"version"`
		}
		r, err := s.raw(c, http.MethodGet, path, nil, nil)
		if err != nil || r.Status != http.StatusOK {
			return
		}
		_ = json.Unmarshal(r.Body, &obj)
		if r, err := s.raw(c, http.MethodDelete, path, hubapi.ChangeReason{Reason: "e2e cleanup"}, ifMatch(obj.Version)); err != nil || r.Status/100 != 2 {
			t.Logf("cleanup: delete %s: %v %s", path, err, r)
		}
	}
	if ruleID != "" {
		// There is no GET of one rule: take its version from the list.
		r, err := s.raw(c, http.MethodGet, "/v1/admin/notifications/rules", nil, nil)
		if err == nil && r.Status == http.StatusOK {
			var l hubapi.NotificationRuleList
			_ = json.Unmarshal(r.Body, &l)
			for _, x := range l.Items {
				if x.ID == ruleID {
					if r, err := s.raw(c, http.MethodDelete, "/v1/admin/notifications/rules/"+ruleID, hubapi.ChangeReason{Reason: "e2e cleanup"}, ifMatch(x.Version)); err != nil || r.Status/100 != 2 {
						t.Logf("cleanup: delete rule %s: %v %s", ruleID, err, r)
					}
				}
			}
		}
	}
	if channelID != "" {
		del("/v1/admin/notifications/channels/" + channelID)
	}
}
