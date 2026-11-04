package v1alpha1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestTargetRefIndexValue(t *testing.T) {
	tests := []struct {
		kind WorkloadKind
		name string
		want string
	}{
		{KindDeployment, "web", "Deployment/web"},
		{KindCronJob, "nightly", "CronJob/nightly"},
	}
	for _, tt := range tests {
		if got := TargetRefIndexValue(tt.kind, tt.name); got != tt.want {
			t.Errorf("TargetRefIndexValue(%q, %q) = %q, want %q", tt.kind, tt.name, got, tt.want)
		}
	}
}

func TestRecommendationName(t *testing.T) {
	tests := []struct {
		kind  WorkloadKind
		name  string
		taken bool
		want  string
	}{
		{KindDeployment, "web", false, "web"},
		{KindStatefulSet, "web", true, "web-statefulset"},
	}
	for _, tt := range tests {
		if got := RecommendationName(tt.kind, tt.name, tt.taken); got != tt.want {
			t.Errorf("RecommendationName(%q, %q, %v) = %q, want %q", tt.kind, tt.name, tt.taken, got, tt.want)
		}
	}
}

// TestReasonsDocumented keeps the Reason* constants and docs/contracts/events.md in sync.
func TestReasonsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/contracts/events.md")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "wellknown.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		vs, ok := node.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || !strings.HasPrefix(vs.Names[0].Name, "Reason") || len(vs.Values) != 1 {
			return true
		}
		lit, ok := vs.Values[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		v, _ := strconv.Unquote(lit.Value)
		n++
		if !strings.Contains(string(doc), "`"+v+"`") {
			t.Errorf("reason %s (%q) is not documented in docs/contracts/events.md", vs.Names[0].Name, v)
		}
		return true
	})
	if n == 0 {
		t.Fatal("no Reason* constants found")
	}
}

func TestAddToScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"RightsizingPolicy", "RightsizingPolicyList", "RightsizingRecommendation", "RightsizingRecommendationList"} {
		if !s.Recognizes(GroupVersion.WithKind(kind)) {
			t.Errorf("scheme does not recognize %s", kind)
		}
	}
}
