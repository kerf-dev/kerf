package admin

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/tomer1983/resource-optimizer/api/v1alpha1"
	"github.com/tomer1983/resource-optimizer/api/v1alpha1/validate"
	"github.com/tomer1983/resource-optimizer/internal/hub/hubapi"
)

// ErrUnknownPolicy is returned (wrapped, with the id) for a change that names an unknown policyId.
var ErrUnknownPolicy = errors.New("unknown policy")

// InvalidError is an invalid change of the preview.
type InvalidError struct {
	// Index is the position of the change in the request.
	Index int
	// Code is the problem code: hubapi.CodeInvalidPolicy, CodeInvalidOverride or CodeTypeImmutable.
	Code string
	Errs field.ErrorList
}

func (e *InvalidError) Error() string {
	msgs := make([]string, len(e.Errs))
	for i, err := range e.Errs {
		msgs[i] = err.Error()
	}
	return fmt.Sprintf("change %d: %s", e.Index, strings.Join(msgs, "; "))
}

// ApplyChanges returns cands with the preview's hypothetical changes applied (pure; does not modify cands).
func ApplyChanges(cands []Candidate, changes []hubapi.PolicyChange) ([]Candidate, error) {
	out := append([]Candidate(nil), cands...)
	for i, ch := range changes {
		var err error
		switch ch.Op {
		case hubapi.PreviewDelete:
			out, err = deleteCandidate(out, ch.PolicyID)
		case hubapi.PreviewUpsert:
			out, err = upsert(out, i, ch)
		default:
			err = &InvalidError{Index: i, Code: hubapi.CodeInvalidPolicy, Errs: field.ErrorList{
				field.NotSupported(field.NewPath("op"), ch.Op, []string{hubapi.PreviewUpsert, hubapi.PreviewDelete}),
			}}
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func indexOf(cands []Candidate, policyID string) int {
	if policyID == "" {
		return -1
	}
	for i := range cands {
		if cands[i].PolicyID == policyID {
			return i
		}
	}
	return -1
}

func deleteCandidate(cands []Candidate, policyID string) ([]Candidate, error) {
	i := indexOf(cands, policyID)
	if i < 0 {
		return nil, fmt.Errorf("%w: %q", ErrUnknownPolicy, policyID)
	}
	return append(cands[:i:i], cands[i+1:]...), nil
}

func upsert(cands []Candidate, idx int, ch hubapi.PolicyChange) ([]Candidate, error) {
	invalid := func(code string, errs ...*field.Error) error {
		return &InvalidError{Index: idx, Code: code, Errs: errs}
	}
	d := ch.Policy
	if d == nil {
		return nil, invalid(hubapi.CodeInvalidPolicy, field.Required(field.NewPath("policy"), "upsert needs a policy"))
	}
	c, err := normalise(d, invalid)
	if err != nil {
		return nil, err
	}
	if errs := validate.HubPolicy(c.Type, c.Name, &c.Spec); len(errs) > 0 {
		return nil, &InvalidError{Index: idx, Code: hubapi.CodeInvalidPolicy, Errs: errs}
	}

	var pos int
	if ch.PolicyID != "" {
		if pos = indexOf(cands, ch.PolicyID); pos < 0 {
			return nil, fmt.Errorf("%w: %q", ErrUnknownPolicy, ch.PolicyID)
		}
		if old := cands[pos].Type; old != c.Type {
			return nil, invalid(hubapi.CodeTypeImmutable, field.Forbidden(field.NewPath("policy", "type"),
				fmt.Sprintf("type is immutable (%s)", old)))
		}
	} else {
		pos = sameIdentity(cands, &c)
	}
	out := append([]Candidate(nil), cands...)
	if pos < 0 {
		return append(out, c), nil
	}
	c.PolicyID = out[pos].PolicyID
	out[pos] = c
	return out, nil
}

// normalise turns a draft into a hub candidate the way the hub stores a policy of its type.
func normalise(d *hubapi.PolicyDraft, invalid func(string, ...*field.Error) error) (Candidate, error) {
	c := Candidate{Name: d.Name, Origin: hubapi.PolicySourceHub, Type: d.Type, Spec: *d.Spec.DeepCopy()}
	if len(d.Clusters) > 0 {
		c.Clusters = append([]string(nil), d.Clusters...)
	}
	switch d.Type {
	case v1alpha1.HubPolicyTypeGlobalDefault:
		c.Name, c.Spec.Priority, c.Clusters = v1alpha1.GlobalDefaultsPolicyName, v1alpha1.PriorityGlobalDefault, nil
	case v1alpha1.HubPolicyTypeClusterDefault:
		if len(d.Clusters) != 1 {
			return c, invalid(hubapi.CodeInvalidPolicy, field.Invalid(field.NewPath("policy", "clusters"), d.Clusters,
				"a cluster default applies to exactly one cluster"))
		}
		c.Name, c.Spec.Priority = v1alpha1.ClusterDefaultsPolicyName, v1alpha1.PriorityClusterDefault
	case v1alpha1.HubPolicyTypeWorkloadOverride:
		var errs field.ErrorList
		key, err := hubapi.ParseWorkloadID(d.WorkloadID)
		if err != nil {
			errs = append(errs, field.Invalid(field.NewPath("policy", "workloadId"), d.WorkloadID, err.Error()))
		}
		sel := d.Spec.Selector
		if len(sel.Namespaces)+len(sel.ExcludeNamespaces)+len(sel.MatchLabels)+len(sel.Kinds)+len(sel.Workloads) > 0 {
			errs = append(errs, field.Forbidden(field.NewPath("policy", "spec", "selector"), "the hub sets the selector of a workload override"))
		}
		if d.Spec.Priority != 0 {
			errs = append(errs, field.Forbidden(field.NewPath("policy", "spec", "priority"), "the hub sets the priority of a workload override"))
		}
		if len(errs) > 0 {
			return c, invalid(hubapi.CodeInvalidOverride, errs...)
		}
		kind := v1alpha1.WorkloadKind(key.Kind)
		c.Name = v1alpha1.OverridePolicyName(key.Namespace, kind, key.Name)
		c.Spec.Priority = v1alpha1.PriorityWorkloadOverride
		c.Spec.Selector = validate.OverrideSelector(key.Namespace, kind, key.Name)
		c.Clusters = []string{key.Cluster}
		c.WorkloadID = key.ID()
	}
	return c, nil
}

// sameIdentity returns the index of the hub candidate c replaces, or -1.
func sameIdentity(cands []Candidate, c *Candidate) int {
	for i := range cands {
		o := &cands[i]
		if o.Origin == hubapi.PolicySourceGit || o.Type != c.Type {
			continue
		}
		switch c.Type {
		case v1alpha1.HubPolicyTypeGlobalDefault:
			return i
		case v1alpha1.HubPolicyTypeClusterDefault:
			if len(o.Clusters) > 0 && o.Clusters[0] == c.Clusters[0] {
				return i
			}
		case v1alpha1.HubPolicyTypeWorkloadOverride:
			if o.WorkloadID == c.WorkloadID {
				return i
			}
		default:
			if o.Name == c.Name {
				return i
			}
		}
	}
	return -1
}
