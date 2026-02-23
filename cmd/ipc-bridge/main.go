// Package main is the entry point for the K8sClaw IPC bridge sidecar.
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

	"github.com/k8sclaw/k8sclaw/internal/eventbus"
	"github.com/k8sclaw/k8sclaw/internal/ipc"
)

func main() {
	var basePath string
	var agentRunID string
	var instanceName string
	var eventBusURL string

	flag.StringVar(&basePath, "ipc-path", "/ipc", "Base path for IPC directory")
	flag.StringVar(&agentRunID, "agent-run-id", os.Getenv("AGENT_RUN_ID"), "Agent run ID")
	flag.StringVar(&instanceName, "instance", os.Getenv("INSTANCE_NAME"), "ClawInstance name")
	flag.StringVar(&eventBusURL, "event-bus-url", os.Getenv("EVENT_BUS_URL"), "Event bus (NATS) URL")
	flag.Parse()

	if agentRunID == "" {
		panic("AGENT_RUN_ID is required")
	}
	if eventBusURL == "" {
		eventBusURL = "nats://nats.k8sclaw-system.svc:4222"
	}

	log := zap.New(zap.UseDevMode(false)).WithName("ipc-bridge")

	// Connect to event bus
	bus, err := eventbus.NewNATSEventBus(eventBusURL)
	if err != nil {
		log.Error(err, "failed to connect to event bus")
		os.Exit(1)
	}
	defer bus.Close()

	// Create and start bridge
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	bridge := ipc.NewBridge(basePath, agentRunID, instanceName, bus, log)
	if err := bridge.Start(ctx); err != nil {
		log.Error(err, "bridge failed")
		os.Exit(1)
	}
}
