package guardrails

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
	"github.com/tomer1983/resource-optimizer/internal/agent/policy"
	"github.com/tomer1983/resource-optimizer/internal/agent/workload"
)

var now = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

func ctr(name, cpuReq, memReq, cpuLim, memLim string) workload.Container {
	c := corev1.Container{Name: name, Image: "nginx", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}}
	set := func(l corev1.ResourceList, r corev1.ResourceName, v string) {
		if v != "" {
			l[r] = resource.MustParse(v)
		}
	}
	set(c.Resources.Requests, corev1.ResourceCPU, cpuReq)
	set(c.Resources.Requests, corev1.ResourceMemory, memReq)
	set(c.Resources.Limits, corev1.ResourceCPU, cpuLim)
	set(c.Resources.Limits, corev1.ResourceMemory, memLim)
	return workload.Container{Container: c}
}

func rec(cpuReq, memReq, cpuLim, memLim string) *hubclient.WorkloadRecommendation {
	return &hubclient.WorkloadRecommendation{
		ID: "11111111-1111-4111-a111-111111111111", Confidence: 0.9, RiskFlags: []string{},
		Settings:     hubclient.ValueSettings{Profile: "production", CPULimits: "keep"},
		DataCoverage: hubclient.DataCoverage{Ratio: 1, Window: "336h", Covered: "336h"},
		Containers: []hubclient.ContainerRecommendation{{
			Name:        "app",
			Recommended: hubclient.Resources{Requests: &hubclient.ResourceValues{CPU: cpuReq, Memory: memReq}, Limits: &hubclient.ResourceValues{CPU: cpuLim, Memory: memLim}},
		}},
	}
}

func effAuto() policy.Effective {
	return policy.Resolve(policy.Workload{Namespace: "d", Kind: v1alpha1.KindDeployment, Name: "w",
		Annotations: map[string]string{v1alpha1.AnnotationMode: "auto-patch"}}, nil, nil)
}

func base() Input {
	return Input{Now: now, Effective: effAuto(), Strategy: v1alpha1.StrategyInPlace, HubFresh: true,
		Rec: rec("200m", "200Mi", "", ""), Containers: []workload.Container{ctr("app", "300m", "256Mi", "1", "512Mi")}}
}

func target(d Decision, r corev1.ResourceName, limit bool) string {
	for _, t := range d.Targets {
		if t.Name == "app" {
			l := t.Requests
			if limit {
				l = t.Limits
			}
			if q, ok := l[r]; ok {
				return q.String()
			}
		}
	}
	return ""
}

// TestGuardrails covers every guardrail: each case changes one input of a passing baseline.
func TestGuardrails(t *testing.T) {
	ago := func(d time.Duration) *time.Time { x := now.Add(-d); return &x }
	tests := []struct {
		name    string
		mut     func(*Input)
		outcome Outcome
		reason  string
		check   func(*testing.T, Decision)
	}{
		{name: "baseline applies", mut: func(*Input) {}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied,
			check: func(t *testing.T, d Decision) {
				if target(d, corev1.ResourceCPU, false) != "200m" || target(d, corev1.ResourceMemory, false) != "200Mi" {
					t.Errorf("targets %+v", d.Targets)
				}
				if len(d.Resources) != 2 {
					t.Errorf("resources %v", d.Resources)
				}
			}},
		{name: "mode audit skips", mut: func(in *Input) {
			in.Effective.CPUMode, in.Effective.MemoryMode = v1alpha1.ModeAudit, v1alpha1.ModeAudit
		}, outcome: Skipped, reason: v1alpha1.ReasonModeAudit},
		{name: "mode off skips", mut: func(in *Input) {
			in.Effective.CPUMode, in.Effective.MemoryMode = v1alpha1.ModeOff, v1alpha1.ModeOff
		}, outcome: Skipped, reason: v1alpha1.ReasonModeOff},
		{name: "cpu only touches cpu", mut: func(in *Input) { in.Effective.MemoryMode = v1alpha1.ModeAudit }, outcome: Apply,
			reason: v1alpha1.ReasonRecommendationApplied, check: func(t *testing.T, d Decision) {
				if target(d, corev1.ResourceMemory, false) != "" || len(d.Resources) != 1 || d.Resources[0] != "cpu" {
					t.Errorf("memory must not be targeted: %+v %v", d.Targets, d.Resources)
				}
			}},
		{name: "no recommendation", mut: func(in *Input) { in.Rec = nil }, outcome: NoRecommendation},
		{name: "hub unreachable", mut: func(in *Input) { in.HubFresh = false }, outcome: Blocked, reason: v1alpha1.ReasonHubUnreachable},
		{name: "hub unreachable without recommendation", mut: func(in *Input) { in.HubFresh = false; in.Rec = nil }, outcome: Blocked, reason: v1alpha1.ReasonHubUnreachable},
		{name: "stale settings", mut: func(in *Input) { in.Rec.Settings.Profile = "dev" }, outcome: Skipped, reason: v1alpha1.ReasonRecommendationStale},
		{name: "hpa-coupled flag", mut: func(in *Input) { in.Rec.RiskFlags = []string{"hpa-coupled"} }, outcome: Blocked, reason: v1alpha1.ReasonHPACoupled},
		{name: "hpa found locally", mut: func(in *Input) { in.HPACoupled = true }, outcome: Blocked, reason: v1alpha1.ReasonHPACoupled},
		{name: "learning period", mut: func(in *Input) { in.Rec.DataCoverage.Covered = "100h" }, outcome: Blocked, reason: v1alpha1.ReasonLearningPeriod},
		{name: "learning period from ratio", mut: func(in *Input) { in.Rec.DataCoverage = hubclient.DataCoverage{Ratio: 0.25, Window: "336h"} },
			outcome: Blocked, reason: v1alpha1.ReasonLearningPeriod},
		{name: "low confidence", mut: func(in *Input) { in.Rec.Confidence = 0.5 }, outcome: Blocked, reason: v1alpha1.ReasonLowConfidence},
		{name: "freeze window", mut: func(in *Input) {
			in.Effective.FreezeWindows = []v1alpha1.TimeWindow{{Schedule: "0 9 * * *", Duration: &metav1.Duration{Duration: 2 * time.Hour}}}
		}, outcome: Blocked, reason: v1alpha1.ReasonFreezeWindow},
		{name: "deferred outside maintenance window", mut: func(in *Input) { in.Strategy = v1alpha1.StrategyDeferred },
			outcome: Deferred, reason: v1alpha1.ReasonOutsideMaintenanceWindow, check: func(t *testing.T, d Decision) {
				if target(d, corev1.ResourceCPU, false) != "200m" || len(d.Changes) == 0 {
					t.Errorf("deferred must carry the change to apply later: %+v", d)
				}
			}},
		{name: "deferred inside maintenance window applies", mut: func(in *Input) {
			in.Strategy = v1alpha1.StrategyDeferred
			in.Effective.MaintenanceWindows = []v1alpha1.TimeWindow{{Schedule: "55 9 * * *", Duration: &metav1.Duration{Duration: 10 * time.Minute}}}
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied},
		{name: "deferred without a change is not deferred", mut: func(in *Input) {
			in.Strategy = v1alpha1.StrategyDeferred
			in.Rec = rec("290m", "250Mi", "", "")
		}, outcome: Skipped, reason: v1alpha1.ReasonNoSignificantChange},
		{name: "freeze beats deferred", mut: func(in *Input) {
			in.Strategy = v1alpha1.StrategyDeferred
			in.Effective.FreezeWindows = []v1alpha1.TimeWindow{{Start: &metav1.Time{Time: now.Add(-time.Hour)}, End: &metav1.Time{Time: now.Add(time.Hour)}}}
		}, outcome: Blocked, reason: v1alpha1.ReasonFreezeWindow},
		{name: "cooldown after change", mut: func(in *Input) { in.LastAppliedAt = ago(time.Hour) }, outcome: Blocked, reason: v1alpha1.ReasonCooldown},
		{name: "cooldown over", mut: func(in *Input) { in.LastAppliedAt = ago(25 * time.Hour) }, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied},
		{name: "rollback blocks decreases", mut: func(in *Input) { in.RolledBackAt = ago(time.Hour) }, outcome: Blocked, reason: v1alpha1.ReasonCooldown},
		{name: "memory rollback blocks memory decreases only", mut: func(in *Input) {
			in.RolledBackAt = ago(time.Hour)
			in.RolledBackResources = []string{"memory"}
			in.Effective.MemoryMode = v1alpha1.ModeAudit
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied},
		{name: "memory rollback blocks a memory decrease", mut: func(in *Input) {
			in.RolledBackAt = ago(time.Hour)
			in.RolledBackResources = []string{"memory"}
		}, outcome: Blocked, reason: v1alpha1.ReasonCooldown},
		{name: "rollback allows increases", mut: func(in *Input) {
			in.RolledBackAt = ago(time.Hour)
			in.Rec = rec("600m", "400Mi", "", "")
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied},
		{name: "max change clamps decrease", mut: func(in *Input) { in.Rec = rec("10m", "40Mi", "", "") }, outcome: Apply,
			reason: v1alpha1.ReasonRecommendationApplied, check: func(t *testing.T, d Decision) {
				if got := target(d, corev1.ResourceCPU, false); got != "150m" {
					t.Errorf("cpu = %s, want 150m (50%% of 300m)", got)
				}
				if got := target(d, corev1.ResourceMemory, false); got != "128Mi" {
					t.Errorf("memory = %s, want 128Mi", got)
				}
			}},
		{name: "increases not capped", mut: func(in *Input) { in.Rec = rec("2", "1Gi", "", "") }, outcome: Apply,
			reason: v1alpha1.ReasonRecommendationApplied, check: func(t *testing.T, d Decision) {
				if target(d, corev1.ResourceCPU, false) != "2" || target(d, corev1.ResourceMemory, false) != "1Gi" {
					t.Errorf("targets %+v", d.Targets)
				}
				if target(d, corev1.ResourceCPU, true) != "2" || target(d, corev1.ResourceMemory, true) != "1Gi" {
					t.Errorf("limits must be raised to the request: %+v", d.Targets)
				}
			}},
		{name: "floors", mut: func(in *Input) {
			in.Containers = []workload.Container{ctr("app", "12m", "34Mi", "", "")}
			in.Rec = rec("1m", "1Mi", "", "")
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, check: func(t *testing.T, d Decision) {
			if target(d, corev1.ResourceCPU, false) != "12m" && target(d, corev1.ResourceCPU, false) != "10m" {
				t.Errorf("cpu below floor: %s", target(d, corev1.ResourceCPU, false))
			}
			if q := resource.MustParse(target(d, corev1.ResourceMemory, false)); q.Cmp(policy.DefaultMinMemory) < 0 {
				t.Errorf("memory below floor: %s", q.String())
			}
		}},
		{name: "throttled never decreases cpu", mut: func(in *Input) {
			in.Rec.Containers[0].RiskFlags = []string{"throttled"}
			in.Effective.MemoryMode = v1alpha1.ModeAudit
		}, outcome: Blocked, reason: v1alpha1.ReasonThrottled},
		{name: "oom-killed blocks memory decrease, cpu still applies", mut: func(in *Input) { in.Rec.RiskFlags = []string{"oom-killed"} },
			outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, check: func(t *testing.T, d Decision) {
				if target(d, corev1.ResourceMemory, false) != "256Mi" || len(d.Refusals) != 1 || d.Refusals[0].Reason != v1alpha1.ReasonOOMKilled {
					t.Errorf("memory must stay: %+v refusals %+v", d.Targets, d.Refusals)
				}
			}},
		{name: "jvm memory in-place needs RestartContainer", mut: func(in *Input) {
			in.Containers[0].Image = "eclipse-temurin:21-jre"
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, check: func(t *testing.T, d Decision) {
			if len(d.Refusals) != 1 || d.Refusals[0].Reason != v1alpha1.ReasonRestartPolicyRequired || d.Resources[0] != "cpu" || len(d.Resources) != 1 {
				t.Errorf("refusals %+v resources %v", d.Refusals, d.Resources)
			}
		}},
		{name: "jvm memory with RestartContainer ok", mut: func(in *Input) {
			in.Containers[0].Image = "eclipse-temurin:21-jre"
			in.Containers[0].ResizePolicy = []corev1.ContainerResizePolicy{{ResourceName: corev1.ResourceMemory, RestartPolicy: corev1.RestartContainer}}
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, check: func(t *testing.T, d Decision) {
			if len(d.Refusals) != 0 || len(d.Resources) != 2 {
				t.Errorf("refusals %+v resources %v", d.Refusals, d.Resources)
			}
		}},
		{name: "jvm memory with rollout ok", mut: func(in *Input) {
			in.Containers[0].Image = "eclipse-temurin:21-jre"
			in.Strategy = v1alpha1.StrategyRollout
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied},
		{name: "jvm memory only change blocked", mut: func(in *Input) {
			in.Containers[0].Image = "python:3.13"
			in.Effective.CPUMode = v1alpha1.ModeAudit
		}, outcome: Blocked, reason: v1alpha1.ReasonRestartPolicyRequired},
		{name: "tolerance", mut: func(in *Input) { in.Rec = rec("290m", "250Mi", "", "") }, outcome: Skipped, reason: v1alpha1.ReasonNoSignificantChange},
		{name: "remove cpu limit", mut: func(in *Input) {
			in.Rec = rec("300m", "256Mi", "", "")
			in.Rec.Containers[0].RemoveLimits = []string{"cpu"}
		}, outcome: Apply, reason: v1alpha1.ReasonRecommendationApplied, check: func(t *testing.T, d Decision) {
			if target(d, corev1.ResourceCPU, true) != "" || len(d.Targets[0].RemoveLimits) != 1 {
				t.Errorf("cpu limit not removed: %+v", d.Targets)
			}
		}},
		{name: "unknown container ignored", mut: func(in *Input) { in.Rec.Containers[0].Name = "gone" }, outcome: Skipped, reason: v1alpha1.ReasonNoSignificantChange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			tt.mut(&in)
			d := Evaluate(in)
			if d.Outcome != tt.outcome || (tt.reason != "" && d.Reason != tt.reason) {
				t.Fatalf("got %s/%s (%s), want %s/%s", d.Outcome, d.Reason, d.Message, tt.outcome, tt.reason)
			}
			if tt.check != nil {
				tt.check(t, d)
			}
		})
	}
}

func TestHeapBound(t *testing.T) {
	tests := []struct {
		c    corev1.Container
		want bool
	}{
		{corev1.Container{Image: "eclipse-temurin:21"}, true},
		{corev1.Container{Image: "registry/app/openjdk:17"}, true},
		{corev1.Container{Image: "python:3.12-slim"}, true},
		{corev1.Container{Image: "nginx", Command: []string{"/usr/bin/java", "-jar", "x.jar"}}, true},
		{corev1.Container{Image: "busybox", Env: []corev1.EnvVar{{Name: "JAVA_TOOL_OPTIONS"}}}, true},
		{corev1.Container{Image: "nginx:1.27"}, false},
		{corev1.Container{Image: "colinianking/stress-ng:77b17fb95ec2f00e1c2ac2a8a20fe799150d8bd5"}, false},
		{corev1.Container{Image: "javascript-runner"}, false},
	}
	for _, tt := range tests {
		if got := workload.HeapBound(tt.c); got != tt.want {
			t.Errorf("HeapBound(%s %v) = %v, want %v", tt.c.Image, tt.c.Command, got, tt.want)
		}
	}
}

// TestPaused (D-022): a hub pause blocks every change like a freeze; audit workloads stay skipped.
func TestPaused(t *testing.T) {
	in := base()
	in.Paused, in.PauseMessage = true, "automation is paused in the hub (cluster): incident"
	d := Evaluate(in)
	if d.Outcome != Blocked || d.Reason != v1alpha1.ReasonPaused || d.Message != in.PauseMessage || len(d.Proposed) == 0 {
		t.Fatalf("got %+v", d)
	}
	in.Effective = policy.Resolve(policy.Workload{Namespace: "d", Kind: v1alpha1.KindDeployment, Name: "w"}, nil, nil)
	if d := Evaluate(in); d.Outcome != Skipped || d.Reason != v1alpha1.ReasonModeAudit {
		t.Fatalf("audit while paused: %+v", d)
	}
}
