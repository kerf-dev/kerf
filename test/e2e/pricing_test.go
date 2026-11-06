//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// TestPricingRateSources: the pricing service labels every node's rate with its source, and the hub
// shows them with the pricing settings and in the pricing.coverage readiness details. The dev fleet
// runs without the AWS Cost and Usage Report, so every node keeps a list, spot or static rate and the
// report status is absent.
func TestPricingRateSources(t *testing.T) {
	t.Parallel()
	nonCUR := map[string]bool{hubapi.RateSourceList: true, hubapi.RateSourceSpot: true, hubapi.RateSourceStatic: true}
	var doc hubapi.PricingSettingsDoc
	// The source label appears with the first refresh of a new pricing service (1 min in dev).
	eventually(t, 5*time.Minute, 10*time.Second, "pricing rate sources for both spokes", func() (bool, string) {
		doc = hubapi.PricingSettingsDoc{}
		as(t, hubapi.UserRoleViewer).get(t, "/v1/admin/settings/pricing", &doc)
		if doc.Rates == nil {
			return false, "no rates: the hub cannot read the pricing series"
		}
		found := map[string][]hubapi.PricingRateSource{}
		for _, c := range doc.Rates.Clusters {
			found[c.Cluster] = c.Sources
		}
		for _, cl := range []cluster{env.Spoke1, env.Spoke2} {
			if len(found[cl.Name]) == 0 {
				return false, fmt.Sprintf("%s: no rate sources (clusters %v)", cl.Name, found)
			}
		}
		return true, ""
	})
	if doc.Rates.CUR != nil {
		t.Errorf("cost and usage report status %+v, want none (the reader is off by default)", *doc.Rates.CUR)
	}
	for _, c := range doc.Rates.Clusters {
		for _, s := range c.Sources {
			if !nonCUR[s.Source] || s.Nodes <= 0 {
				t.Errorf("%s: rate source %+v, want list, spot or static with nodes", c.Cluster, s)
			}
		}
		t.Logf("%s: %+v", c.Cluster, c.Sources)
	}

	// pricing.coverage carries the same breakdown in its details.
	r := waitReadiness(t, env.Spoke1.Name, 3*time.Minute, "pricing.coverage with rate sources", func(r hubapi.ClusterReadiness) (bool, string) {
		for _, c := range r.Checks {
			if c.ID != "pricing.coverage" {
				continue
			}
			for k := range c.Details {
				if strings.HasPrefix(k, "source.") {
					return true, ""
				}
			}
			return false, fmt.Sprintf("pricing.coverage %s %q details %v", c.Status, c.Message, c.Details)
		}
		return false, "no pricing.coverage check"
	})
	for _, c := range r.Checks {
		if c.ID == "pricing.coverage" {
			if _, ok := c.Details["curLastReadOK"]; ok {
				t.Errorf("pricing.coverage has curLastReadOK with the reader off: %v", c.Details)
			}
			t.Logf("pricing.coverage: %s %q %v", c.Status, c.Message, c.Details)
		}
	}
}
