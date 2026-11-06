// Package validate checks RightsizingPolicy specs. The hub (every policy write) and the agent
// (every hub policy before it is materialised, and the policy inventory it reports) use it, so a
// policy is accepted or refused the same way everywhere (docs/contracts/admin.md).
//
// Spec enforces exactly the rules of the CRD schema (OpenAPI and CEL markers in api/v1alpha1),
// plus the checks the agent makes when it evaluates a policy and that the CRD cannot express:
// cron schedules and time zones of windows parse, schedules have a positive duration, one-off
// windows end after they start, durations and quantities are not negative, namespace globs are
// valid path.Match patterns and matchLabels are valid label keys and values. HubPolicy adds the
// rules for hub-managed policies (types, reserved names, priority bands).
//
// The functions return field.ErrorList, so messages carry the field path ("spec.cpu.limitRatio").
package validate

import (
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/kerf-dev/kerf/api/v1alpha1"
)

// Limits from the CRD markers.
const (
	MaxNamespaces    = 64
	MaxNameLength    = 253
	MaxKinds         = 4
	MaxWorkloads     = 100
	MaxWindows       = 16
	MaxScheduleLen   = 128
	MaxTimeZoneLen   = 64
	MaxNamespaceLen  = 63
	MinChangePercent = 1
	MaxChangePercent = 100
	MinUtilization   = 1
	MaxUtilization   = 100
)

var (
	limitRatioRE = regexp.MustCompile(`^([1-9][0-9]*)(\.[0-9]{1,3})?$`)
	ratioRE      = regexp.MustCompile(`^(0(\.[0-9]{1,3})?|1(\.0{1,3})?)$`)
	cronParser   = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
)

// Spec validates a RightsizingPolicy spec. fld is its path, e.g. field.NewPath("spec").
func Spec(s *v1alpha1.RightsizingPolicySpec, fld *field.Path) field.ErrorList {
	var errs field.ErrorList
	if s == nil {
		return errs
	}
	errs = append(errs, Selector(&s.Selector, fld.Child("selector"))...)
	if s.Priority < 0 || s.Priority > v1alpha1.PriorityMax {
		errs = append(errs, field.Invalid(fld.Child("priority"), s.Priority, fmt.Sprintf("must be between 0 and %d", v1alpha1.PriorityMax)))
	}
	if s.Profile != "" && !oneOf(s.Profile, v1alpha1.ProfileDev, v1alpha1.ProfileProduction, v1alpha1.ProfileHighAvailability) {
		errs = append(errs, field.NotSupported(fld.Child("profile"), s.Profile, []string{"dev", "production", "high-availability"}))
	}
	if c := s.CPU; c != nil {
		p := fld.Child("cpu")
		errs = append(errs, mode(c.Mode, p.Child("mode"))...)
		if c.Limits != "" && !oneOf(c.Limits, v1alpha1.CPULimitKeep, v1alpha1.CPULimitRemove, v1alpha1.CPULimitRatio) {
			errs = append(errs, field.NotSupported(p.Child("limits"), c.Limits, []string{"keep", "remove", "ratio"}))
		}
		if c.LimitRatio != "" && !limitRatioRE.MatchString(c.LimitRatio) {
			errs = append(errs, field.Invalid(p.Child("limitRatio"), c.LimitRatio, "must be a decimal >= 1 with at most 3 decimals, e.g. \"2\" or \"1.5\""))
		}
		if c.Limits == v1alpha1.CPULimitRatio && c.LimitRatio == "" {
			errs = append(errs, field.Required(p.Child("limitRatio"), "limitRatio is required when limits is ratio"))
		}
		if c.LimitRatio != "" && c.Limits != v1alpha1.CPULimitRatio {
			errs = append(errs, field.Forbidden(p.Child("limitRatio"), "limitRatio is only allowed when limits is ratio"))
		}
	}
	if m := s.Memory; m != nil {
		errs = append(errs, mode(m.Mode, fld.Child("memory", "mode"))...)
	}
	if a := s.Apply; a != nil {
		p := fld.Child("apply")
		if a.Strategy != "" && !oneOf(a.Strategy, v1alpha1.StrategyInPlace, v1alpha1.StrategyRollout, v1alpha1.StrategyDeferred) {
			errs = append(errs, field.NotSupported(p.Child("strategy"), a.Strategy, []string{"in-place", "rollout", "deferred"}))
		}
		errs = append(errs, duration(a.FallbackAfter, p.Child("fallbackAfter"))...)
		errs = append(errs, Windows(a.MaintenanceWindows, p.Child("maintenanceWindows"))...)
		errs = append(errs, Windows(a.FreezeWindows, p.Child("freezeWindows"))...)
	}
	if g := s.Guardrails; g != nil {
		p := fld.Child("guardrails")
		errs = append(errs, duration(g.LearningPeriod, p.Child("learningPeriod"))...)
		errs = append(errs, ratio(g.MinConfidence, p.Child("minConfidence"))...)
		if g.MaxChangePercent != nil && (*g.MaxChangePercent < MinChangePercent || *g.MaxChangePercent > MaxChangePercent) {
			errs = append(errs, field.Invalid(p.Child("maxChangePercent"), *g.MaxChangePercent, "must be between 1 and 100"))
		}
		errs = append(errs, quantity(g.MinCPU, p.Child("minCPU"))...)
		errs = append(errs, quantity(g.MinMemory, p.Child("minMemory"))...)
		errs = append(errs, duration(g.Cooldown, p.Child("cooldown"))...)
		if rb := g.Rollback; rb != nil {
			rp := p.Child("rollback")
			errs = append(errs, duration(rb.WatchDuration, rp.Child("watchDuration"))...)
			if rb.OOMKills != nil && *rb.OOMKills < 1 {
				errs = append(errs, field.Invalid(rp.Child("oomKills"), *rb.OOMKills, "must be at least 1"))
			}
			if rb.Restarts != nil && *rb.Restarts < 1 {
				errs = append(errs, field.Invalid(rp.Child("restarts"), *rb.Restarts, "must be at least 1"))
			}
			errs = append(errs, ratio(rb.ThrottlingRatio, rp.Child("throttlingRatio"))...)
		}
	}
	if h := s.HPA; h != nil && h.TargetBounds != nil {
		p := fld.Child("hpa", "targetBounds")
		b := h.TargetBounds
		errs = append(errs, utilization(b.MinUtilization, p.Child("minUtilization"))...)
		errs = append(errs, utilization(b.MaxUtilization, p.Child("maxUtilization"))...)
		if b.MinUtilization != nil && b.MaxUtilization != nil && *b.MinUtilization > *b.MaxUtilization {
			errs = append(errs, field.Invalid(p, fmt.Sprintf("%d > %d", *b.MinUtilization, *b.MaxUtilization), "minUtilization must be <= maxUtilization"))
		}
	}
	return errs
}

// Selector validates a policy selector.
func Selector(s *v1alpha1.PolicySelector, fld *field.Path) field.ErrorList {
	var errs field.ErrorList
	errs = append(errs, globs(s.Namespaces, fld.Child("namespaces"))...)
	errs = append(errs, globs(s.ExcludeNamespaces, fld.Child("excludeNamespaces"))...)
	for k, v := range s.MatchLabels {
		p := fld.Child("matchLabels").Key(k)
		for _, msg := range validation.IsQualifiedName(k) {
			errs = append(errs, field.Invalid(p, k, msg))
		}
		for _, msg := range validation.IsValidLabelValue(v) {
			errs = append(errs, field.Invalid(p, v, msg))
		}
	}
	if len(s.Kinds) > MaxKinds {
		errs = append(errs, field.TooMany(fld.Child("kinds"), len(s.Kinds), MaxKinds))
	}
	seenKind := map[v1alpha1.WorkloadKind]bool{}
	for i, k := range s.Kinds {
		p := fld.Child("kinds").Index(i)
		if !validKind(k) {
			errs = append(errs, field.NotSupported(p, k, kindNames))
		} else if seenKind[k] {
			errs = append(errs, field.Duplicate(p, k))
		}
		seenKind[k] = true
	}
	if len(s.Workloads) > MaxWorkloads {
		errs = append(errs, field.TooMany(fld.Child("workloads"), len(s.Workloads), MaxWorkloads))
	}
	for i, w := range s.Workloads {
		errs = append(errs, WorkloadRef(w, fld.Child("workloads").Index(i))...)
	}
	return errs
}

// WorkloadRef validates one selector.workloads entry.
func WorkloadRef(w v1alpha1.NamespacedWorkloadRef, fld *field.Path) field.ErrorList {
	var errs field.ErrorList
	if w.Kind == "" {
		errs = append(errs, field.Required(fld.Child("kind"), ""))
	} else if !validKind(w.Kind) {
		errs = append(errs, field.NotSupported(fld.Child("kind"), w.Kind, kindNames))
	}
	switch {
	case w.Namespace == "":
		errs = append(errs, field.Required(fld.Child("namespace"), ""))
	case len(w.Namespace) > MaxNamespaceLen:
		errs = append(errs, field.TooLong(fld.Child("namespace"), w.Namespace, MaxNamespaceLen))
	}
	switch {
	case w.Name == "":
		errs = append(errs, field.Required(fld.Child("name"), ""))
	case len(w.Name) > MaxNameLength:
		errs = append(errs, field.TooLong(fld.Child("name"), w.Name, MaxNameLength))
	}
	return errs
}

// Windows validates maintenance or freeze windows.
func Windows(ws []v1alpha1.TimeWindow, fld *field.Path) field.ErrorList {
	var errs field.ErrorList
	if len(ws) > MaxWindows {
		errs = append(errs, field.TooMany(fld, len(ws), MaxWindows))
	}
	for i := range ws {
		errs = append(errs, Window(&ws[i], fld.Index(i))...)
	}
	return errs
}

// Window validates one time window: schedule + duration, or start + end.
func Window(w *v1alpha1.TimeWindow, fld *field.Path) field.ErrorList {
	var errs field.ErrorList
	recurring := w.Schedule != ""
	if recurring {
		if w.Duration == nil || w.Start != nil || w.End != nil {
			errs = append(errs, field.Invalid(fld, w.Schedule, "set either schedule and duration, or start and end"))
		}
		if len(w.Schedule) > MaxScheduleLen {
			errs = append(errs, field.TooLong(fld.Child("schedule"), w.Schedule, MaxScheduleLen))
		} else if _, err := cronParser.Parse(w.Schedule); err != nil {
			errs = append(errs, field.Invalid(fld.Child("schedule"), w.Schedule, "not a 5-field cron expression: "+err.Error()))
		}
		if w.Duration != nil && w.Duration.Duration <= 0 {
			errs = append(errs, field.Invalid(fld.Child("duration"), w.Duration.Duration.String(), "must be positive"))
		}
	} else {
		if w.Start == nil || w.End == nil || w.Duration != nil {
			errs = append(errs, field.Invalid(fld, "", "set either schedule and duration, or start and end"))
		} else if !w.End.After(w.Start.Time) {
			errs = append(errs, field.Invalid(fld.Child("end"), w.End.UTC().Format(time.RFC3339), "must be after start"))
		}
	}
	if w.TimeZone != "" {
		if len(w.TimeZone) > MaxTimeZoneLen {
			errs = append(errs, field.TooLong(fld.Child("timeZone"), w.TimeZone, MaxTimeZoneLen))
		} else if _, err := time.LoadLocation(w.TimeZone); err != nil {
			errs = append(errs, field.Invalid(fld.Child("timeZone"), w.TimeZone, "unknown IANA time zone"))
		}
	}
	return errs
}

// HubPolicy validates a hub-managed policy of type typ (v1alpha1.HubPolicyType*) with CR name
// name: the spec, the CR name and the type's reserved name and priority band. A workload
// override must select exactly one workload, in its namespaces and kinds.
func HubPolicy(typ, name string, s *v1alpha1.RightsizingPolicySpec) field.ErrorList {
	errs := Spec(s, field.NewPath("spec"))
	np, pp := field.NewPath("name"), field.NewPath("spec", "priority")
	for _, msg := range validation.IsDNS1123Subdomain(name) {
		errs = append(errs, field.Invalid(np, name, msg))
	}
	var prio int32
	if s != nil {
		prio = s.Priority
	}
	switch typ {
	case v1alpha1.HubPolicyTypeGlobalDefault:
		errs = append(errs, fixed(np, name, v1alpha1.GlobalDefaultsPolicyName, pp, prio, v1alpha1.PriorityGlobalDefault)...)
		errs = append(errs, noSelector(s)...)
	case v1alpha1.HubPolicyTypeClusterDefault:
		errs = append(errs, fixed(np, name, v1alpha1.ClusterDefaultsPolicyName, pp, prio, v1alpha1.PriorityClusterDefault)...)
		errs = append(errs, noSelector(s)...)
	case v1alpha1.HubPolicyTypePolicy:
		if strings.HasPrefix(name, v1alpha1.ReservedNamePrefix) {
			errs = append(errs, field.Invalid(np, name, fmt.Sprintf("names starting with %q are reserved for the hub", v1alpha1.ReservedNamePrefix)))
		}
		if prio < v1alpha1.PriorityPolicyMin || prio > v1alpha1.PriorityPolicyMax {
			errs = append(errs, field.Invalid(pp, prio, fmt.Sprintf("must be between %d and %d for hub policies", v1alpha1.PriorityPolicyMin, v1alpha1.PriorityPolicyMax)))
		}
	case v1alpha1.HubPolicyTypeWorkloadOverride:
		if !strings.HasPrefix(name, v1alpha1.OverridePolicyNamePrefix) {
			errs = append(errs, field.Invalid(np, name, "must be OverridePolicyName(namespace, kind, name)"))
		}
		if prio != v1alpha1.PriorityWorkloadOverride {
			errs = append(errs, field.Invalid(pp, prio, fmt.Sprintf("must be %d", v1alpha1.PriorityWorkloadOverride)))
		}
		errs = append(errs, overrideSelector(name, s)...)
	default:
		errs = append(errs, field.NotSupported(field.NewPath("type"), typ, []string{
			v1alpha1.HubPolicyTypeGlobalDefault, v1alpha1.HubPolicyTypeClusterDefault,
			v1alpha1.HubPolicyTypePolicy, v1alpha1.HubPolicyTypeWorkloadOverride,
		}))
	}
	return errs
}

// OverrideSelector returns the selector the hub sets on the workload override of a workload:
// its namespace, kind and the workload itself (namespaces and kinds narrow it for agents that
// do not know selector.workloads).
func OverrideSelector(namespace string, kind v1alpha1.WorkloadKind, name string) v1alpha1.PolicySelector {
	return v1alpha1.PolicySelector{
		Namespaces: []string{namespace},
		Kinds:      []v1alpha1.WorkloadKind{kind},
		Workloads:  []v1alpha1.NamespacedWorkloadRef{{Kind: kind, Namespace: namespace, Name: name}},
	}
}

func overrideSelector(name string, s *v1alpha1.RightsizingPolicySpec) field.ErrorList {
	sp := field.NewPath("spec", "selector")
	if s == nil || len(s.Selector.Workloads) != 1 {
		return field.ErrorList{field.Invalid(sp.Child("workloads"), nil, "a workload override selects exactly one workload")}
	}
	w := s.Selector.Workloads[0]
	want := OverrideSelector(w.Namespace, w.Kind, w.Name)
	var errs field.ErrorList
	if !equalStrings(s.Selector.Namespaces, want.Namespaces) || len(s.Selector.Kinds) != 1 || s.Selector.Kinds[0] != w.Kind ||
		len(s.Selector.ExcludeNamespaces) > 0 || len(s.Selector.MatchLabels) > 0 {
		errs = append(errs, field.Invalid(sp, "", "a workload override's selector must be OverrideSelector(namespace, kind, name)"))
	}
	if exp := v1alpha1.OverridePolicyName(w.Namespace, w.Kind, w.Name); name != exp {
		errs = append(errs, field.Invalid(field.NewPath("name"), name, "must be "+exp))
	}
	return errs
}

func noSelector(s *v1alpha1.RightsizingPolicySpec) field.ErrorList {
	if s == nil {
		return nil
	}
	sel := s.Selector
	if len(sel.Namespaces)+len(sel.ExcludeNamespaces)+len(sel.MatchLabels)+len(sel.Kinds)+len(sel.Workloads) > 0 {
		return field.ErrorList{field.Forbidden(field.NewPath("spec", "selector"), "defaults apply to every workload; the selector must be empty")}
	}
	return nil
}

func fixed(np *field.Path, name, wantName string, pp *field.Path, prio, wantPrio int32) field.ErrorList {
	var errs field.ErrorList
	if name != wantName {
		errs = append(errs, field.Invalid(np, name, "must be "+wantName))
	}
	if prio != wantPrio {
		errs = append(errs, field.Invalid(pp, prio, fmt.Sprintf("must be %d", wantPrio)))
	}
	return errs
}

var kindNames = []string{"Deployment", "StatefulSet", "DaemonSet", "CronJob"}

func validKind(k v1alpha1.WorkloadKind) bool {
	return oneOf(k, v1alpha1.KindDeployment, v1alpha1.KindStatefulSet, v1alpha1.KindDaemonSet, v1alpha1.KindCronJob)
}

func mode(m v1alpha1.Mode, p *field.Path) field.ErrorList {
	if m != "" && !oneOf(m, v1alpha1.ModeOff, v1alpha1.ModeAudit, v1alpha1.ModeAutoPatch) {
		return field.ErrorList{field.NotSupported(p, m, []string{"off", "audit", "auto-patch"})}
	}
	return nil
}

func ratio(v string, p *field.Path) field.ErrorList {
	if v == "" {
		return nil
	}
	if !ratioRE.MatchString(v) {
		return field.ErrorList{field.Invalid(p, v, "must be a decimal string from 0 to 1 with at most 3 decimals, e.g. \"0.7\"")}
	}
	if _, err := strconv.ParseFloat(v, 64); err != nil {
		return field.ErrorList{field.Invalid(p, v, err.Error())}
	}
	return nil
}

func duration(d *metav1.Duration, p *field.Path) field.ErrorList {
	if d != nil && d.Duration < 0 {
		return field.ErrorList{field.Invalid(p, d.Duration.String(), "must not be negative")}
	}
	return nil
}

func quantity(q *resource.Quantity, p *field.Path) field.ErrorList {
	if q != nil && q.Sign() < 0 {
		return field.ErrorList{field.Invalid(p, q.String(), "must not be negative")}
	}
	return nil
}

func utilization(v *int32, p *field.Path) field.ErrorList {
	if v != nil && (*v < MinUtilization || *v > MaxUtilization) {
		return field.ErrorList{field.Invalid(p, *v, "must be between 1 and 100")}
	}
	return nil
}

func globs(gs []string, p *field.Path) field.ErrorList {
	var errs field.ErrorList
	if len(gs) > MaxNamespaces {
		errs = append(errs, field.TooMany(p, len(gs), MaxNamespaces))
	}
	seen := map[string]bool{}
	for i, g := range gs {
		ip := p.Index(i)
		switch {
		case len(g) > MaxNameLength:
			errs = append(errs, field.TooLong(ip, g, MaxNameLength))
		case seen[g]:
			errs = append(errs, field.Duplicate(ip, g))
		default:
			if _, err := path.Match(g, ""); err != nil {
				errs = append(errs, field.Invalid(ip, g, "not a valid glob: "+err.Error()))
			}
		}
		seen[g] = true
	}
	return errs
}

func oneOf[T comparable](v T, allowed ...T) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
