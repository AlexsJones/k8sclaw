package webproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/sessionkey"
)

// JSONRPC types for MCP protocol.

// JSONRPCRequest is a JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is a JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError is a JSON-RPC 2.0 error.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpSession tracks an SSE connection for MCP.
type mcpSession struct {
	id     string
	events chan []byte
	done   chan struct{}
}

var (
	mcpSessionsMu sync.Mutex
	mcpSessions   = make(map[string]*mcpSession)
)

// handleMCPSSE opens an SSE connection for MCP and sends the endpoint event.
func (p *Proxy) handleMCPSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	sessionID := fmt.Sprintf("mcp-%d", time.Now().UnixNano())
	session := &mcpSession{
		id:     sessionID,
		events: make(chan []byte, 64),
		done:   make(chan struct{}),
	}

	mcpSessionsMu.Lock()
	mcpSessions[sessionID] = session
	mcpSessionsMu.Unlock()

	defer func() {
		mcpSessionsMu.Lock()
		delete(mcpSessions, sessionID)
		mcpSessionsMu.Unlock()
		close(session.done)
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// Send the endpoint event telling the client where to POST messages
	endpointURL := fmt.Sprintf("/message?sessionId=%s", sessionID)
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointURL)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-session.events:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handleMCPMessage handles JSON-RPC requests from MCP clients.
func (p *Proxy) handleMCPMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")

	mcpSessionsMu.Lock()
	session, ok := mcpSessions[sessionID]
	mcpSessionsMu.Unlock()

	if !ok {
		writeError(w, http.StatusBadRequest, "invalid or expired session")
		return
	}

	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON-RPC request: "+err.Error())
		return
	}

	var resp JSONRPCResponse
	resp.JSONRPC = "2.0"
	resp.ID = req.ID

	switch req.Method {
	case "initialize":
		resp.Result = map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
			"serverInfo": map[string]interface{}{
				"name":    "sympozium-agent",
				"version": "1.0.0",
			},
		}

	case "tools/list":
		inst, err := p.getAgent(r.Context())
		if err != nil {
			resp.Error = &JSONRPCError{Code: -32603, Message: err.Error()}
			break
		}
		resp.Result = map[string]interface{}{
			"tools": []map[string]interface{}{
				{
					"name":        "ask_agent",
					"description": fmt.Sprintf("Send a task to the %s agent", inst.Name),
					"inputSchema": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"task": map[string]interface{}{
								"type":        "string",
								"description": "The task or question for the agent",
							},
						},
						"required": []string{"task"},
					},
				},
			},
		}

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &JSONRPCError{Code: -32602, Message: "invalid params"}
			break
		}

		var args struct {
			Task string `json:"task"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			resp.Error = &JSONRPCError{Code: -32602, Message: "invalid arguments"}
			break
		}

		result, err := p.executeAgentTask(r.Context(), args.Task, session)
		if err != nil {
			resp.Error = &JSONRPCError{Code: -32603, Message: err.Error()}
			break
		}
		resp.Result = map[string]interface{}{
			"content": []map[string]interface{}{
				{
					"type": "text",
					"text": result,
				},
			},
		}

	case "completion/complete":
		resp.Result = map[string]interface{}{
			"completion": map[string]interface{}{
				"values": []string{},
			},
		}

	default:
		resp.Error = &JSONRPCError{Code: -32601, Message: "method not found: " + req.Method}
	}

	// Send response over the SSE connection
	data, _ := json.Marshal(resp)
	select {
	case session.events <- data:
	default:
		p.log.Info("MCP session buffer full, dropping response")
	}

	// Also respond to the POST request with 202 Accepted
	w.WriteHeader(http.StatusAccepted)
}

// executeAgentTask creates an AgentRun and waits for the result.
func (p *Proxy) executeAgentTask(ctx context.Context, task string, session *mcpSession) (string, error) {
	inst, err := p.getAgent(ctx)
	if err != nil {
		return "", err
	}

	provider := resolveProvider(inst)
	authSecret := resolveAuthSecret(inst)

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: inst.Name + "-mcp-",
			Namespace:    inst.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance": inst.Name,
				"sympozium.ai/source":   "web-proxy",
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   inst.Name,
			AgentID:    "primary",
			SessionKey: sessionkey.ForMCP(inst.Name, session.id),
			Task:       sympoziumv1alpha1.NewStringTask(task),
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:                 provider,
				Model:                    inst.Spec.Agents.Default.Model,
				BaseURL:                  inst.Spec.Agents.Default.BaseURL,
				AuthSecretRef:            authSecret,
				ProviderHeaders:          inst.Spec.Agents.Default.ProviderHeaders,
				ProviderHeadersSecretRef: inst.Spec.Agents.Default.ProviderHeadersSecretRef,
			},
			Skills:           inst.Spec.Skills,
			Timeout:          inst.Spec.Agents.Default.ParseRunTimeout(),
			ImagePullSecrets: inst.Spec.ImagePullSecrets,
			Lifecycle:        inst.Spec.Agents.Default.Lifecycle,
			Env:              inst.Spec.Agents.Default.Env,
		},
	}

	if err := p.k8s.Create(ctx, run); err != nil {
		return "", fmt.Errorf("failed to create agent run: %w", err)
	}

	p.log.Info("Created AgentRun from MCP request", "run", run.Name, "instance", inst.Name)

	// Wait for completion
	completedCh, err := p.eventBus.Subscribe(ctx, eventbus.TopicAgentRunCompleted)
	if err != nil {
		return "", err
	}
	failedCh, err := p.eventBus.Subscribe(ctx, eventbus.TopicAgentRunFailed)
	if err != nil {
		return "", err
	}

	timeout := time.After(10 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout:
			return "", fmt.Errorf("agent run timed out")
		case event := <-completedCh:
			if event.Metadata["agentRunID"] != run.Name {
				continue
			}
			var result struct {
				Response string `json:"response"`
			}
			_ = json.Unmarshal(event.Data, &result)
			if result.Response == "" {
				return "(no response)", nil
			}
			return result.Response, nil
		case event := <-failedCh:
			if event.Metadata["agentRunID"] != run.Name {
				continue
			}
			var result struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(event.Data, &result)
			return "", fmt.Errorf("agent run failed: %s", result.Error)
		}
	}
}

// sendProgressNotification sends a progress notification over the MCP SSE connection.
func (p *Proxy) sendProgressNotification(session *mcpSession, content string) {
	notification := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/progress",
		"params": map[string]interface{}{
			"progressToken": "agent-run",
			"progress":      0,
			"total":         1,
		},
	}
	_ = content // used in notification context
	data, _ := json.Marshal(notification)
	select {
	case session.events <- data:
	default:
	}
}

// listAgents is unused but kept for potential future use.
func (p *Proxy) listAgentsMCP(ctx context.Context) ([]string, error) {
	instances, err := p.listAgents(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, inst := range instances {
		names = append(names, inst.Name)
	}
	return names, nil
}

// resolveSystemPrompt extracts the system prompt from instance config.
func resolveSystemPrompt(inst *sympoziumv1alpha1.Agent) string {
	if inst.Spec.Memory != nil {
		return inst.Spec.Memory.SystemPrompt
	}
	return ""
}

// buildSessionKey creates a unique session key for MCP requests.
func buildSessionKey(instanceName string) string {
	return fmt.Sprintf("mcp-%s-%d", instanceName, time.Now().UnixNano())
}

// formatMCPToolResult formats an agent response for MCP tool call results.
func formatMCPToolResult(response string) []map[string]interface{} {
	return []map[string]interface{}{
		{
			"type": "text",
			"text": strings.TrimSpace(response),
		},
	}
}
