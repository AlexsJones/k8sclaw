// Package ipc implements the IPC bridge sidecar that mediates communication
// between ephemeral agent pods and the durable KubeClaw control plane.
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
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/kubeclaw/kubeclaw/internal/eventbus"
)

// IPCDir layout constants matching the design doc protocol.
const (
	DirInput    = "input"
	DirOutput   = "output"
	DirSpawn    = "spawn"
	DirTools    = "tools"
	DirMessages = "messages"
)

// Bridge is the IPC bridge sidecar process.
type Bridge struct {
	BasePath       string // Root IPC path (e.g., /ipc)
	AgentRunID     string
	InstanceName   string
	EventBus       eventbus.EventBus
	Log            logr.Logger
	Watcher        *Watcher
	agentDone      chan struct{} // signalled when result.json is received
	processedFiles sync.Map     // dedup fsnotify Create+Write for the same file
}

// NewBridge creates a new IPC bridge.
func NewBridge(basePath, agentRunID, instanceName string, bus eventbus.EventBus, log logr.Logger) *Bridge {
	return &Bridge{
		BasePath:     basePath,
		AgentRunID:   agentRunID,
		InstanceName: instanceName,
		EventBus:     bus,
		Log:          log,
		agentDone:    make(chan struct{}),
	}
}

// Start initializes the IPC directory structure, starts file watchers,
// and subscribes to inbound events from the control plane.
func (b *Bridge) Start(ctx context.Context) error {
	b.Log.Info("Starting IPC bridge",
		"agentRunID", b.AgentRunID,
		"basePath", b.BasePath,
	)

	// Create IPC directory structure
	dirs := []string{DirInput, DirOutput, DirSpawn, DirTools, DirMessages}
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
	metadata := map[string]string{
		"agentRunID":   b.AgentRunID,
		"instanceName": b.InstanceName,
	}

	switch {
	case filename == "result.json":
		// Final result
		event, _ := eventbus.NewEvent(eventbus.TopicAgentRunCompleted, metadata, json.RawMessage(data))
		if err := b.EventBus.Publish(ctx, eventbus.TopicAgentRunCompleted, event); err != nil {
			b.Log.Error(err, "failed to publish completion event")
		}
		// Signal that the agent is done so the bridge can exit.
		select {
		case b.agentDone <- struct{}{}:
		default:
		}

	case filename == "status.json":
		// Status update
		event, _ := eventbus.NewEvent("agent.status.update", metadata, json.RawMessage(data))
		if err := b.EventBus.Publish(ctx, "agent.status.update", event); err != nil {
			b.Log.Error(err, "failed to publish status event")
		}

	case len(filename) > 7 && filename[:7] == "stream-":
		// Streaming chunk
		event, _ := eventbus.NewEvent(eventbus.TopicAgentStreamChunk, metadata, json.RawMessage(data))
		if err := b.EventBus.Publish(ctx, eventbus.TopicAgentStreamChunk, event); err != nil {
			b.Log.Error(err, "failed to publish stream chunk")
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

// handleSpawnRequest processes a spawn request file.
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

	metadata := map[string]string{
		"agentRunID":   b.AgentRunID,
		"instanceName": b.InstanceName,
	}

	event, _ := eventbus.NewEvent(eventbus.TopicAgentSpawnRequest, metadata, json.RawMessage(data))
	if err := b.EventBus.Publish(ctx, eventbus.TopicAgentSpawnRequest, event); err != nil {
		b.Log.Error(err, "failed to publish spawn request")
	}

	b.Log.Info("Forwarded spawn request to control plane")
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

	metadata := map[string]string{
		"agentRunID":   b.AgentRunID,
		"instanceName": b.InstanceName,
	}

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

	metadata := map[string]string{
		"agentRunID":   b.AgentRunID,
		"instanceName": b.InstanceName,
	}

	event, _ := eventbus.NewEvent(eventbus.TopicChannelMessageSend, metadata, json.RawMessage(data))
	if err := b.EventBus.Publish(ctx, eventbus.TopicChannelMessageSend, event); err != nil {
		b.Log.Error(err, "failed to publish outbound message")
	}
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
		}
	}
}
