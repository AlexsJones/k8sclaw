package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type agentResult struct {
	Status   string `json:"status"`
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
	Metrics  struct {
		DurationMs   int64 `json:"durationMs"`
		InputTokens  int   `json:"inputTokens"`
		OutputTokens int   `json:"outputTokens"`
	} `json:"metrics"`
}

type streamChunk struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Index   int    `json:"index"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("agent-runner starting")

	task := getEnv("TASK", "")
	if task == "" {
		if b, err := os.ReadFile("/ipc/input/task.json"); err == nil {
			var input struct {
				Task string `json:"task"`
			}
			if json.Unmarshal(b, &input) == nil && input.Task != "" {
				task = input.Task
			}
		}
	}
	if task == "" {
		fatal("TASK env var is empty and no /ipc/input/task.json found")
	}

	systemPrompt := getEnv("SYSTEM_PROMPT", "You are a helpful AI assistant.")
	provider := strings.ToLower(getEnv("MODEL_PROVIDER", "openai"))
	modelName := getEnv("MODEL_NAME", "gpt-4o-mini")
	baseURL := getEnv("MODEL_BASE_URL", "")

	if baseURL == "" {
		switch provider {
		case "openai":
			baseURL = "https://api.openai.com/v1"
		case "anthropic":
			baseURL = "https://api.anthropic.com/v1"
		case "ollama":
			baseURL = "http://ollama.default.svc:11434/v1"
		default:
			baseURL = "https://api.openai.com/v1"
		}
	}
	baseURL = strings.TrimRight(baseURL, "/")

	apiKey := firstNonEmpty(
		os.Getenv("API_KEY"),
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("AZURE_OPENAI_API_KEY"),
		os.Getenv("GITHUB_TOKEN"),
	)

	log.Printf("provider=%s model=%s baseURL=%s task=%q", provider, modelName, baseURL, truncate(task, 80))

	_ = os.MkdirAll("/ipc/output", 0o755)

	start := time.Now()
	result, err := callLLM(baseURL, apiKey, modelName, systemPrompt, task)
	elapsed := time.Since(start)

	var res agentResult
	res.Metrics.DurationMs = elapsed.Milliseconds()

	if err != nil {
		log.Printf("LLM call failed: %v", err)
		res.Status = "error"
		res.Error = err.Error()
	} else {
		log.Printf("LLM call succeeded (tokens: in=%d out=%d)",
			result.Usage.PromptTokens, result.Usage.CompletionTokens)
		res.Status = "success"
		if len(result.Choices) > 0 {
			res.Response = result.Choices[0].Message.Content
		}
		res.Metrics.InputTokens = result.Usage.PromptTokens
		res.Metrics.OutputTokens = result.Usage.CompletionTokens
	}

	if res.Response != "" {
		writeJSON("/ipc/output/stream-0.json", streamChunk{
			Type:    "text",
			Content: res.Response,
			Index:   0,
		})
	}

	writeJSON("/ipc/output/result.json", res)

	if res.Status == "error" {
		log.Printf("agent-runner finished with error: %s", res.Error)
		os.Exit(1)
	}
	log.Println("agent-runner finished successfully")
}

func callLLM(baseURL, apiKey, model, systemPrompt, task string) (*chatResponse, error) {
	url := baseURL + "/chat/completions"

	body, _ := json.Marshal(chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: task},
		},
		Stream: false,
	})

	const maxRetries = 5

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}

		client := &http.Client{Timeout: 5 * time.Minute}
		resp, err := client.Do(req)
		if err != nil {
			if attempt < maxRetries {
				wait := backoff(attempt)
				log.Printf("HTTP error (attempt %d/%d), retrying in %s: %v", attempt+1, maxRetries+1, wait, err)
				time.Sleep(wait)
				continue
			}
			return nil, fmt.Errorf("HTTP request failed after %d attempts: %w", maxRetries+1, err)
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var chatResp chatResponse
			if err := json.Unmarshal(respBody, &chatResp); err != nil {
				return nil, fmt.Errorf("parsing response: %w (body: %s)", err, truncate(string(respBody), 300))
			}
			return &chatResp, nil
		}

		// Parse the error body for classification.
		apiErr := parseAPIError(respBody)

		// Permanent errors — don't retry.
		if isPermanentError(resp.StatusCode, apiErr) {
			return nil, fmt.Errorf("%s (HTTP %d): %s", apiErr.friendlyMessage(), resp.StatusCode, truncate(string(respBody), 500))
		}

		// Retryable errors (429 rate limit, 5xx server errors).
		if attempt < maxRetries && isRetryable(resp.StatusCode) {
			wait := retryAfter(resp, attempt)
			log.Printf("HTTP %d (attempt %d/%d), retrying in %s: %s",
				resp.StatusCode, attempt+1, maxRetries+1, wait, apiErr.friendlyMessage())
			time.Sleep(wait)
			continue
		}

		return nil, fmt.Errorf("LLM returned HTTP %d after %d attempts: %s",
			resp.StatusCode, attempt+1, truncate(string(respBody), 500))
	}

	return nil, fmt.Errorf("LLM request failed after %d attempts", maxRetries+1)
}

// apiError represents a parsed error from an OpenAI-compatible API.
type apiError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func (e apiError) friendlyMessage() string {
	switch e.Error.Code {
	case "insufficient_quota":
		return "API quota exceeded — check your plan and billing"
	case "invalid_api_key":
		return "invalid API key"
	case "model_not_found":
		return "model not found"
	case "rate_limit_exceeded":
		return "rate limited"
	default:
		if e.Error.Message != "" {
			return e.Error.Message
		}
		if e.Error.Type != "" {
			return e.Error.Type
		}
		return "unknown API error"
	}
}

func parseAPIError(body []byte) apiError {
	var ae apiError
	_ = json.Unmarshal(body, &ae)
	return ae
}

// isPermanentError returns true for errors that should not be retried.
func isPermanentError(status int, ae apiError) bool {
	// Quota exhaustion is permanent until the user upgrades.
	if ae.Error.Code == "insufficient_quota" || ae.Error.Type == "insufficient_quota" {
		return true
	}
	// Auth / model errors are permanent.
	if status == 401 || status == 403 || status == 404 {
		return true
	}
	if ae.Error.Code == "invalid_api_key" || ae.Error.Code == "model_not_found" {
		return true
	}
	return false
}

func isRetryable(status int) bool {
	return status == 429 || status >= 500
}

// retryAfter computes the wait duration, respecting Retry-After header if present.
func retryAfter(resp *http.Response, attempt int) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return backoff(attempt)
}

func backoff(attempt int) time.Duration {
	secs := math.Min(float64(int(1)<<uint(attempt)), 30) // 1, 2, 4, 8, 16, 30
	return time.Duration(secs) * time.Second
}

func writeJSON(path string, v any) {
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o755)
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Printf("WARNING: failed to marshal JSON for %s: %v", path, err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("WARNING: failed to write %s: %v", path, err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func fatal(msg string) {
	log.Println("FATAL: " + msg)
	_ = os.MkdirAll("/ipc/output", 0o755)
	writeJSON("/ipc/output/result.json", agentResult{
		Status: "error",
		Error:  msg,
	})
	os.Exit(1)
}
