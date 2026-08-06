package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// mcpToolManifest mirrors mcpbridge.MCPToolManifest for JSON deserialization.
type mcpToolManifest struct {
	Tools []mcpToolEntry `json:"tools"`
}

// mcpToolEntry mirrors mcpbridge.MCPToolDef.
type mcpToolEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Server      string         `json:"server"`
	Timeout     int            `json:"timeout"`
	InputSchema map[string]any `json:"inputSchema"`
}

// mcpRequest mirrors mcpbridge.MCPRequest for IPC.
type mcpRequest struct {
	ID        string            `json:"id"`
	Server    string            `json:"server,omitempty"`
	Tool      string            `json:"tool"`
	Arguments json.RawMessage   `json:"arguments"`
	Meta      map[string]string `json:"_meta,omitempty"`
}

// mcpResult mirrors mcpbridge.MCPResult for IPC.
type mcpResult struct {
	ID      string          `json:"id"`
	Success bool            `json:"success"`
	Content json.RawMessage `json:"content,omitempty"`
	Error   string          `json:"error,omitempty"`
	IsError bool            `json:"isError,omitempty"`
}

// mcpContent mirrors mcpbridge.MCPContent.
type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// mcpToolRegistry maps prefixed tool names to their definitions.
// Populated by loadMCPTools at startup before any tool calls,
// then read-only during dispatch. Protected by mcpToolRegistryMu
// for safety.
var (
	mcpToolRegistry   = map[string]mcpToolEntry{}
	mcpToolRegistryMu sync.RWMutex
)

// mcpToolsDir is the IPC directory for MCP tool request/result files.
// Configurable for testing.
var mcpToolsDir = "/ipc/tools"

// mcpRequestCounter provides unique IDs for MCP requests.
var mcpRequestCounter atomic.Int64

// lookupMCPTool returns the tool entry and true if found.
func lookupMCPTool(name string) (mcpToolEntry, bool) {
	mcpToolRegistryMu.RLock()
	defer mcpToolRegistryMu.RUnlock()
	t, ok := mcpToolRegistry[name]
	return t, ok
}

// loadMCPTools reads the MCP tool manifest and returns ToolDef entries
// for the LLM tool list. It also populates mcpToolRegistry for dispatch.
// If the manifest file doesn't exist within the wait period, it returns nil.
func loadMCPTools(manifestPath string) []ToolDef {
	// Wait for the manifest file to appear (bridge may still be starting)
	var data []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		data, err = os.ReadFile(manifestPath)
		if err == nil && len(data) > 0 {
			break
		}
		data = nil
		time.Sleep(500 * time.Millisecond)
	}

	if len(data) == 0 {
		return nil
	}

	var manifest mcpToolManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		log.Printf("WARNING: failed to parse MCP tool manifest: %v", err)
		return nil
	}

	if len(manifest.Tools) == 0 {
		return nil
	}

	var tools []ToolDef
	mcpToolRegistryMu.Lock()
	for _, t := range manifest.Tools {
		mcpToolRegistry[t.Name] = t

		desc := t.Description
		if desc == "" {
			desc = "MCP tool"
		}
		desc += fmt.Sprintf(" [MCP: %s]", t.Server)

		params := t.InputSchema
		if params == nil {
			params = map[string]any{"type": "object"}
		}

		tools = append(tools, ToolDef{
			Name:        t.Name,
			Description: desc,
			Parameters:  params,
		})
	}
	mcpToolRegistryMu.Unlock()

	log.Printf("Loaded %d MCP tool(s) from manifest", len(tools))
	return tools
}

// executeMCPTool dispatches an MCP tool call via file-based IPC to the
// mcp-bridge sidecar. It mirrors the executeCommand pattern exactly.
func executeMCPTool(ctx context.Context, tool mcpToolEntry, argsJSON string) string {
	// Validate argsJSON is valid JSON
	if !json.Valid([]byte(argsJSON)) {
		return "Error: invalid JSON arguments for MCP tool call"
	}

	id := fmt.Sprintf("%d", mcpRequestCounter.Add(1))

	req := mcpRequest{
		ID:        id,
		Server:    tool.Server,
		Tool:      tool.Name,
		Arguments: json.RawMessage(argsJSON),
		Meta:      traceMetadata(ctx),
	}

	toolsDir := mcpToolsDir
	reqPath := filepath.Join(toolsDir, fmt.Sprintf("mcp-request-%s.json", id))
	resPath := filepath.Join(toolsDir, fmt.Sprintf("mcp-result-%s.json", id))

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Sprintf("Error marshalling MCP request: %v", err)
	}

	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		return fmt.Sprintf("Error creating MCP tools directory: %v", err)
	}
	if err := os.WriteFile(reqPath, data, 0o644); err != nil {
		return fmt.Sprintf("Error writing MCP request: %v", err)
	}

	log.Printf("Wrote MCP request %s: tool=%s server=%s", id, tool.Name, tool.Server)

	// Poll for result with a deadline (server timeout + 10s buffer)
	timeoutSec := tool.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	deadline := time.Now().Add(time.Duration(timeoutSec+10) * time.Second)

	for time.Now().Before(deadline) {
		// Check context cancellation
		if ctx.Err() != nil {
			return "Error: context cancelled while waiting for MCP tool result"
		}

		resData, err := os.ReadFile(resPath)
		if err == nil {
			if len(resData) == 0 {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			var result mcpResult
			if err := json.Unmarshal(resData, &result); err != nil {
				// Partial write — retry once
				time.Sleep(100 * time.Millisecond)
				resData2, err2 := os.ReadFile(resPath)
				if err2 != nil || json.Unmarshal(resData2, &result) != nil {
					return fmt.Sprintf("Error parsing MCP result: %v", err)
				}
			}

			_ = os.Remove(reqPath)
			_ = os.Remove(resPath)

			return formatMCPResult(result, tool.Name)
		}
		time.Sleep(150 * time.Millisecond)
	}

	return "Error: timed out waiting for MCP tool result. The mcp-bridge sidecar may not be running."
}

// formatMCPResult converts an MCP result to a string for the LLM.
func formatMCPResult(r mcpResult, toolName string) string {
	if !r.Success || r.IsError {
		var errMsg string
		if r.Error != "" {
			errMsg = fmt.Sprintf("MCP Error: %s", r.Error)
		} else {
			var content []mcpContent
			if json.Unmarshal(r.Content, &content) == nil {
				for _, c := range content {
					if c.Text != "" {
						errMsg = fmt.Sprintf("MCP Error: %s", c.Text)
						break
					}
				}
			}
			if errMsg == "" {
				errMsg = "MCP Error: unknown error"
			}
		}
		return errMsg
	}

	var content []mcpContent
	if err := json.Unmarshal(r.Content, &content); err != nil {
		return string(r.Content)
	}

	var sb strings.Builder
	for _, c := range content {
		if c.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(c.Text)
		}
	}

	output := sb.String()
	if output == "" {
		output = "(no output)"
	}
	if len(output) > 8_000 {
		// Back off over a rune split by the cut — at most UTFMax-1 bytes. Only
		// the trailing rune is examined: validating the whole prefix would be
		// quadratic and would rewind to the first invalid byte anywhere in the
		// output, discarding everything after it for binary-ish results.
		truncated := output[:8_000]
		for i := 0; i < utf8.UTFMax-1 && len(truncated) > 0; i++ {
			if r, size := utf8.DecodeLastRuneInString(truncated); r != utf8.RuneError || size > 1 {
				break
			}
			truncated = truncated[:len(truncated)-1]
		}
		output = truncated + "\n... (output truncated)"
	}
	return output
}
