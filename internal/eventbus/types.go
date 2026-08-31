// Package eventbus provides an abstraction over the event bus (NATS JetStream)
// used for communication between Sympozium components.
package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Event represents a message on the event bus.
type Event struct {
	// Ctx carries trace context extracted from NATS message headers.
	// Not serialized to JSON. Consumers can use this to continue a trace.
	Ctx context.Context `json:"-"`

	// Topic is the event topic (e.g., "agent.run.completed").
	Topic string `json:"topic"`

	// Timestamp when the event was published.
	Timestamp time.Time `json:"timestamp"`

	// Metadata contains key-value metadata.
	Metadata map[string]string `json:"metadata"`

	// Data is the event payload.
	Data json.RawMessage `json:"data"`
}

// EventBus defines the interface for the event bus.
type EventBus interface {
	// Publish sends an event to the bus.
	Publish(ctx context.Context, topic string, event *Event) error

	// Subscribe returns a channel that receives events for the given topic.
	Subscribe(ctx context.Context, topic string) (<-chan *Event, error)

	// Close shuts down the event bus connection.
	Close() error
}

// Topics used by Sympozium components.
const (
	TopicAgentRunRequested    = "agent.run.requested"
	TopicAgentRunStarted      = "agent.run.started"
	TopicAgentRunCompleted    = "agent.run.completed"
	TopicAgentRunFailed       = "agent.run.failed"
	TopicAgentStreamChunk     = "agent.stream.chunk"
	TopicAgentSpawnRequest    = "agent.spawn.request"
	TopicChannelMessageRecv   = "channel.message.received"
	TopicChannelMessageSend   = "channel.message.send"
	TopicChannelHealthUpdate  = "channel.health.update"
	TopicToolExecRequest      = "tool.exec.request"
	TopicToolExecResult       = "tool.exec.result"
	TopicToolApprovalRequest  = "tool.approval.request"
	TopicToolApprovalResponse = "tool.approval.response"
	TopicAgentDelegateResult  = "agent.delegate.result" // per-run: agent.delegate.result.{parentRunID}
	TopicAgentSubagentRequest = "agent.subagent.request"
	TopicAgentSubagentResult  = "agent.subagent.result" // per-run: agent.subagent.result.{parentRunID}

	// Sidecar-initiated LLM prompting. The sidecar drives the orchestration
	// loop and asks the agent-runner to issue single LLM calls instead of
	// the model driving tool calls. TopicPromptRequest is the bridge-relayed
	// request from the sidecar to the agent-runner; TopicPromptResult is
	// the agent-runner's reply back through the bridge.
	// Per-run: agent.prompt.result.{runID}.
	TopicPromptRequest = "agent.prompt.request"
	TopicPromptResult  = "agent.prompt.result"

	// ContextClear is the agent-runner inbound event the bridge publishes when
	// a sidecar writes /ipc/context/clear-{RequestID}.json.
	TopicContextClear = "agent.context.clear"

	// GateVerdict carries a gate verdict from the controller to a parked agent
	// pod, which continues its existing conversation rather than being replaced
	// by a successor run. Per-run: agent.gate.verdict.{runID}. The bridge is the
	// only subscriber; it writes the payload to /ipc/gate/verdict-{attempt}.json
	// for the agent-runner to pick up.
	TopicGateVerdict = "agent.gate.verdict"

	TopicScheduleUpsert    = "schedule.upsert"
	TopicStimulusDelivered = "ensemble.stimulus.delivered"

	// Density telemetry (from llmfit DaemonSet via FitnessCache)
	TopicDensityUpdated     = "density.updated"           // per-node fitness snapshot
	TopicDensityNodeStale   = "density.node.stale"        // node stopped reporting
	TopicPlacementCompleted = "model.placement.completed" // placement decision recorded
	TopicModelEviction      = "model.eviction.triggered"  // model being re-placed
)

// NewEvent creates a new event with the current timestamp.
func NewEvent(topic string, metadata map[string]string, data interface{}) (*Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshalling event data: %w", err)
	}
	return &Event{
		Topic:     topic,
		Timestamp: time.Now(),
		Metadata:  metadata,
		Data:      raw,
	}, nil
}
