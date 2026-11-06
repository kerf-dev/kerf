// Package notify sends the hub's activity events and a weekly digest to Slack, Microsoft Teams and
// signed webhooks (docs/notifications.md).
//
// The leader (internal/hub/leader.go) reads new rows of the audit log, matches them against the
// rules, queues one delivery per event and channel and sends the queue with retries. A delivery
// holds a neutral Message; it is rendered for the channel type on every attempt, so a retry is
// signed with a fresh timestamp and the stored row never contains a URL or a secret.
package notify

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// PayloadVersion is the version of the webhook payload (docs/contracts/notifications.md).
const PayloadVersion = 1

// Message kinds.
const (
	KindEvent  = hubapi.DeliveryKindEvent
	KindDigest = hubapi.DeliveryKindDigest
	KindTest   = hubapi.DeliveryKindTest
)

// Message is what a delivery carries, and the body of a webhook (inside the envelope of Payload).
// It never contains credentials: free text goes through Redact and only well-known details are kept.
type Message struct {
	// ID is stable across retries: the audit event id for an event, a fresh id for a digest or test.
	ID   string    `json:"id"`
	Kind string    `json:"kind"`
	Time time.Time `json:"time"`

	Event  *EventMessage  `json:"event,omitempty"`
	Digest *DigestMessage `json:"digest,omitempty"`
	Test   *TestMessage   `json:"test,omitempty"`
}

// EventMessage describes one event.
type EventMessage struct {
	// Type is a hubapi.NotificationEventType.
	Type string `json:"type"`
	// Reason is the CamelCase reason of the audit event (OOMKills, FreezeWindow, ...).
	Reason string `json:"reason,omitempty"`
	// Cluster is empty for fleet-wide automation events.
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// Workload is set for workload events.
	Workload  *WorkloadRef `json:"workload,omitempty"`
	Strategy  string       `json:"strategy,omitempty"`
	Resources []string     `json:"resources,omitempty"`
	Changes   []Change     `json:"changes,omitempty"`
	// Message is the event's free text, redacted and shortened.
	Message string `json:"message,omitempty"`
	// Details holds a fixed set of well-known detail keys of the event.
	Details map[string]string `json:"details,omitempty"`
	// Actor is who paused or resumed automation (a user name, or "system").
	Actor string `json:"actor,omitempty"`
	// FromState and ToState are the readiness states of readiness_degraded.
	FromState string `json:"fromState,omitempty"`
	ToState   string `json:"toState,omitempty"`
	// Scope is "cluster" or "fleet" for automation events.
	Scope string `json:"scope,omitempty"`
	// Link points to the workload (or cluster) page of the web UI; empty without notifications.baseURL.
	Link string `json:"link,omitempty"`
}

// WorkloadRef identifies a workload inside the event's cluster and namespace.
type WorkloadRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Change is one changed resource value of a container.
type Change struct {
	Container string `json:"container"`
	// Resource is "cpu" or "memory"; Field is "requests" or "limits".
	Resource string `json:"resource"`
	Field    string `json:"field"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
}

// TestMessage marks a test message.
type TestMessage struct {
	Channel string `json:"channel"`
}

// DigestMessage is the weekly digest.
type DigestMessage struct {
	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`
	// EstimatedSavings is the fleet's estimated monthly savings now and a week ago.
	EstimatedSavings EstimatedSavings `json:"estimatedSavings"`
	// RealizedSavings is nil while the hub has no realized-savings source (RealizedSavingsSource).
	RealizedSavings  *RealizedSavings `json:"realizedSavings,omitempty"`
	TopOpportunities []Opportunity    `json:"topOpportunities"`
	Changes          ChangeCounts     `json:"changes"`
	// Attention lists clusters that need a look; empty when all is well.
	Attention []Attention `json:"attention"`
	Link      string      `json:"link,omitempty"`
}

// EstimatedSavings are USD per month.
type EstimatedSavings struct {
	CurrentUSD  float64  `json:"currentUSD"`
	PreviousUSD *float64 `json:"previousUSD,omitempty"`
	ChangeUSD   *float64 `json:"changeUSD,omitempty"`
}

// RealizedSavings are the savings that applied changes actually delivered in the digest period.
type RealizedSavings struct {
	PeriodUSD         float64  `json:"periodUSD"`
	PreviousPeriodUSD *float64 `json:"previousPeriodUSD,omitempty"`
	// Since is when the first applied change started to save (the "Realized since <date>" of the UI).
	Since *time.Time `json:"since,omitempty"`
}

// Opportunity is a workload with a large estimated saving.
type Opportunity struct {
	Cluster           string  `json:"cluster"`
	Namespace         string  `json:"namespace"`
	Kind              string  `json:"kind"`
	Name              string  `json:"name"`
	MonthlySavingsUSD float64 `json:"monthlySavingsUSD"`
	Link              string  `json:"link,omitempty"`
}

// ChangeCounts counts the changes of the digest period.
type ChangeCounts struct {
	Applied    int `json:"applied"`
	RolledBack int `json:"rolledBack"`
	Failed     int `json:"failed"`
}

// Attention is a cluster that needs a look.
type Attention struct {
	Cluster string   `json:"cluster"`
	Reasons []string `json:"reasons"`
	Link    string   `json:"link,omitempty"`
}

// Links builds web UI links from the configured base URL ("" = no links).
type Links struct{ Base string }

func (l Links) join(parts ...string) string {
	if l.Base == "" {
		return ""
	}
	out := strings.TrimRight(l.Base, "/")
	for _, p := range parts {
		out += "/" + url.PathEscape(p)
	}
	return out
}

// Workload links to the workload page.
func (l Links) Workload(cluster, namespace, kind, name string) string {
	return l.join("workloads", cluster, namespace, kind, name)
}

// Cluster links to the cluster page.
func (l Links) Cluster(cluster string) string { return l.join("clusters", cluster) }

// Overview links to the overview page.
func (l Links) Overview() string {
	if l.Base == "" {
		return ""
	}
	return strings.TrimRight(l.Base, "/") + "/"
}

// Opportunities links to the opportunities page.
func (l Links) Opportunities() string { return l.join("opportunities") }

// Title is the one-line summary of an event message ("Rolled back payments/checkout (OOMKills)").
func (e EventMessage) Title() string {
	where := e.Cluster
	if e.Workload != nil {
		where = fmt.Sprintf("%s/%s", e.Namespace, e.Workload.Name)
		if e.Cluster != "" {
			where += " on " + e.Cluster
		}
	}
	reason := ""
	if e.Reason != "" {
		reason = " (" + e.Reason + ")"
	}
	switch e.Type {
	case hubapi.NotifyApplied:
		return "Applied new resources to " + where
	case hubapi.NotifyRolledBack:
		return "Rolled back " + where + reason
	case hubapi.NotifyFailed:
		return "Failed to change " + where + reason
	case hubapi.NotifyBlocked:
		return "Blocked " + where + reason
	case hubapi.NotifyReadinessDegraded:
		return fmt.Sprintf("Cluster %s is less ready: %s → %s", e.Cluster, e.FromState, e.ToState)
	case hubapi.NotifyAutomationPaused:
		return "Automation paused " + e.scopeText()
	case hubapi.NotifyAutomationResumed:
		return "Automation resumed " + e.scopeText()
	}
	return e.Type + " " + where
}

func (e EventMessage) scopeText() string {
	if e.Scope == "fleet" || e.Cluster == "" {
		return "for the whole fleet"
	}
	return "for cluster " + e.Cluster
}

// Title is the one-line summary of any message.
func (m Message) Title() string {
	switch {
	case m.Event != nil:
		return m.Event.Title()
	case m.Digest != nil:
		return "Kerf weekly digest"
	case m.Test != nil:
		return "Kerf test notification"
	}
	return m.Kind
}

// detailKeys are the event details that are passed on; everything else is dropped (details are
// free-form strings from the agent and may carry things that don't belong in a chat).
var detailKeys = []string{"oomKills", "restarts", "throttlingRatio", "watchDuration", "resizeStatus", "previousMode", "newMode", "pauseScope", "approvedBy"}

// FilterDetails keeps only the well-known detail keys, redacting their values.
func FilterDetails(d map[string]string) map[string]string {
	var out map[string]string
	for _, k := range detailKeys {
		if v := d[k]; v != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = Redact(v, 120)
		}
	}
	return out
}

var (
	// URLs of the destinations Kerf sends to carry the secret in their path.
	secretURLRE = regexp.MustCompile(`(?i)https?://[^\s"'<>]*(hooks\.slack\.com|webhook\.office\.com|\.logic\.azure\.com|\.webhook\.office365\.com|\.powerplatform\.com|\.environment\.api\.[a-z.]*)[^\s"'<>]*`)
	userinfoRE  = regexp.MustCompile(`(?i)(://)[^\s/@:]+:[^\s/@]*@`)
	bearerRE    = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{6,}`)
	paramRE     = regexp.MustCompile(`(?i)\b(token|access_token|password|passwd|secret|api[_-]?key|apikey|sig|signature|authorization|auth)(["']?\s*[:=]\s*["']?)[^\s"'&,;]+`)
	opaqueRE    = regexp.MustCompile(`\b[A-Za-z0-9_\-+/=]{32,}\b`)
	prefixedRE  = regexp.MustCompile(`\b(xox[abposr]-[A-Za-z0-9-]+|gh[pousr]_[A-Za-z0-9]+|AKIA[0-9A-Z]{16}|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]*)`)
)

// Redact removes credentials from free text and shortens it to at most max runes: webhook URLs,
// URL credentials, bearer and basic tokens, token/password/secret parameters, JWTs, cloud keys and
// long opaque strings. It is deliberately greedy: a false positive costs a word, a miss a leak.
func Redact(s string, max int) string {
	s = secretURLRE.ReplaceAllString(s, "[redacted-url]")
	s = userinfoRE.ReplaceAllString(s, "${1}[redacted]@")
	s = bearerRE.ReplaceAllString(s, "$1 [redacted]")
	s = paramRE.ReplaceAllString(s, "${1}${2}[redacted]")
	s = prefixedRE.ReplaceAllString(s, "[redacted]")
	s = opaqueRE.ReplaceAllStringFunc(s, func(w string) string {
		// Long words that are plain letters (a long name) are kept; mixed letters and digits are not.
		hasDigit, hasLetter := false, false
		for _, r := range w {
			switch {
			case r >= '0' && r <= '9':
				hasDigit = true
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
				hasLetter = true
			}
		}
		if hasDigit && hasLetter {
			return "[redacted]"
		}
		return w
	})
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max-1]) + "…"
	}
	return s
}

// ConvertChanges lists the changed values of containers, requests then limits, cpu then memory.
func ConvertChanges(cs []hubapi.ContainerChange) []Change {
	var out []Change
	pick := func(rv *hubapi.ResourceValues, res string) string {
		if rv == nil {
			return ""
		}
		if res == "cpu" {
			return rv.CPU
		}
		return rv.Memory
	}
	for _, c := range cs {
		for _, field := range []string{"requests", "limits"} {
			for _, res := range []string{"cpu", "memory"} {
				var from, to string
				if c.From != nil {
					rv := c.From.Requests
					if field == "limits" {
						rv = c.From.Limits
					}
					from = pick(rv, res)
				}
				if c.To != nil {
					rv := c.To.Requests
					if field == "limits" {
						rv = c.To.Limits
					}
					to = pick(rv, res)
				}
				if from != to {
					out = append(out, Change{Container: Redact(c.Name, 63), Resource: res, Field: field, From: from, To: to})
				}
			}
		}
	}
	return out
}
