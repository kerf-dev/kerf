# Notification webhook contract

Changing a field, header or the signature scheme here is a contract change (see
[CONTRIBUTING.md](../../CONTRIBUTING.md#before-you-start)). How to set notifications up is in
[notifications.md](../notifications.md).

A **webhook channel** receives an HTTPS `POST` with a JSON body for every event its rules match and for the weekly
digest. Slack and Microsoft Teams channels receive a chat message instead (a Block Kit message, an Adaptive Card);
only the webhook channel's body is a contract.

## Versioning

Every body has `"version": 1`. Within a version Kerf only adds fields, so a receiver must ignore fields it does not
know. A breaking change gets `version: 2`, and the hub then sends version 1 to existing channels until they are
changed (there is no change planned).

## Request

```
POST <your URL>
Content-Type: application/json
User-Agent: kerf-hub/<hub version>
X-Kerf-Event: rolled_back
X-Kerf-Delivery: 6f1c2a8e-0d7b-4c55-a0b1-8f9e2f0d1c3a
X-Kerf-Timestamp: 1793955438
X-Kerf-Signature: sha256=9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
```

| Header | Meaning |
|---|---|
| `X-Kerf-Event` | The event type (`applied`, `rolled_back`, `failed`, `blocked`, `readiness_degraded`, `automation_paused`, `automation_resumed`), or `digest`, or `test`. |
| `X-Kerf-Delivery` | The message id (`id` in the body). **The same on every retry** of a message: use it to drop duplicates. For an event it is the id of the event in the audit log (`GET /v1/audit`). |
| `X-Kerf-Timestamp` | When this attempt was sent, in Unix seconds. It is also in the body as `timestamp`, so the signature covers it. A retry has a new timestamp. |
| `X-Kerf-Signature` | `sha256=` and the lower-case hex HMAC-SHA256 of the **raw request body** under the channel's signing secret. Only present when the channel has a secret. |

Answer with any `2xx` status within 15 seconds. Anything else is a failed attempt: Kerf retries `5xx`, `429` (it
honors `Retry-After`) and network errors with exponential backoff (30 s, 1 min, 2 min, … at most 15 min apart) for
one hour, then marks the delivery failed (Admin → Notifications shows it; the metric
`kerf_notification_deliveries_total{result="failed"}` counts it). Other `4xx` answers are not retried: a revoked
URL does not come back. Redirects are not followed. Messages to one channel are rate limited (default 20 per minute);
the surplus waits.

## Body

```json
{
  "version": 1,
  "timestamp": 1793955438,
  "id": "6f1c2a8e-0d7b-4c55-a0b1-8f9e2f0d1c3a",
  "kind": "event",
  "time": "2026-11-04T10:15:00Z",
  "event": {
    "type": "rolled_back",
    "reason": "OOMKills",
    "cluster": "prod-eu",
    "namespace": "payments",
    "workload": {"kind": "Deployment", "name": "checkout"},
    "strategy": "in-place",
    "resources": ["memory"],
    "changes": [
      {"container": "app", "resource": "memory", "field": "requests", "from": "384Mi", "to": "1Gi"}
    ],
    "message": "2 OOM kills within 30m after change (threshold 1)",
    "details": {"oomKills": "2", "watchDuration": "30m"},
    "link": "https://kerf.example.com/workloads/prod-eu/payments/Deployment/checkout"
  }
}
```

| Field | Notes |
|---|---|
| `version` | `1`. |
| `timestamp` | Unix seconds of this attempt; equals `X-Kerf-Timestamp`. |
| `id` | The message id; equals `X-Kerf-Delivery`. |
| `kind` | `event`, `digest` or `test`. Exactly one of `event`, `digest`, `test` is present, matching `kind`. |
| `time` | When it happened (events), the end of the digest's week, or when the test was made. RFC 3339, UTC. |

### `event`

| Field | Notes |
|---|---|
| `type` | `applied`, `rolled_back`, `failed`, `blocked` (the audit event types of [events.md](events.md)), `readiness_degraded` (a cluster's [readiness](readiness.md) state went down), `automation_paused`, `automation_resumed`. |
| `reason` | The CamelCase reason of the audit event (`OOMKills`, `FreezeWindow`, …); absent for `readiness_degraded` and the automation types. |
| `cluster` | The cluster; absent for fleet-wide automation events. |
| `namespace`, `workload` | `workload` is `{kind, name}`. Absent for cluster and fleet events. |
| `strategy`, `resources`, `changes` | For workload events: the strategy, `cpu` and/or `memory`, and each changed value (`field` is `requests` or `limits`; `from`/`to` are Kubernetes quantities, one of them is absent when the value was added or removed). |
| `message` | The event's free text, with credentials removed and shortened to 500 characters. For automation events the reason the operator gave. |
| `details` | Only the keys `oomKills`, `restarts`, `throttlingRatio`, `watchDuration`, `resizeStatus`, `previousMode`, `newMode`, `pauseScope`, `approvedBy`. |
| `actor` | Who paused or resumed automation (a user name, or `system` for an automatic resume). |
| `scope` | `cluster` or `fleet` for the automation types. |
| `fromState`, `toState` | The readiness states of `readiness_degraded`. |
| `link` | The workload page (or the cluster page, or the overview) in the web UI. Absent unless the hub has `notifications.baseURL`. |

### `digest`

```json
{
  "version": 1, "timestamp": 1793955438, "id": "…", "kind": "digest", "time": "2026-11-09T08:00:00Z",
  "digest": {
    "periodStart": "2026-11-02T08:00:00Z",
    "periodEnd": "2026-11-09T08:00:00Z",
    "estimatedSavings": {"currentUSD": 1234, "previousUSD": 1000, "changeUSD": 234},
    "realizedSavings": {"periodUSD": 310, "previousPeriodUSD": 280, "since": "2026-10-03T14:00:00Z"},
    "topOpportunities": [
      {"cluster": "prod-eu", "namespace": "payments", "kind": "Deployment", "name": "checkout", "monthlySavingsUSD": 410, "link": "https://…"}
    ],
    "changes": {"applied": 12, "rolledBack": 1, "failed": 0},
    "attention": [{"cluster": "dev-1", "reasons": ["automation is paused"], "link": "https://…"}],
    "teams": [
      {"team": "payments", "displayName": "Payments", "workloads": 14, "monthlySavingsUSD": 620, "link": "https://…"},
      {"team": "unassigned", "displayName": "Unassigned", "workloads": 3, "monthlySavingsUSD": 40}
    ],
    "link": "https://kerf.example.com/"
  }
}
```

`estimatedSavings` are USD per month for the whole fleet now and a week ago (`previousUSD` and `changeUSD` are
absent without history). `realizedSavings` is what the applied changes actually saved during the week (see
[realized-savings.md](../realized-savings.md)); `since` is when the first of them started to save, and the chat
messages label the figure "Realized since \<date\>". It is a different quantity than `estimatedSavings` and is never
added to it. `previousPeriodUSD` is absent when the savings began inside the week. The whole field is absent until
some change has released requests.
`topOpportunities` has at most five workloads. `attention` lists the clusters whose agent is silent, whose readiness
is below *Automation ready*, or whose automation is paused; it is empty when all is well.
`teams` is the estimated monthly savings per [team](../teams.md), largest first, with `unassigned` for the workloads in
no team; the field is absent while no team is defined. Each `link` opens Opportunities filtered to the team.

### `test`

`{"test": {"channel": "<channel name>"}}`, sent by **Send test**.

## Verifying the signature

Compute the HMAC-SHA256 of the body **exactly as received** (do not parse and re-encode it), compare in constant
time, and reject a request whose `timestamp` is more than five minutes old.

Go:

```go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

var secret = []byte(os.Getenv("KERF_WEBHOOK_SECRET"))

func handler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Kerf-Signature"))) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var m struct {
		Version   int   `json:"version"`
		Timestamp int64 `json:"timestamp"`
	}
	if json.Unmarshal(body, &m) != nil || m.Timestamp != mustInt(r.Header.Get("X-Kerf-Timestamp")) ||
		time.Since(time.Unix(m.Timestamp, 0)).Abs() > 5*time.Minute {
		http.Error(w, "stale or inconsistent timestamp", http.StatusUnauthorized)
		return
	}
	// Handle the message; X-Kerf-Delivery is the id to de-duplicate retries on.
	w.WriteHeader(http.StatusNoContent)
}

func mustInt(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

func main() { http.HandleFunc("/kerf", handler); _ = http.ListenAndServe(":8080", nil) }
```

Python (standard library only):

```python
import hmac, hashlib, json, os, time
from http.server import BaseHTTPRequestHandler, HTTPServer

SECRET = os.environ["KERF_WEBHOOK_SECRET"].encode()


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        want = "sha256=" + hmac.new(SECRET, body, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(want, self.headers.get("X-Kerf-Signature", "")):
            return self.send_error(401, "bad signature")
        msg = json.loads(body)
        if str(msg["timestamp"]) != self.headers.get("X-Kerf-Timestamp") or abs(time.time() - msg["timestamp"]) > 300:
            return self.send_error(401, "stale or inconsistent timestamp")
        # Handle the message; X-Kerf-Delivery is the id to de-duplicate retries on.
        self.send_response(204)
        self.end_headers()


HTTPServer(("", 8080), Handler).serve_forever()
```

With `curl`, to try a receiver by hand:

```bash
body='{"version":1,"timestamp":'"$(date +%s)"',"id":"test","kind":"test","time":"2026-11-04T10:15:00Z","test":{"channel":"try"}}'
sig="sha256=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "$KERF_WEBHOOK_SECRET" -hex | sed 's/^.* //')"
curl -i -X POST http://localhost:8080/kerf -H 'Content-Type: application/json' -H "X-Kerf-Signature: $sig" \
  -H "X-Kerf-Timestamp: $(echo "$body" | sed 's/.*"timestamp":\([0-9]*\).*/\1/')" --data "$body"
```

## What is never sent

URLs, signing secrets, tokens and passwords never appear in a message, in the delivery history or in the API: the
channel URL and secret are encrypted at rest and shown masked. Free text of events passes through a filter that
removes webhook URLs, URL credentials, bearer and basic tokens, `token=`/`password=` values, JWTs, cloud access
keys and long opaque strings; of an event's `details` only the keys above are kept.
