//go:build e2e

package e2e

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	rd "github.com/kerf-dev/kerf/api/v1alpha1/readiness"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// hubChecksSince reports whether the hub's metrics checks of a readiness report ran at or after t0.
// metrics.freshness stands for them: the hub runs them together, only at registration, on a
// re-check or recompute, and every check interval (1h by default).
func hubChecksSince(r hubapi.ClusterReadiness, t0 time.Time) (bool, string) {
	c := check(r, rd.CheckFreshness)
	if c == nil {
		return false, "no metrics.freshness check"
	}
	if c.CheckedAt.Before(t0.Add(-2 * time.Second)) {
		return false, "metrics.freshness checked at " + c.CheckedAt.Format(time.TimeOnly) + ", requested at " + t0.Format(time.TimeOnly)
	}
	return true, ""
}

// TestReadinessRecheck: a recompute and a re-check each re-run the hub's metrics checks at once,
// instead of leaving them as old as the check interval. On the fresh results, the scrape interval
// matches the dev scrapers, an absent series is never reported as an age, and every failing metrics
// check says how to re-check.
func TestReadinessRecheck(t *testing.T) {
	cl := env.Spoke1.Name

	t.Run("recompute", func(t *testing.T) {
		var acc hubapi.RecomputeAccepted
		as(t, hubapi.UserRoleAdmin).call(t, http.MethodPost, "/v1/admin/recompute?cluster="+url.QueryEscape(cl), nil, nil, http.StatusAccepted, &acc)
		eventually(t, 2*time.Minute, 5*time.Second, cl+" hub checks after a recompute", func() (bool, string) {
			return hubChecksSince(readiness(t, cl), acc.RequestedAt)
		})
	})

	var r hubapi.ClusterReadiness
	t.Run("recheck", func(t *testing.T) {
		acc := recheck(t, cl)
		eventually(t, 2*time.Minute, 5*time.Second, cl+" hub checks after a re-check", func() (bool, string) {
			r = readiness(t, cl)
			return hubChecksSince(r, acc.RequestedAt)
		})
	})
	if t.Failed() {
		return
	}

	t.Run("results", func(t *testing.T) {
		if c := check(r, rd.CheckScrapeInterval); c == nil {
			t.Error("no metrics.scrape-interval check")
		} else if c.Status != string(rd.StatusPass) {
			t.Errorf("metrics.scrape-interval = %s: %s (details %v)", c.Status, c.Message, c.Details)
		} else if d, err := time.ParseDuration(c.Details["interval"]); err != nil || d <= 0 || d > 65*time.Second || c.Details["series"] == "" {
			t.Errorf("metrics.scrape-interval details %v", c.Details)
		}
		if c := check(r, rd.CheckFreshness); c != nil {
			for k, v := range c.Details {
				if v == "1h0m0s" {
					t.Errorf("metrics.freshness reports the lookback as an age: %s=%s", k, v)
				}
			}
			if strings.Contains(c.Message, "over 1h old") {
				t.Errorf("metrics.freshness message %q", c.Message)
			}
		}
		if c := check(r, rd.CheckNodeLabels); c != nil && c.Status == string(rd.StatusWarn) && c.Details["nodes"] != "" && !strings.Contains(c.Message, " of "+c.Details["nodes"]+" nodes") {
			t.Errorf("metrics.node-labels message %q does not say how many of %s nodes", c.Message, c.Details["nodes"])
		}
		for _, c := range r.Checks {
			if c.Source != "hub" || c.Remediation == "" || (c.Status != string(rd.StatusWarn) && c.Status != string(rd.StatusFail)) {
				continue
			}
			// metrics.recommendations is refreshed every minute, not by the metrics checks.
			if (!strings.HasPrefix(c.ID, "metrics.") && c.ID != rd.CheckPricingCoverage) || c.ID == rd.CheckRecommendations {
				continue
			}
			if !strings.Contains(c.Remediation, "/v1/clusters/"+cl+"/readiness/recheck") {
				t.Errorf("%s remediation does not say how to re-check: %q", c.ID, c.Remediation)
			}
			if strings.Contains(c.Remediation, "insecure_skip_verify") {
				t.Errorf("%s remediation turns off TLS verification", c.ID)
			}
		}
	})
}
