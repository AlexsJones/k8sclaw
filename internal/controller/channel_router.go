// Package controller contains the channel message router which bridges
// inbound channel messages (WhatsApp, Telegram, etc.) to AgentRuns and
// routes completed responses back through the originating channel.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeclawv1alpha1 "github.com/kubeclaw/kubeclaw/api/v1alpha1"
	channelpkg "github.com/kubeclaw/kubeclaw/internal/channel"
	"github.com/kubeclaw/kubeclaw/internal/eventbus"
)

// ChannelRouter subscribes to channel.message.received on the event bus,
// creates AgentRuns for inbound messages, and routes completed responses
// back to the originating channel via channel.message.send.
type ChannelRouter struct {
	Client   client.Client
	EventBus eventbus.EventBus
	Log      logr.Logger
}

// Start begins listening for inbound channel messages and completed agent runs.
// It blocks until ctx is cancelled.
func (cr *ChannelRouter) Start(ctx context.Context) error {
	cr.Log.Info("Starting channel message router")

	// Subscribe to inbound channel messages.
	inboundCh, err := cr.EventBus.Subscribe(ctx, eventbus.TopicChannelMessageRecv)
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", eventbus.TopicChannelMessageRecv, err)
	}

	// Subscribe to completed agent runs to route responses back.
	completedCh, err := cr.EventBus.Subscribe(ctx, eventbus.TopicAgentRunCompleted)
	if err != nil {
		return fmt.Errorf("subscribing to %s: %w", eventbus.TopicAgentRunCompleted, err)
	}

	for {
		select {
		case <-ctx.Done():
			cr.Log.Info("Channel router shutting down")
			return nil

		case event := <-inboundCh:
			cr.handleInbound(ctx, event)

		case event := <-completedCh:
			cr.handleCompleted(ctx, event)
		}
	}
}

// resolveProvider returns the AI provider for the instance.
// It prefers the explicit Provider field on AuthRefs, falling back to
// guessing from the auth secret names.
func resolveProvider(inst *kubeclawv1alpha1.ClawInstance) string {
	for _, ref := range inst.Spec.AuthRefs {
		if ref.Provider != "" {
			return ref.Provider
		}
	}
	// Fallback: guess from secret name (e.g. "<inst>-openai-key").
	for _, ref := range inst.Spec.AuthRefs {
		for _, p := range []string{"anthropic", "azure-openai", "ollama", "openai"} {
			if strings.Contains(ref.Secret, p) {
				return p
			}
		}
	}
	return "openai"
}

// handleInbound processes an inbound channel message by creating an AgentRun.
func (cr *ChannelRouter) handleInbound(ctx context.Context, event *eventbus.Event) {
	var msg channelpkg.InboundMessage
	if err := json.Unmarshal(event.Data, &msg); err != nil {
		cr.Log.Error(err, "failed to unmarshal inbound message")
		return
	}

	if msg.Text == "" || msg.InstanceName == "" {
		cr.Log.Info("Skipping empty inbound message", "instance", msg.InstanceName)
		return
	}

	cr.Log.Info("Received channel message",
		"channel", msg.Channel,
		"instance", msg.InstanceName,
		"sender", msg.SenderName,
		"text", truncateForLog(msg.Text, 80),
	)

	// Look up the ClawInstance to get config and namespace.
	var instances kubeclawv1alpha1.ClawInstanceList
	if err := cr.Client.List(ctx, &instances); err != nil {
		cr.Log.Error(err, "failed to list ClawInstances")
		return
	}

	var inst *kubeclawv1alpha1.ClawInstance
	for i := range instances.Items {
		if instances.Items[i].Name == msg.InstanceName {
			inst = &instances.Items[i]
			break
		}
	}
	if inst == nil {
		cr.Log.Info("ClawInstance not found for channel message", "instance", msg.InstanceName)
		return
	}

	// Resolve model configuration from the ClawInstance (same logic as TUI).
	provider := resolveProvider(inst)
	authSecret := ""
	if len(inst.Spec.AuthRefs) > 0 {
		authSecret = inst.Spec.AuthRefs[0].Secret
	}

	// Create an AgentRun for the inbound message.
	run := &kubeclawv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: msg.InstanceName + "-ch-",
			Namespace:    inst.Namespace,
			Labels: map[string]string{
				"kubeclaw.io/instance":       msg.InstanceName,
				"kubeclaw.io/source":         "channel",
				"kubeclaw.io/source-channel": msg.Channel,
			},
			Annotations: map[string]string{
				"kubeclaw.io/reply-channel": msg.Channel,
				"kubeclaw.io/reply-chat-id": msg.ChatID,
				"kubeclaw.io/sender-name":   msg.SenderName,
				"kubeclaw.io/sender-id":     msg.SenderID,
			},
		},
		Spec: kubeclawv1alpha1.AgentRunSpec{
			InstanceRef: msg.InstanceName,
			AgentID:     "primary",
			SessionKey:  fmt.Sprintf("channel-%s-%s-%d", msg.Channel, msg.ChatID, time.Now().UnixNano()),
			Task:        msg.Text,
			Model: kubeclawv1alpha1.ModelSpec{
				Provider:      provider,
				Model:         inst.Spec.Agents.Default.Model,
				BaseURL:       inst.Spec.Agents.Default.BaseURL,
				AuthSecretRef: authSecret,
			},
			Skills:  inst.Spec.Skills,
			Timeout: &metav1.Duration{Duration: 10 * time.Minute},
		},
	}

	if err := cr.Client.Create(ctx, run); err != nil {
		cr.Log.Error(err, "failed to create AgentRun from channel message",
			"instance", msg.InstanceName, "channel", msg.Channel)
		return
	}

	cr.Log.Info("Created AgentRun from channel message",
		"run", run.Name,
		"instance", msg.InstanceName,
		"channel", msg.Channel,
	)
}

// agentResult matches the result structure emitted by the agent-runner.
type agentResult struct {
	Status   string `json:"status"`
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
}

// handleCompleted processes a completed AgentRun and routes the response
// back through the originating channel if it came from one.
func (cr *ChannelRouter) handleCompleted(ctx context.Context, event *eventbus.Event) {
	agentRunID := event.Metadata["agentRunID"]
	instanceName := event.Metadata["instanceName"]

	if agentRunID == "" {
		return
	}

	// Find the AgentRun to check if it originated from a channel.
	var runs kubeclawv1alpha1.AgentRunList
	if err := cr.Client.List(ctx, &runs, client.MatchingLabels{
		"kubeclaw.io/source": "channel",
	}); err != nil {
		cr.Log.Error(err, "failed to list channel-sourced AgentRuns")
		return
	}

	var run *kubeclawv1alpha1.AgentRun
	for i := range runs.Items {
		if runs.Items[i].Name == agentRunID {
			run = &runs.Items[i]
			break
		}
	}
	// Also try matching by status.podName or generated name prefix
	if run == nil {
		for i := range runs.Items {
			if runs.Items[i].Status.PodName != "" && strings.Contains(agentRunID, runs.Items[i].Name) {
				run = &runs.Items[i]
				break
			}
		}
	}

	if run == nil {
		// Not a channel-sourced run — ignore.
		return
	}

	replyChannel := run.Annotations["kubeclaw.io/reply-channel"]
	replyChatID := run.Annotations["kubeclaw.io/reply-chat-id"]

	if replyChannel == "" {
		return
	}

	// Extract the response from the completed event.
	var result agentResult
	if err := json.Unmarshal(event.Data, &result); err != nil {
		cr.Log.Error(err, "failed to unmarshal agent result")
		return
	}

	responseText := result.Response
	if responseText == "" && result.Error != "" {
		responseText = fmt.Sprintf("Error: %s", result.Error)
	}
	if responseText == "" {
		responseText = "(no response)"
	}

	// Publish outbound message to the channel.
	outMsg := channelpkg.OutboundMessage{
		Channel: replyChannel,
		ChatID:  replyChatID,
		Text:    responseText,
	}

	outEvent, err := eventbus.NewEvent(eventbus.TopicChannelMessageSend, map[string]string{
		"instanceName": instanceName,
		"channel":      replyChannel,
	}, outMsg)
	if err != nil {
		cr.Log.Error(err, "failed to create outbound event")
		return
	}

	if err := cr.EventBus.Publish(ctx, eventbus.TopicChannelMessageSend, outEvent); err != nil {
		cr.Log.Error(err, "failed to publish channel reply",
			"channel", replyChannel, "chatId", replyChatID)
		return
	}

	cr.Log.Info("Routed agent response to channel",
		"run", run.Name,
		"channel", replyChannel,
		"responseLen", len(responseText),
	)
}

func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
