package capacity

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/kerf-dev/kerf/internal/agent/hubclient"
)

// CAStatus is what the report needs of the Cluster Autoscaler status ConfigMap
// (--write-status-configmap, data key "status").
type CAStatus struct {
	Object        hubclient.CapacityObjectRef
	Health        string
	ScaleDown     string
	LastProbeTime time.Time
	NodeGroups    []CANodeGroup
}

// CANodeGroup is one node group of the status.
type CANodeGroup struct {
	Name      string
	Health    string
	MinSize   *int
	MaxSize   *int
	Target    *int
	ScaleDown string
}

// caYAML is the structured status of Cluster Autoscaler 1.30 and later.
type caYAML struct {
	AutoscalerStatus string `json:"autoscalerStatus"`
	ClusterWide      struct {
		Health struct {
			Status        string `json:"status"`
			LastProbeTime string `json:"lastProbeTime"`
		} `json:"health"`
		ScaleDown struct {
			Status string `json:"status"`
		} `json:"scaleDown"`
	} `json:"clusterWide"`
	NodeGroups []struct {
		Name   string `json:"name"`
		Health struct {
			Status              string `json:"status"`
			CloudProviderTarget *int   `json:"cloudProviderTarget"`
			MinSize             *int   `json:"minSize"`
			MaxSize             *int   `json:"maxSize"`
		} `json:"health"`
		ScaleDown struct {
			Status string `json:"status"`
		} `json:"scaleDown"`
	} `json:"nodeGroups"`
}

// ParseCAStatus parses the "status" value of the Cluster Autoscaler status ConfigMap: the YAML
// format (1.30+) or the older text format. ok is false when neither matches.
func ParseCAStatus(s string) (CAStatus, bool) {
	var y caYAML
	if err := yaml.Unmarshal([]byte(s), &y); err == nil && (y.AutoscalerStatus != "" || y.ClusterWide.Health.Status != "" || len(y.NodeGroups) > 0) {
		st := CAStatus{Health: y.ClusterWide.Health.Status, ScaleDown: y.ClusterWide.ScaleDown.Status}
		if st.Health == "" {
			st.Health = y.AutoscalerStatus
		}
		st.LastProbeTime = parseTime(y.ClusterWide.Health.LastProbeTime)
		for _, g := range y.NodeGroups {
			st.NodeGroups = append(st.NodeGroups, CANodeGroup{
				Name: g.Name, Health: g.Health.Status, MinSize: g.Health.MinSize, MaxSize: g.Health.MaxSize,
				Target: g.Health.CloudProviderTarget, ScaleDown: g.ScaleDown.Status,
			})
		}
		return st, true
	}
	return parseCAText(s)
}

var (
	caTextHealth    = regexp.MustCompile(`^\s*Health:\s+(\w+)`)
	caTextScaleDown = regexp.MustCompile(`^\s*ScaleDown:\s+(\w+)`)
	caTextName      = regexp.MustCompile(`^\s*Name:\s+(\S+)`)
	caTextProbe     = regexp.MustCompile(`^\s*LastProbeTime:\s+(.+?)\s*$`)
	caTextNum       = func(k string) *regexp.Regexp { return regexp.MustCompile(k + `=(\d+)`) }
	caTextMin       = caTextNum("minSize")
	caTextMax       = caTextNum("maxSize")
	caTextTarget    = caTextNum("cloudProviderTarget")
)

// parseCAText parses the text format:
//
//	Cluster-wide:
//	  Health:      Healthy (ready=3 ...)
//	               LastProbeTime:      2024-01-02 03:04:05.6 +0000 UTC
//	  ScaleDown:   NoCandidates (candidates=0)
//	NodeGroups:
//	  Name:        ng-1
//	  Health:      Healthy (ready=3 ... cloudProviderTarget=3 (minSize=1, maxSize=10))
//	  ScaleDown:   NoCandidates (candidates=0)
func parseCAText(s string) (CAStatus, bool) {
	var st CAStatus
	section := ""
	var cur *CANodeGroup
	found := false
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Cluster-wide:"):
			section, found = "cluster", true
			continue
		case strings.HasPrefix(t, "NodeGroups:"):
			section, found = "groups", true
			continue
		}
		switch section {
		case "cluster":
			if m := caTextHealth.FindStringSubmatch(line); m != nil {
				st.Health = m[1]
			} else if m := caTextScaleDown.FindStringSubmatch(line); m != nil {
				st.ScaleDown = m[1]
			} else if m := caTextProbe.FindStringSubmatch(line); m != nil && st.LastProbeTime.IsZero() {
				st.LastProbeTime = parseTime(m[1])
			}
		case "groups":
			if m := caTextName.FindStringSubmatch(line); m != nil {
				st.NodeGroups = append(st.NodeGroups, CANodeGroup{Name: m[1]})
				cur = &st.NodeGroups[len(st.NodeGroups)-1]
				continue
			}
			if cur == nil {
				continue
			}
			if m := caTextHealth.FindStringSubmatch(line); m != nil {
				cur.Health = m[1]
				cur.MinSize, cur.MaxSize, cur.Target = intMatch(caTextMin, line), intMatch(caTextMax, line), intMatch(caTextTarget, line)
			} else if m := caTextScaleDown.FindStringSubmatch(line); m != nil {
				cur.ScaleDown = m[1]
			}
		}
	}
	return st, found
}

func intMatch(re *regexp.Regexp, s string) *int {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return nil
	}
	return &n
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " m="); i > 0 { // Go's monotonic clock reading
		s = s[:i]
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
