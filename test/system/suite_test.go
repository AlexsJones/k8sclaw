//go:build system

package system_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/apiserver"
	"github.com/sympozium-ai/sympozium/internal/controller"
	"github.com/sympozium-ai/sympozium/internal/orchestrator"
)

var (
	k8sClient client.Client
	mux       http.Handler
	testCtx   context.Context
	testEnv   *envtest.Environment
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithCancel(context.Background())
	testCtx = ctx
	defer cancel()

	scheme := runtime.NewScheme()
	must(clientgoscheme.AddToScheme(scheme))
	must(sympoziumv1alpha1.AddToScheme(scheme))
	// CRDs themselves, so tests can assert on served schema (print columns).
	must(apiextensionsv1.AddToScheme(scheme))

	// Start envtest — real etcd + kube-apiserver, no kubelet.
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
		},
		Scheme: scheme,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "envtest start: %v\n", err)
		os.Exit(1)
	}

	// Create the controller manager.
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // disable
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "manager: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clientset: %v\n", err)
		os.Exit(1)
	}

	log := logr.Discard()
	podBuilder := orchestrator.NewPodBuilder("test")

	// Register all controllers (mirrors cmd/controller/main.go).
	must((&controller.AgentReconciler{
		Client:   mgr.GetClient(),
		Scheme:   scheme,
		Log:      log,
		ImageTag: "test",
	}).SetupWithManager(mgr))

	must((&controller.AgentRunReconciler{
		Client:          mgr.GetClient(),
		APIReader:       mgr.GetAPIReader(),
		Scheme:          scheme,
		Log:             log,
		PodBuilder:      podBuilder,
		Clientset:       clientset,
		ImageTag:        "test",
		RunHistoryLimit: controller.DefaultRunHistoryLimit,
	}).SetupWithManager(mgr))

	must((&controller.EnsembleReconciler{
		Client: mgr.GetClient(),
		Scheme: scheme,
		Log:    log,
	}).SetupWithManager(mgr))

	must((&controller.ModelReconciler{
		Client:    mgr.GetClient(),
		Scheme:    scheme,
		Log:       log,
		Clientset: clientset,
	}).SetupWithManager(mgr))

	must((&controller.SympoziumScheduleReconciler{
		Client: mgr.GetClient(),
		Scheme: scheme,
		Log:    log,
	}).SetupWithManager(mgr))

	must((&controller.MCPServerReconciler{
		Client:   mgr.GetClient(),
		Scheme:   scheme,
		Log:      log,
		ImageTag: "test",
	}).SetupWithManager(mgr))

	must((&controller.SkillPackReconciler{
		Client: mgr.GetClient(),
		Scheme: scheme,
		Log:    log,
	}).SetupWithManager(mgr))

	must((&controller.SympoziumPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: scheme,
		Log:    log,
	}).SetupWithManager(mgr))

	must((&controller.SympoziumConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: scheme,
		Log:    log,
	}).SetupWithManager(mgr))

	// Start the manager in the background.
	go func() {
		if err := mgr.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "manager start: %v\n", err)
		}
	}()

	// Wait for informer caches to sync.
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		fmt.Fprintln(os.Stderr, "cache sync failed")
		os.Exit(1)
	}

	k8sClient = mgr.GetClient()

	// Build the API server HTTP handler (no auth — a nil token reader).
	srv := apiserver.NewServer(k8sClient, nil, clientset, log)
	mux = srv.Handler(nil)

	// Ensure required namespaces exist (envtest doesn't create them).
	for _, nsName := range []string{"default", "sympozium-system"} {
		_ = k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: nsName},
		})
	}

	code := m.Run()

	cancel()
	_ = testEnv.Stop()
	os.Exit(code)
}

func must(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}
