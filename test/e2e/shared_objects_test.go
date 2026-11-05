//go:build e2e

package e2e

// Objects outside the suite's namespaces that scenarios change for a while: the agent's ClusterRole
// (pods/resize rule, TestReadinessRBAC), the spoke-2 agent's hub token Secret (TestHubUnreachable),
// the webhook Deployment (TestWebhook) and kube-state-metrics (TestReadinessKSMDown). They belong to
// Helm releases that Helm applies server-side as field manager "helm". A plain update by the suite
// takes over the fields it wrote, and the next `helm upgrade` then fails with a field-manager
// conflict. So after restoring such an object the suite also drops its own managedFields entry.
// Run markers make a running suite visible to other tracks' pre-deploy checks (CLAUDE.md).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// suiteFieldManager is the field manager of every write of the suite.
const suiteFieldManager = "kerf-e2e"

// legacyFieldManager is the default manager earlier suite versions wrote with (the test binary).
const legacyFieldManager = "e2e.test"

// releaseFields removes the suite's managedFields entries from obj (re-read by key), so the
// owning Helm release can apply those fields again without a conflict. The values stay.
func releaseFields(c context.Context, cl cluster, obj client.Object) error {
	key := client.ObjectKeyFromObject(obj)
	var lastErr error
	for i := 0; i < 5; i++ {
		if lastErr = cl.C.Get(c, key, obj); lastErr != nil {
			time.Sleep(time.Second)
			continue
		}
		mf := obj.GetManagedFields()
		var keep []metav1.ManagedFieldsEntry
		for _, e := range mf {
			if e.Manager != suiteFieldManager && e.Manager != legacyFieldManager {
				keep = append(keep, e)
			}
		}
		if len(keep) == len(mf) {
			return nil
		}
		var value any = keep
		if len(keep) == 0 {
			value = []map[string]any{{}} // an empty list would leave managedFields unchanged
		}
		patch, err := json.Marshal([]map[string]any{
			{"op": "test", "path": "/metadata/resourceVersion", "value": obj.GetResourceVersion()},
			{"op": "replace", "path": "/metadata/managedFields", "value": value},
		})
		if err != nil {
			return err
		}
		if lastErr = cl.C.Patch(c, obj, client.RawPatch(types.JSONPatchType, patch)); lastErr == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("drop field manager %s from %T %s: %w", suiteFieldManager, obj, key, lastErr)
}

// runMarker is the namespace that shows a running suite on both spokes (e2e-run-<id>).
var runMarker = "e2e-run-" + randSuffix()

// markRun deletes markers left by killed runs and creates this run's marker on both spokes.
func markRun() error {
	c, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, cl := range []cluster{env.Spoke1, env.Spoke2} {
		var list corev1.NamespaceList
		if err := cl.C.List(c, &list, client.MatchingLabels{suiteLabel: "true", runMarkerLabel: "true"}); err != nil {
			return fmt.Errorf("%s: list run markers: %w", cl.Name, err)
		}
		for i := range list.Items {
			if list.Items[i].DeletionTimestamp == nil {
				_ = cl.C.Delete(c, &list.Items[i])
				fmt.Printf("deleted the run marker %s left on %s by an interrupted run\n", list.Items[i].Name, cl.Name)
			}
		}
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: runMarker, Labels: map[string]string{suiteLabel: "true", runMarkerLabel: "true"},
			Annotations: map[string]string{"e2e.kerf.getkerf.dev/note": "make e2e is running: don't deploy agent/webhook to this cluster until it is gone"}}}
		if err := cl.C.Create(c, ns); err != nil {
			return fmt.Errorf("%s: create run marker: %w", cl.Name, err)
		}
	}
	fmt.Printf("run marker namespace %s on %s and %s\n", runMarker, env.Spoke1.Name, env.Spoke2.Name)
	return nil
}

const runMarkerLabel = "e2e.kerf.getkerf.dev/run-marker"

// unmarkRun deletes this run's marker (also with E2E_KEEP: it only signals a running suite).
func unmarkRun() {
	c, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, cl := range []cluster{env.Spoke1, env.Spoke2} {
		if err := cl.C.Delete(c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: runMarker}}); err != nil {
			fmt.Fprintf(os.Stderr, "delete run marker %s on %s: %v\n", runMarker, cl.Name, err)
		}
	}
}
