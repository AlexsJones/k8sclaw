// Package eventbus provides an abstraction over the event bus (NATS JetStream)
// used for communication between KubeClaw components.
package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Event represents a message on the event bus.
type Event struct {
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

// Topics used by KubeClaw components.
const (
	TopicAgentRunRequested     = "agent.run.requested"
	TopicAgentRunStarted       = "agent.run.started"
	TopicAgentRunCompleted     = "agent.run.completed"
	TopicAgentRunFailed        = "agent.run.failed"
	TopicAgentStreamChunk      = "agent.stream.chunk"
	TopicAgentSpawnRequest     = "agent.spawn.request"
	TopicChannelMessageRecv    = "channel.message.received"
	TopicChannelMessageSend    = "channel.message.send"
	TopicChannelHealthUpdate   = "channel.health.update"
	TopicToolExecRequest       = "tool.exec.request"
	TopicToolExecResult        = "tool.exec.result"
	TopicToolApprovalRequest   = "tool.approval.request"
	TopicToolApprovalResponse  = "tool.approval.response"
	TopicScheduleUpsert        = "schedule.upsert"
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
