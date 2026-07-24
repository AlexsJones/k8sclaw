package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Memory tool name constants.
const (
	ToolMemorySearch = "memory_search"
	ToolMemoryStore  = "memory_store"
	ToolMemoryList   = "memory_list"
)

// memoryToolNames contains all memory tool names for lookup.
var memoryToolNames = map[string]bool{
	ToolMemorySearch: true,
	ToolMemoryStore:  true,
	ToolMemoryList:   true,
}

// isMemoryTool returns true if the tool name is a memory tool.
func isMemoryTool(name string) bool {
	return memoryToolNames[name]
}

// memoryServerURL is the HTTP endpoint of the memory server.
// Set from MEMORY_SERVER_URL env var at startup.
var memoryServerURL string

// memoryHTTPClient is a shared HTTP client with reasonable timeouts. The
// otelhttp transport injects the W3C traceparent header into every request and
// emits a client span per call, so memory reads/writes appear as spans nested
// under the agent run trace and connect to the memory-server's server span
// (ISI-1406 gap 6 — "spans on the memory part"). When OTel is disabled the
// global propagator/tracer are no-ops, so this adds no header and no span.
var memoryHTTPClient = &http.Client{
	Timeout:   5 * time.Second,
	Transport: otelhttp.NewTransport(http.DefaultTransport),
}

const (
	memoryMaxRetries  = 3
	memoryBaseBackoff = 500 * time.Millisecond
)

// memoryToolDefs returns the static tool definitions for memory tools.
func memoryToolDefs() []ToolDef {
	return []ToolDef{
		{
			Name:        ToolMemorySearch,
			Description: "Search agent memory for relevant past findings, investigations, and context. Use this before starting any investigation to check if similar issues have been seen before.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language search query (e.g., 'kafka consumer lag', 'OOM crash in payments service').",
					},
					"top_k": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results to return (default: 5).",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        ToolMemoryStore,
			Description: "Store a finding, investigation result, or important context in persistent memory for future agent runs. Include enough detail for a future agent to understand and reuse the information.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type":        "string",
						"description": "The content to store. Be specific: include root cause, resolution steps, service names, and namespace.",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Tags for categorization (e.g., ['kafka', 'consumer-lag', 'payments-ns']).",
					},
				},
				"required": []string{"content"},
			},
		},
		{
			Name:        ToolMemoryList,
			Description: "List recent memory entries, optionally filtered by tag. Use this to browse what the agent has learned over time.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tags": map[string]any{
						"type":        "string",
						"description": "Filter by tag (e.g., 'kafka'). Returns entries whose tags contain this string.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of entries to return (default: 20).",
					},
				},
			},
		},
	}
}

// memoryAPIResponse matches the memory server's JSON response format.
type memoryAPIResponse struct {
	Success bool            `json:"success"`
	Content json.RawMessage `json:"content,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// executeMemoryTool dispatches a memory tool call via HTTP to the memory server.
func executeMemoryTool(ctx context.Context, toolName string, argsJSON string) string {
	if memoryServerURL == "" {
		return "Error: memory server not configured (MEMORY_SERVER_URL not set)"
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing arguments: %v", err)
	}

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= memoryMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := memoryBaseBackoff * time.Duration(1<<(attempt-1))
			log.Printf("memory tool retry %d/%d after %v", attempt, memoryMaxRetries, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return fmt.Sprintf("Memory server error: %v", ctx.Err())
			}
		}

		switch toolName {
		case ToolMemorySearch:
			resp, err = memoryPost(ctx, "/search", args)
		case ToolMemoryStore:
			resp, err = memoryPost(ctx, "/store", args)
		case ToolMemoryList:
			resp, err = memoryGet(ctx, "/list", args)
		default:
			return fmt.Sprintf("Unknown memory tool: %s", toolName)
		}

		if err == nil {
			break
		}
		log.Printf("memory server call failed (attempt %d/%d): %v", attempt+1, memoryMaxRetries+1, err)
	}

	if err != nil {
		return fmt.Sprintf("Memory server error after %d attempts: %v", memoryMaxRetries+1, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Sprintf("Error reading memory server response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Memory server returned %d: %s", resp.StatusCode, string(body))
	}

	var apiResp memoryAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return string(body)
	}

	if !apiResp.Success {
		return fmt.Sprintf("Memory error: %s", apiResp.Error)
	}

	return formatMemoryContent(apiResp.Content)
}

func memoryPost(ctx context.Context, path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", memoryServerURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return memoryHTTPClient.Do(req)
}

func memoryGet(ctx context.Context, path string, args map[string]any) (*http.Response, error) {
	url := memoryServerURL + path
	sep := "?"
	if tags, ok := args["tags"].(string); ok && tags != "" {
		url += sep + "tags=" + tags
		sep = "&"
	}
	if limit, ok := args["limit"].(float64); ok && limit > 0 {
		url += sep + "limit=" + fmt.Sprintf("%d", int(limit))
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	return memoryHTTPClient.Do(req)
}

// formatMemoryContent formats the API response content for the LLM.
func formatMemoryContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "(no results)"
	}

	// Try to format as an array of memory entries.
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err == nil && len(entries) > 0 {
		var sb strings.Builder
		for i, entry := range entries {
			if i > 0 {
				sb.WriteString("\n---\n")
			}
			content, _ := entry["content"].(string)
			tags, _ := entry["tags"].([]any)
			createdAt, _ := entry["created_at"].(string)
			id, _ := entry["id"].(float64)

			if id > 0 {
				sb.WriteString(fmt.Sprintf("**Memory #%d**", int(id)))
			}
			if createdAt != "" {
				sb.WriteString(fmt.Sprintf(" (%s)", createdAt))
			}
			if len(tags) > 0 {
				tagStrs := make([]string, 0, len(tags))
				for _, t := range tags {
					if s, ok := t.(string); ok {
						tagStrs = append(tagStrs, s)
					}
				}
				sb.WriteString(fmt.Sprintf(" [%s]", strings.Join(tagStrs, ", ")))
			}
			// Show evidence metadata if present.
			if evidence, ok := entry["evidence"].(map[string]any); ok {
				if kind, ok := evidence["kind"].(string); ok && kind != "" {
					sb.WriteString(fmt.Sprintf(" [evidence: %s", kind))
					if conf, ok := evidence["confidence"].(float64); ok && conf > 0 {
						sb.WriteString(fmt.Sprintf(", confidence=%.1f", conf))
					}
					sb.WriteString("]")
				}
			}
			sb.WriteString("\n")
			sb.WriteString(content)
			sb.WriteString("\n")
		}
		return sb.String()
	}

	// For non-array responses (e.g., store result), return as-is.
	return string(raw)
}

// memoryContextMaxChars caps the auto-injected memory context to avoid
// bloating the system prompt. ~2000 chars ≈ 500 tokens.
const memoryContextMaxChars = 2000

// queryMemoryContext queries the memory server for entries related to the
// current task and returns pre-formatted context for injection into the
// system prompt. Returns empty string on any error or if no results match.
// The parent context carries the run span and the W3C trace context extracted
// from the controller, so this pre-flight /search nests under the BMAD chain
// instead of surfacing as an orphaned single-span trace (ISI-1406).
func queryMemoryContext(parent context.Context, task string, maxResults int) string {
	if memoryServerURL == "" {
		return ""
	}

	// Use the first 200 chars of the task as the search query —
	// FTS5 tokenizes natural language well enough.
	query := task
	if len(query) > 200 {
		query = query[:200]
	}

	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()

	body, _ := json.Marshal(map[string]any{
		"query": query,
		"top_k": maxResults,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", memoryServerURL+"/search", bytes.NewReader(body))
	if err != nil {
		log.Printf("memory context: failed to build request: %v", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := memoryHTTPClient.Do(req)
	if err != nil {
		log.Printf("memory context: server unreachable: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("memory context: server returned %d", resp.StatusCode)
		return ""
	}

	var apiResp memoryAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil || !apiResp.Success {
		return ""
	}

	formatted := formatMemoryContent(apiResp.Content)
	if formatted == "(no results)" || formatted == "[]" {
		return ""
	}

	// Truncate at the last complete entry boundary to stay within budget.
	if len(formatted) > memoryContextMaxChars {
		cut := strings.LastIndex(formatted[:memoryContextMaxChars], "\n---\n")
		if cut > 0 {
			formatted = formatted[:cut]
		} else {
			formatted = formatted[:memoryContextMaxChars]
		}
	}

	return formatted
}

// autoStoreMemory stores a summary of the completed task and response in the
// memory server so future agent runs have context. This is fire-and-forget —
// errors are logged but do not affect the agent run. The parent context carries
// the agent run span so the auto-store write emits a span under the run trace
// (ISI-1406 gap 6); previously this used context.Background(), which detached
// the write from the trace and left memory writes span-less.
func autoStoreMemory(parent context.Context, task, response string) {
	if memoryServerURL == "" {
		return
	}
	// Opt-out: MEMORY_AUTO_STORE=false disables the automatic per-run write
	// while keeping the memory skill's /store endpoint available for curated
	// memory management. Defaults to enabled when the var is unset.
	if !autoStoreEnabled() {
		return
	}

	// Truncate to keep stored entries reasonably sized. The byte limits can be
	// overridden via env vars; a non-positive limit disables truncation for
	// that field.
	maxTask := envInt("MEMORY_AUTO_STORE_MAX_TASK_BYTES", 500)
	maxResponse := envInt("MEMORY_AUTO_STORE_MAX_RESPONSE_BYTES", 1000)
	if maxTask > 0 && len(task) > maxTask {
		task = task[:maxTask] + "..."
	}
	if maxResponse > 0 && len(response) > maxResponse {
		response = response[:maxResponse] + "..."
	}

	content := fmt.Sprintf("Task: %s\n\nResponse: %s", task, response)

	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	body := map[string]any{
		"content": content,
		"tags":    []string{"auto", "agent-run"},
	}
	resp, err := memoryPost(ctx, "/store", body)
	if err != nil {
		log.Printf("auto-store memory failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("auto-store memory: server returned %d", resp.StatusCode)
		return
	}
	log.Printf("auto-stored memory for task (%d bytes)", len(content))
}

// autoStoreEnabled reports whether the automatic per-run memory write is on.
// It defaults to enabled; only an explicit falsey MEMORY_AUTO_STORE value
// ("false", "0", "no", "off") turns it off.
func autoStoreEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MEMORY_AUTO_STORE"))) {
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// envInt returns the integer value of the named env var, or fallback when unset
// or unparseable.
func envInt(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func initMemoryTools() []ToolDef {
	memoryServerURL = os.Getenv("MEMORY_SERVER_URL")
	if memoryServerURL == "" {
		return nil
	}
	// Strip trailing slash.
	memoryServerURL = strings.TrimRight(memoryServerURL, "/")

	log.Printf("Memory server configured: %s", memoryServerURL)
	return memoryToolDefs()
}

// --- Workflow (shared) memory tools ---

// Workflow memory tool name constants.
const (
	ToolWorkflowMemorySearch = "workflow_memory_search"
	ToolWorkflowMemoryStore  = "workflow_memory_store"
	ToolWorkflowMemoryList   = "workflow_memory_list"
)

// workflowMemoryToolNames contains all workflow memory tool names for lookup.
var workflowMemoryToolNames = map[string]bool{
	ToolWorkflowMemorySearch: true,
	ToolWorkflowMemoryStore:  true,
	ToolWorkflowMemoryList:   true,
}

// isWorkflowMemoryTool returns true if the tool name is a workflow memory tool.
func isWorkflowMemoryTool(name string) bool {
	return workflowMemoryToolNames[name]
}

// workflowMemoryServerURL is the HTTP endpoint of the shared pack-level memory server.
var workflowMemoryServerURL string

// workflowMemoryAccess is the access mode for this persona ("read-write" or "read-only").
var workflowMemoryAccess string

// Membrane configuration (from WORKFLOW_MEMBRANE_* env vars).
var (
	membraneVisibility      string   // default visibility for entries created by this persona
	membraneTrustPeers      []string // agent configs in this persona's trust group
	membraneAcceptTags      []string // tags this persona wants to receive
	membraneExposeTags      []string // tags this persona is allowed to expose; entries with non-matching tags are forced private
	membraneMaxAge          string   // time decay TTL (e.g., "24h")
	membraneMinEvidenceKind string   // minimum evidence kind filter for queries
)

// workflowMemoryToolDefs returns tool definitions for the shared workflow memory.
func workflowMemoryToolDefs() []ToolDef {
	defs := []ToolDef{
		{
			Name:        ToolWorkflowMemorySearch,
			Description: "Search the shared team memory for knowledge contributed by any persona in the workflow. Use this to find context from other team members' investigations and findings.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Natural language search query across all team knowledge.",
					},
					"top_k": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results to return (default: 5).",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        ToolWorkflowMemoryList,
			Description: "List recent entries in the shared team memory, optionally filtered by tag or source persona. Use this to browse what the team has learned.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tags": map[string]any{
						"type":        "string",
						"description": "Filter by tag (e.g., 'kafka' or a persona name like 'researcher').",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of entries to return (default: 20).",
					},
				},
			},
		},
	}

	// Only include the store tool for read-write personas.
	if workflowMemoryAccess != "read-only" {
		storeProps := map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The content to share with the team. Be specific: include findings, root causes, service names, and relevant details.",
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Tags for categorization (e.g., ['kafka', 'consumer-lag']). Your persona name is added automatically.",
			},
		}
		storeProps["evidence"] = map[string]any{
			"type":        "object",
			"description": "Evidence trace for provenance tracking. Attach this when storing findings backed by tool outputs or external sources.",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"tool_result", "external_source", "llm_interpretation", "agent_opinion"},
					"description": "Evidence quality tier: tool_result (direct tool output), external_source (URL/doc reference), llm_interpretation (model analysis), agent_opinion (subjective assessment).",
				},
				"tool_call": map[string]any{
					"type":        "string",
					"description": "Tool name and arguments that produced this finding (for tool_result kind).",
				},
				"raw_result": map[string]any{
					"type":        "string",
					"description": "Unmodified tool output or source content (truncated to key details).",
				},
				"source": map[string]any{
					"type":        "string",
					"description": "URL, document reference, or upstream memory entry ID.",
				},
				"confidence": map[string]any{
					"type":        "number",
					"description": "Confidence level from 0.0 to 1.0.",
				},
			},
		}
		storeDesc := "Store a finding in the shared team memory so other personas in the workflow can access it. Entries are automatically tagged with your persona name for attribution. You can attach an evidence trace to record how the finding was derived."

		// Add membrane parameters when configured.
		if membraneVisibility != "" {
			storeProps["visibility"] = map[string]any{
				"type":        "string",
				"enum":        []string{"public", "trusted", "private"},
				"description": "Visibility level: 'public' (all team members), 'trusted' (trust group only), 'private' (only you). Defaults to your configured default.",
			}
			storeProps["parent_id"] = map[string]any{
				"type":        "integer",
				"description": "ID of a parent memory entry this derives from (for provenance tracking).",
			}
			storeDesc += " You can control visibility (public/trusted/private) and link to parent entries for provenance."
		}

		defs = append(defs, ToolDef{
			Name:        ToolWorkflowMemoryStore,
			Description: storeDesc,
			Parameters: map[string]any{
				"type":       "object",
				"properties": storeProps,
				"required":   []string{"content"},
			},
		})
	}

	return defs
}

// executeWorkflowMemoryTool dispatches a workflow memory tool call to the shared memory server.
func executeWorkflowMemoryTool(ctx context.Context, toolName string, argsJSON string) string {
	if workflowMemoryServerURL == "" {
		return "Error: shared workflow memory not configured (WORKFLOW_MEMORY_SERVER_URL not set)"
	}

	if toolName == ToolWorkflowMemoryStore && workflowMemoryAccess == "read-only" {
		return "Error: this persona has read-only access to shared workflow memory"
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing arguments: %v", err)
	}

	instanceName := os.Getenv("INSTANCE_NAME")

	// Auto-tag store calls with the source persona name for attribution.
	if toolName == ToolWorkflowMemoryStore {
		if instanceName != "" {
			tags, _ := args["tags"].([]any)
			tags = append(tags, instanceName)
			args["tags"] = tags
		}
		// Inject membrane fields for store calls.
		if membraneVisibility != "" {
			if _, ok := args["visibility"]; !ok {
				args["visibility"] = membraneVisibility
			}
			args["source_agent"] = instanceName

			// Enforce expose tags: if the persona has an exposeTags list,
			// entries with tags that don't intersect are forced private so
			// other agents cannot see them.
			if len(membraneExposeTags) > 0 {
				if !entryTagsMatchExpose(args["tags"], membraneExposeTags) {
					args["visibility"] = "private"
				}
			}
		}
	}

	// Inject membrane fields for search and list calls.
	if (toolName == ToolWorkflowMemorySearch || toolName == ToolWorkflowMemoryList) && membraneVisibility != "" {
		args["caller_agent"] = instanceName
		if len(membraneTrustPeers) > 0 {
			args["trust_peers"] = membraneTrustPeers
		}
		if len(membraneAcceptTags) > 0 {
			args["accept_tags"] = membraneAcceptTags
		}
		if membraneMaxAge != "" {
			args["max_age"] = membraneMaxAge
		}
	}

	var resp *http.Response
	var err error

	for attempt := 0; attempt <= memoryMaxRetries; attempt++ {
		if attempt > 0 {
			backoff := memoryBaseBackoff * time.Duration(1<<(attempt-1))
			log.Printf("workflow memory tool retry %d/%d after %v", attempt, memoryMaxRetries, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return fmt.Sprintf("Workflow memory server error: %v", ctx.Err())
			}
		}

		switch toolName {
		case ToolWorkflowMemorySearch:
			resp, err = workflowMemoryPost(ctx, "/search", args)
		case ToolWorkflowMemoryStore:
			resp, err = workflowMemoryPost(ctx, "/store", args)
		case ToolWorkflowMemoryList:
			resp, err = workflowMemoryGet(ctx, "/list", args)
		default:
			return fmt.Sprintf("Unknown workflow memory tool: %s", toolName)
		}

		if err == nil {
			break
		}
		log.Printf("workflow memory server call failed (attempt %d/%d): %v", attempt+1, memoryMaxRetries+1, err)
	}

	if err != nil {
		return fmt.Sprintf("Workflow memory server error after %d attempts: %v", memoryMaxRetries+1, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Sprintf("Error reading workflow memory server response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Workflow memory server returned %d: %s", resp.StatusCode, string(body))
	}

	var apiResp memoryAPIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return string(body)
	}

	if !apiResp.Success {
		return fmt.Sprintf("Workflow memory error: %s", apiResp.Error)
	}

	return formatMemoryContent(apiResp.Content)
}

func workflowMemoryPost(ctx context.Context, path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", workflowMemoryServerURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return memoryHTTPClient.Do(req)
}

func workflowMemoryGet(ctx context.Context, path string, args map[string]any) (*http.Response, error) {
	url := workflowMemoryServerURL + path
	sep := "?"
	if tags, ok := args["tags"].(string); ok && tags != "" {
		url += sep + "tags=" + tags
		sep = "&"
	}
	if limit, ok := args["limit"].(float64); ok && limit > 0 {
		url += sep + "limit=" + fmt.Sprintf("%d", int(limit))
		sep = "&"
	}
	// Membrane query params for list endpoint.
	if caller, ok := args["caller_agent"].(string); ok && caller != "" {
		url += sep + "caller_agent=" + caller
		sep = "&"
	}
	if peers, ok := args["trust_peers"].([]string); ok && len(peers) > 0 {
		url += sep + "trust_peers=" + strings.Join(peers, ",")
		sep = "&"
	}
	if maxAge, ok := args["max_age"].(string); ok && maxAge != "" {
		url += sep + "max_age=" + maxAge
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	return memoryHTTPClient.Do(req)
}

// workflowMemoryContextMaxChars caps shared memory auto-injection.
const workflowMemoryContextMaxChars = 800

// queryWorkflowMemoryContext queries the shared workflow memory server for
// entries relevant to the current task. Returns pre-formatted context or empty string.
// The parent context carries the run span and the extracted controller trace,
// so this pre-flight /search joins the BMAD chain instead of orphaning (ISI-1406).
func queryWorkflowMemoryContext(parent context.Context, task string, maxResults int) string {
	if workflowMemoryServerURL == "" {
		return ""
	}

	query := task
	if len(query) > 200 {
		query = query[:200]
	}

	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()

	searchBody := map[string]any{
		"query": query,
		"top_k": maxResults,
	}
	if membraneMinEvidenceKind != "" {
		searchBody["min_kind"] = membraneMinEvidenceKind
	}
	body, _ := json.Marshal(searchBody)

	req, err := http.NewRequestWithContext(ctx, "POST", workflowMemoryServerURL+"/search", bytes.NewReader(body))
	if err != nil {
		log.Printf("workflow memory context: failed to build request: %v", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := memoryHTTPClient.Do(req)
	if err != nil {
		log.Printf("workflow memory context: server unreachable: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("workflow memory context: server returned %d", resp.StatusCode)
		return ""
	}

	var apiResp memoryAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil || !apiResp.Success {
		return ""
	}

	formatted := formatMemoryContent(apiResp.Content)
	if formatted == "(no results)" || formatted == "[]" {
		return ""
	}

	if len(formatted) > workflowMemoryContextMaxChars {
		cut := strings.LastIndex(formatted[:workflowMemoryContextMaxChars], "\n---\n")
		if cut > 0 {
			formatted = formatted[:cut]
		} else {
			formatted = formatted[:workflowMemoryContextMaxChars]
		}
	}

	return formatted
}

// initWorkflowMemoryTools initializes the shared workflow memory tools.
// entryTagsMatchExpose returns true if at least one of the entry's tags
// appears in the expose list. When entryTags is nil/empty, there is no tag
// to match so the entry is not exposed.
func entryTagsMatchExpose(entryTagsRaw any, exposeTags []string) bool {
	tags, ok := entryTagsRaw.([]any)
	if !ok || len(tags) == 0 {
		return false
	}
	exposeSet := make(map[string]bool, len(exposeTags))
	for _, t := range exposeTags {
		exposeSet[t] = true
	}
	for _, t := range tags {
		if s, ok := t.(string); ok && exposeSet[s] {
			return true
		}
	}
	return false
}

func initWorkflowMemoryTools() []ToolDef {
	workflowMemoryServerURL = os.Getenv("WORKFLOW_MEMORY_SERVER_URL")
	if workflowMemoryServerURL == "" {
		return nil
	}
	workflowMemoryServerURL = strings.TrimRight(workflowMemoryServerURL, "/")
	workflowMemoryAccess = os.Getenv("WORKFLOW_MEMORY_ACCESS")
	if workflowMemoryAccess == "" {
		workflowMemoryAccess = "read-write"
	}

	// Read membrane configuration.
	membraneVisibility = os.Getenv("WORKFLOW_MEMBRANE_VISIBILITY")
	if peers := os.Getenv("WORKFLOW_MEMBRANE_TRUST_PEERS"); peers != "" {
		membraneTrustPeers = strings.Split(peers, ",")
	}
	if tags := os.Getenv("WORKFLOW_MEMBRANE_ACCEPT_TAGS"); tags != "" {
		membraneAcceptTags = strings.Split(tags, ",")
	}
	if tags := os.Getenv("WORKFLOW_MEMBRANE_EXPOSE_TAGS"); tags != "" {
		membraneExposeTags = strings.Split(tags, ",")
	}
	membraneMaxAge = os.Getenv("WORKFLOW_MEMBRANE_MAX_AGE")
	membraneMinEvidenceKind = os.Getenv("WORKFLOW_MEMBRANE_MIN_EVIDENCE_KIND")

	if membraneVisibility != "" {
		log.Printf("Membrane configured: visibility=%s trust_peers=%v accept_tags=%v max_age=%s",
			membraneVisibility, membraneTrustPeers, membraneAcceptTags, membraneMaxAge)
	}

	log.Printf("Workflow memory server configured: %s (access: %s)", workflowMemoryServerURL, workflowMemoryAccess)
	return workflowMemoryToolDefs()
}
