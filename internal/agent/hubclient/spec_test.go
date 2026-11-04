package hubclient

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
)

type openAPISchema struct {
	Required   []string                 `json:"required"`
	Properties map[string]openAPISchema `json:"properties"`
}

type openAPIDoc struct {
	Paths      map[string]map[string]any `json:"paths"`
	Components struct {
		Schemas map[string]openAPISchema `json:"schemas"`
	} `json:"components"`
}

func loadSpec(t *testing.T) openAPIDoc {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "api", "openapi", "hub.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	return doc
}

func jsonFields(t reflect.Type) map[string]bool {
	out := map[string]bool{} // name -> omitempty
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		parts := strings.Split(tag, ",")
		out[parts[0]] = slices.Contains(parts[1:], "omitempty")
	}
	return out
}

// TestTypesMatchSpec keeps the hand-written client types in lockstep with api/openapi/hub.yaml.
func TestTypesMatchSpec(t *testing.T) {
	doc := loadSpec(t)
	for name, v := range schemaTypes {
		t.Run(name, func(t *testing.T) {
			s, ok := doc.Components.Schemas[name]
			if !ok {
				t.Fatalf("schema %s not in spec", name)
			}
			fields := jsonFields(reflect.TypeOf(v))
			var specProps, goProps []string
			for p := range s.Properties {
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
			for _, r := range s.Required {
				if omit, ok := fields[r]; ok && omit {
					t.Errorf("required property %q must not be omitempty", r)
				}
			}
		})
	}
}

// TestAgentPathsInSpec checks that the paths the client calls exist with the expected methods.
func TestAgentPathsInSpec(t *testing.T) {
	doc := loadSpec(t)
	want := map[string]string{
		"/v1/clusters/{cluster}/recommendations": "get",
		"/v1/clusters/{cluster}/workload-status": "post",
		"/v1/clusters/{cluster}/events":          "post",
		"/v1/clusters/{cluster}/config":          "get",
	}
	c := &Client{cluster: "{cluster}"}
	for p, m := range want {
		if _, ok := doc.Paths[p][m]; !ok {
			t.Errorf("spec has no %s %s", m, p)
		}
	}
	for _, suffix := range []string{"recommendations", "workload-status", "events", "config"} {
		got := strings.ReplaceAll(c.path(suffix), "%7B", "{")
		got = strings.ReplaceAll(got, "%7D", "}")
		if _, ok := want[got]; !ok {
			t.Errorf("client path %s is not an agent path of the spec", got)
		}
	}
}
