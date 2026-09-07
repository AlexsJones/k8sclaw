package webproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/sessionkey"
)

// ChatCompletionRequest is the OpenAI-compatible chat completions request.
type ChatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`
}

// ChatMessage represents a message in the conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionResponse is the OpenAI-compatible response.
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   *UsageInfo             `json:"usage,omitempty"`
}

// ChatCompletionChoice is a single choice in the response.
type ChatCompletionChoice struct {
	Index        int          `json:"index"`
	Message      *ChatMessage `json:"message,omitempty"`
	Delta        *ChatMessage `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason"`
}

// UsageInfo reports token usage.
type UsageInfo struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ModelsResponse lists available models.
type ModelsResponse struct {
	Object string       `json:"object"`
	Data   []ModelEntry `json:"data"`
}

// ModelEntry is a single model in the models list.
type ModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (p *Proxy) handleListModels(w http.ResponseWriter, r *http.Request) {
	inst, err := p.getAgent(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get instance: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ModelsResponse{
		Object: "list",
		Data: []ModelEntry{
			{
				ID:      inst.Spec.Agents.Default.Model,
				Object:  "model",
				Created: inst.CreationTimestamp.Unix(),
				OwnedBy: "sympozium",
			},
		},
	})
}

func (p *Proxy) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}

	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages array is required")
		return
	}

	// Extract task from messages: last user message is the task,
	// system messages become the system prompt.
	var systemParts []string
	var task string
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, msg.Content)
		case "user":
			task = msg.Content
		}
	}
	if task == "" {
		writeError(w, http.StatusBadRequest, "no user message found")
		return
	}

	ctx := r.Context()
	inst, err := p.getAgent(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get instance: "+err.Error())
		return
	}

	// Build system prompt: instance memory system prompt + request system messages
	systemPrompt := ""
	if inst.Spec.Memory != nil && inst.Spec.Memory.SystemPrompt != "" {
		systemParts = append([]string{inst.Spec.Memory.SystemPrompt}, systemParts...)
	}
	if len(systemParts) > 0 {
		systemPrompt = strings.Join(systemParts, "\n\n")
	}

	// Resolve provider and auth
	provider := resolveProvider(inst)
	authSecret := resolveAuthSecret(inst)
	requestHash := webRequestHash(r.Header.Get("Idempotency-Key"), inst.Name, inst.Spec.Agents.Default.Model, systemPrompt, task)

	// Subscribe to run lifecycle events BEFORE checking for an existing run.
	// This eliminates the race where a run completes between the lookup and
	// the subscribe, which would cause the response to hang.
	completedCh, err := p.eventBus.Subscribe(ctx, eventbus.TopicAgentRunCompleted)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to subscribe: "+err.Error())
		return
	}
	failedCh, err := p.eventBus.Subscribe(ctx, eventbus.TopicAgentRunFailed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to subscribe: "+err.Error())
		return
	}

	if existing, err := p.findRecentWebRun(ctx, inst.Namespace, inst.Name, requestHash, webDedupeWindow); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check duplicate agent run: "+err.Error())
		return
	} else if existing != nil {
		p.log.Info("Reusing AgentRun for duplicate web request", "run", existing.Name, "instance", inst.Name, "requestHash", requestHash)
		if req.Stream {
			p.streamResponse(w, r, existing.Name, completedCh, failedCh)
		} else {
			p.blockingResponse(w, r, existing.Name, inst.Spec.Agents.Default.Model, completedCh, failedCh)
		}
		return
	}

	// Filter out the web-endpoint skill so child AgentRuns don't inherit
	// requiresServer=true (which would make them Deployments instead of Jobs).
	var childSkills []sympoziumv1alpha1.SkillRef
	for _, s := range inst.Spec.Skills {
		if s.SkillPackRef != "web-endpoint" {
			childSkills = append(childSkills, s)
		}
	}

	// Create AgentRun
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: inst.Name + "-web-",
			Namespace:    inst.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance":     inst.Name,
				"sympozium.ai/source":       "web-proxy",
				"sympozium.ai/request-hash": requestHash,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:     inst.Name,
			AgentID:      "primary",
			SessionKey:   sessionkey.ForWebProxy(inst.Name, requestHash),
			Task:         sympoziumv1alpha1.NewStringTask(task),
			SystemPrompt: systemPrompt,
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:                 provider,
				Model:                    inst.Spec.Agents.Default.Model,
				BaseURL:                  inst.Spec.Agents.Default.BaseURL,
				AuthSecretRef:            authSecret,
				ProviderHeaders:          inst.Spec.Agents.Default.ProviderHeaders,
				ProviderHeadersSecretRef: inst.Spec.Agents.Default.ProviderHeadersSecretRef,
				NodeSelector:             inst.Spec.Agents.Default.NodeSelector,
			},
			Skills:           childSkills,
			Timeout:          inst.Spec.Agents.Default.ParseRunTimeout(),
			ImagePullSecrets: inst.Spec.ImagePullSecrets,
			Lifecycle:        inst.Spec.Agents.Default.Lifecycle,
			Env:              inst.Spec.Agents.Default.Env,
		},
	}

	if err := p.k8s.Create(ctx, run); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create agent run: "+err.Error())
		return
	}

	p.log.Info("Created AgentRun from web request", "run", run.Name, "instance", inst.Name)

	if req.Stream {
		p.streamResponse(w, r, run.Name, completedCh, failedCh)
	} else {
		p.blockingResponse(w, r, run.Name, inst.Spec.Agents.Default.Model, completedCh, failedCh)
	}
}

// streamResponse writes SSE chunks as the agent produces output.
func (p *Proxy) streamResponse(w http.ResponseWriter, r *http.Request, runName string, completedCh, failedCh <-chan *eventbus.Event) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Also subscribe to stream chunks
	chunkCh, err := p.eventBus.Subscribe(r.Context(), eventbus.TopicAgentStreamChunk)
	if err != nil {
		return
	}

	ctx := r.Context()
	timeout := time.After(10 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout:
			return
		case event := <-chunkCh:
			if event.Metadata["agentRunID"] != runName {
				continue
			}
			var chunk struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(event.Data, &chunk); err != nil {
				continue
			}
			resp := ChatCompletionResponse{
				ID:      "chatcmpl-" + runName,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Choices: []ChatCompletionChoice{
					{
						Index: 0,
						Delta: &ChatMessage{Role: "assistant", Content: chunk.Content},
					},
				},
			}
			data, _ := json.Marshal(resp)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

		case event := <-completedCh:
			if event.Metadata["agentRunID"] != runName {
				continue
			}
			// Send final chunk with finish_reason
			stop := "stop"
			resp := ChatCompletionResponse{
				ID:      "chatcmpl-" + runName,
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Choices: []ChatCompletionChoice{
					{Index: 0, Delta: &ChatMessage{}, FinishReason: &stop},
				},
			}
			data, _ := json.Marshal(resp)
			fmt.Fprintf(w, "data: %s\n\n", data)
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return

		case event := <-failedCh:
			if event.Metadata["agentRunID"] != runName {
				continue
			}
			var result struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(event.Data, &result)
			errResp := map[string]interface{}{
				"error": map[string]string{
					"message": result.Error,
					"type":    "agent_error",
				},
			}
			data, _ := json.Marshal(errResp)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			return
		}
	}
}

// blockingResponse waits for the agent to complete and returns a single response.
func (p *Proxy) blockingResponse(w http.ResponseWriter, r *http.Request, runName, model string, completedCh, failedCh <-chan *eventbus.Event) {
	ctx := r.Context()
	timeout := time.After(10 * time.Minute)

	// Also collect stream chunks for the full response
	chunkCh, err := p.eventBus.Subscribe(ctx, eventbus.TopicAgentStreamChunk)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to subscribe: "+err.Error())
		return
	}

	var contentParts []string

	for {
		select {
		case <-ctx.Done():
			writeError(w, http.StatusGatewayTimeout, "request cancelled")
			return
		case <-timeout:
			writeError(w, http.StatusGatewayTimeout, "agent run timed out")
			return
		case event := <-chunkCh:
			if event.Metadata["agentRunID"] != runName {
				continue
			}
			var chunk struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(event.Data, &chunk); err == nil {
				contentParts = append(contentParts, chunk.Content)
			}
		case event := <-completedCh:
			if event.Metadata["agentRunID"] != runName {
				continue
			}
			// Try to get the response from the event data first
			var result struct {
				Response string `json:"response"`
			}
			_ = json.Unmarshal(event.Data, &result)

			content := result.Response
			if content == "" && len(contentParts) > 0 {
				content = strings.Join(contentParts, "")
			}
			if content == "" {
				content = "(no response)"
			}

			stop := "stop"
			writeJSON(w, http.StatusOK, ChatCompletionResponse{
				ID:      "chatcmpl-" + runName,
				Object:  "chat.completion",
				Created: time.Now().Unix(),
				Model:   model,
				Choices: []ChatCompletionChoice{
					{
						Index:        0,
						Message:      &ChatMessage{Role: "assistant", Content: content},
						FinishReason: &stop,
					},
				},
			})
			return

		case event := <-failedCh:
			if event.Metadata["agentRunID"] != runName {
				continue
			}
			var result struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(event.Data, &result)
			writeError(w, http.StatusInternalServerError, "agent run failed: "+result.Error)
			return
		}
	}
}

// webDedupeWindow is how far back we look for a matching AgentRun to reuse.
const webDedupeWindow = 15 * time.Minute

func webRequestHash(idempotencyKey, instanceName, model, systemPrompt, task string) string {
	key := strings.TrimSpace(idempotencyKey)
	if key != "" {
		return hashLabelValue("idempotency-key\x00" + instanceName + "\x00" + key)
	}
	return hashLabelValue(strings.Join([]string{"web-chat", instanceName, model, systemPrompt, task}, "\x00"))
}

// hashLabelValue returns a 16-hex-char (64-bit) fingerprint. Collisions are
// acceptable here: the hash is scoped per-instance within the dedup TTL window,
// so the effective keyspace is small.
func hashLabelValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func (p *Proxy) findRecentWebRun(ctx context.Context, namespace, instanceName, requestHash string, ttl time.Duration) (*sympoziumv1alpha1.AgentRun, error) {
	var list sympoziumv1alpha1.AgentRunList
	selector := labels.SelectorFromSet(labels.Set{
		"sympozium.ai/instance":     instanceName,
		"sympozium.ai/source":       "web-proxy",
		"sympozium.ai/request-hash": requestHash,
	})
	if err := p.k8s.List(ctx, &list, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-ttl)
	var candidates []sympoziumv1alpha1.AgentRun
	for _, run := range list.Items {
		if run.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		// Skip terminal runs — subscribing to events for a completed run
		// would hang because the event already fired.
		phase := run.Status.Phase
		if phase == sympoziumv1alpha1.AgentRunPhaseSucceeded ||
			phase == sympoziumv1alpha1.AgentRunPhaseFailed ||
			phase == sympoziumv1alpha1.AgentRunPhaseSkipped {
			continue
		}
		candidates = append(candidates, run)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreationTimestamp.After(candidates[j].CreationTimestamp.Time)
	})
	return &candidates[0], nil
}

// getAgent fetches the Agent for this proxy.
func (p *Proxy) getAgent(ctx context.Context) (*sympoziumv1alpha1.Agent, error) {
	ns := podNamespace()
	var inst sympoziumv1alpha1.Agent
	if err := p.k8s.Get(ctx, client.ObjectKey{Name: p.config.InstanceName, Namespace: ns}, &inst); err != nil {
		return nil, err
	}
	return &inst, nil
}

// podNamespace returns the namespace of the current pod.
func podNamespace() string {
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); ns != "" {
			return ns
		}
	}
	return "default"
}

// resolveProvider returns the AI provider for the instance.
func resolveProvider(inst *sympoziumv1alpha1.Agent) string {
	for _, ref := range inst.Spec.AuthRefs {
		if ref.Provider != "" {
			return ref.Provider
		}
	}
	for _, ref := range inst.Spec.AuthRefs {
		for _, p := range []string{"anthropic", "azure-openai", "bedrock", "lm-studio", "ollama", "openai"} {
			if strings.Contains(ref.Secret, p) {
				return p
			}
		}
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

// listAgents fetches all instances (for namespace-scoped listing).
func (p *Proxy) listAgents(ctx context.Context) ([]sympoziumv1alpha1.Agent, error) {
	var list sympoziumv1alpha1.AgentList
	if err := p.k8s.List(ctx, &list, client.InNamespace("")); err != nil {
		return nil, err
	}
	return list.Items, nil
}
