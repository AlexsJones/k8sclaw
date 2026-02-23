package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	text, inTok, outTok, err := callOpenAI(ctx, "openai", "test-key", srv.URL, "gpt-4o-mini", "You are helpful.", "Say hello")
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
	_, _, _, err := callOpenAI(ctx, "openai", "bad-key", srv.URL, "gpt-4", "sys", "task")
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
	text, inTok, outTok, err := callAnthropic(ctx, "test-anthropic-key", srv.URL, "claude-sonnet-4-20250514", "Be helpful.", "Say hello")
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
	_, _, _, err := callAnthropic(ctx, "bad-key", srv.URL, "claude-sonnet-4-20250514", "sys", "task")
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "400") && !strings.Contains(err.Error(), "API error") {
		t.Errorf("error should mention 400 or API error, got: %v", err)
	}
}

func TestCallOpenAI_AzureRequiresBaseURL(t *testing.T) {
	ctx := t.Context()
	_, _, _, err := callOpenAI(ctx, "azure-openai", "key", "", "gpt-4", "sys", "task")
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

	callOpenAI(ctx, "openai", "k", openaiSrv.URL, "m", "s", "t")
	if !openAICalled {
		t.Error("expected OpenAI server to be called for openai provider")
	}

	callAnthropic(ctx, "k", anthropicSrv.URL, "m", "s", "t")
	if !anthropicCalled {
		t.Error("expected Anthropic server to be called for anthropic provider")
	}
}
