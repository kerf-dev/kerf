package selfcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	authv1 "k8s.io/api/authorization/v1"

	rd "github.com/tomer1983/resource-optimizer/api/v1alpha1/readiness"
	"github.com/tomer1983/resource-optimizer/internal/agent/hubclient"
)

// Permission is one verb on one resource the agent needs, cluster-wide.
type Permission struct {
	Group       string
	Resource    string
	Subresource string
	Verb        string
}

func (p Permission) String() string {
	r := p.Resource
	if p.Subresource != "" {
		r += "/" + p.Subresource
	}
	if p.Group != "" {
		r = p.Group + "/" + r
	}
	return p.Verb + " " + r
}

const crdGroup = "resource-optimizer.oz.io"

func perms(group string, resources []string, verbs ...string) []Permission {
	var out []Permission
	for _, r := range resources {
		res, sub, _ := strings.Cut(r, "/")
		for _, v := range verbs {
			out = append(out, Permission{Group: group, Resource: res, Subresource: sub, Verb: v})
		}
	}
	return out
}

func join(ps ...[]Permission) []Permission {
	var out []Permission
	for _, p := range ps {
		out = append(out, p...)
	}
	return out
}

// Capabilities maps each capability to the permissions it needs; it mirrors the agent chart's
// ClusterRole (deploy/charts/agent/templates/rbac.yaml).
var Capabilities = map[rd.Capability][]Permission{
	rd.CapabilityRead: join(
		perms("", []string{"pods", "nodes"}, "get", "list", "watch"),
		perms("apps", []string{"deployments", "statefulsets", "daemonsets"}, "get", "list", "watch"),
		perms("apps", []string{"replicasets"}, "get", "list"),
		perms("batch", []string{"cronjobs"}, "get", "list", "watch"),
		perms("batch", []string{"jobs"}, "get", "list"),
		perms("autoscaling", []string{"horizontalpodautoscalers"}, "get", "list", "watch"),
		perms("policy", []string{"poddisruptionbudgets"}, "get", "list", "watch"),
		perms(crdGroup, []string{"rightsizingpolicies"}, "get", "list", "watch"),
		perms(crdGroup, []string{"rightsizingrecommendations"}, "get", "list", "watch", "create", "update", "patch", "delete"),
		perms(crdGroup, []string{"rightsizingrecommendations/status"}, "update", "patch"),
	),
	rd.CapabilityResize:   perms("", []string{"pods/resize", "pods"}, "patch"),
	rd.CapabilityRollout:  join(perms("apps", []string{"deployments", "statefulsets", "daemonsets"}, "patch"), perms("batch", []string{"cronjobs"}, "patch")),
	rd.CapabilityHPA:      perms("autoscaling", []string{"horizontalpodautoscalers"}, "patch"),
	rd.CapabilityEvict:    perms("", []string{"pods/eviction"}, "create"),
	rd.CapabilityPolicies: perms(crdGroup, []string{"rightsizingpolicies"}, "create", "update", "delete"),
	rd.CapabilityEvents:   join(perms("", []string{"events"}, "create", "patch"), perms("events.k8s.io", []string{"events"}, "create", "patch")),
}

// Rule is a ClusterRole rule (the JSON of AgentCapability.missing).
type Rule struct {
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

// rules groups denied permissions into ClusterRole rules (one per group and resource).
func rules(denied []Permission) []string {
	type key struct{ group, resource string }
	verbs := map[key][]string{}
	var order []key
	for _, p := range denied {
		r := p.Resource
		if p.Subresource != "" {
			r += "/" + p.Subresource
		}
		k := key{p.Group, r}
		if _, ok := verbs[k]; !ok {
			order = append(order, k)
		}
		verbs[k] = append(verbs[k], p.Verb)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		b, _ := json.Marshal(Rule{APIGroups: []string{k.group}, Resources: []string{k.resource}, Verbs: verbs[k]})
		out = append(out, string(b))
	}
	return out
}

// checkRBAC runs a SelfSubjectAccessReview for every permission of every capability.
func (c *Checker) checkRBAC(ctx context.Context, r *result) {
	allowed := map[Permission]bool{}
	for _, capab := range rd.Capabilities {
		var denied []Permission
		var errs []string
		for _, p := range Capabilities[capab] {
			ok, seen := allowed[p]
			if !seen {
				var err error
				ok, err = c.accessAllowed(ctx, p)
				if err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", p, err))
					continue
				}
				allowed[p] = ok
			}
			if !ok {
				denied = append(denied, p)
			}
		}
		id := "rbac." + string(capab)
		ac := hubclient.AgentCapability{Name: string(capab), Granted: len(denied) == 0 && len(errs) == 0, Missing: rules(denied)}
		switch {
		case len(errs) > 0:
			ac.Message = "SelfSubjectAccessReview failed: " + strings.Join(errs, "; ")
			r.add(id, rd.StatusUnknown, ac.Message, "", "", nil)
		case len(denied) > 0:
			names := make([]string, len(denied))
			for i, p := range denied {
				names[i] = p.String()
			}
			ac.Message = "SelfSubjectAccessReview denied " + strings.Join(names, ", ")
			msg := ac.Message
			if c.Cfg.ReadOnly && capab != rd.CapabilityRead {
				msg += " (the agent is installed with readOnly: true)"
			}
			r.add(id, rd.StatusFail, msg, rbacRemediation(ac.Missing, c.Cfg.ReadOnly && capab != rd.CapabilityRead), remYAMLOrText(c.Cfg.ReadOnly && capab != rd.CapabilityRead),
				map[string]string{"missing": strings.Join(ac.Missing, "\n")})
		default:
			ac.Message = fmt.Sprintf("%d permissions granted", len(Capabilities[capab]))
			r.add(id, rd.StatusPass, ac.Message, "", "", nil)
		}
		r.caps = append(r.caps, ac)
	}
}

func rbacRemediation(missing []string, readOnly bool) string {
	if readOnly {
		return "Upgrade the agent with readOnly: false (helm upgrade agent … --set readOnly=false) to allow automation."
	}
	var b strings.Builder
	b.WriteString("# add to the agent's ClusterRole (or upgrade the agent chart):\nrules:\n")
	for _, m := range missing {
		var r Rule
		_ = json.Unmarshal([]byte(m), &r)
		fmt.Fprintf(&b, "  - apiGroups: [%q]\n    resources: [%q]\n    verbs: [%s]\n", r.APIGroups[0], r.Resources[0], quoteList(r.Verbs))
	}
	return b.String()
}

func remYAMLOrText(text bool) string {
	if text {
		return RemediationCommand
	}
	return RemediationYAML
}

func quoteList(v []string) string {
	q := make([]string, len(v))
	for i, s := range v {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

func (c *Checker) accessAllowed(ctx context.Context, p Permission) (bool, error) {
	ssar := &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{
		ResourceAttributes: &authv1.ResourceAttributes{Group: p.Group, Resource: p.Resource, Subresource: p.Subresource, Verb: p.Verb},
	}}
	if err := c.Client.Create(ctx, ssar); err != nil {
		return false, err
	}
	return ssar.Status.Allowed, nil
}
