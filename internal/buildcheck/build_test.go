package buildcheck

import (
	"bufio"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const root = "../.."

// copiedPaths returns the sources of the COPY instructions of the first (build) stage.
func copiedPaths(t *testing.T, dockerfile string) []string {
	t.Helper()
	f, err := os.Open(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	stages := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FROM":
			stages++
		case "COPY":
			if stages != 1 {
				continue
			}
			args := fields[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "--") {
				args = args[1:]
			}
			if len(args) >= 2 {
				for _, src := range args[:len(args)-1] {
					out = append(out, strings.TrimSuffix(strings.TrimPrefix(src, "./"), "/"))
				}
			}
		}
	}
	return out
}

// TestDockerfilesCopyDependencies: each cmd/<x>/Dockerfile copies every module package that
// ./cmd/<x> imports (directly or not), so `docker build` does not fail with "package not found".
func TestDockerfilesCopyDependencies(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	dockerfiles, err := filepath.Glob(filepath.Join(root, "cmd", "*", "Dockerfile"))
	if err != nil || len(dockerfiles) == 0 {
		t.Fatalf("no Dockerfiles: %v", err)
	}
	for _, df := range dockerfiles {
		cmdDir := filepath.Base(filepath.Dir(df))
		t.Run(cmdDir, func(t *testing.T) {
			copied := copiedPaths(t, df)
			for _, want := range []string{"go.mod", "go.sum"} {
				if !contains(copied, want) {
					t.Errorf("%s does not copy %s", df, want)
				}
			}
			cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.Dir}}{{end}}", "./cmd/"+cmdDir)
			cmd.Dir = abs
			b, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list: %v", err)
			}
			for _, dir := range strings.Fields(string(b)) {
				rel, err := filepath.Rel(abs, dir)
				if err != nil || strings.HasPrefix(rel, "..") {
					continue // module cache
				}
				rel = filepath.ToSlash(rel)
				if !coveredBy(rel, copied) {
					t.Errorf("%s: ./cmd/%s imports %s, which the build stage does not COPY", df, cmdDir, rel)
				}
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func coveredBy(dir string, copied []string) bool {
	for _, c := range copied {
		if c == "." || dir == c || strings.HasPrefix(dir, c+"/") {
			return true
		}
	}
	return false
}

// TestPricingDevHubURL: the dev pricing values reach the hub through the Service `make deploy-hub`
// installs on ro-hub (release name and namespace from make/hub.mk), not through the spokes' alias.
func TestPricingDevHubURL(t *testing.T) {
	mk, err := os.ReadFile(filepath.Join(root, "make", "hub.mk"))
	if err != nil {
		t.Fatal(err)
	}
	ns := regexp.MustCompile(`(?m)^HUB_NAMESPACE\s*\?=\s*(\S+)`).FindSubmatch(mk)
	rel := regexp.MustCompile(`upgrade --install (\S+) deploy/charts/hub`).FindSubmatch(mk)
	if ns == nil || rel == nil {
		t.Fatal("make/hub.mk: HUB_NAMESPACE or the hub release name not found")
	}
	b, err := os.ReadFile(filepath.Join(root, "deploy", "charts", "pricing", "values-dev.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Hub struct {
			URL string `json:"url"`
		} `json:"hub"`
	}
	if err := yaml.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Hub.URL == "" {
		t.Skip("pricing dev values do not use the hub")
	}
	u, err := url.Parse(v.Hub.URL)
	if err != nil {
		t.Fatal(err)
	}
	// the hub chart names its Service after the release (hub.fullname)
	want := string(rel[1]) + "." + string(ns[1]) + ".svc"
	if host := u.Hostname(); host != want && host != want+".cluster.local" {
		t.Errorf("deploy/charts/pricing/values-dev.yaml hub.url host %q, want %q (release %s in namespace %s on ro-hub)", host, want, rel[1], ns[1])
	}
}
