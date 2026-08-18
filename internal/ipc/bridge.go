// Package ipc implements the IPC bridge sidecar that mediates communication
// between ephemeral agent pods and the durable Sympozium control plane.
//
// The bridge watches directories under /ipc for file-based IPC messages from
// the agent container, translates them into event bus messages, and relays
// responses back via file drops.
package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

// isSafeIPCID reports whether an agent-supplied correlation ID (requestId /
// batchId) is safe to interpolate into an IPC result filename. The ID
// originates in the agent's own spawn-request JSON and is echoed back by the
// control plane, so an adversarial agent could otherwise smuggle path
// separators (e.g. "../../tmp/evil") to write result files outside /ipc.
func isSafeIPCID(id string) bool {
	if id == "" || len(id) > 253 {
		return false
	}
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return false
	}
	// A safe ID is a single path element equal to its own base name.
	return filepath.Base(id) == id
}

// IPCDir layout constants matching the design doc protocol.
const (
	DirInput     = "input"
	DirOutput    = "output"
	DirSpawn     = "spawn"
	DirTools     = "tools"
	DirMessages  = "messages"
	DirSchedules = "schedules"
	DirPrompts   = "prompts" // sidecar-initiated LLM prompts
	DirContext   = "context" // clear-context IPC for sidecar-initiated prompts
	DirGate      = "gate"    // gate verdicts delivered to a parked agent-runner
)

// Bridge is the IPC bridge sidecar process.
type Bridge struct {
	BasePath           string // Root IPC path (e.g., /ipc)
	AgentRunID         string
	InstanceName       string
	AgentDisplayName   string
	AgentNamespace     string
	EventBus           eventbus.EventBus
	Log                logr.Logger
	Watcher            *Watcher
	agentDone          chan struct{} // signalled when result.json is received
	processedFiles     sync.Map      // dedup fsnotify Create+Write for the same file
	SuppressCompletion bool          // when true, do not publish TopicAgentRunCompleted (gate mode)

	// enforceOutboundChannels is true when the ALLOWED_OUTBOUND_CHANNELS env is
	// present (set by the controller). When true, outbound tool messages whose
	// channel is not in allowedOutboundChannels are dropped. An empty allowlist
	// means the agent has no configured channels and may not send at all.
	enforceOutboundChannels bool
	allowedOutboundChannels map[string]bool
}

// NewBridge creates a new IPC bridge.
func NewBridge(basePath, agentRunID, instanceName string, bus eventbus.EventBus, log logr.Logger, agentNamespace ...string) *Bridge {
	namespace := os.Getenv("AGENT_NAMESPACE")
	if len(agentNamespace) > 0 {
		namespace = agentNamespace[0]
	}
	allowedChannels, enforceChannels := parseAllowedOutboundChannels()
	return &Bridge{
		BasePath:                basePath,
		AgentRunID:              agentRunID,
		InstanceName:            instanceName,
		AgentDisplayName:        os.Getenv("AGENT_DISPLAY_NAME"),
		AgentNamespace:          namespace,
		EventBus:                bus,
		Log:                     log,
		agentDone:               make(chan struct{}),
		SuppressCompletion:      os.Getenv("GATE_SUPPRESS_COMPLETION") == "true",
		enforceOutboundChannels: enforceChannels,
		allowedOutboundChannels: allowedChannels,
	}
}

// parseAllowedOutboundChannels reads the ALLOWED_OUTBOUND_CHANNELS env. The
// second return is true when the variable is present at all (even if empty),
// which switches on enforcement; absence keeps the legacy allow-all behaviour
// for pods created by an older controller.
func parseAllowedOutboundChannels() (map[string]bool, bool) {
	raw, ok := os.LookupEnv("ALLOWED_OUTBOUND_CHANNELS")
	if !ok {
		return nil, false
	}
	set := make(map[string]bool)
	for _, c := range strings.Split(raw, ",") {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			set[c] = true
		}
	}
	return set, true
}

// metadata returns the standard event metadata for messages emitted by this bridge.
func (b *Bridge) metadata() map[string]string {
	metadata := map[string]string{
		"agentRunID":   b.AgentRunID,
		"instanceName": b.InstanceName,
	}
	if b.AgentNamespace != "" {
		metadata["namespace"] = b.AgentNamespace
	}
	return metadata
}

// Start initializes the IPC directory structure, starts file watchers,
// and subscribes to inbound events from the control plane.
func (b *Bridge) Start(ctx context.Context) error {
	b.Log.Info("Starting IPC bridge",
		"agentRunID", b.AgentRunID,
		"basePath", b.BasePath,
	)

	// Create IPC directory structure
	dirs := []string{DirInput, DirOutput, DirSpawn, DirTools, DirMessages, DirSchedules, DirPrompts, DirContext, DirGate}
	for _, dir := range dirs {
		path := filepath.Join(b.BasePath, dir)
		if err := os.MkdirAll(path, 0750); err != nil {
			return fmt.Errorf("creating IPC directory %s: %w", path, err)
		}
	}

	// Start file watcher
	watcher, err := NewWatcher(b.BasePath, b.Log)
	if err != nil {
		return fmt.Errorf("creating file watcher: %w", err)
	}
	b.Watcher = watcher

	// Watch for agent output
	go b.watchOutput(ctx)

	// Watch for spawn requests
	go b.watchSpawnRequests(ctx)

	// Watch for tool exec requests
	go b.watchToolRequests(ctx)

	// Watch for outbound messages
	go b.watchMessages(ctx)

	// Watch for schedule requests
	go b.watchSchedules(ctx)

	// Watch for sidecar-initiated LLM prompt requests.
	go b.watchPromptRequests(ctx)

	// Watch for prompt results emitted by the agent-runner (audit channel
	// — actual sidecar delivery is filesystem-direct).
	go b.watchPromptResults(ctx)

	// Watch for context-clear IPC so sidecars can reset conversation
	// history between independent units of work.
	go b.watchContextClear(ctx)

	// Subscribe to inbound events from the control plane
	go b.subscribeToInbound(ctx)

	// Wait for context cancellation or agent completion.
	select {
	case <-ctx.Done():
	case <-b.agentDone:
		// Agent wrote result.json — give NATS publish a moment to flush,
		// then exit so the Job can complete.
		b.Log.Info("Agent completed, bridge exiting after grace period")
		time.Sleep(2 * time.Second)
	}
	return watcher.Close()
}

// watchOutput watches /ipc/output/ for agent results and streams.
func (b *Bridge) watchOutput(ctx context.Context) {
	outputPath := filepath.Join(b.BasePath, DirOutput)
	events, err := b.Watcher.Watch(ctx, outputPath)
	if err != nil {
		b.Log.Error(err, "failed to watch output directory")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case fileEvent := <-events:
			b.handleOutputFile(ctx, fileEvent)
		}
	}
}

// handleOutputFile processes a file created in /ipc/output/.
func (b *Bridge) handleOutputFile(ctx context.Context, fe FileEvent) {
	// fsnotify fires both Create and Write for the same file; deduplicate.
	if _, loaded := b.processedFiles.LoadOrStore(fe.Path, true); loaded {
		return
	}

	data, err := os.ReadFile(fe.Path)
	if err != nil {
		b.Log.Error(err, "failed to read output file", "path", fe.Path)
		b.processedFiles.Delete(fe.Path) // allow retry on read error
		return
	}

	filename := filepath.Base(fe.Path)
	metadata := b.metadata()

	switch {
	case filename == "result.json":
		// Final result. When a response gate is configured the controller
		// publishes the completion event after the gate resolves, so the
		// bridge must suppress it here to prevent premature delivery.
		if b.SuppressCompletion {
			b.Log.Info("gate mode: suppressing completion event publication")
		} else {
			event, _ := eventbus.NewEvent(eventbus.TopicAgentRunCompleted, metadata, json.RawMessage(data))
			if err := b.EventBus.Publish(ctx, eventbus.TopicAgentRunCompleted, event); err != nil {
				b.Log.Error(err, "failed to publish completion event")
			}
		}
		// Signal that the agent is done so the bridge can exit.
		select {
		case b.agentDone <- struct{}{}:
		default:
		}

	case strings.HasPrefix(filename, "result-"):
		// A parked attempt's result (see AttemptResult). Not a completion: the
		// runner is still alive awaiting a verdict, and signalling agentDone
		// would tear the bridge down mid-chain. The controller reads the
		// attempt from the runner's stdout marker instead.
		b.Log.Info("parked attempt result observed; holding the bridge open", "file", filename)

	case filename == "status.json":
		// Status update
		event, _ := eventbus.NewEvent("agent.status.update", metadata, json.RawMessage(data))
		if err := b.EventBus.Publish(ctx, "agent.status.update", event); err != nil {
			b.Log.Error(err, "failed to publish status event")
		}

	case len(filename) > 7 && filename[:7] == "stream-":
		// Streaming chunk. When a response gate is configured, suppress
		// stream chunks to prevent the agent's output from leaking to the
		// feed before the gate approves it.
		if b.SuppressCompletion {
			b.Log.V(1).Info("gate mode: suppressing stream chunk")
		} else {
			event, _ := eventbus.NewEvent(eventbus.TopicAgentStreamChunk, metadata, json.RawMessage(data))
			if err := b.EventBus.Publish(ctx, eventbus.TopicAgentStreamChunk, event); err != nil {
				b.Log.Error(err, "failed to publish stream chunk")
			}
		}
	}
}

// watchSpawnRequests watches /ipc/spawn/ for sub-agent spawn requests.
func (b *Bridge) watchSpawnRequests(ctx context.Context) {
	spawnPath := filepath.Join(b.BasePath, DirSpawn)
	events, err := b.Watcher.Watch(ctx, spawnPath)
	if err != nil {
		b.Log.Error(err, "failed to watch spawn directory")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case fe := <-events:
			b.handleSpawnRequest(ctx, fe)
		}
	}
}

// handleSpawnRequest processes a spawn request file. It routes persona delegation
// requests (request-*.json) and ad-hoc subagent requests (subagent-request-*.json)
// to their respective event bus topics.
func (b *Bridge) handleSpawnRequest(ctx context.Context, fe FileEvent) {
	// fsnotify fires both Create and Write for the same file; deduplicate.
	if _, loaded := b.processedFiles.LoadOrStore(fe.Path, true); loaded {
		return
	}

	data, err := os.ReadFile(fe.Path)
	if err != nil {
		b.Log.Error(err, "failed to read spawn request", "path", fe.Path)
		b.processedFiles.Delete(fe.Path)
		return
	}

	metadata := b.metadata()

	// Route by a positive filename allowlist. The bridge also writes its own
	// result-*.json and subagent-result-*.json files into this same directory,
	// so a catch-all "anything not a subagent request is a spawn" branch would
	// re-publish every delegation result as a bogus spawn request. Only files
	// the agent explicitly writes as spawn requests are forwarded.
	base := filepath.Base(fe.Path)
	switch {
	case strings.HasPrefix(base, "subagent-request-"):
		event, _ := eventbus.NewEvent(eventbus.TopicAgentSubagentRequest, metadata, json.RawMessage(data))
		if err := b.EventBus.Publish(ctx, eventbus.TopicAgentSubagentRequest, event); err != nil {
			b.Log.Error(err, "failed to publish subagent spawn request")
		}
		b.Log.Info("Forwarded subagent spawn request to control plane")
	case strings.HasPrefix(base, "request-"):
		event, _ := eventbus.NewEvent(eventbus.TopicAgentSpawnRequest, metadata, json.RawMessage(data))
		if err := b.EventBus.Publish(ctx, eventbus.TopicAgentSpawnRequest, event); err != nil {
			b.Log.Error(err, "failed to publish spawn request")
		}
		b.Log.Info("Forwarded spawn request to control plane")
	default:
		// result-*.json / subagent-result-*.json and any other file are not
		// spawn requests — ignore them.
		b.Log.V(1).Info("ignoring non-spawn file in spawn dir", "file", base)
	}
}

// watchToolRequests watches /ipc/tools/ for exec requests.
func (b *Bridge) watchToolRequests(ctx context.Context) {
	toolsPath := filepath.Join(b.BasePath, DirTools)
	events, err := b.Watcher.Watch(ctx, toolsPath)
	if err != nil {
		b.Log.Error(err, "failed to watch tools directory")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case fe := <-events:
			filename := filepath.Base(fe.Path)
			if len(filename) > 12 && filename[:12] == "exec-request" {
				b.handleExecRequest(ctx, fe)
			}
		}
	}
}

// handleExecRequest processes an exec request and runs it in the sandbox sidecar.
func (b *Bridge) handleExecRequest(ctx context.Context, fe FileEvent) {
	// fsnotify fires both Create and Write for the same file; deduplicate.
	if _, loaded := b.processedFiles.LoadOrStore(fe.Path, true); loaded {
		return
	}

	data, err := os.ReadFile(fe.Path)
	if err != nil {
		b.Log.Error(err, "failed to read exec request", "path", fe.Path)
		b.processedFiles.Delete(fe.Path)
		return
	}

	metadata := b.metadata()

	event, _ := eventbus.NewEvent(eventbus.TopicToolExecRequest, metadata, json.RawMessage(data))
	if err := b.EventBus.Publish(ctx, eventbus.TopicToolExecRequest, event); err != nil {
		b.Log.Error(err, "failed to publish exec request")
	}
}

// watchMessages watches /ipc/messages/ for outbound channel messages.
func (b *Bridge) watchMessages(ctx context.Context) {
	messagesPath := filepath.Join(b.BasePath, DirMessages)
	events, err := b.Watcher.Watch(ctx, messagesPath)
	if err != nil {
		b.Log.Error(err, "failed to watch messages directory")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case fe := <-events:
			b.handleOutboundMessage(ctx, fe)
		}
	}
}

// handleOutboundMessage processes an outbound message to a channel.
func (b *Bridge) handleOutboundMessage(ctx context.Context, fe FileEvent) {
	// fsnotify fires both Create and Write for the same file; deduplicate.
	if _, loaded := b.processedFiles.LoadOrStore(fe.Path, true); loaded {
		return
	}

	data, err := os.ReadFile(fe.Path)
	if err != nil {
		b.Log.Error(err, "failed to read outbound message", "path", fe.Path)
		b.processedFiles.Delete(fe.Path)
		return
	}

	metadata := b.metadata()

	// Enforce the outbound-channel allowlist before doing anything else: agents
	// are adversarial, and the send_channel_message tool otherwise lets them
	// deliver arbitrary text to any channel type. Replies to inbound messages
	// travel via result.json/TopicAgentRunCompleted, not this path, so they are
	// unaffected.
	if !b.outboundChannelAllowed(data) {
		b.Log.Info("Dropping outbound message to channel not configured on this agent",
			"path", fe.Path, "channel", outboundChannelName(data))
		return
	}

	sanitized, dropped, err := b.sanitizeOutboundMessage(data)
	if err != nil {
		b.Log.Error(err, "dropping invalid outbound message", "path", fe.Path)
		b.processedFiles.Delete(fe.Path)
		return
	}
	if len(dropped) > 0 {
		b.Log.Info("Dropped outbound message attribution not matching agent identity",
			"path", fe.Path,
			"allowedUsername", b.allowedOutboundUsername(),
			"fields", strings.Join(dropped, ","))
	}

	event, _ := eventbus.NewEvent(eventbus.TopicChannelMessageSend, metadata, sanitized)
	if err := b.EventBus.Publish(ctx, eventbus.TopicChannelMessageSend, event); err != nil {
		b.Log.Error(err, "failed to publish outbound message")
	}
}

// outboundChannelAllowed reports whether an outbound tool message may be
// delivered given the agent's configured channel allowlist. When enforcement is
// off (pods from an older controller) every channel is allowed.
func (b *Bridge) outboundChannelAllowed(data []byte) bool {
	if !b.enforceOutboundChannels {
		return true
	}
	ch := strings.ToLower(strings.TrimSpace(outboundChannelName(data)))
	return ch != "" && b.allowedOutboundChannels[ch]
}

// outboundChannelName extracts the "channel" field from a raw outbound message
// without disturbing the rest of the payload.
func outboundChannelName(data []byte) string {
	var p struct {
		Channel string `json:"channel"`
	}
	_ = json.Unmarshal(data, &p)
	return p.Channel
}

func (b *Bridge) sanitizeOutboundMessage(data []byte) (json.RawMessage, []string, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, err
	}

	dropped := make([]string, 0, 3)
	if raw, ok := payload["username"]; ok && !b.allowedOutboundUsernameValue(raw) {
		delete(payload, "username")
		dropped = append(dropped, "username")
	}
	for _, field := range []string{"iconUrl", "iconEmoji"} {
		if _, ok := payload[field]; ok {
			delete(payload, field)
			dropped = append(dropped, field)
		}
	}

	sanitized, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	return json.RawMessage(sanitized), dropped, nil
}

func (b *Bridge) allowedOutboundUsernameValue(raw json.RawMessage) bool {
	allowed := b.allowedOutboundUsername()
	if allowed == "" {
		return false
	}
	var username string
	if err := json.Unmarshal(raw, &username); err != nil {
		return false
	}
	return username == allowed
}

func (b *Bridge) allowedOutboundUsername() string {
	if name := strings.TrimSpace(b.AgentDisplayName); name != "" {
		return name
	}
	return strings.TrimSpace(b.InstanceName)
}

// watchSchedules watches /ipc/schedules/ for schedule task requests.
func (b *Bridge) watchSchedules(ctx context.Context) {
	schedulesPath := filepath.Join(b.BasePath, DirSchedules)
	events, err := b.Watcher.Watch(ctx, schedulesPath)
	if err != nil {
		b.Log.Error(err, "failed to watch schedules directory")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case fe := <-events:
			b.handleScheduleRequest(ctx, fe)
		}
	}
}

// handleScheduleRequest processes a schedule request file.
func (b *Bridge) handleScheduleRequest(ctx context.Context, fe FileEvent) {
	// fsnotify fires both Create and Write for the same file; deduplicate.
	if _, loaded := b.processedFiles.LoadOrStore(fe.Path, true); loaded {
		return
	}

	data, err := os.ReadFile(fe.Path)
	if err != nil {
		b.Log.Error(err, "failed to read schedule request", "path", fe.Path)
		b.processedFiles.Delete(fe.Path)
		return
	}

	metadata := b.metadata()

	event, _ := eventbus.NewEvent(eventbus.TopicScheduleUpsert, metadata, json.RawMessage(data))
	if err := b.EventBus.Publish(ctx, eventbus.TopicScheduleUpsert, event); err != nil {
		b.Log.Error(err, "failed to publish schedule request")
	}

	b.Log.Info("Forwarded schedule request to control plane")
}

// watchPromptRequests watches /ipc/prompts/ for sidecar-issued LLM prompt
// requests. Sidecars and the agent-runner share the same /ipc volume, so
// direct filesystem delivery is used for in-pod communication; the bridge's
// role here is auditing/observability: every request is republished as a
// per-run NATS event so the control plane can record who asked the model
// what. Result delivery is from agent-runner back to the sidecar via the
// file system — agent-runner writes /ipc/prompts/result-{RequestID}.json,
// and the sidecar polls for it.
func (b *Bridge) watchPromptRequests(ctx context.Context) {
	promptsPath := filepath.Join(b.BasePath, DirPrompts)
	events, err := b.Watcher.Watch(ctx, promptsPath)
	if err != nil {
		b.Log.Error(err, "failed to watch prompts directory")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case fe := <-events:
			filename := filepath.Base(fe.Path)
			// Skip tmp files: see watchContextClear for rationale.
			if strings.HasPrefix(filename, "request-") && !strings.HasSuffix(filename, ".tmp") {
				b.handlePromptRequest(ctx, fe)
			}
		}
	}
}

// handlePromptRequest validates the request ID is path-safe and publishes an
// audit event. The agent-runner picks up the request file directly because
// the two processes share the /ipc volume — this branch only emits the
// observability record.
func (b *Bridge) handlePromptRequest(ctx context.Context, fe FileEvent) {
	if _, loaded := b.processedFiles.LoadOrStore(fe.Path, true); loaded {
		return
	}

	data, err := os.ReadFile(fe.Path)
	if err != nil {
		b.Log.Error(err, "failed to read prompt request", "path", fe.Path)
		b.processedFiles.Delete(fe.Path)
		return
	}

	var req PromptRequest
	if err := json.Unmarshal(data, &req); err != nil {
		b.Log.Error(err, "dropping invalid prompt request", "path", fe.Path)
		return
	}
	if !isSafeIPCID(req.RequestID) {
		b.Log.Error(fmt.Errorf("unsafe requestId"), "dropping prompt request with path-unsafe requestId", "requestId", req.RequestID)
		return
	}

	metadata := b.metadata()
	topic := fmt.Sprintf("%s.%s", eventbus.TopicPromptRequest, b.AgentRunID)
	event, _ := eventbus.NewEvent(topic, metadata, json.RawMessage(data))
	if err := b.EventBus.Publish(ctx, topic, event); err != nil {
		b.Log.Error(err, "failed to publish prompt request")
		return
	}
	b.Log.Info("Forwarded sidecar prompt request", "requestId", req.RequestID)
}

// watchPromptResults watches /ipc/prompts/ for /ipc/prompts/result-*.json
// files written by the agent-runner. The bridge forwards each as a per-run
// NATS event for the control plane to log latency/usage, but the actual
// delivery to the sidecar happens in-process — the sidecar polls the same
// /ipc/prompts directory it wrote its request into and reads the matching
// result-*.json file when it appears.
func (b *Bridge) watchPromptResults(ctx context.Context) {
	promptsPath := filepath.Join(b.BasePath, DirPrompts)
	events, err := b.Watcher.Watch(ctx, promptsPath)
	if err != nil {
		b.Log.Error(err, "failed to watch prompts directory for results")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case fe := <-events:
			filename := filepath.Base(fe.Path)
			// Skip tmp files: see watchContextClear for rationale.
			if strings.HasPrefix(filename, "result-") && !strings.HasSuffix(filename, ".tmp") {
				b.handlePromptResultAudit(ctx, fe)
			}
		}
	}
}

// handlePromptResultAudit publishes a NATS event for the agent-runner's
// answer to a sidecar prompt request. The actual delivery to the waiting
// sidecar happens via the filesystem file the agent-runner dropped into
// /ipc/prompts/result-{id}.json.
func (b *Bridge) handlePromptResultAudit(ctx context.Context, fe FileEvent) {
	if _, loaded := b.processedFiles.LoadOrStore(fe.Path, true); loaded {
		return
	}

	data, err := os.ReadFile(fe.Path)
	if err != nil {
		b.Log.Error(err, "failed to read prompt result", "path", fe.Path)
		b.processedFiles.Delete(fe.Path)
		return
	}

	var res PromptResult
	if err := json.Unmarshal(data, &res); err != nil {
		b.Log.Error(err, "dropping invalid prompt result", "path", fe.Path)
		return
	}
	if !isSafeIPCID(res.RequestID) {
		b.Log.Error(fmt.Errorf("unsafe requestId"), "dropping prompt result with path-unsafe requestId", "requestId", res.RequestID)
		return
	}

	metadata := b.metadata()
	topic := fmt.Sprintf("%s.%s", eventbus.TopicPromptResult, b.AgentRunID)
	event, _ := eventbus.NewEvent(topic, metadata, json.RawMessage(data))
	if err := b.EventBus.Publish(ctx, topic, event); err != nil {
		b.Log.Error(err, "failed to publish prompt result")
		return
	}
	b.Log.Info("Forwarded prompt result", "requestId", res.RequestID)
}

// watchContextClear watches /ipc/context/ for sidecar clear-context IPC.
// The bridge consumes the file (no result delivery is needed) and publishes
// a single per-run context.clear event so the agent-runner resets its
// conversation history. Fire-and-forget.
func (b *Bridge) watchContextClear(ctx context.Context) {
	contextPath := filepath.Join(b.BasePath, DirContext)
	events, err := b.Watcher.Watch(ctx, contextPath)
	if err != nil {
		b.Log.Error(err, "failed to watch context directory")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case fe := <-events:
			filename := filepath.Base(fe.Path)
			// Skip tmp files: the sidecar's writeJsonAtomic helper writes
			// {path}.tmp then renames to {path}. fsnotify reports both Create
			// events, and reading the .tmp before the rename returns
			// "no such file" because the rename already happened.
			if strings.HasPrefix(filename, "clear-") && !strings.HasSuffix(filename, ".tmp") {
				b.handleContextClear(ctx, fe)
			}
		}
	}
}

// handleContextClear publishes the per-run context.clear event consumed by
// the agent-runner. No result file is written; the sidecar already considers
// the operation fire-and-forget.
func (b *Bridge) handleContextClear(ctx context.Context, fe FileEvent) {
	if _, loaded := b.processedFiles.LoadOrStore(fe.Path, true); loaded {
		return
	}

	data, err := os.ReadFile(fe.Path)
	if err != nil {
		b.Log.Error(err, "failed to read context clear request", "path", fe.Path)
		b.processedFiles.Delete(fe.Path)
		return
	}

	metadata := b.metadata()
	topic := fmt.Sprintf("%s.%s", eventbus.TopicContextClear, b.AgentRunID)
	event, _ := eventbus.NewEvent(topic, metadata, json.RawMessage(data))
	if err := b.EventBus.Publish(ctx, topic, event); err != nil {
		b.Log.Error(err, "failed to publish context clear event")
		return
	}
	b.Log.Info("Forwarded context clear request")
}

// subscribeToInbound subscribes to events from the control plane and
// writes them as files for the agent container to consume.
func (b *Bridge) subscribeToInbound(ctx context.Context) {
	// Subscribe to follow-up messages
	followupCh, err := b.EventBus.Subscribe(ctx, fmt.Sprintf("agent.followup.%s", b.AgentRunID))
	if err != nil {
		b.Log.Error(err, "failed to subscribe to follow-up events")
		return
	}

	// Subscribe to tool exec results
	execResultCh, err := b.EventBus.Subscribe(ctx, fmt.Sprintf("tool.exec.result.%s", b.AgentRunID))
	if err != nil {
		b.Log.Error(err, "failed to subscribe to exec result events")
		return
	}

	// Subscribe to delegate results (blocking delegation tool polls for these files)
	delegateResultCh, err := b.EventBus.Subscribe(ctx, fmt.Sprintf("%s.%s", eventbus.TopicAgentDelegateResult, b.AgentRunID))
	if err != nil {
		b.Log.Error(err, "failed to subscribe to delegate result events")
	}

	// Subscribe to subagent batch results (spawn_subagents tool polls for these files)
	subagentResultCh, err := b.EventBus.Subscribe(ctx, fmt.Sprintf("%s.%s", eventbus.TopicAgentSubagentResult, b.AgentRunID))
	if err != nil {
		b.Log.Error(err, "failed to subscribe to subagent result events")
	}

	// Subscribe to gate verdicts for a parked run. The agent-runner polls
	// /ipc/gate/ for the file written below.
	gateVerdictCh, err := b.EventBus.Subscribe(ctx, fmt.Sprintf("%s.%s", eventbus.TopicGateVerdict, b.AgentRunID))
	if err != nil {
		b.Log.Error(err, "failed to subscribe to gate verdict events")
	}

	for {
		select {
		case <-ctx.Done():
			return

		case event := <-followupCh:
			// Write follow-up message to /ipc/input/
			filename := fmt.Sprintf("followup-%d.json", time.Now().UnixNano())
			path := filepath.Join(b.BasePath, DirInput, filename)
			if err := os.WriteFile(path, event.Data, 0640); err != nil {
				b.Log.Error(err, "failed to write follow-up message")
			}

		case event := <-execResultCh:
			// Write exec result to /ipc/tools/
			filename := fmt.Sprintf("exec-result-%d.json", time.Now().UnixNano())
			path := filepath.Join(b.BasePath, DirTools, filename)
			if err := os.WriteFile(path, event.Data, 0640); err != nil {
				b.Log.Error(err, "failed to write exec result")
			}

		case event := <-delegateResultCh:
			// Write delegate result to /ipc/spawn/result-{requestId}.json so
			// the blocking delegateToPersonaTool can pick it up.
			var parsed struct {
				RequestID string `json:"requestId"`
			}
			if json.Unmarshal(event.Data, &parsed) == nil && parsed.RequestID != "" {
				if !isSafeIPCID(parsed.RequestID) {
					b.Log.Error(fmt.Errorf("unsafe requestId"), "rejecting delegate result with path-unsafe requestId", "requestId", parsed.RequestID)
					continue
				}
				filename := fmt.Sprintf("result-%s.json", parsed.RequestID)
				path := filepath.Join(b.BasePath, DirSpawn, filename)
				if err := os.WriteFile(path, event.Data, 0640); err != nil {
					b.Log.Error(err, "failed to write delegate result", "requestId", parsed.RequestID)
				} else {
					b.Log.Info("Wrote delegate result", "requestId", parsed.RequestID)
				}
			}

		case event := <-subagentResultCh:
			// Write subagent batch result to /ipc/spawn/subagent-result-{batchId}.json
			// so the blocking spawn_subagents tool can pick it up.
			var parsed struct {
				BatchID string `json:"batchId"`
			}
			if json.Unmarshal(event.Data, &parsed) == nil && parsed.BatchID != "" {
				if !isSafeIPCID(parsed.BatchID) {
					b.Log.Error(fmt.Errorf("unsafe batchId"), "rejecting subagent result with path-unsafe batchId", "batchId", parsed.BatchID)
					continue
				}
				filename := fmt.Sprintf("subagent-result-%s.json", parsed.BatchID)
				path := filepath.Join(b.BasePath, DirSpawn, filename)
				if err := os.WriteFile(path, event.Data, 0640); err != nil {
					b.Log.Error(err, "failed to write subagent batch result", "batchId", parsed.BatchID)
				} else {
					b.Log.Info("Wrote subagent batch result", "batchId", parsed.BatchID)
				}
			}

		case event := <-gateVerdictCh:
			// Write the gate verdict to /ipc/gate/verdict-{attempt}.json.
			// Unlike the delegate/subagent paths the filename component is an
			// integer from status.attempt, never agent-supplied, so there is no
			// traversal to guard — but a malformed payload would make a file
			// the runner can never match, so drop it.
			var parsed struct {
				Attempt int `json:"attempt"`
			}
			if err := json.Unmarshal(event.Data, &parsed); err != nil || parsed.Attempt < 1 {
				b.Log.Error(err, "dropping gate verdict with no usable attempt number")
				continue
			}
			filename := fmt.Sprintf("verdict-%d.json", parsed.Attempt)
			path := filepath.Join(b.BasePath, DirGate, filename)
			if err := os.WriteFile(path, event.Data, 0640); err != nil {
				b.Log.Error(err, "failed to write gate verdict", "attempt", parsed.Attempt)
			} else {
				b.Log.Info("Wrote gate verdict", "attempt", parsed.Attempt)
			}
		}
	}
}
