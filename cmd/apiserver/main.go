// Package main is the entry point for the Sympozium API server.
package main

import (
	"context"
	"flag"
	"io/fs"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
	"github.com/alexsjones/sympozium/internal/apiserver"
	"github.com/alexsjones/sympozium/internal/eventbus"
	webui "github.com/alexsjones/sympozium/web"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sympoziumv1alpha1.AddToScheme(scheme))
}

func main() {
	var addr string
	var namespace string
	var eventBusURL string
	var token string
	var serveUI bool

	flag.StringVar(&addr, "addr", ":8080", "API server listen address")
	flag.StringVar(&namespace, "namespace", "sympozium", "Sympozium namespace")
	flag.StringVar(&eventBusURL, "event-bus-url", "nats://nats.sympozium-system.svc:4222", "Event bus URL")
	flag.StringVar(&token, "token", os.Getenv("SYMPOZIUM_UI_TOKEN"), "Bearer token for API authentication (or set SYMPOZIUM_UI_TOKEN)")
	flag.BoolVar(&serveUI, "serve-ui", true, "Serve the embedded web UI alongside the API")
	flag.Parse()

	log := zap.New(zap.UseDevMode(true))
	ctrl.SetLogger(log)

	// Build Kubernetes client
	cfg := ctrl.GetConfigOrDie()
	k8sClient, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // disable metrics; apiserver is not a controller
	})
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Connect to event bus (retry in background if unavailable).
	var bus eventbus.EventBus
	natsbus, err := eventbus.NewNATSEventBus(eventBusURL)
	if err != nil {
		log.Error(err, "event bus not available, starting without streaming support")
	} else {
		bus = natsbus
	}

	kubeClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Error(err, "unable to create kubernetes clientset")
		os.Exit(1)
	}

	// Create and start API server
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if bus != nil {
		defer bus.Close()
	}

	// Start the manager cache in background
	go func() {
		if err := k8sClient.Start(ctx); err != nil {
			log.Error(err, "manager failed")
			os.Exit(1)
		}
	}()

	// Wait for cache sync
	if !k8sClient.GetCache().WaitForCacheSync(ctx) {
		log.Error(nil, "cache sync failed")
		os.Exit(1)
	}

	server := apiserver.NewServer(k8sClient.GetClient(), bus, kubeClient, log.WithName("apiserver"))

	if serveUI {
		// Extract the "dist" subdirectory from the embedded FS.
		frontendFS, fsErr := fs.Sub(webui.Dist, "dist")
		if fsErr != nil {
			log.Error(fsErr, "failed to load embedded frontend")
			os.Exit(1)
		}
		log.Info("Serving web UI", "addr", addr, "auth", token != "")
		if err := server.StartWithUI(addr, token, frontendFS); err != nil {
			log.Error(err, "api server failed")
			os.Exit(1)
		}
	} else {
		if err := server.Start(addr, token); err != nil {
			log.Error(err, "api server failed")
			os.Exit(1)
		}
	}
}
