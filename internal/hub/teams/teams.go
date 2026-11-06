// Package teams resolves which team owns a workload and holds the caller's scope: the teams a
// user may see and act on (docs/teams.md). It has no database or HTTP code; the store applies the
// scope to every read, the hub's authentication puts it in the request context.
package teams

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// Unassigned is the team id of workloads that belong to no team. No team may use it.
const Unassigned = "unassigned"

// DefaultOwnerLabel is the owner label key when the hub config sets none.
const DefaultOwnerLabel = "team"

// IDRE is the form of a team id: a DNS label.
var IDRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidID reports whether id can name a team.
func ValidID(id string) bool { return IDRE.MatchString(id) && id != Unassigned }

// Def is a team's definition: what makes a workload belong to it.
type Def struct {
	ID string
	// Namespaces are glob patterns of namespace names (path.Match), NamespaceLabels namespace labels
	// that must all match; Clusters (globs, empty = all) restricts both. A team with neither
	// Namespaces nor NamespaceLabels has no namespace selector.
	Namespaces      []string
	NamespaceLabels map[string]string
	Clusters        []string
	// OwnerValues are values of the owner label that mean this team, besides its id.
	OwnerValues []string
}

// Workload is what resolution looks at.
type Workload struct {
	Cluster, Namespace string
	Labels             map[string]string
	NamespaceLabels    map[string]string
}

// Resolver maps workloads to teams.
type Resolver struct {
	ownerLabel string
	defs       []Def // sorted by id
	byOwner    map[string]string
}

// NewResolver returns a resolver for defs with the owner label key (DefaultOwnerLabel when empty).
func NewResolver(defs []Def, ownerLabel string) *Resolver {
	if ownerLabel == "" {
		ownerLabel = DefaultOwnerLabel
	}
	r := &Resolver{ownerLabel: ownerLabel, defs: slices.Clone(defs), byOwner: map[string]string{}}
	slices.SortFunc(r.defs, func(a, b Def) int { return strings.Compare(a.ID, b.ID) })
	// A team's own id wins over another team's owner value; otherwise the first team by id.
	for _, d := range r.defs {
		r.byOwner[d.ID] = d.ID
	}
	for _, d := range r.defs {
		for _, v := range d.OwnerValues {
			if _, ok := r.byOwner[v]; !ok && v != "" {
				r.byOwner[v] = d.ID
			}
		}
	}
	return r
}

// OwnerLabel returns the owner label key.
func (r *Resolver) OwnerLabel() string { return r.ownerLabel }

// Resolve returns the team of a workload, or "" (Unassigned). The owner label on the workload wins,
// then the owner label on its namespace, then the first team (by id) whose namespace selector
// matches. An owner label value that names no team falls through to the next rule.
func (r *Resolver) Resolve(w Workload) string {
	if t, ok := r.byOwner[w.Labels[r.ownerLabel]]; ok {
		return t
	}
	if t, ok := r.byOwner[w.NamespaceLabels[r.ownerLabel]]; ok {
		return t
	}
	for _, d := range r.defs {
		if d.selects(w) {
			return d.ID
		}
	}
	return ""
}

func (d Def) selects(w Workload) bool {
	if len(d.Namespaces) == 0 && len(d.NamespaceLabels) == 0 {
		return false
	}
	if len(d.Clusters) > 0 && !globAny(d.Clusters, w.Cluster) {
		return false
	}
	if len(d.Namespaces) > 0 && !globAny(d.Namespaces, w.Namespace) {
		return false
	}
	for k, v := range d.NamespaceLabels {
		if got, ok := w.NamespaceLabels[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func globAny(patterns []string, s string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, s); err == nil && ok {
			return true
		}
	}
	return false
}

// ValidGlob reports whether p is a valid pattern.
func ValidGlob(p string) bool {
	_, err := path.Match(p, "")
	return p != "" && err == nil
}
