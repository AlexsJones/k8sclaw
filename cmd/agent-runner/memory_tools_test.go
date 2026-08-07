package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TestQueryMemoryContext_PropagatesTraceparent is the ISI-1406 regression guard:
// the startup memory auto-injection must carry the W3C traceparent so the
// pre-flight /search nests under the BMAD chain instead of orphaning. It failed
// before queryMemoryContext accepted a parent ctx (it used context.Background()).
func TestQueryMemoryContext_PropagatesTraceparent(t *testing.T) {
	// The otelhttp client transport injects via the global propagator; set it
	// so the test mirrors a deployment with trace context enabled.
	old := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(old)

	var gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("traceparent")
		resp := map[string]any{"success": true, "content": []map[string]any{
			{"id": 1, "content": "x", "created_at": "2026-03-23T00:00:00Z"},
		}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	oldURL := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = oldURL }()

	// Build a parent context carrying a valid (sampled) span context, as the
	// run span would after TRACEPARENT extraction.
	tid, _ := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	sid, _ := trace.SpanIDFromHex("0123456789abcdef")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled, Remote: true,
	})
	parent := trace.ContextWithSpanContext(context.Background(), sc)

	queryMemoryContext(parent, "check pods", 3)

	if gotTraceparent == "" {
		t.Fatal("expected traceparent header on auto-inject /search, got none (orphaned trace)")
	}
	if !strings.Contains(gotTraceparent, tid.String()) {
		t.Errorf("traceparent %q does not carry parent trace id %s", gotTraceparent, tid)
	}
}

func TestMemoryToolDefs(t *testing.T) {
	tools := memoryToolDefs()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	expected := []struct {
		name string
		desc string
	}{
		{ToolMemorySearch, "Search agent memory"},
		{ToolMemoryStore, "Store a finding"},
		{ToolMemoryList, "List recent memory"},
	}

	for i, want := range expected {
		if tools[i].Name != want.name {
			t.Errorf("tools[%d].Name = %q, want %q", i, tools[i].Name, want.name)
		}
		if tools[i].Description == "" {
			t.Errorf("tools[%d].Description is empty", i)
		}
		if tools[i].Parameters == nil {
			t.Errorf("tools[%d].Parameters is nil", i)
		}
	}
}

func TestIsMemoryTool(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"memory_search", true},
		{"memory_store", true},
		{"memory_list", true},
		{"execute_command", false},
		{"read_file", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isMemoryTool(tt.name)
		if got != tt.want {
			t.Errorf("isMemoryTool(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestInitMemoryTools_NoEnv(t *testing.T) {
	// Ensure env is unset.
	t.Setenv("MEMORY_SERVER_URL", "")

	tools := initMemoryTools()
	if tools != nil {
		t.Errorf("expected nil when MEMORY_SERVER_URL is empty, got %d tools", len(tools))
	}
}

func TestInitMemoryTools_WithEnv(t *testing.T) {
	t.Setenv("MEMORY_SERVER_URL", "http://localhost:8080/")

	tools := initMemoryTools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	// Verify trailing slash was stripped.
	if memoryServerURL != "http://localhost:8080" {
		t.Errorf("memoryServerURL = %q, want trailing slash stripped", memoryServerURL)
	}
}

func TestFormatMemoryContent(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "empty content",
			raw:  nil,
			want: "(no results)",
		},
		{
			name: "non-array JSON (store result)",
			raw:  json.RawMessage(`{"id":1,"stored_at":"2026-03-23T00:00:00Z"}`),
			want: `{"id":1,"stored_at":"2026-03-23T00:00:00Z"}`,
		},
		{
			name: "empty array",
			raw:  json.RawMessage(`[]`),
			// Empty array parses but len==0, so falls through to raw content.
			want: "[]",
		},
		{
			name: "array with entries",
			raw:  json.RawMessage(`[{"id":1,"content":"kafka lag detected","tags":["kafka","infra"],"created_at":"2026-03-23T00:00:00Z"}]`),
			want: "**Memory #1** (2026-03-23T00:00:00Z) [kafka, infra]\nkafka lag detected\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatMemoryContent(tt.raw)
			if got != tt.want {
				t.Errorf("formatMemoryContent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteMemoryTool_NoServerURL(t *testing.T) {
	// Save and restore.
	old := memoryServerURL
	memoryServerURL = ""
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), ToolMemorySearch, `{"query":"test"}`)
	if result != "Error: memory server not configured (MEMORY_SERVER_URL not set)" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestExecuteMemoryTool_InvalidJSON(t *testing.T) {
	old := memoryServerURL
	memoryServerURL = "http://localhost:9999"
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), ToolMemorySearch, "not json")
	if result == "" {
		t.Error("expected error for invalid JSON")
	}
}

func TestExecuteMemoryTool_UnknownTool(t *testing.T) {
	old := memoryServerURL
	memoryServerURL = "http://localhost:9999"
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), "unknown_tool", `{}`)
	if result != "Unknown memory tool: unknown_tool" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestExecuteMemoryTool_Search(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/search" {
			t.Errorf("expected /search, got %s", r.URL.Path)
		}

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["query"] != "kafka" {
			t.Errorf("expected query=kafka, got %v", body["query"])
		}

		resp := map[string]any{
			"success": true,
			"content": []map[string]any{
				{"id": 1, "content": "kafka lag detected", "tags": []string{"kafka"}, "created_at": "2026-03-23T00:00:00Z"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), ToolMemorySearch, `{"query":"kafka"}`)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	// Should contain formatted memory entry.
	if got := result; got == "(no results)" {
		t.Error("did not expect (no results)")
	}
}

func TestExecuteMemoryTool_Store(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/store" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}

		resp := map[string]any{
			"success": true,
			"content": map[string]any{"id": 42, "stored_at": "2026-03-23T00:00:00Z"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), ToolMemoryStore, `{"content":"test memory","tags":["test"]}`)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestExecuteMemoryTool_List(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/list" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		// Verify query params.
		if r.URL.Query().Get("tags") != "kafka" {
			t.Errorf("expected tags=kafka, got %q", r.URL.Query().Get("tags"))
		}

		resp := map[string]any{
			"success": true,
			"content": []map[string]any{
				{"id": 1, "content": "kafka issue", "tags": []string{"kafka"}, "created_at": "2026-03-23T00:00:00Z"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), ToolMemoryList, `{"tags":"kafka","limit":10}`)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestExecuteMemoryTool_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "internal error")
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), ToolMemorySearch, `{"query":"test"}`)
	if result == "" {
		t.Fatal("expected non-empty error result")
	}
}

func TestExecuteMemoryTool_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"success": false,
			"error":   "something went wrong",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), ToolMemorySearch, `{"query":"test"}`)
	if result != "Memory error: something went wrong" {
		t.Errorf("unexpected result: %q", result)
	}
}

// ── Retry behaviour ─────────────────────────────────────────────────────────

func TestExecuteMemoryTool_NoRetryOnSuccess(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		resp := map[string]any{
			"success": true,
			"content": []map[string]any{
				{"id": 1, "content": "found it", "tags": []string{"test"}, "created_at": "2026-03-23T00:00:00Z"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), ToolMemorySearch, `{"query":"test"}`)
	if strings.HasPrefix(result, "Memory server error") {
		t.Errorf("expected success, got: %q", result)
	}
	if callCount.Load() != 1 {
		t.Errorf("expected exactly 1 call (no retries), got %d", callCount.Load())
	}
}

func TestExecuteMemoryTool_RetriesExhausted(t *testing.T) {
	old := memoryServerURL
	memoryServerURL = "http://127.0.0.1:1" // port 1 — connection refused
	defer func() { memoryServerURL = old }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := executeMemoryTool(ctx, ToolMemorySearch, `{"query":"test"}`)
	if !strings.Contains(result, "Memory server error after") {
		t.Errorf("expected 'Memory server error after' in result, got: %q", result)
	}
}

// ── queryMemoryContext tests ─────────────────────────────────────────────────

func TestQueryMemoryContext_NoServer(t *testing.T) {
	old := memoryServerURL
	memoryServerURL = ""
	defer func() { memoryServerURL = old }()

	result := queryMemoryContext(context.Background(), "check pods", 3)
	if result != "" {
		t.Errorf("expected empty string when no server, got %q", result)
	}
}

func TestQueryMemoryContext_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/search" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["top_k"] != float64(3) {
			t.Errorf("expected top_k=3, got %v", body["top_k"])
		}
		resp := map[string]any{
			"success": true,
			"content": []map[string]any{
				{"id": 1, "content": "kafka lag detected in payments-ns", "tags": []string{"kafka"}, "created_at": "2026-03-23T00:00:00Z"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := queryMemoryContext(context.Background(), "check kafka consumers", 3)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "kafka lag detected") {
		t.Errorf("expected memory content in result, got %q", result)
	}
}

func TestQueryMemoryContext_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"success": true,
			"content": []map[string]any{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := queryMemoryContext(context.Background(), "something unrelated", 3)
	if result != "" {
		t.Errorf("expected empty string for no results, got %q", result)
	}
}

func TestQueryMemoryContext_ServerDown(t *testing.T) {
	old := memoryServerURL
	memoryServerURL = "http://127.0.0.1:1" // connection refused
	defer func() { memoryServerURL = old }()

	result := queryMemoryContext(context.Background(), "check pods", 3)
	if result != "" {
		t.Errorf("expected empty string when server is down, got %q", result)
	}
}

func TestQueryMemoryContext_Truncation(t *testing.T) {
	// Build a response large enough to exceed memoryContextMaxChars.
	var entries []map[string]any
	for i := 0; i < 20; i++ {
		entries = append(entries, map[string]any{
			"id":         i + 1,
			"content":    strings.Repeat("long content here. ", 30),
			"tags":       []string{"test"},
			"created_at": "2026-03-23T00:00:00Z",
		})
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"success": true, "content": entries}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := queryMemoryContext(context.Background(), "test", 20)
	if len(result) > memoryContextMaxChars {
		t.Errorf("result length %d exceeds max %d", len(result), memoryContextMaxChars)
	}
	if result == "" {
		t.Error("expected non-empty truncated result")
	}
}

func TestQueryMemoryContext_LongTaskTruncated(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		gotQuery, _ = body["query"].(string)
		resp := map[string]any{"success": true, "content": []map[string]any{}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	longTask := strings.Repeat("a", 500)
	queryMemoryContext(context.Background(), longTask, 3)
	if len(gotQuery) > 200 {
		t.Errorf("expected query truncated to 200 chars, got %d", len(gotQuery))
	}
}

// ── autoStoreMemory tests ────────────────────────────────────────────────────

func TestAutoStoreMemory_StoresContent(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/store" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	autoStoreMemory(context.Background(), "list pods", "There are 3 pods running.")

	// autoStoreMemory is synchronous, so gotBody must be populated by now.
	if gotBody == nil {
		t.Fatal("expected /store to be called, but it was not — autoStoreMemory may be running in a goroutine")
	}
	content, _ := gotBody["content"].(string)
	if !strings.Contains(content, "list pods") {
		t.Errorf("expected content to contain task, got %q", content)
	}
	if !strings.Contains(content, "There are 3 pods running.") {
		t.Errorf("expected content to contain response, got %q", content)
	}
	tags, _ := gotBody["tags"].([]any)
	if len(tags) != 2 || tags[0] != "auto" || tags[1] != "agent-run" {
		t.Errorf("unexpected tags: %v", tags)
	}
}

func TestAutoStoreMemory_TruncatesLongContent(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	longTask := strings.Repeat("x", 1000)
	longResponse := strings.Repeat("y", 2000)
	autoStoreMemory(context.Background(), longTask, longResponse)

	content, _ := gotBody["content"].(string)
	// Task truncated to 500 + "...", response to 1000 + "..."
	if len(content) > 1600 {
		t.Errorf("content should be truncated, got %d chars", len(content))
	}
	if !strings.HasSuffix(strings.SplitN(content, "\n", 2)[0], "...") {
		t.Error("expected truncated task to end with ...")
	}
}

func TestAutoStoreMemory_NoopWithoutServer(t *testing.T) {
	old := memoryServerURL
	memoryServerURL = ""
	defer func() { memoryServerURL = old }()

	// Should not panic or error.
	autoStoreMemory(context.Background(), "task", "response")
}

func TestAutoStoreMemory_DisabledByEnv(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	for _, v := range []string{"false", "0", "no", "off", "FALSE"} {
		called = false
		t.Setenv("MEMORY_AUTO_STORE", v)
		autoStoreMemory(context.Background(), "list pods", "3 pods")
		if called {
			t.Errorf("MEMORY_AUTO_STORE=%q: expected no /store call, but server was hit", v)
		}
	}

	// An unset or truthy value keeps auto-store enabled.
	for _, v := range []string{"", "true", "1"} {
		called = false
		t.Setenv("MEMORY_AUTO_STORE", v)
		autoStoreMemory(context.Background(), "list pods", "3 pods")
		if !called {
			t.Errorf("MEMORY_AUTO_STORE=%q: expected /store to be called", v)
		}
	}
}

func TestAutoStoreMemory_TruncationEnvOverrides(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	t.Setenv("MEMORY_AUTO_STORE_MAX_TASK_BYTES", "10")
	t.Setenv("MEMORY_AUTO_STORE_MAX_RESPONSE_BYTES", "20")

	longTask := strings.Repeat("x", 100)
	longResponse := strings.Repeat("y", 100)
	autoStoreMemory(context.Background(), longTask, longResponse)

	content, _ := gotBody["content"].(string)
	// Task truncated to 10 bytes + "...", response to 20 bytes + "...".
	wantTask := strings.Repeat("x", 10) + "..."
	wantResponse := strings.Repeat("y", 20) + "..."
	if want := fmt.Sprintf("Task: %s\n\nResponse: %s", wantTask, wantResponse); content != want {
		t.Errorf("content = %q, want %q", content, want)
	}
}

func TestExecuteMemoryTool_RetriesWithRecovery(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n <= 2 {
			// Simulate server not ready by closing connection.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("server doesn't support hijacking")
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		resp := map[string]any{
			"success": true,
			"content": []map[string]any{
				{"id": 1, "content": "found it", "tags": []string{"test"}, "created_at": "2026-03-23T00:00:00Z"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	old := memoryServerURL
	memoryServerURL = srv.URL
	defer func() { memoryServerURL = old }()

	result := executeMemoryTool(context.Background(), ToolMemorySearch, `{"query":"test"}`)
	if strings.HasPrefix(result, "Memory server error") {
		t.Errorf("expected success after retries, got: %q", result)
	}
	if callCount.Load() < 3 {
		t.Errorf("expected at least 3 calls (2 failures + 1 success), got %d", callCount.Load())
	}
}

// ── ExposeTags enforcement tests ────────────────────────────────────────────

func TestEntryTagsMatchExpose_Match(t *testing.T) {
	tags := []any{"findings", "kafka"}
	expose := []string{"findings", "summary"}
	if !entryTagsMatchExpose(tags, expose) {
		t.Error("expected match: 'findings' is in both lists")
	}
}

func TestEntryTagsMatchExpose_NoMatch(t *testing.T) {
	tags := []any{"kafka", "debug"}
	expose := []string{"findings", "summary"}
	if entryTagsMatchExpose(tags, expose) {
		t.Error("expected no match: no overlap between tags and expose")
	}
}

func TestEntryTagsMatchExpose_EmptyTags(t *testing.T) {
	expose := []string{"findings"}
	if entryTagsMatchExpose(nil, expose) {
		t.Error("expected no match for nil tags")
	}
	if entryTagsMatchExpose([]any{}, expose) {
		t.Error("expected no match for empty tags")
	}
}

func TestEntryTagsMatchExpose_NonStringTags(t *testing.T) {
	tags := []any{42, true}
	expose := []string{"findings"}
	if entryTagsMatchExpose(tags, expose) {
		t.Error("expected no match for non-string tags")
	}
}

func TestWorkflowMemoryStore_ExposeTagsEnforcement(t *testing.T) {
	// Set up a test memory server that captures the request body.
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true, "id": 1})
	}))
	defer srv.Close()

	// Save and restore globals.
	oldURL := workflowMemoryServerURL
	oldVis := membraneVisibility
	oldExpose := membraneExposeTags
	oldAccess := workflowMemoryAccess
	defer func() {
		workflowMemoryServerURL = oldURL
		membraneVisibility = oldVis
		membraneExposeTags = oldExpose
		workflowMemoryAccess = oldAccess
	}()

	workflowMemoryServerURL = srv.URL
	membraneVisibility = "public"
	workflowMemoryAccess = "read-write"
	membraneExposeTags = []string{"findings", "summary"}

	// Store with non-matching tags → should be forced to private.
	result := executeWorkflowMemoryTool(context.Background(), ToolWorkflowMemoryStore,
		`{"content":"test entry","tags":["debug","internal"]}`)
	if strings.HasPrefix(result, "Error") {
		t.Fatalf("unexpected error: %s", result)
	}
	if capturedBody["visibility"] != "private" {
		t.Errorf("visibility = %v, want private (expose tags mismatch)", capturedBody["visibility"])
	}

	// Store with matching tags → should keep configured visibility.
	capturedBody = nil
	result = executeWorkflowMemoryTool(context.Background(), ToolWorkflowMemoryStore,
		`{"content":"test entry","tags":["findings","kafka"]}`)
	if strings.HasPrefix(result, "Error") {
		t.Fatalf("unexpected error: %s", result)
	}
	if capturedBody["visibility"] != "public" {
		t.Errorf("visibility = %v, want public (expose tags match)", capturedBody["visibility"])
	}
}
