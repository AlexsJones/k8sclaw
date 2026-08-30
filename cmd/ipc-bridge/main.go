// Package main is the entry point for the Sympozium IPC bridge sidecar.
// It runs inside agent pods and mediates between the agent container
// and the control plane via the event bus.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
)

func main() {
	var basePath string
	var agentRunID string
	var instanceName string
	var agentNamespace string
	var eventBusURL string

	flag.StringVar(&basePath, "ipc-path", "/ipc", "Base path for IPC directory")
	flag.StringVar(&agentRunID, "agent-run-id", os.Getenv("AGENT_RUN_ID"), "Agent run ID")
	flag.StringVar(&instanceName, "instance", os.Getenv("INSTANCE_NAME"), "Agent name")
	flag.StringVar(&agentNamespace, "namespace", os.Getenv("AGENT_NAMESPACE"), "Agent namespace")
	flag.StringVar(&eventBusURL, "event-bus-url", os.Getenv("EVENT_BUS_URL"), "Event bus (NATS) URL")
	flag.Parse()

	if agentRunID == "" {
		panic("AGENT_RUN_ID is required")
	}
	if eventBusURL == "" {
		eventBusURL = "nats://nats.sympozium-system.svc:4222"
	}

	log := zap.New(zap.UseDevMode(false)).WithName("ipc-bridge")

	// Connect to event bus
	// The bridge deliberately uses only core NATS. Its credential is allowed to
	// relay the small, explicit IPC subject set but cannot manage JetStream or
	// publish directly to arbitrary control-plane subjects.
	bus, err := eventbus.NewCoreNATSEventBus(eventBusURL)
	if err != nil {
		log.Error(err, "failed to connect to event bus")
		os.Exit(1)
	}
	defer bus.Close()

	// Create and start bridge
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	bridge := ipc.NewBridge(basePath, agentRunID, instanceName, bus, log, agentNamespace)
	if err := bridge.Start(ctx); err != nil {
		log.Error(err, "bridge failed")
		os.Exit(1)
	}
}
