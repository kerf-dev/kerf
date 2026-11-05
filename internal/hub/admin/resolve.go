// Package admin computes, in the hub, what the agent computes on a cluster: the effective policy of
// a workload from the hub policies and the Git CRs the agents report (docs/contracts/admin.md §2),
// and the impact of hypothetical policy changes (POST /v1/admin/preview).
//
// Selector matching and winner selection reuse the agent's code (internal/agent/policy), so the
// hub and the agent agree. The hub does not know workload labels and annotations: label selectors
// are judged by the agent's report, annotation modes are inferred from the reported mode sources.
package admin

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/internal/agent/policy"
	"github.com/kerf-dev/kerf/internal/hub/hubapi"
)

// Candidate is a policy that may apply to a cluster's workloads.
type Candidate struct {
	Name   string
	Origin string // hubapi.PolicySourceHub ("hub") or hubapi.PolicySourceGit ("git")
	// Type is v1alpha1.HubPolicyType* for hub policies; "" for Git CRs (they count as type "policy").
	Type     string
	PolicyID string // hub policies only
	// Clusters of hub policies: empty = every cluster (global-default: always all; cluster-default:
	// exactly one). Git CRs: the one cluster that reported it.
	Clusters   []string
	WorkloadID string // workload-override only: hubapi.WorkloadKey.ID() of its workload
	Spec       v1alpha1.RightsizingPolicySpec
}

// Workload is what the hub knows about a workload.
type Workload struct {
	Key    hubapi.WorkloadKey
	Status *hubapi.WorkloadStatus // the agent's latest report; nil = never reported
}

const (
	sourceAnnotation = string(v1alpha1.ModeSourceAnnotation)
	warnNoStatus     = "agent has not reported this workload yet"
)

// builtIn is the built-in defaults as a spec, taken from the agent's resolution without any policy.
var builtIn = builtInSpec()

func builtInSpec() v1alpha1.RightsizingPolicySpec {
	e := policy.Resolve(policy.Workload{}, nil, nil)
	leq, coord, detect := e.MemoryLimitEqualsRequest, e.HPACoordinate, e.JVMDetect
	maxChange, ooms, restarts := e.MaxChangePercent, e.RollbackOOMKills, e.RollbackRestarts
	minT, maxT := e.HPAMinTarget, e.HPAMaxTarget
	minCPU, minMem := e.MinCPU.DeepCopy(), e.MinMemory.DeepCopy()
	return v1alpha1.RightsizingPolicySpec{
		Profile: e.Profile,
		CPU:     &v1alpha1.CPUPolicy{Mode: e.CPUMode, Limits: e.CPULimits, LimitRatio: e.CPULimitRatio},
		Memory:  &v1alpha1.MemoryPolicy{Mode: e.MemoryMode, LimitEqualsRequest: &leq},
		Apply: &v1alpha1.ApplyPolicy{
			Strategy:           e.Strategy,
			FallbackAfter:      &metav1.Duration{Duration: e.FallbackAfter},
			MaintenanceWindows: e.MaintenanceWindows,
			FreezeWindows:      e.FreezeWindows,
		},
		Guardrails: &v1alpha1.Guardrails{
			LearningPeriod:   &metav1.Duration{Duration: e.LearningPeriod},
			MinConfidence:    strconv.FormatFloat(e.MinConfidence, 'f', -1, 64),
			MaxChangePercent: &maxChange,
			MinCPU:           &minCPU,
			MinMemory:        &minMem,
			Cooldown:         &metav1.Duration{Duration: e.Cooldown},
			Rollback: &v1alpha1.RollbackPolicy{
				WatchDuration:   &metav1.Duration{Duration: e.WatchDuration},
				OOMKills:        &ooms,
				Restarts:        &restarts,
				ThrottlingRatio: strconv.FormatFloat(e.ThrottlingRatio, 'f', -1, 64),
			},
		},
		HPA: &v1alpha1.HPAPolicy{
			Coordinate:   &coord,
			TargetBounds: &v1alpha1.HPATargetBounds{MinUtilization: &minT, MaxUtilization: &maxT},
		},
		JVM: &v1alpha1.JVMPolicy{Detect: &detect},
	}
}

// layer is one applied layer: its source (hubapi.Layer*), name and spec.
type layer struct {
	source string
	name   string
	spec   *v1alpha1.RightsizingPolicySpec
}

// resolution is the outcome of resolving one workload.
type resolution struct {
	layers   []hubapi.PolicyLayer
	applied  []layer // applied layers, lowest first, built-in included
	winner   string
	override string
	modes    hubapi.EffectiveModes
	cpuBy    layer
	// labelWarnings are about label selectors the hub cannot evaluate.
	labelWarnings []string
}

// Resolve computes the effective policy of w from all candidates (it filters by cluster itself).
func Resolve(w Workload, cands []Candidate) hubapi.EffectivePolicy {
	r := resolve(w, cands)
	ep := hubapi.EffectivePolicy{
		WorkloadID: w.Key.ID(),
		Layers:     r.layers,
		Fields:     fields(r.applied),
		Modes:      r.modes,
	}
	var warnings []string
	if st := w.Status; st == nil {
		warnings = append(warnings, warnNoStatus)
	} else {
		m := st.Modes
		ep.AgentModes = &m
		ep.AgentPolicy = st.Policy
		warnings = append(warnings, modeDiff("cpu", m.CPU, m.CPUSource, r.modes.CPU, r.modes.CPUSource)...)
		warnings = append(warnings, modeDiff("memory", m.Memory, m.MemorySource, r.modes.Memory, r.modes.MemorySource)...)
		if st.Policy != r.winner {
			warnings = append(warnings, fmt.Sprintf("agent reports policy %s; the hub computes %s", orNone(st.Policy), orNone(r.winner)))
		}
		if st.Override != r.override {
			warnings = append(warnings, fmt.Sprintf("agent reports override %s; the hub computes %s", orNone(st.Override), orNone(r.override)))
		}
	}
	warnings = append(warnings, r.labelWarnings...)
	if len(warnings) > 0 {
		ep.Warnings = warnings
	}
	return ep
}

// Modes returns the effective CPU and memory modes of w.
func Modes(w Workload, cands []Candidate) hubapi.EffectiveModes {
	return resolve(w, cands).modes
}

func modeDiff(res, agentMode, agentSource, mode, source string) []string {
	if agentMode == mode && (agentSource == "" || agentSource == source) {
		return nil
	}
	return []string{fmt.Sprintf("agent reports %s %s (%s); the hub computes %s (%s)", res, agentMode, agentSource, mode, source)}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return strconv.Quote(s)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// selectsWorkload is the selector.workloads check (AND with the other criteria). It is applied in
// addition to policy.Matches, so results are right whether or not Matches knows the field.
func selectsWorkload(sel v1alpha1.PolicySelector, w policy.Workload) bool {
	if len(sel.Workloads) == 0 {
		return true
	}
	for _, r := range sel.Workloads {
		if r.Namespace == w.Namespace && r.Kind == w.Kind && r.Name == w.Name {
			return true
		}
	}
	return false
}

func labelWarning(name string) string {
	return fmt.Sprintf("policy %s selects by labels; the hub cannot verify the workload's labels", name)
}

// ranksAbove reports whether a policy (priority p, name n) wins against one with (wp, wn).
func ranksAbove(p int32, n string, wp int32, wn string) bool {
	return p > wp || (p == wp && n < wn)
}

func hubLayer(source string, c *Candidate, applied bool) hubapi.PolicyLayer {
	prio := c.Spec.Priority
	origin := c.Origin
	if origin == "" {
		origin = hubapi.PolicySourceHub
	}
	l := hubapi.PolicyLayer{
		Source:   source,
		Name:     c.Name,
		Origin:   origin,
		PolicyID: c.PolicyID,
		Priority: &prio,
		Applied:  applied,
	}
	if applied {
		s := c.Spec
		l.Spec = &s
	}
	return l
}

func resolve(w Workload, cands []Candidate) resolution {
	cluster, id := w.Key.Cluster, w.Key.ID()
	pw := policy.Workload{Namespace: w.Key.Namespace, Kind: v1alpha1.WorkloadKind(w.Key.Kind), Name: w.Key.Name}
	st := w.Status

	var globals, clusterDefs, overrides, pols []*Candidate
	for i := range cands {
		c := &cands[i]
		switch c.Type {
		case v1alpha1.HubPolicyTypeGlobalDefault:
			globals = append(globals, c)
		case v1alpha1.HubPolicyTypeClusterDefault:
			if len(c.Clusters) > 0 && c.Clusters[0] == cluster {
				clusterDefs = append(clusterDefs, c)
			}
		case v1alpha1.HubPolicyTypePolicy, "":
			if len(c.Clusters) == 0 || contains(c.Clusters, cluster) {
				pols = append(pols, c)
			}
		case v1alpha1.HubPolicyTypeWorkloadOverride:
			if c.WorkloadID == id {
				overrides = append(overrides, c)
			}
		}
	}

	var r resolution
	r.layers = append(r.layers, hubapi.PolicyLayer{Source: hubapi.LayerBuiltIn, Applied: true})
	r.applied = append(r.applied, layer{source: hubapi.LayerBuiltIn, spec: &builtIn})
	add := func(source string, c *Candidate) {
		r.layers = append(r.layers, hubLayer(source, c, true))
		r.applied = append(r.applied, layer{source: source, name: c.Name, spec: &c.Spec})
	}
	for _, c := range globals {
		add(hubapi.LayerGlobalDefault, c)
	}
	for _, c := range clusterDefs {
		add(hubapi.LayerClusterDefault, c)
	}

	// Winner among type policy and Git CRs.
	var objs []v1alpha1.RightsizingPolicy
	var matched []*Candidate
	type labelCand struct {
		c        *Candidate
		included bool
	}
	var labelled []labelCand
	for _, c := range pols {
		sel := c.Spec.Selector
		if !selectsWorkload(sel, pw) {
			continue
		}
		hasLabels := len(sel.MatchLabels) > 0
		sel.MatchLabels = nil // the hub does not know labels; judged below
		if !policy.Matches(sel, pw) {
			continue
		}
		if hasLabels {
			included := st != nil && st.Policy == c.Name
			labelled = append(labelled, labelCand{c: c, included: included})
			if !included {
				continue
			}
		}
		obj := v1alpha1.RightsizingPolicy{ObjectMeta: metav1.ObjectMeta{Name: c.Name}, Spec: c.Spec}
		obj.Spec.Selector = sel
		objs = append(objs, obj)
		matched = append(matched, c)
	}
	var win *Candidate
	if p := policy.Winner(objs, pw); p != nil {
		for i := range objs {
			if &objs[i] == p {
				win = matched[i]
			}
		}
	}
	for _, lc := range labelled {
		if lc.included || win == nil || ranksAbove(lc.c.Spec.Priority, lc.c.Name, win.Spec.Priority, win.Name) {
			r.labelWarnings = append(r.labelWarnings, labelWarning(lc.c.Name))
		}
	}
	// Policy layers: lowest rank first, so the winner comes last.
	sort.SliceStable(matched, func(i, j int) bool {
		return ranksAbove(matched[j].Spec.Priority, matched[j].Name, matched[i].Spec.Priority, matched[i].Name)
	})
	for _, c := range matched {
		if c == win {
			add(hubapi.LayerPolicy, c)
			r.winner = c.Name
			continue
		}
		l := hubLayer(hubapi.LayerPolicy, c, false)
		if c.Spec.Priority < win.Spec.Priority {
			l.Note = fmt.Sprintf("priority %d < %d of %s", c.Spec.Priority, win.Spec.Priority, win.Name)
		} else {
			l.Note = fmt.Sprintf("priority %d = %d of %s; ties go to the lowest name", c.Spec.Priority, win.Spec.Priority, win.Name)
		}
		r.layers = append(r.layers, l)
	}
	for _, c := range overrides {
		add(hubapi.LayerWorkloadOverride, c)
		r.override = c.Name
	}
	if st != nil {
		for _, l := range annotationLayers(st.Modes) {
			r.layers = append(r.layers, hubapi.PolicyLayer{Source: l.source, Name: l.name, Applied: true, Spec: l.spec})
			r.applied = append(r.applied, l)
		}
	}

	r.modes = hubapi.EffectiveModes{}
	var memBy layer
	for _, l := range r.applied {
		if l.spec.CPU != nil && l.spec.CPU.Mode != "" {
			r.modes.CPU, r.cpuBy = string(l.spec.CPU.Mode), l
		}
		if l.spec.Memory != nil && l.spec.Memory.Mode != "" {
			r.modes.Memory, memBy = string(l.spec.Memory.Mode), l
		}
	}
	r.modes.CPUSource, r.modes.MemorySource = modeSource(r.cpuBy.source), modeSource(memBy.source)
	return r
}

// annotationLayers infers the annotation layers from the modes the agent reports.
func annotationLayers(m hubapi.EffectiveModes) []layer {
	cpu, mem := m.CPUSource == sourceAnnotation, m.MemorySource == sourceAnnotation
	cpuSpec := &v1alpha1.CPUPolicy{Mode: v1alpha1.Mode(m.CPU)}
	memSpec := &v1alpha1.MemoryPolicy{Mode: v1alpha1.Mode(m.Memory)}
	if cpu && mem && m.CPU == m.Memory {
		return []layer{{source: hubapi.LayerAnnotation, name: v1alpha1.AnnotationMode,
			spec: &v1alpha1.RightsizingPolicySpec{CPU: cpuSpec, Memory: memSpec}}}
	}
	var out []layer
	if cpu {
		out = append(out, layer{source: hubapi.LayerAnnotation, name: v1alpha1.AnnotationCPUMode,
			spec: &v1alpha1.RightsizingPolicySpec{CPU: cpuSpec}})
	}
	if mem {
		out = append(out, layer{source: hubapi.LayerAnnotation, name: v1alpha1.AnnotationMemoryMode,
			spec: &v1alpha1.RightsizingPolicySpec{Memory: memSpec}})
	}
	return out
}

// modeSource maps a layer source to the ModeSource enum.
func modeSource(layerSource string) string {
	switch layerSource {
	case hubapi.LayerClusterDefaultChart, hubapi.LayerClusterDefault:
		return string(v1alpha1.ModeSourceClusterDefault)
	case hubapi.LayerPolicy, hubapi.LayerWorkloadOverride:
		return string(v1alpha1.ModeSourcePolicy)
	case hubapi.LayerAnnotation:
		return sourceAnnotation
	}
	return string(v1alpha1.ModeSourceGlobalDefault)
}

// fields overlays the applied layers field by field.
func fields(applied []layer) []hubapi.EffectiveField {
	set := map[string]hubapi.EffectiveField{}
	for _, l := range applied {
		flat := flatten(l.spec)
		if _, ok := flat["cpu.limits"]; ok {
			// The agent takes limits and limitRatio together from the layer that sets limits.
			delete(set, "cpu.limitRatio")
		}
		for p, v := range flat {
			set[p] = hubapi.EffectiveField{Path: p, Value: v, Source: l.source, Name: l.name}
		}
	}
	out := make([]hubapi.EffectiveField, 0, len(set))
	for _, f := range set {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// flatten returns the set leaf fields of a spec (without selector and priority) as JSON path →
// value. Strings are unquoted; numbers and bools are their JSON text; arrays are compact JSON.
func flatten(spec *v1alpha1.RightsizingPolicySpec) map[string]string {
	s := *spec
	s.Selector, s.Priority = v1alpha1.PolicySelector{}, 0
	b, _ := json.Marshal(s) // a spec always marshals
	out := map[string]string{}
	flattenJSON("", b, out)
	return out
}

func flattenJSON(prefix string, raw json.RawMessage, out map[string]string) {
	var obj map[string]json.RawMessage
	if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &obj) == nil {
		for k, v := range obj {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			flattenJSON(p, v, out)
		}
		return
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		out[prefix] = str
		return
	}
	out[prefix] = string(raw)
}
