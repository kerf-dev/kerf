package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"

	_ "time/tzdata" // the digest's time zones, also in an image without /usr/share/zoneinfo
)

// Limits and defaults.
const (
	// DefaultRateLimit is the per-channel limit in messages per minute.
	DefaultRateLimit = 20
	// RetryWindow is how long a failing delivery is retried before it is marked failed.
	RetryWindow = time.Hour
	// Retention is how long finished deliveries are kept.
	Retention = 30 * 24 * time.Hour

	maxRuleEntries = 20
)

// Errors of the service; the hub maps them to problem codes.
var (
	ErrSecretsKeyMissing = errors.New("the hub has no secrets encryption key")
	ErrReadOnly          = errors.New("the channel is defined in the chart values")
	ErrInUse             = errors.New("the channel is in use")
	ErrNoChannels        = errors.New("no channel is configured for the digest")
)

// ValidationError lists invalid fields (400).
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string { return "invalid: " + strings.Join(e.Problems, "; ") }

// InUseError says what still sends to a channel.
type InUseError struct{ Users []string }

func (e *InUseError) Error() string { return "used by " + strings.Join(e.Users, ", ") }
func (e *InUseError) Unwrap() error { return ErrInUse }

// Sealer encrypts and decrypts the secret material of a channel (datasource.Cipher); the channel id
// is its associated data, so a ciphertext copied to another row does not open.
type Sealer interface {
	Encrypt(id string, plain []byte) ([]byte, error)
	Decrypt(id string, blob []byte) ([]byte, error)
}

// Service manages channels, rules and the digest settings and sends test messages. The Dispatcher
// (the leader) does the sending of queued deliveries.
type Service struct {
	Repo   *PG
	Sealer Sealer // nil: channels cannot be created (the URL is a secret)
	Sender *Sender
	Links  Links
	// AllowHTTP accepts http:// channel URLs (tests and receivers inside the cluster).
	AllowHTTP bool
	// RateLimit is the per-channel limit in messages per minute (default DefaultRateLimit).
	RateLimit int
	Metrics   *Metrics
	Log       *slog.Logger
	Now       func() time.Time
	// Digest and Realized feed the weekly digest. Realized may be nil: the digest then leaves the
	// realized-savings line out.
	Digest   DigestSource
	Realized RealizedSavingsSource

	limOnce sync.Once
	lim     *limiter
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) rate() int {
	if s.RateLimit > 0 {
		return s.RateLimit
	}
	return DefaultRateLimit
}

func (s *Service) sender() *Sender {
	if s.Sender != nil {
		return s.Sender
	}
	return &Sender{}
}

// Status is what the page needs to know before it offers to create a channel.
func (s *Service) Status() hubapi.NotificationStatus {
	return hubapi.NotificationStatus{SecretsKey: s.Sealer != nil, Links: s.Links.Base != "", RateLimitPerMinute: s.rate(), RetryWindowSeconds: int(RetryWindow.Seconds())}
}

// secrets is what a channel seals: its URL and, for a webhook, the signing secret.
type secrets struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

func (s *Service) seal(id string, v secrets) ([]byte, error) {
	if s.Sealer == nil {
		return nil, ErrSecretsKeyMissing
	}
	b, _ := json.Marshal(v)
	return s.Sealer.Encrypt(id, b)
}

func (s *Service) open(c Channel) (secrets, error) {
	if s.Sealer == nil {
		return secrets{}, ErrSecretsKeyMissing
	}
	b, err := s.Sealer.Decrypt(c.ID, c.SecretEnc)
	if err != nil {
		return secrets{}, fmt.Errorf("channel secrets: %w", err)
	}
	var v secrets
	if err := json.Unmarshal(b, &v); err != nil {
		return secrets{}, fmt.Errorf("channel secrets: %w", err)
	}
	return v, nil
}

// ChannelAPI converts a stored channel to its API form (masked URL, no secret).
func ChannelAPI(c Channel) hubapi.NotificationChannel {
	out := hubapi.NotificationChannel{ID: c.ID, Name: c.Name, Type: c.Type, Source: c.Source, Enabled: c.Enabled, URL: c.URLHint, SecretSet: c.HasSecret,
		Pending: c.Pending, LastSuccessAt: c.LastSuccessAt, Version: c.Version, CreatedAt: c.CreatedAt, CreatedBy: c.CreatedBy, UpdatedAt: c.UpdatedAt, UpdatedBy: c.UpdatedBy}
	if c.LastStatus != "" && c.LastAttemptAt != nil {
		out.LastDelivery = &hubapi.NotificationLastDelivery{Status: c.LastStatus, At: *c.LastAttemptAt, Error: c.LastError}
	}
	return out
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

const minSecretLen = 16

// ---- channels ----

// ListChannels returns the channels by name.
func (s *Service) ListChannels(ctx context.Context) ([]hubapi.NotificationChannel, error) {
	cs, err := s.Repo.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]hubapi.NotificationChannel, 0, len(cs))
	for _, c := range cs {
		out = append(out, ChannelAPI(c))
	}
	return out, nil
}

// GetChannel returns one channel.
func (s *Service) GetChannel(ctx context.Context, id string) (hubapi.NotificationChannel, error) {
	c, err := s.channel(ctx, id)
	return ChannelAPI(c), err
}

func (s *Service) channel(ctx context.Context, id string) (Channel, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Channel{}, ErrNotFound
	}
	return s.Repo.GetChannel(ctx, id)
}

func (s *Service) validateChannel(in hubapi.NotificationChannelInput, prev *Channel) (secrets, []string) {
	var p []string
	if !nameRE.MatchString(in.Name) {
		p = append(p, "name must be a DNS label (lower case letters, digits, '-'; at most 63)")
	}
	if !slices.Contains(hubapi.NotificationChannelTypes, in.Type) {
		p = append(p, "type must be one of slack, teams, webhook")
	}
	var sec secrets
	if prev != nil {
		if prev.Type != in.Type {
			p = append(p, "type cannot be changed; add a new channel")
		}
	}
	if in.URL != "" {
		if err := ValidateURL(in.URL, s.AllowHTTP); err != nil {
			p = append(p, err.Error())
		}
		sec.URL = strings.TrimSpace(in.URL)
	} else if prev == nil {
		p = append(p, "url is required")
	}
	if in.Secret != "" {
		switch {
		case in.Type != hubapi.NotificationWebhook:
			p = append(p, "only webhook channels have a signing secret")
		case utf8.RuneCountInString(in.Secret) < minSecretLen:
			p = append(p, fmt.Sprintf("secret must have at least %d characters", minSecretLen))
		case utf8.RuneCountInString(in.Secret) > 256:
			p = append(p, "secret must have at most 256 characters")
		}
		sec.Secret = in.Secret
	}
	if in.ClearSecret && in.Secret != "" {
		p = append(p, "secret and clearSecret are exclusive")
	}
	return sec, p
}

// CreateChannel adds a channel.
func (s *Service) CreateChannel(ctx context.Context, in hubapi.NotificationChannelInput, by hubapi.Actor) (hubapi.NotificationChannel, error) {
	sec, problems := s.validateChannel(in, nil)
	if len(problems) > 0 {
		return hubapi.NotificationChannel{}, &ValidationError{problems}
	}
	if s.Sealer == nil {
		return hubapi.NotificationChannel{}, ErrSecretsKeyMissing
	}
	id := uuid.NewString()
	enc, err := s.seal(id, sec)
	if err != nil {
		return hubapi.NotificationChannel{}, err
	}
	c := Channel{ID: id, Name: in.Name, Type: in.Type, Source: hubapi.ChannelSourceAPI, Enabled: in.Enabled == nil || *in.Enabled,
		SecretEnc: enc, URLHint: MaskURL(sec.URL), HasSecret: sec.Secret != ""}
	if err := s.Repo.InsertChannel(ctx, c, by); err != nil {
		return hubapi.NotificationChannel{}, err
	}
	return s.GetChannel(ctx, id)
}

// UpdateChannel replaces a channel; it returns the channel before and after.
func (s *Service) UpdateChannel(ctx context.Context, id string, version int, in hubapi.NotificationChannelInput, by hubapi.Actor) (before, after hubapi.NotificationChannel, err error) {
	prev, err := s.channel(ctx, id)
	if err != nil {
		return before, after, err
	}
	before = ChannelAPI(prev)
	if prev.Source == hubapi.ChannelSourceConfig {
		return before, after, ErrReadOnly
	}
	sec, problems := s.validateChannel(in, &prev)
	if len(problems) > 0 {
		return before, after, &ValidationError{problems}
	}
	// Keep what is not sent again.
	old, err := s.open(prev)
	if err != nil {
		return before, after, err
	}
	if sec.URL == "" {
		sec.URL = old.URL
	}
	if sec.Secret == "" && !in.ClearSecret {
		sec.Secret = old.Secret
	}
	enc, err := s.seal(id, sec)
	if err != nil {
		return before, after, err
	}
	c := Channel{ID: id, Name: in.Name, Type: prev.Type, Enabled: in.Enabled == nil || *in.Enabled, SecretEnc: enc, URLHint: MaskURL(sec.URL), HasSecret: sec.Secret != ""}
	if err := s.Repo.UpdateChannel(ctx, c, version, by); err != nil {
		return before, after, err
	}
	after, err = s.GetChannel(ctx, id)
	return before, after, err
}

// DeleteChannel deletes a channel nothing sends to.
func (s *Service) DeleteChannel(ctx context.Context, id string, version int, by hubapi.Actor) (hubapi.NotificationChannel, error) {
	prev, err := s.channel(ctx, id)
	if err != nil {
		return hubapi.NotificationChannel{}, err
	}
	if prev.Source == hubapi.ChannelSourceConfig {
		return ChannelAPI(prev), ErrReadOnly
	}
	users, err := s.Repo.ChannelUsers(ctx, id)
	if err != nil {
		return hubapi.NotificationChannel{}, err
	}
	if len(users) > 0 {
		return ChannelAPI(prev), &InUseError{users}
	}
	return ChannelAPI(prev), s.Repo.DeleteChannel(ctx, id, version, by)
}

// TestChannel sends a test message to the channel now, from this replica, and records the attempt
// in the delivery history.
func (s *Service) TestChannel(ctx context.Context, id string) (hubapi.NotificationTestResult, error) {
	c, err := s.channel(ctx, id)
	if err != nil {
		return hubapi.NotificationTestResult{}, err
	}
	sec, err := s.open(c)
	if err != nil {
		if errors.Is(err, ErrSecretsKeyMissing) {
			return hubapi.NotificationTestResult{}, err
		}
		return hubapi.NotificationTestResult{OK: false, Error: "the stored URL cannot be decrypted (the secrets key changed?); save the channel again"}, nil
	}
	now := s.now()
	m := Message{ID: uuid.NewString(), Kind: KindTest, Time: now, Test: &TestMessage{Channel: c.Name}}
	req, err := Render(c.Type, m, sec.Secret, now)
	if err != nil {
		return hubapi.NotificationTestResult{}, err
	}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res := s.sender().Send(tctx, sec.URL, req)
	status := hubapi.LastDeliverySent
	d := Delivery{ID: m.ID, ChannelID: c.ID, Kind: KindTest, Summary: m.Title(), Payload: m, CreatedAt: now}
	if _, err := s.Repo.Enqueue(ctx, d, 0); err != nil {
		s.log().WarnContext(ctx, "notify: record test", "err", err)
	} else {
		st, next := hubapi.DeliverySent, now
		if !res.OK {
			st = hubapi.DeliveryFailed
		}
		if err := s.Repo.Settle(ctx, m.ID, st, 1, now, next, res.Err, res.HTTPStatus); err != nil {
			s.log().WarnContext(ctx, "notify: record test", "err", err)
		}
	}
	if !res.OK {
		status = hubapi.LastDeliveryFailed
	}
	if err := s.Repo.RecordResult(ctx, c.ID, status, now, res.Err); err != nil {
		s.log().WarnContext(ctx, "notify: record result", "channel", c.Name, "err", err)
	}
	s.Metrics.count(c.Type, res.OK, false)
	return hubapi.NotificationTestResult{OK: res.OK, HTTPStatus: res.HTTPStatus, LatencyMs: int(res.Latency.Milliseconds()), Error: res.Err}, nil
}

// ---- rules ----

// RuleAPI converts a stored rule to its API form.
func RuleAPI(r Rule) hubapi.NotificationRule {
	return hubapi.NotificationRule{ID: r.ID, Name: r.Name, Enabled: r.Enabled, EventTypes: r.EventTypes, Clusters: nz(r.Clusters), Namespaces: nz(r.Namespaces),
		ChannelIDs: r.ChannelIDs, Version: r.Version, CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy, UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy}
}

// ListRules returns the rules by name.
func (s *Service) ListRules(ctx context.Context) ([]hubapi.NotificationRule, error) {
	rs, err := s.Repo.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]hubapi.NotificationRule, 0, len(rs))
	for _, r := range rs {
		out = append(out, RuleAPI(r))
	}
	return out, nil
}

func (s *Service) validGlobs(field string, globs []string) (out []string, problems []string) {
	if len(globs) > maxRuleEntries {
		problems = append(problems, fmt.Sprintf("%s: at most %d patterns", field, maxRuleEntries))
	}
	for _, g := range globs {
		g = strings.TrimSpace(g)
		if g == "" || len(g) > 253 {
			problems = append(problems, field+": patterns must have 1-253 characters")
			continue
		}
		if _, err := path.Match(g, ""); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %q is not a valid glob pattern", field, g))
			continue
		}
		if !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out, problems
}

// checkChannels reports problems unless every id is a live channel.
func (s *Service) checkChannels(ctx context.Context, ids []string) ([]string, error) {
	live, err := s.Repo.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	var p []string
	for _, id := range ids {
		if !slices.ContainsFunc(live, func(c Channel) bool { return c.ID == id }) {
			p = append(p, fmt.Sprintf("channelIds: unknown channel %s", id))
		}
	}
	return p, nil
}

func uniqueIDs(ids []string) (out []string, problems []string) {
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			problems = append(problems, fmt.Sprintf("channelIds: %q is not a channel id", id))
			continue
		}
		id = strings.ToLower(id)
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, problems
}

func (s *Service) buildRule(ctx context.Context, in hubapi.NotificationRuleInput) (Rule, error) {
	var p []string
	name := strings.TrimSpace(in.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		p = append(p, "name must have 1-100 characters")
	}
	var types []string
	for _, t := range in.EventTypes {
		if !slices.Contains(hubapi.NotificationEventTypes, t) {
			p = append(p, fmt.Sprintf("eventTypes: unknown event type %q", t))
		} else if !slices.Contains(types, t) {
			types = append(types, t)
		}
	}
	if len(types) == 0 {
		p = append(p, "eventTypes: at least one event type is required")
	}
	clusters, pc := s.validGlobs("clusters", in.Clusters)
	namespaces, pn := s.validGlobs("namespaces", in.Namespaces)
	ids, pi := uniqueIDs(in.ChannelIDs)
	p = append(p, pc...)
	p = append(p, pn...)
	p = append(p, pi...)
	if len(ids) == 0 && len(pi) == 0 {
		p = append(p, "channelIds: at least one channel is required")
	}
	if len(ids) > maxRuleEntries {
		p = append(p, fmt.Sprintf("channelIds: at most %d channels", maxRuleEntries))
	}
	if len(pi) == 0 {
		pc, err := s.checkChannels(ctx, ids)
		if err != nil {
			return Rule{}, err
		}
		p = append(p, pc...)
	}
	if len(p) > 0 {
		return Rule{}, &ValidationError{p}
	}
	return Rule{Name: name, Enabled: in.Enabled == nil || *in.Enabled, EventTypes: types, Clusters: clusters, Namespaces: namespaces, ChannelIDs: ids}, nil
}

// CreateRule adds a rule.
func (s *Service) CreateRule(ctx context.Context, in hubapi.NotificationRuleInput, by hubapi.Actor) (hubapi.NotificationRule, error) {
	r, err := s.buildRule(ctx, in)
	if err != nil {
		return hubapi.NotificationRule{}, err
	}
	r.ID = uuid.NewString()
	if err := s.Repo.InsertRule(ctx, r, by); err != nil {
		return hubapi.NotificationRule{}, err
	}
	got, err := s.Repo.GetRule(ctx, r.ID)
	return RuleAPI(got), err
}

// UpdateRule replaces a rule; it returns the rule before and after.
func (s *Service) UpdateRule(ctx context.Context, id string, version int, in hubapi.NotificationRuleInput, by hubapi.Actor) (before, after hubapi.NotificationRule, err error) {
	if _, err := uuid.Parse(id); err != nil {
		return before, after, ErrNotFound
	}
	prev, err := s.Repo.GetRule(ctx, id)
	if err != nil {
		return before, after, err
	}
	before = RuleAPI(prev)
	r, err := s.buildRule(ctx, in)
	if err != nil {
		return before, after, err
	}
	r.ID = id
	if err := s.Repo.UpdateRule(ctx, r, version, by); err != nil {
		return before, after, err
	}
	got, err := s.Repo.GetRule(ctx, id)
	return before, RuleAPI(got), err
}

// DeleteRule deletes a rule and returns it.
func (s *Service) DeleteRule(ctx context.Context, id string, version int, by hubapi.Actor) (hubapi.NotificationRule, error) {
	if _, err := uuid.Parse(id); err != nil {
		return hubapi.NotificationRule{}, ErrNotFound
	}
	prev, err := s.Repo.GetRule(ctx, id)
	if err != nil {
		return hubapi.NotificationRule{}, err
	}
	return RuleAPI(prev), s.Repo.DeleteRule(ctx, id, version, by)
}

// Matches reports whether the rule sends the event: its type is listed and the cluster and the
// namespace match one of the patterns (no patterns = any). An event without a namespace (readiness,
// automation) does not match a rule that lists namespaces.
func (r Rule) Matches(e Event) bool {
	if !r.Enabled || !slices.Contains(r.EventTypes, e.Type) {
		return false
	}
	return globMatch(r.Clusters, e.Cluster) && globMatch(r.Namespaces, e.Namespace)
}

func globMatch(patterns []string, v string) bool {
	if len(patterns) == 0 {
		return true
	}
	if v == "" {
		return false
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, v); ok {
			return true
		}
	}
	return false
}

// configNamespace derives the ids of chart-defined channels from their names, so a channel keeps
// its id (and its rules and history) across restarts.
var configNamespace = uuid.MustParse("5b0d6f4e-2b0e-4f0a-9d63-6b1c1a7e40c1")

// ConfigChannel builds the stored form of a channel defined in the chart values.
func (s *Service) ConfigChannel(name, typ, rawURL, secret string, enabled bool) (Channel, error) {
	in := hubapi.NotificationChannelInput{Name: name, Type: typ, URL: rawURL, Secret: secret}
	sec, problems := s.validateChannel(in, nil)
	if len(problems) > 0 {
		return Channel{}, &ValidationError{problems}
	}
	id := uuid.NewSHA1(configNamespace, []byte(name)).String()
	enc, err := s.seal(id, sec)
	if err != nil {
		return Channel{}, err
	}
	return Channel{ID: id, Name: name, Type: typ, Source: hubapi.ChannelSourceConfig, Enabled: enabled, SecretEnc: enc, URLHint: MaskURL(sec.URL), HasSecret: sec.Secret != ""}, nil
}

// DeliveryAPI converts a delivery to its API form.
func DeliveryAPI(d Delivery) hubapi.NotificationDelivery {
	out := hubapi.NotificationDelivery{ID: d.ID, ChannelID: d.ChannelID, ChannelName: d.ChannelName, Kind: d.Kind, EventType: d.EventType, Summary: d.Summary,
		Status: d.Status, Attempts: d.Attempts, LastError: d.LastError, HTTPStatus: d.HTTPStatus, CreatedAt: d.CreatedAt, LastAttemptAt: d.LastAttemptAt, SentAt: d.SentAt}
	if d.Status == hubapi.DeliveryPending {
		next := d.NextAttemptAt
		out.NextAttemptAt = &next
	}
	return out
}
