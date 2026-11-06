//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// TestMaintenance (smoke tier): Admin -> Maintenance lists the hub replicas (one leader)
// and, for both spokes, the agent's version and the webhook pods the agent found. A readiness
// recheck makes each agent run its self-checks, which carry the webhook versions.
func TestMaintenance(t *testing.T) {
	t.Parallel()
	for _, cl := range []cluster{env.Spoke1, env.Spoke2} {
		if cl.Name == env.Spoke2.Name {
			unlock := lockSpoke2(t)
			waitReadiness(t, cl.Name, 3*time.Minute, "agent answered the recheck", func(hubapi.ClusterReadiness) (bool, string) { return true, "" })
			unlock()
			continue
		}
		waitReadiness(t, cl.Name, 3*time.Minute, "agent answered the recheck", func(hubapi.ClusterReadiness) (bool, string) { return true, "" })
	}

	var m hubapi.Maintenance
	as(t, hubapi.UserRoleViewer).get(t, "/v1/admin/maintenance", &m)
	if m.HubVersion == "" {
		t.Error("no hubVersion")
	}
	leaders, hubs := 0, 0
	agents := map[string]hubapi.ComponentVersion{}
	webhooks := map[string]int{}
	for _, c := range m.Components {
		switch c.Component {
		case hubapi.ComponentHub:
			hubs++
			if c.Leader {
				leaders++
			}
			if c.Version == "" || c.Instance == "" {
				t.Errorf("hub replica without version or name: %+v", c)
			}
		case hubapi.ComponentAgent:
			agents[c.Cluster] = c
		case hubapi.ComponentWebhook:
			if c.Version == "" {
				t.Errorf("webhook pod %s/%s without version", c.Cluster, c.Instance)
			}
			webhooks[c.Cluster]++
		}
	}
	if hubs == 0 || leaders != 1 {
		t.Errorf("%d hub replicas, %d leaders; want >= 1 and exactly 1", hubs, leaders)
	}
	for _, cl := range []cluster{env.Spoke1, env.Spoke2} {
		a, ok := agents[cl.Name]
		if !ok || a.Version == "" || a.AgentStatus != "connected" {
			t.Errorf("%s agent: %+v (listed %v)", cl.Name, a, ok)
		}
		if webhooks[cl.Name] == 0 {
			t.Errorf("%s: no webhook pods reported", cl.Name)
		}
		t.Logf("%s: agent %s (%s), %d webhook pods", cl.Name, a.Version, a.Compat, webhooks[cl.Name])
	}
	switch m.Update.State {
	case hubapi.UpdateUpToDate, hubapi.UpdateAvailable, hubapi.UpdateDisabled, hubapi.UpdateFailed, hubapi.UpdatePending, hubapi.UpdateDevBuild:
		t.Logf("update check: %s (latest %q, error %q)", m.Update.State, m.Update.LatestVersion, m.Update.Error)
	default:
		t.Errorf("update state %q", m.Update.State)
	}
}
