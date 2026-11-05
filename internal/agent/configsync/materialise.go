package configsync

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kerf-dev/kerf/api/v1alpha1"
	"github.com/kerf-dev/kerf/api/v1alpha1/validate"
	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// Messages reported in the policy inventory.
const (
	MsgNameTaken = "name taken by an unmanaged policy"
	MsgReadOnly  = "agent is read-only: hub policies are not materialised"
)

// IsHubManaged reports whether a RightsizingPolicy was materialised from the hub config.
func IsHubManaged(p *v1alpha1.RightsizingPolicy) bool {
	return p.Labels[v1alpha1.LabelManagedBy] == v1alpha1.ManagedByHub
}

// Result is the outcome of one materialisation.
type Result struct {
	Created, Updated, Deleted []string
	// Inventory lists every RightsizingPolicy CR plus the hub policies that are not materialised.
	Inventory []hubclient.PolicyInventoryItem
}

// Materialise makes the hub-labelled RightsizingPolicy CRs equal to cfg.Policies: it creates and
// updates them (server-side apply, field manager kerf) and deletes labelled CRs
// whose name is not in the list. CRs without the label are never touched; a hub policy whose
// name is taken by one is reported invalid. Invalid hub policies are not applied (an older valid
// CR of the same name stays). With cfg nil (no config received yet) or readOnly nothing is
// written. The inventory is built in every case.
func Materialise(ctx context.Context, c client.Client, cfg *hubclient.ClusterConfig, readOnly bool, log *slog.Logger) (Result, error) {
	var res Result
	var list v1alpha1.RightsizingPolicyList
	if err := c.List(ctx, &list); err != nil {
		return res, fmt.Errorf("list RightsizingPolicies: %w", err)
	}
	byName := map[string]*v1alpha1.RightsizingPolicy{}
	for i := range list.Items {
		byName[list.Items[i].Name] = &list.Items[i]
	}
	// problems: hub policy name -> why its current config version is not in effect.
	problems := map[string]string{}
	var errs []error
	if cfg != nil {
		want := map[string]bool{}
		for i := range cfg.Policies {
			cp := &cfg.Policies[i]
			want[cp.Name] = true
			if fe := validate.HubPolicy(cp.Type, cp.Name, &cp.Spec); len(fe) > 0 {
				problems[cp.Name] = fmt.Sprintf("hub version %d is invalid and not applied: %s", cp.Version, aggregate(fe))
				continue
			}
			cur := byName[cp.Name]
			if cur != nil && !IsHubManaged(cur) {
				problems[cp.Name] = MsgNameTaken
				continue
			}
			if readOnly {
				if cur == nil || !upToDate(cur, cp) {
					problems[cp.Name] = MsgReadOnly
				}
				continue
			}
			if cur != nil && upToDate(cur, cp) {
				continue
			}
			obj, err := apply(ctx, c, cp)
			if err != nil {
				problems[cp.Name] = "not materialised: " + err.Error()
				errs = append(errs, fmt.Errorf("policy %s: %w", cp.Name, err))
				continue
			}
			if cur == nil {
				res.Created = append(res.Created, cp.Name)
			} else {
				res.Updated = append(res.Updated, cp.Name)
			}
			byName[cp.Name] = obj
		}
		if !readOnly {
			for name, p := range byName {
				if !IsHubManaged(p) || want[name] {
					continue
				}
				if err := c.Delete(ctx, p, client.Preconditions{UID: &p.UID}); err != nil && !apierrors.IsNotFound(err) {
					errs = append(errs, fmt.Errorf("delete policy %s: %w", name, err))
					continue
				}
				res.Deleted = append(res.Deleted, name)
				delete(byName, name)
			}
		}
	}
	slices.Sort(res.Created)
	slices.Sort(res.Updated)
	slices.Sort(res.Deleted)
	if log != nil && len(res.Created)+len(res.Updated)+len(res.Deleted) > 0 {
		log.InfoContext(ctx, "materialised hub policies", "configVersion", cfg.ConfigVersion,
			"created", res.Created, "updated", res.Updated, "deleted", res.Deleted)
	}
	res.Inventory = inventory(byName, cfg, problems)
	return res, errors.Join(errs...)
}

// upToDate reports whether a hub CR already holds the policy (labels, annotations, spec).
func upToDate(cur *v1alpha1.RightsizingPolicy, cp *hubclient.ConfigPolicy) bool {
	a := cur.Annotations
	if !IsHubManaged(cur) || a[v1alpha1.AnnotationHubPolicyID] != cp.ID ||
		a[v1alpha1.AnnotationHubVersion] != strconv.Itoa(cp.Version) || a[v1alpha1.AnnotationHubPolicyType] != cp.Type {
		return false
	}
	return jsonEqual(cur.Spec, cp.Spec)
}

func jsonEqual(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// apply writes one hub policy with server-side apply. Fields someone else added to the spec
// (e.g. kubectl edit) are removed afterwards with an update, so the CR equals the hub policy.
func apply(ctx context.Context, c client.Client, cp *hubclient.ConfigPolicy) (*v1alpha1.RightsizingPolicy, error) {
	desired := &v1alpha1.RightsizingPolicy{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "RightsizingPolicy"},
		ObjectMeta: metav1.ObjectMeta{
			Name:   cp.Name,
			Labels: map[string]string{v1alpha1.LabelManagedBy: v1alpha1.ManagedByHub},
			Annotations: map[string]string{
				v1alpha1.AnnotationHubPolicyID:   cp.ID,
				v1alpha1.AnnotationHubVersion:    strconv.Itoa(cp.Version),
				v1alpha1.AnnotationHubPolicyType: cp.Type,
			},
		},
		Spec: *cp.Spec.DeepCopy(),
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(desired)
	if err != nil {
		return nil, err
	}
	delete(m, "status")
	unstructured.RemoveNestedField(m, "metadata", "creationTimestamp")
	u := &unstructured.Unstructured{Object: m}
	if err := c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(v1alpha1.FieldManager), client.ForceOwnership); err != nil {
		return nil, err
	}
	// The response of the apply (not the cache, which may lag) is the CR as stored.
	var got v1alpha1.RightsizingPolicy
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &got); err != nil {
		return nil, err
	}
	if !jsonEqual(got.Spec, desired.Spec) {
		got.Spec = desired.Spec
		if err := c.Update(ctx, &got, client.FieldOwner(v1alpha1.FieldManager)); err != nil {
			return nil, err
		}
	}
	return &got, nil
}

func aggregate(fe field.ErrorList) string {
	s := fe.ToAggregate().Error()
	if len(s) > 900 {
		s = s[:897] + "..."
	}
	return s
}

// inventory lists every CR (hub and Git) and the hub policies without a CR, sorted by name.
func inventory(byName map[string]*v1alpha1.RightsizingPolicy, cfg *hubclient.ClusterConfig, problems map[string]string) []hubclient.PolicyInventoryItem {
	out := []hubclient.PolicyInventoryItem{}
	for name, p := range byName {
		if p.DeletionTimestamp != nil {
			continue
		}
		spec := p.Spec.DeepCopy()
		it := hubclient.PolicyInventoryItem{Name: name, Source: hubclient.PolicySourceGit, Priority: p.Spec.Priority,
			Generation: p.Generation, Valid: true, Spec: spec}
		if IsHubManaged(p) {
			it.Source = hubclient.PolicySourceHub
			it.HubPolicyID = p.Annotations[v1alpha1.AnnotationHubPolicyID]
			it.HubVersion, _ = strconv.Atoi(p.Annotations[v1alpha1.AnnotationHubVersion])
			it.HubPolicyType = p.Annotations[v1alpha1.AnnotationHubPolicyType]
			if fe := validate.HubPolicy(it.HubPolicyType, name, spec); len(fe) > 0 {
				it.Valid, it.Message = false, aggregate(fe)
			}
			if msg, ok := problems[name]; ok && msg != MsgNameTaken {
				it.Valid, it.Message = false, msg
			}
		} else if fe := validate.Spec(spec, field.NewPath("spec")); len(fe) > 0 {
			// Informational: the agent keeps treating Git policies as before.
			it.Valid, it.Message = false, aggregate(fe)
		}
		out = append(out, it)
	}
	if cfg != nil {
		for i := range cfg.Policies {
			cp := &cfg.Policies[i]
			msg, bad := problems[cp.Name]
			if !bad {
				continue
			}
			if p, ok := byName[cp.Name]; ok && IsHubManaged(p) {
				continue // reported above, on the CR that stays in effect
			}
			spec := cp.Spec.DeepCopy()
			out = append(out, hubclient.PolicyInventoryItem{Name: cp.Name, Source: hubclient.PolicySourceHub,
				Priority: cp.Priority, Valid: false, Message: msg, HubPolicyID: cp.ID, HubVersion: cp.Version,
				HubPolicyType: cp.Type, Spec: spec})
		}
	}
	// A Git CR and a hub policy can share a name (collision): then the source decides.
	slices.SortFunc(out, func(a, b hubclient.PolicyInventoryItem) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Source, b.Source))
	})
	return out
}
