package configsync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/kerf-dev/kerf/api/v1alpha1"
)

var (
	k8s     client.Client
	testCtx = context.Background()
	skipEnv string
	testLog = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
)

// assetsDir finds envtest binaries: $KUBEBUILDER_ASSETS or ./bin/envtest/k8s/<version>-<os>-<arch>.
func assetsDir() string {
	if d := os.Getenv("KUBEBUILDER_ASSETS"); d != "" {
		return d
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	matches, _ := filepath.Glob(filepath.Join(root, "bin", "envtest", "k8s", "1.35*-"+runtime.GOOS+"-"+runtime.GOARCH))
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func TestMain(m *testing.M) { os.Exit(runSuite(m)) }

func runSuite(m *testing.M) int {
	assets := assetsDir()
	if assets == "" {
		skipEnv = "envtest binaries not found: run `make agent-envtest` or set KUBEBUILDER_ASSETS"
		return m.Run()
	}
	_, file, _, _ := runtime.Caller(0)
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(filepath.Dir(file), "..", "..", "..", "api", "crds")},
		ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: assets,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest:", err)
		return 1
	}
	defer func() { _ = env.Stop() }()
	scheme := k8sruntime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)
	if k8s, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		fmt.Fprintln(os.Stderr, "client:", err)
		return 1
	}
	return m.Run()
}

func needEnv(t *testing.T) {
	t.Helper()
	if skipEnv != "" {
		t.Skip(skipEnv)
	}
}
