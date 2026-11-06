package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Webhook headers (docs/contracts/notifications.md).
const (
	HeaderSignature = "X-Kerf-Signature"
	HeaderTimestamp = "X-Kerf-Timestamp"
	HeaderDelivery  = "X-Kerf-Delivery"
	HeaderEvent     = "X-Kerf-Event"
)

// Request is a rendered message: the body and the headers to send with it.
type Request struct {
	Body    []byte
	Headers map[string]string
}

// Render renders m for a channel of type typ. secret is the webhook signing secret ("" = unsigned);
// now is the send time that is signed.
func Render(typ string, m Message, secret string, now time.Time) (Request, error) {
	var body any
	h := map[string]string{"Content-Type": "application/json"}
	switch typ {
	case hubapi.NotificationSlack:
		body = slackBody(m)
	case hubapi.NotificationTeams:
		body = teamsBody(m)
	case hubapi.NotificationWebhook:
		body = envelope{Version: PayloadVersion, Timestamp: now.Unix(), Message: m}
	default:
		return Request{}, fmt.Errorf("unknown channel type %q", typ)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return Request{}, err
	}
	if typ == hubapi.NotificationWebhook {
		h[HeaderTimestamp] = strconv.FormatInt(now.Unix(), 10)
		h[HeaderDelivery] = m.ID
		h[HeaderEvent] = m.eventName()
		if secret != "" {
			h[HeaderSignature] = Sign(secret, b)
		}
	}
	return Request{Body: b, Headers: h}, nil
}

// envelope is the webhook body: the payload version and the send time (also sent as
// X-Kerf-Timestamp, so the signature over the body covers it), then the message.
type envelope struct {
	Version   int   `json:"version"`
	Timestamp int64 `json:"timestamp"`
	Message
}

func (m Message) eventName() string {
	if m.Event != nil {
		return m.Event.Type
	}
	return m.Kind
}

// Sign returns the X-Kerf-Signature value of body: "sha256=" and the hex HMAC-SHA256 under secret.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether signature is the signature of body under secret (constant time).
func Verify(secret string, body []byte, signature string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(signature))
}

// ---- formatting helpers ----

func usd(v float64) string {
	neg := v < 0
	n := int64(math.Round(math.Abs(v)))
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-$" + s
	}
	return "$" + s
}

func signedUSD(v float64) string {
	if v >= 0.5 {
		return "+" + usd(v)
	}
	return usd(v)
}

func (c Change) text() string {
	switch {
	case c.From == "":
		return fmt.Sprintf("%s: %s %s set to %s", c.Container, c.Resource, c.Field, c.To)
	case c.To == "":
		return fmt.Sprintf("%s: %s %s removed (was %s)", c.Container, c.Resource, c.Field, c.From)
	}
	return fmt.Sprintf("%s: %s %s %s → %s", c.Container, c.Resource, c.Field, c.From, c.To)
}

type fact struct{ Name, Value string }

// facts are the label/value rows of an event message.
func (e EventMessage) facts() []fact {
	var f []fact
	add := func(n, v string) {
		if v != "" {
			f = append(f, fact{n, v})
		}
	}
	add("Cluster", e.Cluster)
	add("Namespace", e.Namespace)
	if e.Workload != nil {
		add("Workload", e.Workload.Kind+" "+e.Workload.Name)
	}
	add("Reason", e.Reason)
	add("Strategy", e.Strategy)
	if len(e.Resources) > 0 {
		add("Resources", strings.Join(e.Resources, ", "))
	}
	add("By", e.Actor)
	return f
}

func (d DigestMessage) savingsLine() string {
	s := fmt.Sprintf("Estimated savings: %s per month", usd(d.EstimatedSavings.CurrentUSD))
	if c := d.EstimatedSavings.ChangeUSD; c != nil {
		s += fmt.Sprintf(" (%s vs last week)", signedUSD(*c))
	}
	return s
}

func (d DigestMessage) realizedLine() string {
	if d.RealizedSavings == nil {
		return ""
	}
	s := fmt.Sprintf("Realized savings this week: %s", usd(d.RealizedSavings.PeriodUSD))
	if p := d.RealizedSavings.PreviousPeriodUSD; p != nil {
		s += fmt.Sprintf(" (%s vs the week before)", signedUSD(d.RealizedSavings.PeriodUSD-*p))
	}
	return s
}

func (d DigestMessage) changesLine() string {
	return fmt.Sprintf("Changes: %d applied, %d rolled back, %d failed", d.Changes.Applied, d.Changes.RolledBack, d.Changes.Failed)
}

func (o Opportunity) text() string {
	return fmt.Sprintf("%s/%s on %s: %s per month", o.Namespace, o.Name, o.Cluster, usd(o.MonthlySavingsUSD))
}

func (a Attention) text() string {
	return fmt.Sprintf("%s: %s", a.Cluster, strings.Join(a.Reasons, "; "))
}

func period(d DigestMessage) string {
	return d.PeriodStart.UTC().Format("2 Jan") + " – " + d.PeriodEnd.UTC().Format("2 Jan 2006")
}

// ---- Slack (incoming webhook, Block Kit) ----

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func slackLink(url, text string) string {
	if url == "" {
		return slackEscape(text)
	}
	return "<" + strings.NewReplacer("|", "%7C", ">", "%3E", "<", "%3C").Replace(url) + "|" + slackEscape(text) + ">"
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func slackBody(m Message) map[string]any {
	title := m.Title()
	blocks := []any{map[string]any{"type": "header", "text": map[string]any{"type": "plain_text", "text": clip(title, 150)}}}
	section := func(text string) {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": clip(text, 2900)}})
	}
	link := ""
	switch {
	case m.Event != nil:
		e := m.Event
		var fields []any
		for _, f := range e.facts() {
			fields = append(fields, map[string]any{"type": "mrkdwn", "text": "*" + f.Name + "*\n" + slackEscape(f.Value)})
		}
		if len(fields) > 10 {
			fields = fields[:10]
		}
		if len(fields) > 0 {
			blocks = append(blocks, map[string]any{"type": "section", "fields": fields})
		}
		if len(e.Changes) > 0 {
			var lines []string
			for _, c := range e.Changes {
				lines = append(lines, "• "+slackEscape(c.text()))
			}
			section(strings.Join(lines, "\n"))
		}
		if e.Message != "" {
			section("> " + slackEscape(e.Message))
		}
		if e.FromState != "" {
			section(fmt.Sprintf("Readiness: *%s* → *%s*", slackEscape(e.FromState), slackEscape(e.ToState)))
		}
		link = e.Link
	case m.Digest != nil:
		d := m.Digest
		section("*" + slackEscape(d.savingsLine()) + "*")
		if l := d.realizedLine(); l != "" {
			section(slackEscape(l))
		}
		section(slackEscape(d.changesLine()))
		if len(d.TopOpportunities) > 0 {
			lines := []string{"*Top opportunities*"}
			for _, o := range d.TopOpportunities {
				lines = append(lines, "• "+slackLink(o.Link, o.text()))
			}
			section(strings.Join(lines, "\n"))
		}
		if len(d.Attention) > 0 {
			lines := []string{"*Clusters that need attention*"}
			for _, a := range d.Attention {
				lines = append(lines, "• "+slackLink(a.Link, a.text()))
			}
			section(strings.Join(lines, "\n"))
		} else {
			section("All clusters look fine.")
		}
		link = d.Link
	case m.Test != nil:
		section("This is a test message from Kerf for the channel *" + slackEscape(m.Test.Channel) + "*. If you can read it, notifications reach this channel.")
	}
	ctx := "Kerf · " + m.Time.UTC().Format("2 Jan 2006 15:04 UTC")
	if m.Digest != nil {
		ctx = "Kerf · " + period(*m.Digest)
	}
	blocks = append(blocks, map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "text": ctx}}})
	if link != "" {
		blocks = append(blocks, map[string]any{"type": "actions", "elements": []any{map[string]any{
			"type": "button", "text": map[string]any{"type": "plain_text", "text": "Open in Kerf"}, "url": link}}})
	}
	return map[string]any{"text": title, "blocks": blocks}
}

// ---- Microsoft Teams (Workflows webhook, Adaptive Card) ----

func teamsBody(m Message) map[string]any {
	text := func(t string, extra map[string]any) map[string]any {
		b := map[string]any{"type": "TextBlock", "text": t, "wrap": true}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	body := []any{text(m.Title(), map[string]any{"weight": "Bolder", "size": "Medium"})}
	link := ""
	switch {
	case m.Event != nil:
		e := m.Event
		var facts []any
		for _, f := range e.facts() {
			facts = append(facts, map[string]any{"title": f.Name, "value": f.Value})
		}
		if len(facts) > 0 {
			body = append(body, map[string]any{"type": "FactSet", "facts": facts})
		}
		for _, c := range e.Changes {
			body = append(body, text("• "+c.text(), map[string]any{"spacing": "None"}))
		}
		if e.Message != "" {
			body = append(body, text(e.Message, map[string]any{"isSubtle": true}))
		}
		link = e.Link
	case m.Digest != nil:
		d := m.Digest
		body = append(body, text(period(*d), map[string]any{"isSubtle": true, "spacing": "None"}), text(d.savingsLine(), map[string]any{"weight": "Bolder"}))
		if l := d.realizedLine(); l != "" {
			body = append(body, text(l, nil))
		}
		body = append(body, text(d.changesLine(), nil))
		if len(d.TopOpportunities) > 0 {
			body = append(body, text("Top opportunities", map[string]any{"weight": "Bolder"}))
			for _, o := range d.TopOpportunities {
				body = append(body, text("• "+o.text(), map[string]any{"spacing": "None"}))
			}
		}
		if len(d.Attention) > 0 {
			body = append(body, text("Clusters that need attention", map[string]any{"weight": "Bolder"}))
			for _, a := range d.Attention {
				body = append(body, text("• "+a.text(), map[string]any{"spacing": "None"}))
			}
		} else {
			body = append(body, text("All clusters look fine.", nil))
		}
		link = d.Link
	case m.Test != nil:
		body = append(body, text("This is a test message from Kerf for the channel "+m.Test.Channel+". If you can read it, notifications reach this channel.", nil))
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    body,
	}
	if link != "" {
		card["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": "Open in Kerf", "url": link}}
	}
	return map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"contentUrl":  nil,
			"content":     card,
		}},
	}
}
