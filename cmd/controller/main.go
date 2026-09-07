// Package main is the entry point for the Sympozium controller manager.
// It starts all CRD controllers: Agent, AgentRun, SympoziumPolicy, SkillPack.
package main

import (
	"context"
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	llmfitv1alpha1 "github.com/sympozium-ai/llmfit-dra/api/v1alpha1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller"
	"github.com/sympozium-ai/sympozium/internal/dra"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/orchestrator"
	"github.com/sympozium-ai/sympozium/internal/pricing"
	"github.com/sympozium-ai/sympozium/pkg/telemetry"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
	imageTag = "latest" // overridden via -ldflags at build time
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(sympoziumv1alpha1.AddToScheme(scheme))
	utilruntime.Must(llmfitv1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayv1.Install(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var natsURL string
	var maxRunHistory int
	var delegationControllerExecutor bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&natsURL, "nats-url", "", "NATS URL for channel message routing. If empty, reads NATS_URL env var.")
	flag.IntVar(&maxRunHistory, "max-run-history", controller.DefaultRunHistoryLimit,
		"Maximum number of completed AgentRuns to keep per instance before pruning oldest.")
	flag.BoolVar(&delegationControllerExecutor, "delegation-controller-executor", false,
		"Opt-in: spawn delegation-edge targets controller-side as a fallback for "+
			"models that never emit the delegate_to_persona tool. Default off; the "+
			"tool-driven delegation path is unchanged. Also enabled via "+
			"SYMPOZIUM_DELEGATION_CONTROLLER_EXECUTOR=true.")
	flag.Parse()

	// Resolve the image tag used for runtime-spawned pods (agent-runner,
	// memory-server, MCP servers, channel sidecars). The package-level
	// imageTag default ("latest") is only overridden via -ldflags at build
	// time, which the fork CI does not set — so the Helm chart propagates
	// .Values.image.tag through the SYMPOZIUM_IMAGE_TAG env var instead.
	// Honor it here so the AgentRun and Agent reconcilers stamp the pinned
	// tag on run pods and the memory sidecar, matching what NewPodBuilder,
	// the ensemble/mcpserver/model controllers already do. Without this the
	// controller silently spawns agent-runner:latest — a pre-ISI-1406 image
	// with no memory autoStore or traceparent propagation, so memory and
	// chain-trace telemetry never reaches the collector (ISI-1406 / ISI-1417).
	if v := os.Getenv("SYMPOZIUM_IMAGE_TAG"); v != "" {
		imageTag = v
		setupLog.Info("Image tag resolved from SYMPOZIUM_IMAGE_TAG env", "tag", imageTag)
	}

	// Env override mirrors the SYMPOZIUM_IMAGE_TAG pattern so the Helm chart can
	// toggle the controller-side delegation executor via a chart value without
	// passing CLI flags. Default false — flag default already off; env only
	// flips it on.
	if os.Getenv("SYMPOZIUM_DELEGATION_CONTROLLER_EXECUTOR") == "true" {
		delegationControllerExecutor = true
	}

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// Initialize OpenTelemetry SDK. Falls back to noop if OTEL_EXPORTER_OTLP_ENDPOINT is unset.
	tel, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "sympozium-controller",
	})
	if err != nil {
		setupLog.Error(err, "OTel init failed, continuing without telemetry")
	}
	defer tel.Shutdown(context.Background())

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
	if err := (&controller.AgentReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Log:      ctrl.Log.WithName("controllers").WithName("Agent"),
		ImageTag: imageTag,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Agent")
		os.Exit(1)
	}

	// Initialize dynamic client for Agent Sandbox CRD support.
	// Auto-detect CRDs unless explicitly disabled via AGENT_SANDBOX_ENABLED=false.
	var dynamicClient dynamic.Interface
	if os.Getenv("AGENT_SANDBOX_ENABLED") != "false" {
		dc, err := dynamic.NewForConfig(mgr.GetConfig())
		if err != nil {
			setupLog.Error(err, "unable to create dynamic client for agent-sandbox")
		} else if controller.CheckAgentSandboxCRDs(dc) {
			dynamicClient = dc
			setupLog.Info("Agent Sandbox CRD support enabled (auto-detected)")
		} else {
			setupLog.Info("Agent Sandbox CRDs not found in cluster, feature disabled")
		}
	} else {
		setupLog.Info("Agent Sandbox explicitly disabled via AGENT_SANDBOX_ENABLED=false")
	}

	// Cost estimation price table. The Loader must use the uncached APIReader:
	// no ConfigMap informer exists, and a cached Get would silently start a
	// cluster-wide ConfigMap list+watch.
	var pricingLoader *pricing.Loader
	if cmName := os.Getenv("SYMPOZIUM_PRICING_CONFIGMAP"); cmName != "" {
		cmNamespace := os.Getenv("SYMPOZIUM_PRICING_NAMESPACE")
		if cmNamespace == "" {
			cmNamespace = os.Getenv("POD_NAMESPACE")
		}
		pricingLoader = &pricing.Loader{
			Reader:    mgr.GetAPIReader(),
			Name:      cmName,
			Namespace: cmNamespace,
		}
		setupLog.Info("Cost estimation enabled", "configMap", cmName, "namespace", cmNamespace)
	}

	agentRunReconciler := &controller.AgentRunReconciler{
		Client:                       mgr.GetClient(),
		APIReader:                    mgr.GetAPIReader(),
		Scheme:                       mgr.GetScheme(),
		Log:                          ctrl.Log.WithName("controllers").WithName("AgentRun"),
		PodBuilder:                   podBuilder,
		Clientset:                    clientset,
		ImageTag:                     imageTag,
		RunHistoryLimit:              maxRunHistory,
		DelegationControllerExecutor: delegationControllerExecutor,
		DynamicClient:                dynamicClient,
		Pricing:                      pricingLoader,
	}
	if err := agentRunReconciler.SetupWithManager(mgr); err != nil {
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

	if err := (&controller.WorkspaceSessionReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("WorkspaceSession"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "WorkspaceSession")
		os.Exit(1)
	}

	ensembleReconciler := &controller.EnsembleReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("Ensemble"),
	}
	if err := ensembleReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Ensemble")
		os.Exit(1)
	}

	if err := (&controller.MCPServerReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Log:      ctrl.Log.WithName("controllers").WithName("MCPServer"),
		ImageTag: imageTag,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "MCPServer")
		os.Exit(1)
	}

	if err := (&controller.SympoziumConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("SympoziumConfig"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SympoziumConfig")
		os.Exit(1)
	}

	if err := (&controller.AgentRuntimeReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("AgentRuntime"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AgentRuntime")
		os.Exit(1)
	}

	if err := (&controller.HarnessSessionReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Log:    ctrl.Log.WithName("controllers").WithName("HarnessSession"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HarnessSession")
		os.Exit(1)
	}

	modelReconciler := &controller.ModelReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Log:       ctrl.Log.WithName("controllers").WithName("Model"),
		Clientset: clientset,
		// Claim-based placement is runtime-detected: llmfit-dra ships with the
		// sympozium chart, but pre-1.34 clusters (no DRA) degrade gracefully.
		DRA: dra.NewDetector(clientset.Discovery()),
	}
	if err := modelReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Model")
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
			agentRunReconciler.EventBus = eb
			ensembleReconciler.EventBus = eb

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

			spawnRouter := &controller.SpawnRouter{
				Client:   mgr.GetClient(),
				EventBus: eb,
				Log:      ctrl.Log.WithName("spawn-router"),
			}
			if err := mgr.Add(spawnRouter); err != nil {
				setupLog.Error(err, "unable to add spawn router")
				os.Exit(1)
			}

			// --- llmfit density cache (populates via NATS events or REST API polling) ---
			densityCache := controller.NewDensityCache(90 * time.Second) // 1.5x default 60s event interval

			// Try NATS subscriber first; fall back to REST API poller.
			densitySub := &controller.DensitySubscriber{
				NATSUrl: natsURL,
				Cache:   densityCache,
				Log:     ctrl.Log.WithName("density-subscriber"),
			}
			if err := mgr.Add(densitySub); err != nil {
				setupLog.Error(err, "unable to add density subscriber")
				os.Exit(1)
			}

			// Also start REST API poller as fallback for when llmfit binary
			// doesn't have the NATS feature compiled in.
			densityPoller := &controller.DensityPoller{
				K8sClient: mgr.GetClient(),
				Cache:     densityCache,
				Log:       ctrl.Log.WithName("density-poller"),
			}
			if err := mgr.Add(densityPoller); err != nil {
				setupLog.Error(err, "unable to add density poller")
				os.Exit(1)
			}

			modelReconciler.DensityCache = densityCache
			ensembleReconciler.DensityCache = densityCache

			// Register Prometheus metrics for density data.
			densityMetrics := controller.NewDensityMetrics(densityCache)
			metrics.Registry.MustRegister(densityMetrics)
			setupLog.Info("llmfit density cache enabled — model placement will use cached density data")

			// --- llmfit density watcher (live model eviction on degradation) ---
			if os.Getenv("LLMFIT_LIVE_EVICTION") == "true" {
				densityWatcher := &controller.DensityWatcher{
					Client:   mgr.GetClient(),
					Cache:    densityCache,
					EventBus: eb,
					Log:      ctrl.Log.WithName("density-watcher"),
				}
				if err := mgr.Add(densityWatcher); err != nil {
					setupLog.Error(err, "unable to add density watcher")
					os.Exit(1)
				}
				setupLog.Info("llmfit density watcher enabled — models will be re-placed on degradation")
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
