package hubapi

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
)

// schemaTypes maps components/schemas names to the Go types that mirror them.
var schemaTypes = map[string]any{
	// WO-00 .. WO-14
	"ResourceValues":          ResourceValues{},
	"Resources":               Resources{},
	"ContainerResources":      ContainerResources{},
	"WorkloadRef":             WorkloadRef{},
	"ValueSettings":           ValueSettings{},
	"ContainerRecommendation": ContainerRecommendation{},
	"Savings":                 Savings{},
	"DataCoverage":            DataCoverage{},
	"WorkloadRecommendation":  WorkloadRecommendation{},
	"HPACoordination":         HPACoordination{},
	"RecommendationPricing":   RecommendationPricing{},
	"RecommendationList":      RecommendationList{},
	"EffectiveModes":          EffectiveModes{},
	"WorkloadStatus":          WorkloadStatus{},
	"JVMContainer":            JVMContainer{},
	"WorkloadStatusBatch":     WorkloadStatusBatch{},
	"ContainerChange":         ContainerChange{},
	"Event":                   Event{},
	"EventBatch":              EventBatch{},
	"BatchResult":             BatchResult{},
	"AuditEvent":              AuditEvent{},
	"AuditEventList":          AuditEventList{},
	"UsageStats":              UsageStats{},
	"ResourceExplanation":     ResourceExplanation{},
	"JVMExplanation":          JVMExplanation{},
	"ContainerExplanation":    ContainerExplanation{},
	"ConfidenceFactor":        ConfidenceFactor{},
	"ConfidenceFactors":       ConfidenceFactors{},
	"ModeCounts":              ModeCounts{},
	"ResourceTotals":          ResourceTotals{},
	"Summary":                 Summary{},
	"ClusterSummary":          ClusterSummary{},
	"FleetSummary":            FleetSummary{},
	"ClusterInfo":             ClusterInfo{},
	"ClusterList":             ClusterList{},
	"WorkloadRow":             WorkloadRow{},
	"WorkloadList":            WorkloadList{},
	"FacetCount":              FacetCount{},
	"WorkloadFacets":          WorkloadFacets{},
	"WorkloadDetail":          WorkloadDetail{},
	"ContainerSeries":         ContainerSeries{},
	"UsageSeries":             UsageSeries{},
	"WorkloadHistory":         WorkloadHistory{},
	"SavingsPoint":            SavingsPoint{},
	"SavingsTrend":            SavingsTrend{},
	"RecomputeAccepted":       RecomputeAccepted{},
	"Problem":                 Problem{},

	// WO-19 admin area
	"Actor":                      Actor{},
	"ChangeReason":               ChangeReason{},
	"LoginRequest":               LoginRequest{},
	"Me":                         Me{},
	"PasswordChange":             PasswordChange{},
	"User":                       User{},
	"UserList":                   UserList{},
	"UserCreate":                 UserCreate{},
	"UserUpdate":                 UserUpdate{},
	"PasswordReset":              PasswordReset{},
	"PasswordResetResult":        PasswordResetResult{},
	"TokenInfo":                  TokenInfo{},
	"TokenList":                  TokenList{},
	"TokenCreate":                TokenCreate{},
	"TokenCreated":               TokenCreated{},
	"RecommenderProfileSettings": RecommenderProfileSettings{},
	"RecommenderSettings":        RecommenderSettings{},
	"RecommenderSettingsDoc":     RecommenderSettingsDoc{},
	"RecommenderSettingsUpdate":  RecommenderSettingsUpdate{},
	"ClusterPricingAdjustment":   ClusterPricingAdjustment{},
	"PricingSettings":            PricingSettings{},
	"PricingSettingsDoc":         PricingSettingsDoc{},
	"PricingSettingsUpdate":      PricingSettingsUpdate{},
	"Settings":                   Settings{},
	"SettingsVersion":            SettingsVersion{},
	"SettingsVersionList":        SettingsVersionList{},
	"PolicyInput":                PolicyInput{},
	"PolicyDraft":                PolicyDraft{},
	"AdminPolicy":                AdminPolicy{},
	"AdminPolicyList":            AdminPolicyList{},
	"PolicyVersion":              PolicyVersion{},
	"PolicyVersionList":          PolicyVersionList{},
	"PolicyRevert":               PolicyRevert{},
	"PolicyInventoryItem":        PolicyInventoryItem{},
	"ClusterPolicyInventory":     ClusterPolicyInventory{},
	"PolicyInventoryList":        PolicyInventoryList{},
	"PolicyChange":               PolicyChange{},
	"PreviewRequest":             PreviewRequest{},
	"ImpactItem":                 ImpactItem{},
	"ImpactPreview":              ImpactPreview{},
	"Automation":                 Automation{},
	"AutomationUpdate":           AutomationUpdate{},
	"FleetAutomation":            FleetAutomation{},
	"AgentConfigSync":            AgentConfigSync{},
	"ClusterAdmin":               ClusterAdmin{},
	"ClusterCreate":              ClusterCreate{},
	"ClusterCreated":             ClusterCreated{},
	"ClusterUpdate":              ClusterUpdate{},
	"ClusterConfig":              ClusterConfig{},
	"ConfigPolicy":               ConfigPolicy{},
	"ConfigFlags":                ConfigFlags{},
	"ConfigApproval":             ConfigApproval{},
	"ApprovalRequest":            ApprovalRequest{},
	"Approval":                   Approval{},
	"WorkloadOverrideInput":      WorkloadOverrideInput{},
	"PolicyLayer":                PolicyLayer{},
	"EffectiveField":             EffectiveField{},
	"EffectivePolicy":            EffectivePolicy{},
	"ConfigTarget":               ConfigTarget{},
	"ConfigChange":               ConfigChange{},
	"ConfigChangeList":           ConfigChangeList{},

	// RightsizingPolicy spec: the CRD types are the Go mirror (api/v1alpha1).
	"RightsizingPolicySpec": v1alpha1.RightsizingPolicySpec{},
	"PolicySelector":        v1alpha1.PolicySelector{},
	"NamespacedWorkloadRef": v1alpha1.NamespacedWorkloadRef{},
	"CPUPolicy":             v1alpha1.CPUPolicy{},
	"MemoryPolicy":          v1alpha1.MemoryPolicy{},
	"ApplyPolicy":           v1alpha1.ApplyPolicy{},
	"TimeWindow":            v1alpha1.TimeWindow{},
	"Guardrails":            v1alpha1.Guardrails{},
	"RollbackPolicy":        v1alpha1.RollbackPolicy{},
	"HPAPolicy":             v1alpha1.HPAPolicy{},
	"HPATargetBounds":       v1alpha1.HPATargetBounds{},
	"JVMPolicy":             v1alpha1.JVMPolicy{},
}

// Object schemas without a named Go type (inline structs or not used by the hub).
var unmapped = map[string]string{
	"Point": "array, Go type Point [2]float64",
}

// Enums whose Go list must equal the spec.
var enumLists = map[string][]string{
	"UserRole":         UserRoles,
	"ConfigAction":     ConfigActions,
	"ConfigTargetType": ConfigTargetTypes,
	"LayerSource":      LayerSources,
	"Mode":             Modes,
	"Phase":            Phases,
	"EventType":        EventTypes,
	"Strategy":         Strategies,
	"ModeSource":       ModeSources,
	"WorkloadKind":     WorkloadKinds,
	"RiskFlag":         AllRiskFlags,
	"HubPolicyType": {v1alpha1.HubPolicyTypeGlobalDefault, v1alpha1.HubPolicyTypeClusterDefault,
		v1alpha1.HubPolicyTypePolicy, v1alpha1.HubPolicyTypeWorkloadOverride},
	"ClusterPolicySource": {PolicySourceHub, PolicySourceGit, PolicySourceBoth},
	"Environment":         {EnvironmentProd, EnvironmentNonProd},
	"SettingsSection":     {SettingsRecommender, SettingsPricing},
	"TokenRole":           {RoleAgent, RoleUI, RoleAdmin},
}

type schema struct {
	Ref        string            `json:"$ref"`
	Type       any               `json:"type"`
	Required   []string          `json:"required"`
	Properties map[string]schema `json:"properties"`
	AllOf      []schema          `json:"allOf"`
	Enum       []string          `json:"enum"`
}

type spec struct {
	Paths      map[string]map[string]any `json:"paths"`
	Components struct {
		Schemas map[string]schema `json:"schemas"`
	} `json:"components"`
}

func loadSpec(t *testing.T) spec {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "api", "openapi", "hub.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var s spec
	if err := yaml.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// flatten merges the properties and required lists of a schema and its allOf members.
func flatten(doc spec, s schema) (props map[string]bool, required []string) {
	props = map[string]bool{}
	var walk func(s schema)
	walk = func(s schema) {
		if s.Ref != "" {
			walk(doc.Components.Schemas[strings.TrimPrefix(s.Ref, "#/components/schemas/")])
		}
		for p := range s.Properties {
			props[p] = true
		}
		required = append(required, s.Required...)
		for _, a := range s.AllOf {
			walk(a)
		}
	}
	walk(s)
	return props, required
}

// jsonFields returns json name -> omitempty, following embedded structs.
func jsonFields(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if f.Anonymous && (tag == "" || strings.HasPrefix(tag, ",")) {
			for k, v := range jsonFields(f.Type) {
				out[k] = v
			}
			continue
		}
		if tag == "" || tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		out[parts[0]] = slices.Contains(parts[1:], "omitempty")
	}
	return out
}

// TestTypesMatchSpec keeps the hub's Go types in lockstep with api/openapi/hub.yaml.
func TestTypesMatchSpec(t *testing.T) {
	doc := loadSpec(t)
	for name, v := range schemaTypes {
		t.Run(name, func(t *testing.T) {
			s, ok := doc.Components.Schemas[name]
			if !ok {
				t.Fatalf("schema %s not in spec", name)
			}
			props, required := flatten(doc, s)
			fields := jsonFields(reflect.TypeOf(v))
			var specProps, goProps []string
			for p := range props {
				specProps = append(specProps, p)
			}
			for p := range fields {
				goProps = append(goProps, p)
			}
			sort.Strings(specProps)
			sort.Strings(goProps)
			if !slices.Equal(specProps, goProps) {
				t.Errorf("properties differ:\n spec: %v\n go:   %v", specProps, goProps)
			}
			for _, r := range required {
				if omit, ok := fields[r]; ok && omit {
					t.Errorf("required property %q must not be omitempty", r)
				}
			}
		})
	}
}

// TestEverySchemaMapped makes a new object schema fail until it gets a Go type here.
func TestEverySchemaMapped(t *testing.T) {
	doc := loadSpec(t)
	for name, s := range doc.Components.Schemas {
		if len(s.Properties) == 0 && len(s.AllOf) == 0 {
			continue // scalar, enum or array
		}
		if _, ok := schemaTypes[name]; ok {
			continue
		}
		if _, ok := unmapped[name]; ok {
			continue
		}
		t.Errorf("schema %s has no Go type in schemaTypes", name)
	}
}

func TestEnumsMatchSpec(t *testing.T) {
	doc := loadSpec(t)
	for name, want := range enumLists {
		got := doc.Components.Schemas[name].Enum
		if !slices.Equal(got, want) {
			t.Errorf("enum %s:\n spec: %v\n go:   %v", name, got, want)
		}
	}
}

// TestAdminPathsInSpec pins the operations of the admin area (method + path), so a rename in
// the spec is a visible contract change.
func TestAdminPathsInSpec(t *testing.T) {
	doc := loadSpec(t)
	want := []string{
		"get /v1/clusters/{cluster}/config",
		"post /v1/auth/login", "post /v1/auth/logout", "get /v1/me", "post /v1/me/password",
		"get /v1/admin/settings", "get /v1/admin/settings/recommender", "put /v1/admin/settings/recommender",
		"get /v1/admin/settings/pricing", "put /v1/admin/settings/pricing", "get /v1/admin/settings/{section}/versions",
		"get /v1/admin/policies", "post /v1/admin/policies", "get /v1/admin/policies/{policyId}",
		"put /v1/admin/policies/{policyId}", "delete /v1/admin/policies/{policyId}",
		"get /v1/admin/policies/{policyId}/versions", "post /v1/admin/policies/{policyId}/revert",
		"get /v1/admin/policy-inventory", "post /v1/admin/preview",
		"post /v1/admin/clusters", "get /v1/admin/clusters/{cluster}", "patch /v1/admin/clusters/{cluster}",
		"delete /v1/admin/clusters/{cluster}", "put /v1/clusters/{cluster}/automation",
		"get /v1/admin/automation", "put /v1/admin/automation",
		"get /v1/workloads/{id}/effective-policy", "get /v1/workloads/{id}/override",
		"put /v1/workloads/{id}/override", "delete /v1/workloads/{id}/override", "post /v1/workloads/{id}/approve",
		"get /v1/admin/tokens", "post /v1/admin/tokens", "delete /v1/admin/tokens/{tokenId}",
		"get /v1/admin/users", "post /v1/admin/users", "get /v1/admin/users/{username}",
		"patch /v1/admin/users/{username}", "delete /v1/admin/users/{username}",
		"post /v1/admin/users/{username}/reset-password", "get /v1/audit/config",
	}
	for _, w := range want {
		m, p, _ := strings.Cut(w, " ")
		if _, ok := doc.Paths[p][m]; !ok {
			t.Errorf("spec has no %s", w)
		}
	}
}

func TestUserRoles(t *testing.T) {
	tests := []struct {
		role, min string
		want      bool
	}{
		{UserRoleAdmin, UserRoleViewer, true},
		{UserRoleOperator, UserRoleOperator, true},
		{UserRoleViewer, UserRoleOperator, false},
		{"root", UserRoleViewer, false},
		{TokenUserRole(RoleUI), UserRoleViewer, true},
		{TokenUserRole(RoleUI), UserRoleOperator, false},
		{TokenUserRole(RoleAdmin), UserRoleAdmin, true},
		{TokenUserRole(RoleAgent), UserRoleViewer, false},
	}
	for _, tt := range tests {
		if got := UserRoleAtLeast(tt.role, tt.min); got != tt.want {
			t.Errorf("UserRoleAtLeast(%q, %q) = %v, want %v", tt.role, tt.min, got, tt.want)
		}
	}
}
