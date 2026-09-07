package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestGetEnv(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		fallback string
		envVal   string
		want     string
	}{
		{"returns env value when set", "TEST_GET_ENV_1", "default", "custom", "custom"},
		{"returns fallback when unset", "TEST_GET_ENV_2", "default", "", "default"},
		{"returns empty string fallback", "TEST_GET_ENV_3", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal != "" {
				t.Setenv(tt.key, tt.envVal)
			}
			got := getEnv(tt.key, tt.fallback)
			if got != tt.want {
				t.Errorf("getEnv(%q, %q) = %q, want %q", tt.key, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name string
		vals []string
		want string
	}{
		{"returns first non-empty", []string{"", "a", "b"}, "a"},
		{"returns first when set", []string{"x", "y"}, "x"},
		{"returns empty when all empty", []string{"", "", ""}, ""},
		{"handles no args", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstNonEmpty(tt.vals...)
			if got != tt.want {
				t.Errorf("firstNonEmpty(%v) = %q, want %q", tt.vals, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"short string unchanged", "hello", 10, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"long string truncated", "hello world", 5, "hello..."},
		{"empty string", "", 5, ""},
		{"zero length", "hello", 0, "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.s, tt.n)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
			}
		})
	}
}

func TestWriteJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "test.json")

	data := map[string]string{"key": "value"}
	writeJSON(path, data)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if got["key"] != "value" {
		t.Errorf("got key=%q, want %q", got["key"], "value")
	}
}

func TestWriteJSON_CreatesSubdirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "out.json")

	writeJSON(path, agentResult{Status: "success"})

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("expected file to be created through nested directories")
	}
}

func TestAgentResultJSON(t *testing.T) {
	res := agentResult{
		Status:   "success",
		Response: "hello world",
	}
	res.Metrics.DurationMs = 1234
	res.Metrics.InputTokens = 10
	res.Metrics.OutputTokens = 20

	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got agentResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "success" {
		t.Errorf("status = %q, want %q", got.Status, "success")
	}
	if got.Response != "hello world" {
		t.Errorf("response = %q, want %q", got.Response, "hello world")
	}
	if got.Metrics.DurationMs != 1234 {
		t.Errorf("durationMs = %d, want 1234", got.Metrics.DurationMs)
	}
	if got.Metrics.InputTokens != 10 || got.Metrics.OutputTokens != 20 {
		t.Errorf("tokens = (%d, %d), want (10, 20)", got.Metrics.InputTokens, got.Metrics.OutputTokens)
	}
}

func TestAgentResult_ErrorOmitsResponse(t *testing.T) {
	res := agentResult{
		Status: "error",
		Error:  "something broke",
	}

	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	json.Unmarshal(b, &raw)
	if _, ok := raw["response"]; ok {
		t.Error("expected response field to be omitted on error result")
	}
}

func TestStreamChunkJSON(t *testing.T) {
	chunk := streamChunk{Type: "text", Content: "hello", Index: 0}
	b, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	var got streamChunk
	json.Unmarshal(b, &got)
	if got.Type != "text" || got.Content != "hello" || got.Index != 0 {
		t.Errorf("chunk roundtrip failed: %+v", got)
	}
}

func TestCallOpenAI_MockServer(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": 1234567890,
			"model":   "gpt-4o-mini",
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]string{
						"role":    "assistant",
						"content": "Hello from mock!",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     5,
				"completion_tokens": 10,
				"total_tokens":      15,
			},
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx := t.Context()
	text, inTok, outTok, _, err := callOpenAI(ctx, "openai", "test-key", srv.URL, "gpt-4o-mini", "You are helpful.", "Say hello", nil, nil)
	if err != nil {
		t.Fatalf("callOpenAI error: %v", err)
	}
	if text != "Hello from mock!" {
		t.Errorf("text = %q, want %q", text, "Hello from mock!")
	}
	if inTok != 5 {
		t.Errorf("input tokens = %d, want 5", inTok)
	}
	if outTok != 10 {
		t.Errorf("output tokens = %d, want 10", outTok)
	}
}

func TestCallOpenAI_ServerError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"message": "invalid api key",
				"type":    "invalid_request_error",
				"code":    "invalid_api_key",
			},
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx := t.Context()
	_, _, _, _, err := callOpenAI(ctx, "openai", "bad-key", srv.URL, "gpt-4", "sys", "task", nil, nil)
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "API error") {
		t.Errorf("error should mention 401 or API error, got: %v", err)
	}
}

func TestCallAnthropic_MockServer(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s (expected /v1/messages)", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		if r.Header.Get("X-Api-Key") != "test-anthropic-key" {
			t.Errorf("unexpected x-api-key header: %s", r.Header.Get("X-Api-Key"))
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_test",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-sonnet-4-20250514",
			"content": []map[string]string{
				{
					"type": "text",
					"text": "Hello from Anthropic mock!",
				},
			},
			"stop_reason": "end_turn",
			"usage": map[string]int{
				"input_tokens":  8,
				"output_tokens": 12,
			},
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx := t.Context()
	text, inTok, outTok, _, err := callAnthropic(ctx, "test-anthropic-key", srv.URL, "claude-sonnet-4-20250514", "Be helpful.", "Say hello", nil, nil)
	if err != nil {
		t.Fatalf("callAnthropic error: %v", err)
	}
	if text != "Hello from Anthropic mock!" {
		t.Errorf("text = %q, want %q", text, "Hello from Anthropic mock!")
	}
	if inTok != 8 {
		t.Errorf("input tokens = %d, want 8", inTok)
	}
	if outTok != 12 {
		t.Errorf("output tokens = %d, want 12", outTok)
	}
}

func TestCallAnthropic_ServerError(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"type": "error",
			"error": map[string]string{
				"type":    "invalid_request_error",
				"message": "Your credit balance is too low",
			},
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx := t.Context()
	_, _, _, _, err := callAnthropic(ctx, "bad-key", srv.URL, "claude-sonnet-4-20250514", "sys", "task", nil, nil)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "400") && !strings.Contains(err.Error(), "API error") {
		t.Errorf("error should mention 400 or API error, got: %v", err)
	}
}

func TestCallOpenAI_AzureRequiresBaseURL(t *testing.T) {
	ctx := t.Context()
	_, _, _, _, err := callOpenAI(ctx, "azure-openai", "key", "", "gpt-4", "sys", "task", nil, nil)
	if err == nil {
		t.Fatal("expected error when azure-openai has no base URL")
	}
	if !strings.Contains(err.Error(), "requires MODEL_BASE_URL") {
		t.Errorf("error = %v, want mention of MODEL_BASE_URL", err)
	}
}

func TestProviderRouting(t *testing.T) {
	openAICalled := false
	anthropicCalled := false

	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAICalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "test", "object": "chat.completion", "model": "m",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]string{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer openaiSrv.Close()

	anthropicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicCalled = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_test", "type": "message", "role": "assistant", "model": "m",
			"content":     []map[string]string{{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer anthropicSrv.Close()

	ctx := t.Context()

	callOpenAI(ctx, "openai", "k", openaiSrv.URL, "m", "s", "t", nil, nil)
	if !openAICalled {
		t.Error("expected OpenAI server to be called for openai provider")
	}

	callAnthropic(ctx, "k", anthropicSrv.URL, "m", "s", "t", nil, nil)
	if !anthropicCalled {
		t.Error("expected Anthropic server to be called for anthropic provider")
	}

	// Verify Bedrock routing uses the mock client interface.
	bedrockCalled := false
	mockClient := &mockBedrockClient{
		handler: func(ctx context.Context, input *bedrockruntime.ConverseInput) (*bedrockruntime.ConverseOutput, error) {
			bedrockCalled = true
			return &bedrockruntime.ConverseOutput{
				Output: &types.ConverseOutputMemberMessage{
					Value: types.Message{
						Role:    types.ConversationRoleAssistant,
						Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "ok"}},
					},
				},
				StopReason: types.StopReasonEndTurn,
				Usage:      &types.TokenUsage{InputTokens: aws.Int32(1), OutputTokens: aws.Int32(1)},
			}, nil
		},
	}
	callBedrockWithClient(ctx, mockClient, "m", "s", "t", nil)
	if !bedrockCalled {
		t.Error("expected Bedrock mock client to be called")
	}
}

func TestCallAnthropic_ToolUseFlow(t *testing.T) {
	// Simulate the Anthropic tool-calling loop:
	//   1. First response: model returns tool_use block → agent executes tool → sends tool_result
	//   2. Second response: model returns final text
	callCount := 0

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// First call: model wants to use a tool.
			json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_tool", "type": "message", "role": "assistant", "model": "claude-sonnet-4-20250514",
				"content": []map[string]any{
					{
						"type": "text",
						"text": "I'll read that file for you.",
					},
					{
						"type":  "tool_use",
						"id":    "toolu_01ABC",
						"name":  "read_file",
						"input": map[string]string{"path": "/tmp/testfile.txt"},
					},
				},
				"stop_reason": "tool_use",
				"usage":       map[string]int{"input_tokens": 20, "output_tokens": 30},
			})
			return
		}

		// Second call: model produces final text after receiving tool result.
		// Verify the request body includes the tool_result.
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		if len(messages) < 3 {
			t.Errorf("expected at least 3 messages (user + assistant + tool_result), got %d", len(messages))
		}

		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_final", "type": "message", "role": "assistant", "model": "claude-sonnet-4-20250514",
			"content": []map[string]any{
				{"type": "text", "text": "The file contains: hello world"},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 50, "output_tokens": 15},
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Create a temp file for the read_file tool to read.
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "testfile.txt")
	os.WriteFile(tmpFile, []byte("hello world"), 0o644)

	// Define a minimal read_file tool.
	tools := []ToolDef{
		{
			Name:        "read_file",
			Description: "Read a file",
			Parameters: map[string]any{
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "File path"},
				},
				"required": []string{"path"},
			},
		},
	}

	ctx := t.Context()
	text, inTok, outTok, toolCalls, err := callAnthropic(ctx, "key", srv.URL, "claude-sonnet-4-20250514", "sys", "Read /tmp/testfile.txt", tools, nil)
	if err != nil {
		t.Fatalf("callAnthropic tool-use error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls (tool_use + final), got %d", callCount)
	}
	if toolCalls != 1 {
		t.Errorf("expected 1 tool call, got %d", toolCalls)
	}
	if text != "The file contains: hello world" {
		t.Errorf("text = %q, want %q", text, "The file contains: hello world")
	}
	if inTok != 70 { // 20 + 50
		t.Errorf("input tokens = %d, want 70", inTok)
	}
	if outTok != 45 { // 30 + 15
		t.Errorf("output tokens = %d, want 45", outTok)
	}
}

func TestCallAnthropic_MultipleToolCalls(t *testing.T) {
	// Verify handling of multiple tool_use blocks in a single response.
	callCount := 0

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// Model returns two tool_use blocks at once.
			json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_multi", "type": "message", "role": "assistant", "model": "claude-sonnet-4-20250514",
				"content": []map[string]any{
					{"type": "tool_use", "id": "toolu_01A", "name": "read_file",
						"input": map[string]string{"path": "/workspace/a.txt"}},
					{"type": "tool_use", "id": "toolu_01B", "name": "read_file",
						"input": map[string]string{"path": "/workspace/b.txt"}},
				},
				"stop_reason": "tool_use",
				"usage":       map[string]int{"input_tokens": 10, "output_tokens": 20},
			})
			return
		}

		// Verify both tool_result blocks are present.
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		messages, _ := body["messages"].([]any)
		lastMsg, _ := messages[len(messages)-1].(map[string]any)
		content, _ := lastMsg["content"].([]any)
		resultCount := 0
		for _, c := range content {
			block, _ := c.(map[string]any)
			if block["type"] == "tool_result" {
				resultCount++
			}
		}
		if resultCount != 2 {
			t.Errorf("expected 2 tool_result blocks, got %d", resultCount)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_done", "type": "message", "role": "assistant", "model": "claude-sonnet-4-20250514",
			"content":     []map[string]any{{"type": "text", "text": "Both files read."}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 30, "output_tokens": 5},
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	tools := []ToolDef{
		{
			Name:        "read_file",
			Description: "Read a file",
			Parameters: map[string]any{
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "File path"},
				},
				"required": []string{"path"},
			},
		},
	}

	ctx := t.Context()
	text, _, _, toolCalls, err := callAnthropic(ctx, "key", srv.URL, "claude-sonnet-4-20250514", "sys", "Read both", tools, nil)
	if err != nil {
		t.Fatalf("callAnthropic multi-tool error: %v", err)
	}
	if toolCalls != 2 {
		t.Errorf("expected 2 tool calls, got %d", toolCalls)
	}
	if text != "Both files read." {
		t.Errorf("text = %q, want %q", text, "Both files read.")
	}
}

func TestCallAnthropic_ToolErrorIsError(t *testing.T) {
	// Verify that tool results starting with "Error:" set is_error=true in
	// the tool_result block sent back to Anthropic.
	callCount := 0
	var capturedBody map[string]any

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_err", "type": "message", "role": "assistant", "model": "claude-sonnet-4-20250514",
				"content": []map[string]any{
					{"type": "tool_use", "id": "toolu_err", "name": "read_file",
						"input": map[string]string{"path": "/nonexistent/file.txt"}},
				},
				"stop_reason": "tool_use",
				"usage":       map[string]int{"input_tokens": 5, "output_tokens": 10},
			})
			return
		}

		json.NewDecoder(r.Body).Decode(&capturedBody)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_recovery", "type": "message", "role": "assistant", "model": "claude-sonnet-4-20250514",
			"content":     []map[string]any{{"type": "text", "text": "The file was not found."}},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 15, "output_tokens": 8},
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	tools := []ToolDef{
		{
			Name:        "read_file",
			Description: "Read a file",
			Parameters: map[string]any{
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "File path"},
				},
				"required": []string{"path"},
			},
		},
	}

	ctx := t.Context()
	text, _, _, _, err := callAnthropic(ctx, "key", srv.URL, "claude-sonnet-4-20250514", "sys", "Read /nonexistent/file.txt", tools, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "The file was not found." {
		t.Errorf("text = %q, want %q", text, "The file was not found.")
	}

	// Verify the is_error field was set on the tool_result.
	messages, _ := capturedBody["messages"].([]any)
	lastMsg, _ := messages[len(messages)-1].(map[string]any)
	content, _ := lastMsg["content"].([]any)
	for _, c := range content {
		block, _ := c.(map[string]any)
		if block["type"] == "tool_result" {
			isErr, _ := block["is_error"].(bool)
			if !isErr {
				t.Error("expected is_error=true for tool result starting with 'Error:'")
			}
		}
	}
}

// TestCallOpenAI_EmptyTerminalTurnFallsBack reproduces the exact qwen3.5-9b
// on LM Studio failure pattern at the wire level:
//
//	Turn 1: assistant emits reasoning content + tool_calls, finish_reason="tool_calls"
//	Turn 2: assistant emits EMPTY content, finish_reason="stop"
//
// Before the accumulated-reasoning fallback was added, the agent returned an
// empty response and the UX showed "No result available" despite 292 output
// tokens having been generated. This test is the regression guard.
func TestCallOpenAI_EmptyTerminalTurnFallsBack(t *testing.T) {
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// Reasoning preamble + tool call.
			json.NewEncoder(w).Encode(map[string]any{
				"id": "chatcmpl-1", "object": "chat.completion", "model": "qwen/qwen3.5-9b",
				"choices": []map[string]any{{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "I'll scan the cluster now for security issues.",
						"tool_calls": []map[string]any{{
							"id":   "call_abc",
							"type": "function",
							"function": map[string]any{
								"name":      "read_file",
								"arguments": `{"path":"/tmp/scan"}`,
							},
						}},
					},
					"finish_reason": "tool_calls",
				}},
				"usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
			})
			return
		}

		// Terminal turn: empty content (qwen3.5 quirk after tool result).
		json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-2", "object": "chat.completion", "model": "qwen/qwen3.5-9b",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "",
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 200, "completion_tokens": 242, "total_tokens": 442},
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	tools := []ToolDef{
		{Name: "read_file", Description: "Read a file",
			Parameters: map[string]any{
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			}},
	}

	text, inTok, outTok, toolCalls, err := callOpenAI(t.Context(),
		"lm-studio", "key", srv.URL, "qwen/qwen3.5-9b",
		"You are a security scanner.", "Scan the cluster", tools, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text == "" {
		t.Fatal("response is EMPTY — fallback did not kick in (REGRESSION)")
	}
	wantSynthetic := "(Agent completed its task via tool calls but did not produce a final text summary.)"
	if text != wantSynthetic {
		t.Errorf("response should be synthetic message, got: %q", text)
	}
	if callCount != 2 {
		t.Errorf("callCount = %d, want 2 (tool-call turn + terminal turn)", callCount)
	}
	if toolCalls != 1 {
		t.Errorf("toolCalls = %d, want 1", toolCalls)
	}
	if inTok != 300 || outTok != 292 {
		t.Errorf("tokens = (%d,%d), want (300,292) accumulated across both turns", inTok, outTok)
	}
}

// TestCallOpenAI_ReasoningContentFallback guards against the qwen3.x/deepseek-r1
// reasoning-model pattern where LM Studio/Ollama put the model's final answer in
// a non-standard `reasoning_content` field while leaving the standard `content`
// field empty. The provider must surface reasoning_content so users see the
// actual answer rather than a preamble (or blank response).
func TestCallOpenAI_ReasoningContentFallback(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Simulate qwen3.5 on LM Studio: empty content, answer in reasoning_content.
		w.Write([]byte(`{
			"id": "chatcmpl-r1",
			"object": "chat.completion",
			"model": "qwen/qwen3.5-9b",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "",
					"reasoning_content": "Based on the scan results, I found 2 privileged containers and 1 default service account violation. Severity: HIGH."
				},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 500, "completion_tokens": 120, "total_tokens": 620}
		}`))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	text, _, _, _, err := callOpenAI(t.Context(),
		"lm-studio", "", srv.URL, "qwen/qwen3.5-9b",
		"You are a security scanner.", "scan", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text == "" {
		t.Fatal("response is EMPTY — reasoning_content fallback failed (REGRESSION)")
	}
	if !strings.Contains(text, "2 privileged containers") {
		t.Errorf("response should contain reasoning_content, got: %q", text)
	}
}

// TestSanitizeReasoningArtifacts exercises the stripping of qwen-native
// <tool_call>, <think>, and <tool_use> blocks that leak into
// reasoning_content when LM Studio fails to parse them into structured
// tool_calls.
func TestSanitizeReasoningArtifacts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"strips complete tool_call block",
			"Good, found 7 namespaces.\n\n<tool_call>\n<function=execute_command>\n<parameter=command>\nkubectl get pods\n</parameter>\n</function>\n</tool_call>",
			"Good, found 7 namespaces.",
		},
		{
			"strips think block",
			"<think>Let me reason about this carefully.</think>The answer is 42.",
			"The answer is 42.",
		},
		{
			"strips multiple blocks",
			"Result: X.\n<think>pondering</think>\n<tool_call>doing stuff</tool_call>\nDone.",
			"Result: X.\n\nDone.",
		},
		{
			"truncates unclosed tool_call",
			"Partial finding.\n<tool_call>\n<function=broken\nno closing tag",
			"Partial finding.",
		},
		{
			"preserves clean text",
			"All 3 pods are Running with 0 restarts.",
			"All 3 pods are Running with 0 restarts.",
		},
		{
			"case-insensitive tag match",
			"Summary.<TOOL_CALL>junk</TOOL_CALL>Done.",
			"Summary.Done.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeReasoningArtifacts(c.in)
			// Allow the blank-line normaliser to produce 1-2 trailing/middle
			// newlines; collapse both expected and actual to compare.
			if got != c.want {
				t.Errorf("\ngot:  %q\nwant: %q", got, c.want)
			}
		})
	}
}

// TestCallOpenAI_ReasoningContentStripsArtifacts: proves that <think> blocks
// in reasoning_content are sanitized and the surrounding text becomes the
// user-facing response. (Tool-call recovery is covered by a separate test.)
func TestCallOpenAI_ReasoningContentStripsArtifacts(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "chatcmpl-art",
			"object": "chat.completion",
			"model": "qwen/qwen3.5-9b",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "",
					"reasoning_content": "<think>let me count carefully...</think>Good, I found 7 namespaces in total."
				},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 500, "completion_tokens": 120, "total_tokens": 620}
		}`))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	text, _, _, _, err := callOpenAI(t.Context(),
		"lm-studio", "", srv.URL, "qwen/qwen3.5-9b", "sys", "scan", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text == "" {
		t.Fatal("response is EMPTY")
	}
	if strings.Contains(text, "<think>") || strings.Contains(text, "count carefully") {
		t.Errorf("response still contains <think> artifacts: %q", text)
	}
	if !strings.Contains(text, "7 namespaces") {
		t.Errorf("response should retain the real content, got: %q", text)
	}
}

// TestParseQwenToolCalls verifies recovery of qwen-native tool calls from
// reasoning_content, including numeric-parameter coercion.
func TestParseQwenToolCalls(t *testing.T) {
	in := `Good, I found 7 namespaces.

<tool_call>
<function=execute_command>
<parameter=command>
kubectl get pods -A
</parameter>
<parameter=timeout>
30
</parameter>
</function>
</tool_call>

Let me also check nodes.

<tool_call>
<function=execute_command>
<parameter=command>kubectl get nodes</parameter>
</function>
</tool_call>`

	calls := parseQwenToolCalls(in)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[0].Name != "execute_command" {
		t.Errorf("call[0].Name = %q, want execute_command", calls[0].Name)
	}
	// Args should be JSON with timeout coerced to int.
	var args0 map[string]any
	if err := json.Unmarshal([]byte(calls[0].Input), &args0); err != nil {
		t.Fatalf("cannot unmarshal call[0] args: %v", err)
	}
	cmd, _ := args0["command"].(string)
	if !strings.Contains(cmd, "kubectl get pods -A") {
		t.Errorf("call[0].command = %q", cmd)
	}
	// timeout should be a number (JSON unmarshal makes it float64), not a string.
	if _, ok := args0["timeout"].(float64); !ok {
		t.Errorf("call[0].timeout should be a number, got %T: %v", args0["timeout"], args0["timeout"])
	}
	// Second call has only command.
	var args1 map[string]any
	json.Unmarshal([]byte(calls[1].Input), &args1)
	if args1["command"] != "kubectl get nodes" {
		t.Errorf("call[1].command = %v", args1["command"])
	}
}

// TestCoerceScalar exercises the parameter type heuristic.
func TestCoerceScalar(t *testing.T) {
	cases := map[string]any{
		"42":          int64(42),
		"-7":          int64(-7),
		"3.14":        float64(3.14),
		"true":        true,
		"false":       false,
		"hello":       "hello",
		"":            "",
		"kubectl get": "kubectl get",
	}
	for in, want := range cases {
		got := coerceScalar(in)
		if got != want {
			t.Errorf("coerceScalar(%q) = %v (%T), want %v (%T)", in, got, got, want, want)
		}
	}
}

// TestCallOpenAI_QwenToolCallRecovery: end-to-end, when reasoning_content
// carries qwen-native <tool_call> blocks, those become structured ToolCalls
// that the loop will dispatch. Regression guard for the ensemble-early-
// termination bug.
func TestCallOpenAI_QwenToolCallRecovery(t *testing.T) {
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// Reasoning content carries a tool_call block; standard fields empty.
			w.Write([]byte(`{
				"id": "c1", "object": "chat.completion", "model": "qwen/qwen3.5-9b",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "",
						"reasoning_content": "I'll check the pods.\n\n<tool_call>\n<function=read_file>\n<parameter=path>/tmp/pods</parameter>\n</function>\n</tool_call>"
					},
					"finish_reason": "stop"
				}],
				"usage": {"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150}
			}`))
			return
		}
		// Second call: model produces a proper final answer.
		w.Write([]byte(`{
			"id": "c2", "object": "chat.completion", "model": "qwen/qwen3.5-9b",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Found 3 pods: a, b, c."},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 200, "completion_tokens": 20, "total_tokens": 220}
		}`))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	tools := []ToolDef{
		{Name: "read_file", Description: "Read a file",
			Parameters: map[string]any{
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			}},
	}
	text, _, _, toolCalls, err := callOpenAI(t.Context(),
		"lm-studio", "", srv.URL, "qwen/qwen3.5-9b", "sys", "list pods", tools, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected 2 HTTP calls (recovered tool + final), got %d", callCount)
	}
	if toolCalls != 1 {
		t.Errorf("toolCalls = %d, want 1 (the recovered qwen tool_call)", toolCalls)
	}
	if text != "Found 3 pods: a, b, c." {
		t.Errorf("text = %q, want proper final answer", text)
	}
}

// TestCallOpenAI_ContentPreferredOverReasoning: when both `content` and
// `reasoning_content` are present, `content` wins (standard behavior).
func TestCallOpenAI_ContentPreferredOverReasoning(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "chatcmpl-r2",
			"object": "chat.completion",
			"model": "qwen/qwen3.5-9b",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "Found 3 pods.",
					"reasoning_content": "Let me think... counting pods... there are 3."
				},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 50, "completion_tokens": 20, "total_tokens": 70}
		}`))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	text, _, _, _, err := callOpenAI(t.Context(),
		"lm-studio", "", srv.URL, "qwen/qwen3.5-9b", "sys", "count pods", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "Found 3 pods." {
		t.Errorf("text = %q, want 'Found 3 pods.' (content field, not reasoning)", text)
	}
}

// TestCallAnthropic_EmptyTerminalTurnFallsBack is the equivalent regression
// guard for the Anthropic provider: if the terminal turn has no text blocks,
// the accumulated reasoning from earlier turns surfaces in the response.
func TestCallAnthropic_EmptyTerminalTurnFallsBack(t *testing.T) {
	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")

		if callCount == 1 {
			// Reasoning text + tool_use block.
			json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-test",
				"content": []map[string]any{
					{"type": "text", "text": "Let me check that file for you."},
					{"type": "tool_use", "id": "tu_1", "name": "read_file",
						"input": map[string]string{"path": "/tmp/scan"}},
				},
				"stop_reason": "tool_use",
				"usage":       map[string]int{"input_tokens": 100, "output_tokens": 50},
			})
			return
		}

		// Terminal turn: no text blocks at all (e.g. refusal, empty response).
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_2", "type": "message", "role": "assistant", "model": "claude-test",
			"content":     []map[string]any{},
			"stop_reason": "end_turn",
			"usage":       map[string]int{"input_tokens": 200, "output_tokens": 10},
		})
	})

	srv := httptest.NewServer(handler)
	defer srv.Close()

	tools := []ToolDef{
		{Name: "read_file", Description: "Read a file",
			Parameters: map[string]any{
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			}},
	}

	text, _, _, _, err := callAnthropic(t.Context(), "key", srv.URL, "claude-test", "sys", "Check file", tools, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text == "" {
		t.Fatal("response is EMPTY — fallback did not kick in (REGRESSION)")
	}
	wantSynthetic := "(Agent completed its task via tool calls but did not produce a final text summary.)"
	if text != wantSynthetic {
		t.Errorf("response should be synthetic message, got: %q", text)
	}
}

// ── Tool registration tests ──────────────────────────────────────────────────

func TestDefaultTools_SubagentsDisabledByDefault(t *testing.T) {
	// Ensure SUBAGENTS_ENABLED is not set.
	t.Setenv("SUBAGENTS_ENABLED", "")

	tools := defaultTools()
	for _, tool := range tools {
		if tool.Name == ToolSpawnSubagents {
			t.Error("spawn_subagents tool should not be registered when SUBAGENTS_ENABLED is not set")
		}
	}
}

func TestDefaultTools_SubagentsEnabledWhenSet(t *testing.T) {
	t.Setenv("SUBAGENTS_ENABLED", "true")

	tools := defaultTools()
	var found bool
	for _, tool := range tools {
		if tool.Name == ToolSpawnSubagents {
			found = true
			break
		}
	}
	if !found {
		t.Error("spawn_subagents tool should be registered when SUBAGENTS_ENABLED=true")
	}
}

func TestDefaultTools_AlwaysIncludesCoreTool(t *testing.T) {
	tools := defaultTools()
	var hasExec bool
	for _, tool := range tools {
		if tool.Name == ToolExecuteCommand {
			hasExec = true
		}
	}
	if !hasExec {
		t.Error("execute_command tool should always be registered")
	}
}

func TestDefaultTools_DelegateAbsentWithoutEdges(t *testing.T) {
	t.Setenv("ENSEMBLE_RELATIONSHIPS", "")
	tools := defaultTools()
	for _, tool := range tools {
		if tool.Name == ToolDelegateToPersona {
			t.Error("delegate_to_persona tool should not be registered when ENSEMBLE_RELATIONSHIPS is not set")
		}
	}
}

func TestDefaultTools_DelegateAbsentWithSupervisionEdgeOnly(t *testing.T) {
	t.Setenv("ENSEMBLE_RELATIONSHIPS", `[{"target":"supervisor","type":"supervision"}]`)
	tools := defaultTools()
	for _, tool := range tools {
		if tool.Name == ToolDelegateToPersona {
			t.Error("delegate_to_persona tool should not be registered for supervision-only edges")
		}
	}
}

func TestDefaultTools_DelegateRegisteredWithDelegationEdge(t *testing.T) {
	t.Setenv("ENSEMBLE_RELATIONSHIPS", `[{"target":"writer","type":"delegation"}]`)
	tools := defaultTools()
	var found bool
	for _, tool := range tools {
		if tool.Name == ToolDelegateToPersona {
			found = true
			break
		}
	}
	if !found {
		t.Error("delegate_to_persona tool should be registered when a delegation edge is present")
	}
}

func TestDefaultTools_DelegateRegisteredWithSequentialEdge(t *testing.T) {
	t.Setenv("ENSEMBLE_RELATIONSHIPS", `[{"target":"reviewer","type":"sequential"}]`)
	tools := defaultTools()
	var found bool
	for _, tool := range tools {
		if tool.Name == ToolDelegateToPersona {
			found = true
			break
		}
	}
	if !found {
		t.Error("delegate_to_persona tool should be registered when a sequential edge is present")
	}
}

func TestDefaultTools_DelegateEnumeratesValidTargets(t *testing.T) {
	t.Setenv("ENSEMBLE_RELATIONSHIPS", `[{"target":"writer","type":"delegation"},{"target":"reviewer","type":"sequential"}]`)
	tools := defaultTools()
	var delegateTool *ToolDef
	for i := range tools {
		if tools[i].Name == ToolDelegateToPersona {
			delegateTool = &tools[i]
			break
		}
	}
	if delegateTool == nil {
		t.Fatal("delegate_to_persona tool not registered")
	}
	props, _ := delegateTool.Parameters["properties"].(map[string]any)
	targetParam, _ := props["targetPersona"].(map[string]any)
	enum, _ := targetParam["enum"].([]string)
	if len(enum) != 2 {
		t.Fatalf("expected 2 enum values, got %d: %v", len(enum), enum)
	}
	wantTargets := map[string]bool{"writer": true, "reviewer": true}
	for _, v := range enum {
		if !wantTargets[v] {
			t.Errorf("unexpected enum value %q", v)
		}
	}
}

func TestReadSkipMarker(t *testing.T) {
	dir := t.TempDir()

	t.Run("absent marker is not a skip", func(t *testing.T) {
		reason, skip := readSkipMarker(filepath.Join(dir, "missing"))
		if skip {
			t.Fatalf("expected skip=false for missing marker, got reason=%q", reason)
		}
		if reason != "" {
			t.Fatalf("expected empty reason, got %q", reason)
		}
	})

	t.Run("marker with reason", func(t *testing.T) {
		path := filepath.Join(dir, "skip-reason")
		if err := os.WriteFile(path, []byte("  no new items in queue\n"), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		reason, skip := readSkipMarker(path)
		if !skip {
			t.Fatal("expected skip=true")
		}
		if reason != "no new items in queue" {
			t.Fatalf("reason = %q, want trimmed %q", reason, "no new items in queue")
		}
	})

	t.Run("empty marker yields default reason", func(t *testing.T) {
		path := filepath.Join(dir, "skip-empty")
		if err := os.WriteFile(path, []byte("   \n"), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
		reason, skip := readSkipMarker(path)
		if !skip {
			t.Fatal("expected skip=true even with empty contents")
		}
		if reason == "" {
			t.Fatal("expected a non-empty default reason")
		}
	})
}
