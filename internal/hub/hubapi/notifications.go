package hubapi

import "time"

// Notifications: Slack, Microsoft Teams and signed webhook channels, the rules that route events to
// them, the weekly digest and the delivery history. docs/notifications.md; the payload a generic
// webhook receives is documented in docs/contracts/notifications.md.

// Channel types (NotificationChannelType).
const (
	NotificationSlack   = "slack"
	NotificationTeams   = "teams"
	NotificationWebhook = "webhook"
)

// NotificationChannelTypes lists the NotificationChannelType enum.
var NotificationChannelTypes = []string{NotificationSlack, NotificationTeams, NotificationWebhook}

// Channel sources (NotificationChannel.source): created through the API, or defined in the chart
// values (read-only in the API).
const (
	ChannelSourceAPI    = "api"
	ChannelSourceConfig = "config"
)

// Notification event types (NotificationEventType): the workload event types that matter to people,
// plus the cluster and fleet events of the config audit.
const (
	NotifyApplied           = "applied"
	NotifyRolledBack        = "rolled_back"
	NotifyFailed            = "failed"
	NotifyBlocked           = "blocked"
	NotifyReadinessDegraded = "readiness_degraded"
	NotifyAutomationPaused  = "automation_paused"
	NotifyAutomationResumed = "automation_resumed"
)

// NotificationEventTypes lists the NotificationEventType enum.
var NotificationEventTypes = []string{NotifyApplied, NotifyRolledBack, NotifyFailed, NotifyBlocked,
	NotifyReadinessDegraded, NotifyAutomationPaused, NotifyAutomationResumed}

// Delivery kinds (NotificationDelivery.kind), delivery states (NotificationDelivery.status) and the
// state of a channel's last attempt (NotificationLastDelivery.status).
const (
	DeliveryKindEvent  = "event"
	DeliveryKindDigest = "digest"
	DeliveryKindTest   = "test"

	DeliveryPending = "pending"
	DeliverySent    = "sent"
	DeliveryFailed  = "failed"

	LastDeliverySent     = "sent"
	LastDeliveryFailed   = "failed"
	LastDeliveryRetrying = "retrying"
)

// NotificationDeliveryKinds lists the NotificationDelivery.kind enum.
var NotificationDeliveryKinds = []string{DeliveryKindEvent, DeliveryKindDigest, DeliveryKindTest}

// NotificationDeliveryStatuses lists the NotificationDelivery.status enum.
var NotificationDeliveryStatuses = []string{DeliveryPending, DeliverySent, DeliveryFailed}

// Config actions and target type of notification changes (ConfigAction, ConfigTargetType).
const (
	ActionNotificationChannelCreated = "NotificationChannelCreated"
	ActionNotificationChannelUpdated = "NotificationChannelUpdated"
	ActionNotificationChannelDeleted = "NotificationChannelDeleted"
	ActionNotificationRuleCreated    = "NotificationRuleCreated"
	ActionNotificationRuleUpdated    = "NotificationRuleUpdated"
	ActionNotificationRuleDeleted    = "NotificationRuleDeleted"
	ActionNotificationDigestUpdated  = "NotificationDigestUpdated"

	TargetNotification = "notification"
)

// Problem codes of the notification endpoints.
const (
	CodeSecretsKeyMissing = "secrets_key_missing"
	CodeChannelInUse      = "channel_in_use"
	CodeChannelReadOnly   = "channel_read_only"
)

// NotificationChannelInput is the body of POST /v1/admin/notifications/channels and
// PUT /v1/admin/notifications/channels/{channelId}. url and secret are write-only: omitted on
// update they keep their stored values.
type NotificationChannelInput struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	URL         string `json:"url,omitempty"`
	Secret      string `json:"secret,omitempty"`
	ClearSecret bool   `json:"clearSecret,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
	Reason      string `json:"reason"`
}

// NotificationLastDelivery is the outcome of a channel's most recent delivery attempt.
type NotificationLastDelivery struct {
	Status string    `json:"status"`
	At     time.Time `json:"at"`
	Error  string    `json:"error,omitempty"`
}

// NotificationChannel is a destination. url is masked (scheme and host only).
type NotificationChannel struct {
	ID            string                    `json:"id"`
	Name          string                    `json:"name"`
	Type          string                    `json:"type"`
	Source        string                    `json:"source"`
	Enabled       bool                      `json:"enabled"`
	URL           string                    `json:"url"`
	SecretSet     bool                      `json:"secretSet"`
	Pending       int                       `json:"pending"`
	LastDelivery  *NotificationLastDelivery `json:"lastDelivery,omitempty"`
	LastSuccessAt *time.Time                `json:"lastSuccessAt,omitempty"`
	Version       int                       `json:"version"`
	CreatedAt     time.Time                 `json:"createdAt"`
	CreatedBy     *Actor                    `json:"createdBy,omitempty"`
	UpdatedAt     time.Time                 `json:"updatedAt"`
	UpdatedBy     *Actor                    `json:"updatedBy,omitempty"`
}

// NotificationChannelList is the response of GET /v1/admin/notifications/channels.
type NotificationChannelList struct {
	Items []NotificationChannel `json:"items"`
}

// NotificationTestResult is the response of POST /v1/admin/notifications/channels/{channelId}/test.
type NotificationTestResult struct {
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	LatencyMs  int    `json:"latencyMs"`
	Error      string `json:"error,omitempty"`
}

// NotificationRuleInput is the body of POST /v1/admin/notifications/rules and
// PUT /v1/admin/notifications/rules/{ruleId}.
type NotificationRuleInput struct {
	Name       string   `json:"name"`
	Enabled    *bool    `json:"enabled,omitempty"`
	EventTypes []string `json:"eventTypes"`
	Clusters   []string `json:"clusters,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`
	ChannelIDs []string `json:"channelIds"`
	Reason     string   `json:"reason"`
}

// NotificationRule routes events to channels.
type NotificationRule struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Enabled    bool      `json:"enabled"`
	EventTypes []string  `json:"eventTypes"`
	Clusters   []string  `json:"clusters"`
	Namespaces []string  `json:"namespaces"`
	ChannelIDs []string  `json:"channelIds"`
	Version    int       `json:"version"`
	CreatedAt  time.Time `json:"createdAt"`
	CreatedBy  *Actor    `json:"createdBy,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt"`
	UpdatedBy  *Actor    `json:"updatedBy,omitempty"`
}

// NotificationRuleList is the response of GET /v1/admin/notifications/rules.
type NotificationRuleList struct {
	Items []NotificationRule `json:"items"`
}

// NotificationDigestInput is the body of PUT /v1/admin/notifications/digest.
type NotificationDigestInput struct {
	Enabled    bool     `json:"enabled"`
	Weekday    int      `json:"weekday"`
	Time       string   `json:"time"`
	Timezone   string   `json:"timezone"`
	ChannelIDs []string `json:"channelIds"`
	Reason     string   `json:"reason"`
}

// NotificationDigest is the weekly digest's settings.
type NotificationDigest struct {
	Enabled    bool       `json:"enabled"`
	Weekday    int        `json:"weekday"`
	Time       string     `json:"time"`
	Timezone   string     `json:"timezone"`
	ChannelIDs []string   `json:"channelIds"`
	NextSendAt *time.Time `json:"nextSendAt,omitempty"`
	LastSentAt *time.Time `json:"lastSentAt,omitempty"`
	Version    int        `json:"version"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	UpdatedBy  *Actor     `json:"updatedBy,omitempty"`
}

// NotificationDigestQueued is the response of POST /v1/admin/notifications/digest/send.
type NotificationDigestQueued struct {
	Queued int `json:"queued"`
}

// NotificationDelivery is one message to one channel: queued, sent, or failed after its retries.
type NotificationDelivery struct {
	ID            string     `json:"id"`
	ChannelID     string     `json:"channelId"`
	ChannelName   string     `json:"channelName"`
	Kind          string     `json:"kind"`
	EventType     string     `json:"eventType,omitempty"`
	Summary       string     `json:"summary"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"nextAttemptAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	HTTPStatus    int        `json:"httpStatus,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastAttemptAt *time.Time `json:"lastAttemptAt,omitempty"`
	SentAt        *time.Time `json:"sentAt,omitempty"`
}

// NotificationDeliveryList is the response of GET /v1/admin/notifications/deliveries.
type NotificationDeliveryList struct {
	Items    []NotificationDelivery `json:"items"`
	Continue string                 `json:"continue,omitempty"`
}

// NotificationStatus is the response of GET /v1/admin/notifications/status: what the page needs to
// know before it offers to create a channel.
type NotificationStatus struct {
	// SecretsKey: webhook URLs can be stored (the secrets encryption key is configured).
	SecretsKey bool `json:"secretsKey"`
	// Links: messages link to the UI (notifications.baseURL is set).
	Links bool `json:"links"`
	// RateLimitPerMinute is the per-channel limit.
	RateLimitPerMinute int `json:"rateLimitPerMinute"`
	// RetryWindowSeconds is how long a failing delivery is retried before it is marked failed.
	RetryWindowSeconds int `json:"retryWindowSeconds"`
}
