// Package controller contains the channel message router which bridges
// inbound channel messages (WhatsApp, Telegram, etc.) to AgentRuns and
// routes completed responses back through the originating channel.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	channelpkg "github.com/sympozium-ai/sympozium/internal/channel"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
	"github.com/sympozium-ai/sympozium/internal/sessionkey"
)

var routerTracer = otel.Tracer("sympozium.ai/channel-router")

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

// memorySystemPrompt returns the Memory.SystemPrompt for the instance,
// safely handling a nil MemorySpec pointer.
func memorySystemPrompt(inst *sympoziumv1alpha1.Agent) string {
	if inst == nil || inst.Spec.Memory == nil {
		return ""
	}
	return inst.Spec.Memory.SystemPrompt
}

// resolveProvider returns the AI provider for the instance.
// It prefers the explicit Provider field on AuthRefs, falling back to
// guessing from the auth secret names.
func resolveProvider(inst *sympoziumv1alpha1.Agent) string {
	// Check explicit provider label (set by Ensemble controller).
	if p := inst.Labels["sympozium.ai/provider"]; p != "" {
		return p
	}
	for _, ref := range inst.Spec.AuthRefs {
		if ref.Provider != "" {
			return ref.Provider
		}
	}
	// Fallback: guess from secret name (e.g. "<inst>-openai-key").
	for _, ref := range inst.Spec.AuthRefs {
		for _, p := range []string{"anthropic", "azure-openai", "bedrock", "lm-studio", "ollama", "openai"} {
			if strings.Contains(ref.Secret, p) {
				return p
			}
		}
	}
	// Fallback: infer from baseURL for keyless local providers.
	if base := inst.Spec.Agents.Default.BaseURL; base != "" {
		if strings.Contains(base, "ollama") || strings.Contains(base, ":11434") {
			return "ollama"
		}
		if strings.Contains(base, "lm-studio") || strings.Contains(base, ":1234") {
			return "lm-studio"
		}
		// Generic OpenAI-compatible local provider.
		return "custom"
	}
	return "openai"
}

// resolveAuthSecret returns the first non-empty auth secret reference.
func resolveAuthSecret(inst *sympoziumv1alpha1.Agent) string {
	for _, ref := range inst.Spec.AuthRefs {
		if strings.TrimSpace(ref.Secret) != "" {
			return ref.Secret
		}
	}
	return ""
}

// applyTriggers evaluates the channel's start/stop keyword rules against
// the inbound message, persists any mute-state transition, emits the
// associated Slack reaction, and returns true when the router should
// proceed to create an AgentRun for this message.
//
// On read errors the function fails open (proceeds), so a transient
// API-server hiccup never silently swallows messages.
func (cr *ChannelRouter) applyTriggers(
	ctx context.Context,
	span trace.Span,
	inst *sympoziumv1alpha1.Agent,
	msg channelpkg.InboundMessage,
) bool {
	spec := channelTriggerSpec(inst, msg.Channel)
	if spec == nil {
		cr.emitReaction(ctx, inst, msg, triggerProcess)
		return true
	}

	store := newMuteStore(cr.Client, inst)
	muted, err := store.IsMuted(ctx, msg.Channel, msg.ChatID)
	if err != nil {
		cr.Log.Error(err, "failed to read channel mute state — processing message anyway",
			"instance", msg.InstanceName, "channel", msg.Channel, "chatId", msg.ChatID)
		cr.emitReaction(ctx, inst, msg, triggerProcess)
		return true
	}

	decision := evaluateTrigger(spec, msg.Text, muted)
	logKV := []interface{}{
		"instance", msg.InstanceName,
		"channel", msg.Channel,
		"chatId", msg.ChatID,
	}

	switch decision {
	case triggerProcess:
		cr.emitReaction(ctx, inst, msg, triggerProcess)
		return true
	case triggerDrop:
		span.SetAttributes(attribute.Bool("sympozium.trigger.muted", true))
		cr.Log.Info("Channel message dropped (chat muted)", logKV...)
		return false
	case triggerStop, triggerResume:
		newMuted := decision == triggerStop
		if err := store.SetMuted(ctx, msg.Channel, msg.ChatID, newMuted); err != nil {
			cr.Log.Error(err, "failed to persist mute state", logKV...)
		}
		transition := "stop"
		logMsg := "Channel chat muted by stop keyword"
		if decision == triggerResume {
			transition = "resume"
			logMsg = "Channel chat unmuted by start keyword"
		}
		span.SetAttributes(attribute.String("sympozium.trigger.transition", transition))
		cr.Log.Info(logMsg, logKV...)
		cr.emitReaction(ctx, inst, msg, decision)
		return false
	default:
		// All triggerDecision values are handled above; this is
		// here only to keep the compiler happy if a new variant
		// is added without updating this switch.
		return true
	}
}

// handleInbound processes an inbound channel message by creating an AgentRun.
func (cr *ChannelRouter) handleInbound(ctx context.Context, event *eventbus.Event) {
	// Use trace context propagated via NATS headers from the channel pod.
	if event.Ctx != nil {
		ctx = event.Ctx
	}

	var msg channelpkg.InboundMessage
	if err := json.Unmarshal(event.Data, &msg); err != nil {
		cr.Log.Error(err, "failed to unmarshal inbound message")
		return
	}

	if msg.Text == "" || msg.InstanceName == "" {
		cr.Log.Info("Skipping empty inbound message", "instance", msg.InstanceName)
		return
	}

	ctx, span := routerTracer.Start(ctx, "channel_router.handle_inbound",
		trace.WithAttributes(
			attribute.String("sympozium.channel", msg.Channel),
			attribute.String("sympozium.instance", msg.InstanceName),
			attribute.String("sympozium.sender.id", msg.SenderID),
		),
	)
	defer span.End()

	cr.Log.Info("Received channel message",
		"channel", msg.Channel,
		"instance", msg.InstanceName,
		"sender", msg.SenderName,
		"text", truncateForLog(msg.Text, 80),
	)

	// Look up the Agent to get config and namespace.
	var instances sympoziumv1alpha1.AgentList
	if err := cr.Client.List(ctx, &instances); err != nil {
		cr.Log.Error(err, "failed to list Agents")
		return
	}

	var inst *sympoziumv1alpha1.Agent
	for i := range instances.Items {
		if instances.Items[i].Name == msg.InstanceName {
			inst = &instances.Items[i]
			break
		}
	}
	if inst == nil {
		cr.Log.Info("Agent not found for channel message", "instance", msg.InstanceName)
		return
	}

	// Enforce channel access control before creating an AgentRun.
	if allowed, denyMsg := checkChannelAccess(inst, &msg); !allowed {
		span.SetAttributes(attribute.Bool("sympozium.access.denied", true))
		recordChannelAccess(ctx, "denied", msg.Channel, msg.InstanceName)
		cr.Log.Info("Channel message denied by access control",
			"instance", msg.InstanceName, "channel", msg.Channel,
			"senderId", msg.SenderID, "chatId", msg.ChatID)
		if denyMsg != "" {
			cr.sendDenialResponse(ctx, msg, denyMsg)
		}
		return
	}
	// Complementary positive signal so denial rate = denied / (allowed+denied).
	recordChannelAccess(ctx, "allowed", msg.Channel, msg.InstanceName)

	// Evaluate stop/start keyword triggers (mute state, reactions).
	// Returns false when the message must not be turned into an AgentRun.
	if !cr.applyTriggers(ctx, span, inst, msg) {
		return
	}

	// Resolve model configuration from the Agent (same logic as TUI).
	provider := resolveProvider(inst)
	authSecret := resolveAuthSecret(inst)

	// Create an AgentRun for the inbound message.
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: msg.InstanceName + "-ch-",
			Namespace:    inst.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance":       msg.InstanceName,
				"sympozium.ai/source":         "channel",
				"sympozium.ai/source-channel": msg.Channel,
			},
			Annotations: map[string]string{
				"sympozium.ai/reply-channel":      msg.Channel,
				"sympozium.ai/reply-chat-id":      msg.ChatID,
				"sympozium.ai/reply-thread-id":    msg.ThreadID,
				"sympozium.ai/reply-message-ts":   msg.Metadata["ts"],
				"sympozium.ai/sender-name":        msg.SenderName,
				"sympozium.ai/sender-id":          msg.SenderID,
				"sympozium.ai/agent-display-name": resolveAgentDisplayName(inst),
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   msg.InstanceName,
			AgentID:    "primary",
			SessionKey: sessionkey.ForChannel(msg.Channel, msg.ChatID, msg.ThreadID),
			Task:       sympoziumv1alpha1.NewStringTask(msg.Text),
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:                 provider,
				Model:                    inst.Spec.Agents.Default.Model,
				BaseURL:                  inst.Spec.Agents.Default.BaseURL,
				AuthSecretRef:            authSecret,
				ProviderHeaders:          inst.Spec.Agents.Default.ProviderHeaders,
				ProviderHeadersSecretRef: inst.Spec.Agents.Default.ProviderHeadersSecretRef,
				NodeSelector:             inst.Spec.Agents.Default.NodeSelector,
			},
			Skills:           inst.Spec.Skills,
			Timeout:          inst.Spec.Agents.Default.ParseRunTimeout(),
			ImagePullSecrets: inst.Spec.ImagePullSecrets,
			Lifecycle:        inst.Spec.Agents.Default.Lifecycle,
			SystemPrompt:     memorySystemPrompt(inst),
			Volumes:          inst.Spec.Volumes,
			VolumeMounts:     inst.Spec.VolumeMounts,
			Env:              inst.Spec.Agents.Default.Env,
		},
	}

	// Propagate trace context via annotation so the controller reconciler
	// can link its span to this trace.
	sc := trace.SpanFromContext(ctx).SpanContext()
	if sc.HasTraceID() && sc.HasSpanID() {
		run.Annotations["otel.dev/traceparent"] = fmt.Sprintf("00-%s-%s-01", sc.TraceID().String(), sc.SpanID().String())
	}

	if err := cr.Client.Create(ctx, run); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		cr.Log.Error(err, "failed to create AgentRun from channel message",
			"instance", msg.InstanceName, "channel", msg.Channel)
		return
	}

	span.SetAttributes(attribute.String("sympozium.agentrun.name", run.Name))
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
	if event.Ctx != nil {
		ctx = event.Ctx
	}

	agentRunID := event.Metadata["agentRunID"]
	instanceName := event.Metadata["instanceName"]

	if agentRunID == "" {
		return
	}

	ctx, span := routerTracer.Start(ctx, "channel_router.handle_completed",
		trace.WithAttributes(
			attribute.String("sympozium.agentrun.id", agentRunID),
			attribute.String("sympozium.instance", instanceName),
		),
	)
	defer span.End()

	// Find the AgentRun to check if it originated from a channel.
	var runs sympoziumv1alpha1.AgentRunList
	if err := cr.Client.List(ctx, &runs, client.MatchingLabels{
		"sympozium.ai/source": "channel",
	}); err != nil {
		cr.Log.Error(err, "failed to list channel-sourced AgentRuns")
		return
	}

	var run *sympoziumv1alpha1.AgentRun
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

	replyChannel := run.Annotations["sympozium.ai/reply-channel"]
	replyChatID := run.Annotations["sympozium.ai/reply-chat-id"]
	replyThreadID := run.Annotations["sympozium.ai/reply-thread-id"]
	replyMessageTS := run.Annotations["sympozium.ai/reply-message-ts"]

	if replyChannel == "" {
		return
	}

	// Extract the response from the completed event.
	var result agentResult
	if err := json.Unmarshal(event.Data, &result); err != nil {
		cr.Log.Error(err, "failed to unmarshal agent result")
		return
	}

	// A preRun hook skipped the run (no work to do) — stay silent rather than
	// posting the skip reason back to the channel.
	if result.Status == ipc.ResultStatusSkipped {
		cr.Log.Info("Skipped run — no channel reply", "run", run.Name)
		return
	}

	responseText := result.Response
	if responseText == "" && result.Error != "" {
		responseText = fmt.Sprintf("Error: %s", result.Error)
	}
	if responseText == "" {
		responseText = "(no response)"
	}

	// Publish outbound message to the channel. Attribute the reply to the
	// agent's display name so a shared channel bot (e.g. one Slack app across a
	// multi-agent Ensemble) posts under distinct per-agent identities. Channels
	// that don't support attribution ignore Username.
	outMsg := channelpkg.OutboundMessage{
		Channel:  replyChannel,
		ChatID:   replyChatID,
		ThreadID: replyThreadID,
		Text:     responseText,
		Username: displayNameForReply(run),
	}
	if replyMessageTS != "" {
		outMsg.Metadata = map[string]string{"replyToTS": replyMessageTS}
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

// resolveAgentDisplayName returns the human-readable name for an agent,
// falling back to its resource name when no displayName is set.
func resolveAgentDisplayName(inst *sympoziumv1alpha1.Agent) string {
	if name := strings.TrimSpace(inst.Spec.DisplayName); name != "" {
		return name
	}
	return inst.Name
}

// displayNameForReply returns the attribution name to post a channel reply
// under, read from the annotation stamped at run creation, falling back to the
// agent reference.
func displayNameForReply(run *sympoziumv1alpha1.AgentRun) string {
	if name := strings.TrimSpace(run.Annotations["sympozium.ai/agent-display-name"]); name != "" {
		return name
	}
	return run.Spec.AgentRef
}

// checkChannelAccess evaluates access control rules for the channel that
// produced this message. Returns (allowed, denyMessage).
func checkChannelAccess(
	inst *sympoziumv1alpha1.Agent,
	msg *channelpkg.InboundMessage,
) (bool, string) {
	var ch *sympoziumv1alpha1.ChannelSpec
	for i := range inst.Spec.Channels {
		if inst.Spec.Channels[i].Type == msg.Channel {
			ch = &inst.Spec.Channels[i]
			break
		}
	}
	if ch == nil || ch.AccessControl == nil {
		return true, "" // no rules = allow all
	}
	ac := ch.AccessControl

	// Chat allowlist.
	if len(ac.AllowedChats) > 0 && !stringSliceContains(ac.AllowedChats, msg.ChatID) {
		return false, ac.DenyMessage
	}

	// Sender allowlist.
	if len(ac.AllowedSenders) > 0 && !stringSliceContains(ac.AllowedSenders, msg.SenderID) {
		return false, ac.DenyMessage
	}

	// Sender denylist (overrides allowlist).
	if len(ac.DeniedSenders) > 0 && stringSliceContains(ac.DeniedSenders, msg.SenderID) {
		return false, ac.DenyMessage
	}

	return true, ""
}

// sendDenialResponse sends a denial message back through the originating channel.
func (cr *ChannelRouter) sendDenialResponse(ctx context.Context, msg channelpkg.InboundMessage, text string) {
	cr.publishOutbound(ctx, msg.InstanceName, channelpkg.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Text:    text,
	}, "denial response")
}

// emitReaction publishes a per-channel reaction (delegated to
// reactionForDecision) when one is appropriate. No-op otherwise.
func (cr *ChannelRouter) emitReaction(
	ctx context.Context,
	inst *sympoziumv1alpha1.Agent,
	msg channelpkg.InboundMessage,
	decision triggerDecision,
) {
	out := reactionForDecision(inst, msg, decision)
	if out == nil {
		return
	}
	cr.publishOutbound(ctx, msg.InstanceName, *out, "reaction")
}

// publishOutbound is the single point where the router emits messages
// (replies, denials, reactions) onto the outbound channel topic. It
// logs failures without bubbling them — outbound emission is always
// best-effort from the router's perspective.
func (cr *ChannelRouter) publishOutbound(
	ctx context.Context,
	instanceName string,
	out channelpkg.OutboundMessage,
	kind string,
) {
	event, err := eventbus.NewEvent(eventbus.TopicChannelMessageSend, map[string]string{
		"instanceName": instanceName,
		"channel":      out.Channel,
	}, out)
	if err != nil {
		cr.Log.Error(err, "failed to build outbound event", "kind", kind, "channel", out.Channel)
		return
	}
	if err := cr.EventBus.Publish(ctx, eventbus.TopicChannelMessageSend, event); err != nil {
		cr.Log.Error(err, "failed to publish outbound event",
			"kind", kind, "channel", out.Channel, "chatId", out.ChatID)
	}
}

func stringSliceContains(list []string, val string) bool {
	for _, v := range list {
		if v == val {
			return true
		}
	}
	return false
}
