// Package main is the entry point for the Sympozium controller manager.
// It starts all CRD controllers: SympoziumInstance, AgentRun, SympoziumPolicy, SkillPack.
package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
	"github.com/alexsjones/sympozium/internal/controller"
	"github.com/alexsjones/sympozium/internal/eventbus"
	"github.com/alexsjones/sympozium/internal/orchestrator"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
	imageTag = "latest" // overridden via -ldflags at build time
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sympoziumv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var natsURL string
	var maxRunHistory int

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&natsURL, "nats-url", "", "NATS URL for channel message routing. If empty, reads NATS_URL env var.")
	flag.IntVar(&maxRunHistory, "max-run-history", controller.DefaultRunHistoryLimit,
		"Maximum number of completed AgentRuns to keep per instance before pruning oldest.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "sympozium-controller-leader",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Set up the PodBuilder used by AgentRunReconciler
	podBuilder := orchestrator.NewPodBuilder(imageTag)

	// Create a kubernetes.Clientset for pod log access.
	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to create kubernetes clientset")
		os.Exit(1)
	}

	// Register controllers
	if err := (&controller.SympoziumInstanceReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Log:      ctrl.Log.WithName("controllers").WithName("SympoziumInstance"),
		ImageTag: imageTag,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SympoziumInstance")
		os.Exit(1)
	}

	if err := (&controller.AgentRunReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Log:             ctrl.Log.WithName("controllers").WithName("AgentRun"),
		PodBuilder:      podBuilder,
		Clientset:       clientset,
		ImageTag:        imageTag,
		RunHistoryLimit: maxRunHistory,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AgentRun")
		os.Exit(1)
	}

	if err := (&controller.SympoziumPolicyReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("SympoziumPolicy"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SympoziumPolicy")
		os.Exit(1)
	}

	if err := (&controller.SkillPackReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("SkillPack"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SkillPack")
		os.Exit(1)
	}

	if err := (&controller.SympoziumScheduleReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("SympoziumSchedule"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SympoziumSchedule")
		os.Exit(1)
	}

	if err := (&controller.PersonaPackReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("PersonaPack"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PersonaPack")
		os.Exit(1)
	}

	// --- Channel message router (optional — requires NATS) ---
	if natsURL == "" {
		natsURL = os.Getenv("NATS_URL")
	}
	if natsURL != "" {
		eb, err := eventbus.NewNATSEventBus(natsURL)
		if err != nil {
			setupLog.Error(err, "unable to connect to NATS — channel routing disabled")
		} else {
			router := &controller.ChannelRouter{
				Client:   mgr.GetClient(),
				EventBus: eb,
				Log:      ctrl.Log.WithName("channel-router"),
			}
			if err := mgr.Add(router); err != nil {
				setupLog.Error(err, "unable to add channel router")
				os.Exit(1)
			}

			schedRouter := &controller.ScheduleRouter{
				Client:   mgr.GetClient(),
				EventBus: eb,
				Log:      ctrl.Log.WithName("schedule-router"),
			}
			if err := mgr.Add(schedRouter); err != nil {
				setupLog.Error(err, "unable to add schedule router")
				os.Exit(1)
			}

			setupLog.Info("Channel message router enabled", "natsURL", natsURL)
		}
	} else {
		setupLog.Info("No NATS_URL configured — channel message routing disabled")
	}

	// Health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting Sympozium controller manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
