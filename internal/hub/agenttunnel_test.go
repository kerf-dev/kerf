package hub

import "testing"

func TestValidateAgentTunnels(t *testing.T) {
	ok := AgentTunnel{Cluster: "prod-eu-1", URL: "https://kerf-agent.example.internal", TokenFile: "/etc/hub/agent-tunnels/0/token"}
	with := func(f func(*AgentTunnel)) AgentTunnel { t := ok; f(&t); return t }
	tests := []struct {
		name    string
		tunnels []AgentTunnel
		wantErr bool
	}{
		{name: "none", tunnels: nil},
		{name: "valid", tunnels: []AgentTunnel{ok}},
		{name: "http allowed", tunnels: []AgentTunnel{with(func(t *AgentTunnel) { t.URL = "http://kerf-agent:8443" })}},
		{name: "bad cluster", tunnels: []AgentTunnel{with(func(t *AgentTunnel) { t.Cluster = "Prod_1" })}, wantErr: true},
		{name: "duplicate", tunnels: []AgentTunnel{ok, ok}, wantErr: true},
		{name: "bad url", tunnels: []AgentTunnel{with(func(t *AgentTunnel) { t.URL = "kerf-agent:443" })}, wantErr: true},
		{name: "no token file", tunnels: []AgentTunnel{with(func(t *AgentTunnel) { t.TokenFile = "" })}, wantErr: true},
		{name: "negative connections", tunnels: []AgentTunnel{with(func(t *AgentTunnel) { t.Connections = -1 })}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateAgentTunnels(tt.tunnels); (err != nil) != tt.wantErr {
				t.Fatalf("validateAgentTunnels() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
