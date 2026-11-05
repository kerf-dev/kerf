//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// The in-cluster git server (WO-01) holds the repo Argo CD syncs. The main Application
// (argocd/workloads) syncs only the repo root (non-recursive), so scenarios own e2e/<namespace>/.

var gitMu sync.Mutex // serialize pushes from parallel scenarios

// gitWrite sets the files of dir e2e/<ns>/ (nil files: remove the dir) and pushes to main.
func gitWrite(t *testing.T, ns string, files map[string][]byte, msg string) {
	t.Helper()
	gitMu.Lock()
	defer gitMu.Unlock()
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@kerf.local",
			"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@kerf.local")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	for attempt := 0; ; attempt++ {
		_ = os.RemoveAll(filepath.Join(work, "repo"))
		run("clone", "-q", "--depth", "1", "-b", "main", env.GitURL, "repo")
		dir := filepath.Join(work, "repo", "e2e", ns)
		_ = os.RemoveAll(dir)
		if files != nil {
			must(t, os.MkdirAll(dir, 0o755))
			for name, b := range files {
				must(t, os.WriteFile(filepath.Join(dir, name), b, 0o644))
			}
		}
		cmd := exec.Command("git", "add", "-A")
		cmd.Dir = filepath.Join(work, "repo")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git add: %v\n%s", err, out)
		}
		commit := exec.Command("git", "-c", "user.name=e2e", "-c", "user.email=e2e@kerf.local", "commit", "-q", "-m", msg)
		commit.Dir = filepath.Join(work, "repo")
		if out, err := commit.CombinedOutput(); err != nil {
			if strings.Contains(string(out), "nothing to commit") {
				return
			}
			t.Fatalf("git commit: %v\n%s", err, out)
		}
		push := exec.Command("git", "push", "-q", "origin", "main")
		push.Dir = filepath.Join(work, "repo")
		out, err := push.CombinedOutput()
		if err == nil {
			return
		}
		if attempt >= 4 {
			t.Fatalf("git push: %v\n%s", err, out)
		}
		t.Logf("git push rejected (%v), retrying", err)
		time.Sleep(2 * time.Second)
	}
}

var applicationGVK = schema.GroupVersionKind{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application"}

// argoApp creates an Application syncing e2e/<ns>/ into ns with the D-006 settings (same as the
// main `workloads` Application: ignore fields of field manager kerf, respect the
// ignored differences on sync, automated sync with self-heal). Deleted (cascading) at the end.
func argoApp(t *testing.T, cl cluster, ns string) string {
	t.Helper()
	name := ns
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(applicationGVK)
	app.SetName(name)
	app.SetNamespace(env.ArgoNamespace)
	app.SetLabels(map[string]string{suiteLabel: "true"})
	app.SetFinalizers([]string{"resources-finalizer.argocd.argoproj.io"})
	ignore := []any{}
	for _, k := range [][2]string{{"apps", "Deployment"}, {"apps", "StatefulSet"}, {"apps", "DaemonSet"}, {"batch", "CronJob"}} {
		ignore = append(ignore, map[string]any{"group": k[0], "kind": k[1], "managedFieldsManagers": []any{"kerf"}})
	}
	app.Object["spec"] = map[string]any{
		"project":     "default",
		"source":      map[string]any{"repoURL": env.GitInCluster, "targetRevision": "main", "path": "e2e/" + ns},
		"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": ns},
		"syncPolicy": map[string]any{
			"automated":   map[string]any{"prune": true, "selfHeal": true},
			"syncOptions": []any{"RespectIgnoreDifferences=true"},
		},
		"ignoreDifferences": ignore,
	}
	must(t, cl.C.Create(ctx(t), app))
	t.Cleanup(func() {
		if env.Keep {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = cl.C.Delete(c, app)
		// Wait for the cascading delete before the namespace goes away.
		for i := 0; i < 60; i++ {
			if err := cl.C.Get(c, client.ObjectKeyFromObject(app), app.DeepCopy()); err != nil {
				break
			}
			time.Sleep(2 * time.Second)
		}
		gitWrite(t, ns, nil, "e2e: remove "+ns)
	})
	return name
}

type appStatus struct {
	Sync, Health, Revision, OpPhase string
	OpFinished                      time.Time
}

func getApp(t *testing.T, cl cluster, name string) (appStatus, *unstructured.Unstructured) {
	t.Helper()
	app := &unstructured.Unstructured{}
	app.SetGroupVersionKind(applicationGVK)
	if err := cl.C.Get(ctx(t), client.ObjectKey{Namespace: env.ArgoNamespace, Name: name}, app); err != nil {
		return appStatus{Sync: "error: " + err.Error()}, nil
	}
	s := appStatus{}
	s.Sync, _, _ = unstructured.NestedString(app.Object, "status", "sync", "status")
	s.Revision, _, _ = unstructured.NestedString(app.Object, "status", "sync", "revision")
	s.Health, _, _ = unstructured.NestedString(app.Object, "status", "health", "status")
	s.OpPhase, _, _ = unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if f, ok, _ := unstructured.NestedString(app.Object, "status", "operationState", "finishedAt"); ok {
		s.OpFinished, _ = time.Parse(time.RFC3339, f)
	}
	return s, app
}

func waitAppSynced(t *testing.T, cl cluster, name string, timeout time.Duration) {
	t.Helper()
	eventually(t, timeout, 3*time.Second, "Application "+name+" Synced/Healthy", func() (bool, string) {
		s, _ := getApp(t, cl, name)
		return s.Sync == "Synced" && s.Health == "Healthy", fmt.Sprintf("%+v", s)
	})
}

// hardSync does a hard refresh and then an explicit sync, and waits until that sync finished.
func hardSync(t *testing.T, cl cluster, name string) {
	t.Helper()
	_, app := getApp(t, cl, name)
	if app == nil {
		t.Fatalf("application %s not found", name)
	}
	patch := []byte(`{"metadata":{"annotations":{"argocd.argoproj.io/refresh":"hard"}}}`)
	must(t, cl.C.Patch(ctx(t), app, client.RawPatch("application/merge-patch+json", patch)))
	eventually(t, 2*time.Minute, 2*time.Second, "hard refresh of "+name, func() (bool, string) {
		_, a := getApp(t, cl, name)
		if a == nil {
			return false, "missing"
		}
		_, pending := a.GetAnnotations()["argocd.argoproj.io/refresh"]
		return !pending, "refresh annotation still present"
	})
	started := time.Now().Add(-time.Second)
	op := map[string]any{"operation": map[string]any{
		"initiatedBy": map[string]any{"username": "e2e"},
		"sync":        map[string]any{"revision": "main", "syncOptions": []any{"RespectIgnoreDifferences=true"}},
	}}
	b, _ := yaml.Marshal(op)
	j, _ := yaml.YAMLToJSON(b)
	_, app = getApp(t, cl, name)
	must(t, cl.C.Patch(ctx(t), app, client.RawPatch("application/merge-patch+json", j)))
	eventually(t, 3*time.Minute, 2*time.Second, "explicit sync of "+name, func() (bool, string) {
		s, _ := getApp(t, cl, name)
		return s.OpPhase == "Succeeded" && s.OpFinished.After(started), fmt.Sprintf("%+v", s)
	})
}
