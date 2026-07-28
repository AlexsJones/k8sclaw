package main

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/openai/openai-go/v3"
)

func TestOpenAIReplaceToolResults(t *testing.T) {
	p := &openaiProvider{
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("system"),
			openai.UserMessage("task"),
		},
	}
	p.AddToolResults([]ToolResult{
		{CallID: "call-1", Content: "big output one"},
		{CallID: "call-2", Content: "big output two"},
	})

	p.ReplaceToolResults(map[string]string{"call-1": "[elided]", "unknown": "ignored"})

	var seen int
	for _, m := range p.messages {
		tm := m.OfTool
		if tm == nil {
			continue
		}
		seen++
		content := tm.Content.OfString.Or("")
		switch tm.ToolCallID {
		case "call-1":
			if content != "[elided]" {
				t.Errorf("call-1 content = %q, want %q", content, "[elided]")
			}
		case "call-2":
			if content != "big output two" {
				t.Errorf("call-2 should be untouched, got %q", content)
			}
		default:
			t.Errorf("unexpected tool_call_id %q", tm.ToolCallID)
		}
	}
	if seen != 2 {
		t.Errorf("found %d tool messages, want 2 — rewriting must not drop messages, "+
			"or the assistant's tool_calls entry is orphaned and the API rejects the request", seen)
	}
}

func TestOpenAIReplaceToolResults_EmptyMapIsNoOp(t *testing.T) {
	p := &openaiProvider{}
	p.AddToolResults([]ToolResult{{CallID: "call-1", Content: "original"}})
	p.ReplaceToolResults(nil)

	if got := p.messages[0].OfTool.Content.OfString.Or(""); got != "original" {
		t.Errorf("content = %q, want unchanged", got)
	}
}

func TestAnthropicReplaceToolResults(t *testing.T) {
	p := &anthropicProvider{
		messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("task")),
		},
	}
	p.AddToolResults([]ToolResult{
		{CallID: "tu-1", Content: "big output one", IsError: true},
		{CallID: "tu-2", Content: "big output two"},
	})

	p.ReplaceToolResults(map[string]string{"tu-1": "[elided]"})

	var checked int
	for _, m := range p.messages {
		for _, b := range m.Content {
			tr := b.OfToolResult
			if tr == nil {
				continue
			}
			checked++
			if tr.ToolUseID == "tu-1" {
				if len(tr.Content) != 1 || tr.Content[0].OfText == nil ||
					tr.Content[0].OfText.Text != "[elided]" {
					t.Errorf("tu-1 was not rewritten: %+v", tr.Content)
				}
				if !tr.IsError.Or(false) {
					t.Error("is_error must survive elision so a failure still reads as a failure")
				}
			}
		}
	}
	if checked != 2 {
		t.Errorf("found %d tool_result blocks, want 2", checked)
	}
}

func TestBedrockReplaceToolResults(t *testing.T) {
	p := &bedrockProvider{}
	p.AddToolResults([]ToolResult{
		{CallID: "tu-1", Content: "big output one", IsError: true},
		{CallID: "tu-2", Content: "big output two"},
	})

	p.ReplaceToolResults(map[string]string{"tu-1": "[elided]"})

	var checked int
	for _, m := range p.messages {
		for _, b := range m.Content {
			tr, ok := b.(*types.ContentBlockMemberToolResult)
			if !ok {
				continue
			}
			checked++
			id := aws.ToString(tr.Value.ToolUseId)
			text := tr.Value.Content[0].(*types.ToolResultContentBlockMemberText).Value
			switch id {
			case "tu-1":
				if text != "[elided]" {
					t.Errorf("tu-1 content = %q, want %q", text, "[elided]")
				}
				if tr.Value.Status != types.ToolResultStatusError {
					t.Error("error status must survive elision")
				}
			case "tu-2":
				if text != "big output two" {
					t.Errorf("tu-2 should be untouched, got %q", text)
				}
			}
		}
	}
	if checked != 2 {
		t.Errorf("found %d toolResult blocks, want 2", checked)
	}
}
