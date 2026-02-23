// Package main is the entry point for the K8sClaw API server.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	k8sclawv1alpha1 "github.com/k8sclaw/k8sclaw/api/v1alpha1"
	"github.com/k8sclaw/k8sclaw/internal/apiserver"
	"github.com/k8sclaw/k8sclaw/internal/eventbus"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(k8sclawv1alpha1.AddToScheme(scheme))
}

func main() {
	var addr string
	var namespace string
	var eventBusURL string

	flag.StringVar(&addr, "addr", ":8080", "API server listen address")
	flag.StringVar(&namespace, "namespace", "k8sclaw", "K8sClaw namespace")
	flag.StringVar(&eventBusURL, "event-bus-url", "nats://nats.k8sclaw-system.svc:4222", "Event bus URL")
	flag.Parse()

	log := zap.New(zap.UseDevMode(true))
	ctrl.SetLogger(log)

	// Build Kubernetes client
	cfg := ctrl.GetConfigOrDie()
	k8sClient, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // disable metrics; apiserver is not a controller
	})
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Connect to event bus (retry in background if unavailable).
	var bus eventbus.EventBus
	bus, err = eventbus.NewNATSEventBus(eventBusURL)
	if err != nil {
		log.Error(err, "event bus not available, starting without streaming support")
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

	server := apiserver.NewServer(k8sClient.GetClient(), bus, log.WithName("apiserver"))
	if err := server.Start(addr); err != nil {
		log.Error(err, "api server failed")
		os.Exit(1)
	}
}
