// Package umbrella checks the Kerf umbrella chart's wiring to its component charts
// (WO-18d). Rendering checks are in `make charts-lint` (charts-lint-umbrella).
package umbrella

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var components = []string{"hub", "web", "pricing", "agent", "webhook"}

// TestTemplatesGated fails when a component template is not wrapped in its chart's enable gate:
// such a file would be rendered in every mode of the umbrella chart (e.g. a hub object on a spoke).
func TestTemplatesGated(t *testing.T) {
	for _, c := range components {
		files, _ := filepath.Glob(filepath.Join("..", c, "templates", "*.yaml"))
		if len(files) == 0 {
			t.Fatalf("%s: no templates", c)
		}
		head := `{{- if include "` + c + `.c.enabled" . -}}` + "\n"
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			if !strings.HasPrefix(s, head) || !strings.HasSuffix(s, "{{ end -}}\n") {
				t.Errorf("%s: wrap the file in %q ... %q (WO-18d gate)", f, strings.TrimSpace(head), "{{ end -}}")
			}
		}
	}
}

// TestSubchartLinks fails when charts/<component> is not a link to the component chart.
func TestSubchartLinks(t *testing.T) {
	for _, c := range components {
		target, err := os.Readlink(filepath.Join("charts", c))
		if err != nil || target != filepath.Join("..", "..", c) {
			t.Errorf("charts/%s must be a symlink to ../../%s (got %q, %v)", c, c, target, err)
		}
	}
}

// TestSchemaIsJSON fails on a malformed values.schema.json (Helm would reject every install).
func TestSchemaIsJSON(t *testing.T) {
	b, err := os.ReadFile("values.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("values.schema.json: %v", err)
	}
}
