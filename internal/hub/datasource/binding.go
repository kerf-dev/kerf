package datasource

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// ClusterLabel is the label every query result carries the registered cluster name in.
const ClusterLabel = "cluster"

var labelNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// matcherRE splits one PromQL label matcher: name, operator, double-quoted value.
var matcherRE = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=)\s*("(?:[^"\\]|\\.)*")\s*$`)

// Matcher selects one cluster's series in its datasource, built from the cluster's metrics binding.
// Every query selector gets Selectors(); Relabel rewrites query results to carry the
// registered cluster name in label `cluster`, so nothing downstream sees the binding.
type Matcher struct {
	// Cluster is the registered cluster name.
	Cluster string
	// Label is the cluster label key; "" = no cluster matcher (a datasource of one cluster).
	Label string
	// Value is the cluster label value.
	Value string
	// Extra are normalised extra label matchers, e.g. env="prod".
	Extra []string
}

// DefaultMatcher is the matcher of a cluster without a binding: label cluster = its name.
func DefaultMatcher(cluster string) Matcher {
	return Matcher{Cluster: cluster, Label: ClusterLabel, Value: cluster}
}

// NewMatcher builds the matcher of cluster from its binding; nil = DefaultMatcher.
func NewMatcher(cluster string, b *hubapi.MetricsBinding) (Matcher, error) {
	if b == nil {
		return DefaultMatcher(cluster), nil
	}
	if err := ValidateBinding(*b); err != nil {
		return Matcher{}, err
	}
	m := Matcher{Cluster: cluster, Label: b.ClusterLabel, Value: b.ClusterLabelValue}
	if m.Value == "" {
		m.Value = cluster
	}
	if m.Label == "" {
		m.Value = ""
	}
	for _, s := range b.ExtraMatchers {
		n, err := NormalizeMatcher(s)
		if err != nil {
			return Matcher{}, err
		}
		m.Extra = append(m.Extra, n)
	}
	return m, nil
}

// ValidateBinding checks a binding's label key and extra matchers (not that the datasource exists).
func ValidateBinding(b hubapi.MetricsBinding) error {
	var errs []error
	if b.ClusterLabel != "" && !labelNameRE.MatchString(b.ClusterLabel) {
		errs = append(errs, fmt.Errorf("clusterLabel %q is not a valid label name", b.ClusterLabel))
	}
	if b.ClusterLabel == "" && b.ClusterLabelValue != "" {
		errs = append(errs, errors.New("clusterLabelValue needs a clusterLabel"))
	}
	if len(b.ExtraMatchers) > 20 {
		errs = append(errs, errors.New("at most 20 extraMatchers"))
	}
	for _, s := range b.ExtraMatchers {
		name, _, _, err := ParseMatcher(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if b.ClusterLabel != "" && name == b.ClusterLabel {
			errs = append(errs, fmt.Errorf("extraMatcher %q: use clusterLabel/clusterLabelValue for the cluster label", s))
		}
	}
	return errors.Join(errs...)
}

// ParseMatcher parses a PromQL label matcher such as env="prod" or team=~"a|b".
func ParseMatcher(s string) (name, op, value string, err error) {
	m := matcherRE.FindStringSubmatch(s)
	if m == nil {
		return "", "", "", fmt.Errorf("extraMatcher %q: want <label><op>\"<value>\" with op =, !=, =~ or !~", s)
	}
	value, err = strconv.Unquote(m[3])
	if err != nil {
		return "", "", "", fmt.Errorf("extraMatcher %q: %w", s, err)
	}
	if strings.HasSuffix(m[2], "~") {
		if _, err := regexp.Compile("^(?:" + value + ")$"); err != nil {
			return "", "", "", fmt.Errorf("extraMatcher %q: invalid regex: %w", s, err)
		}
	}
	return m[1], m[2], value, nil
}

// NormalizeMatcher returns the canonical form of a label matcher (name, op, Go-quoted value).
func NormalizeMatcher(s string) (string, error) {
	name, op, value, err := ParseMatcher(s)
	if err != nil {
		return "", err
	}
	return name + op + strconv.Quote(value), nil
}

// IsDefault reports whether the matcher equals the behaviour without a binding (no relabelling, no
// extra matchers).
func (m Matcher) IsDefault() bool {
	return m.Label == ClusterLabel && m.Value == m.Cluster && len(m.Extra) == 0
}

// Selectors returns the label matchers selecting the cluster's series.
func (m Matcher) Selectors() []string {
	var out []string
	if m.Label != "" {
		out = append(out, fmt.Sprintf("%s=%q", m.Label, m.Value))
	}
	return append(out, m.Extra...)
}

// Sel returns a selector `{<cluster matchers>, extra...}`; "" when there is nothing to match.
func (m Matcher) Sel(extra ...string) string {
	parts := append(m.Selectors(), extra...)
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Relabels reports whether query results need label cluster rewritten to the registered name.
func (m Matcher) Relabels() bool {
	return m.Label != ClusterLabel || m.Value != m.Cluster
}

// Relabel wraps an instant-vector expression so that every series carries cluster="<Cluster>".
// It is the identity when the series already do. Apply it to every raw vector before any
// aggregation or join, so `by (cluster, ...)` and `on (cluster, ...)` see the registered name.
func (m Matcher) Relabel(expr string) string {
	if !m.Relabels() {
		return expr
	}
	return fmt.Sprintf("label_replace(%s, %q, %q, \"\", \"\")", expr, ClusterLabel, m.Cluster)
}
