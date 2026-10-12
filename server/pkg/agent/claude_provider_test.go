package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func newClaudeProviderForTest(t *testing.T) *claudeProvider {
	t.Helper()
	return mustNewProvider(t, "claude", Config{Logger: slog.Default()}).(*claudeProvider)
}

func TestClaudeHandleAssistantText(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	ch := make(chan Event, 10)

	msg := claudeSDKMessage{
		Type: "assistant",
		Message: mustMarshal(t, claudeMessageContent{
			Role: "assistant",
			Content: []claudeContentBlock{
				{Type: "text", Text: "Hello world"},
			},
		}),
	}

	turn := b.handleAssistant(
		context.Background(),
		msg,
		testExecutionEventEmitter(ch),
		make(map[string]TokenUsage),
		"assistant-1",
	)
	output, tools := turn.text, turn.toolUses

	if output != "Hello world" {
		t.Fatalf("expected output 'Hello world', got %q", output)
	}
	if tools != 0 {
		t.Fatalf("expected no tool uses, got %d", tools)
	}
	select {
	case event := <-ch:
		item, ok := event.(CompletedItem)
		if !ok || item.Key != "assistant-1" || item.Role != RoleAgent || len(item.Parts) != 1 {
			t.Fatalf("unexpected event: %#v", event)
		}
		part, ok := item.Parts[0].(TextPart)
		if !ok || part.Text != "Hello world" {
			t.Fatalf("unexpected part: %#v", item.Parts[0])
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestClaudeHandleAssistantToolUse(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	ch := make(chan Event, 10)

	msg := claudeSDKMessage{
		Type: "assistant",
		Message: mustMarshal(t, claudeMessageContent{
			Role: "assistant",
			Content: []claudeContentBlock{
				{
					Type:  "tool_use",
					ID:    "call-1",
					Name:  "Read",
					Input: mustMarshal(t, map[string]any{"path": "/tmp/foo"}),
				},
			},
		}),
	}

	turn := b.handleAssistant(
		context.Background(),
		msg,
		testExecutionEventEmitter(ch),
		make(map[string]TokenUsage),
		"assistant-1",
	)
	output, tools := turn.text, turn.toolUses

	if output != "" {
		t.Fatalf("tool_use should not add to output, got %q", output)
	}
	if tools != 1 {
		t.Fatalf("expected one tool use, got %d", tools)
	}
	select {
	case event := <-ch:
		item, ok := event.(CompletedItem)
		if !ok || item.Key != "assistant-1" || item.Role != RoleAgent || len(item.Parts) != 1 {
			t.Fatalf("unexpected event: %#v", event)
		}
		part, ok := item.Parts[0].(ToolCallPart)
		if !ok || part.Name != "Read" || part.CallID != "call-1" {
			t.Fatalf("unexpected part: %#v", item.Parts[0])
		}
		if part.Input["path"] != "/tmp/foo" {
			t.Fatalf("expected input path /tmp/foo, got %v", part.Input["path"])
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestClaudeHandleAssistantServerToolUsePreservesMixedContent(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	events := make(chan Event, 2)
	msg := claudeSDKMessage{
		Type: "assistant",
		Message: mustMarshal(t, claudeMessageContent{
			ID:   "assistant-server-tool",
			Role: "assistant",
			Content: []claudeContentBlock{
				{Type: "text", Text: "I will search."},
				{
					Type: "server_tool_use", ID: "search-1", Name: "web_search",
					Input: mustMarshal(t, map[string]any{"query": "Boardly"}),
				},
			},
		}),
	}

	turn := b.handleAssistant(
		context.Background(), msg, testExecutionEventEmitter(events),
		make(map[string]TokenUsage), "fallback",
	)
	if !turn.understood || turn.toolUses != 1 || turn.text != "I will search." {
		t.Fatalf("turn = %#v", turn)
	}
	item := (<-events).(CompletedItem)
	if item.Key != "assistant-server-tool" || len(item.Parts) != 2 {
		t.Fatalf("item = %#v", item)
	}
	call, ok := item.Parts[1].(ToolCallPart)
	if !ok || call.CallID != "search-1" || call.Name != "web_search" || call.Input["query"] != "Boardly" {
		t.Fatalf("tool call = %#v", item.Parts[1])
	}
}

func TestClaudeHandleAssistantWebSearchResultCompletesServerTool(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	events := make(chan Event, 2)
	msg := claudeSDKMessage{
		Type: "assistant",
		Message: mustMarshal(t, claudeMessageContent{
			ID:   "assistant-search-result",
			Role: "assistant",
			Content: []claudeContentBlock{
				{
					Type: "web_search_tool_result", ToolUseID: "search-1",
					Content: mustMarshal(t, []any{map[string]any{
						"type": "web_search_result", "title": "Boardly",
					}}),
				},
			},
		}),
	}

	turn := b.handleAssistant(
		context.Background(), msg, testExecutionEventEmitter(events),
		make(map[string]TokenUsage), "fallback",
	)
	if !turn.understood || turn.toolUses != 1 {
		t.Fatalf("turn = %#v", turn)
	}
	item := (<-events).(CompletedItem)
	if item.Key != "assistant-search-result" || len(item.Parts) != 1 {
		t.Fatalf("item = %#v", item)
	}
	result, ok := item.Parts[0].(ToolResultPart)
	if !ok || result.CallID != "search-1" || result.Name != "web_search" ||
		result.Status != ToolResultStatusCompleted || !strings.Contains(result.Output, "Boardly") {
		t.Fatalf("tool result = %#v", item.Parts[0])
	}
}

func TestClaudeHandleAssistantRecognizesRedactedThinkingWithoutPersistingOpaqueData(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	events := make(chan Event, 1)
	msg := claudeSDKMessage{
		Type: "assistant",
		Message: json.RawMessage(
			`{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque-provider-data"}]}`,
		),
	}
	turn := b.handleAssistant(
		context.Background(), msg, testExecutionEventEmitter(events),
		make(map[string]TokenUsage), "fallback",
	)
	if !turn.understood || turn.toolUses != 0 || turn.text != "" {
		t.Fatalf("turn = %#v", turn)
	}
	select {
	case event := <-events:
		t.Fatalf("redacted thinking must not be persisted: %#v", event)
	default:
	}
}

func TestClaudeHandleAssistantMCPToolUseAndResult(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	events := make(chan Event, 2)
	use := claudeSDKMessage{Type: "assistant", Message: mustMarshal(t, claudeMessageContent{
		ID: "mcp-use-message", Role: "assistant", Content: []claudeContentBlock{{
			Type: "mcp_tool_use", ID: "mcp-1", ServerName: "github", Name: "search",
			Input: mustMarshal(t, map[string]any{"query": "Boardly"}),
		}},
	})}
	result := claudeSDKMessage{Type: "assistant", Message: mustMarshal(t, claudeMessageContent{
		ID: "mcp-result-message", Role: "assistant", Content: []claudeContentBlock{{
			Type: "mcp_tool_result", ToolUseID: "mcp-1",
			Content: mustMarshal(t, map[string]any{"content": "found"}),
		}},
	})}

	useTurn := b.handleAssistant(
		context.Background(), use, testExecutionEventEmitter(events),
		make(map[string]TokenUsage), "use-fallback",
	)
	resultTurn := b.handleAssistant(
		context.Background(), result, testExecutionEventEmitter(events),
		make(map[string]TokenUsage), "result-fallback",
	)
	if useTurn.contractErr != nil || resultTurn.contractErr != nil {
		t.Fatalf("contract errors: use=%v result=%v", useTurn.contractErr, resultTurn.contractErr)
	}
	callItem := (<-events).(CompletedItem)
	call := callItem.Parts[0].(ToolCallPart)
	if call.CallID != "mcp-1" || call.Name != "github/search" {
		t.Fatalf("MCP tool call = %#v", call)
	}
	resultItem := (<-events).(CompletedItem)
	toolResult := resultItem.Parts[0].(ToolResultPart)
	if toolResult.CallID != "mcp-1" || toolResult.Name != "" ||
		toolResult.Status != ToolResultStatusCompleted || !strings.Contains(toolResult.Output, "found") {
		t.Fatalf("MCP tool result = %#v", toolResult)
	}
}

func TestClaudeHandleAssistantAdvisorResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     any
		wantStatus  ToolResultStatus
		wantText    string
		wantFailure bool
	}{
		{
			name: "plaintext",
			content: map[string]any{
				"type": "advisor_result", "text": "use a channel-based coordinator",
			},
			wantStatus: ToolResultStatusCompleted,
			wantText:   "use a channel-based coordinator",
		},
		{
			name: "redacted",
			content: map[string]any{
				"type": "advisor_redacted_result", "encrypted_content": "opaque-secret",
			},
			wantStatus: ToolResultStatusCompleted,
			wantText:   "advisor_redacted_result",
		},
		{
			name: "error",
			content: map[string]any{
				"type": "advisor_tool_result_error", "error_code": "overloaded",
			},
			wantStatus:  ToolResultStatusFailed,
			wantText:    "overloaded",
			wantFailure: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			b := newClaudeProviderForTest(t)
			events := make(chan Event, 1)
			msg := claudeSDKMessage{Type: "assistant", Message: mustMarshal(t, claudeMessageContent{
				ID: "advisor-message", Role: "assistant", Content: []claudeContentBlock{
					{
						Type: "server_tool_use", ID: "advisor-1", Name: "advisor",
						Input: mustMarshal(t, map[string]any{}),
					},
					{
						Type: "advisor_tool_result", ToolUseID: "advisor-1",
						Content: mustMarshal(t, test.content),
					},
				},
			})}

			turn := b.handleAssistant(
				context.Background(), msg, testExecutionEventEmitter(events),
				make(map[string]TokenUsage), "fallback",
			)
			if turn.contractErr != nil {
				t.Fatalf("handleAssistant() error = %v", turn.contractErr)
			}
			item := (<-events).(CompletedItem)
			if len(item.Parts) < 2 {
				t.Fatalf("parts = %#v, want advisor call and result", item.Parts)
			}
			call, callOK := item.Parts[0].(ToolCallPart)
			result, resultOK := item.Parts[1].(ToolResultPart)
			if !callOK || call.CallID != "advisor-1" || call.Name != "advisor" ||
				!resultOK || result.CallID != "advisor-1" || result.Name != "advisor" ||
				result.Status != test.wantStatus || !strings.Contains(result.Output, test.wantText) {
				t.Fatalf("advisor parts = %#v", item.Parts)
			}
			if strings.Contains(result.Output, "opaque-secret") {
				t.Fatalf("advisor result leaked encrypted content: %s", result.Output)
			}
			_, gotFailure := item.Parts[len(item.Parts)-1].(ErrorPart)
			if gotFailure != test.wantFailure {
				t.Fatalf("failure part = %v, want %v: %#v", gotFailure, test.wantFailure, item.Parts)
			}
		})
	}
}

func TestClaudeToolResultPartsRejectMalformedKnownResults(t *testing.T) {
	t.Parallel()

	for _, blockType := range []string{
		"web_search_tool_result", "mcp_tool_result", "web_fetch_tool_result",
		"code_execution_tool_result", "bash_code_execution_tool_result",
		"text_editor_code_execution_tool_result", "tool_search_tool_result",
		"advisor_tool_result",
	} {
		t.Run(blockType, func(t *testing.T) {
			t.Parallel()
			for _, content := range []json.RawMessage{nil, json.RawMessage(`null`)} {
				_, err := claudeToolResultParts(claudeContentBlock{
					Type: blockType, ToolUseID: "call-1", Content: content,
				})
				if err == nil {
					t.Fatalf("claudeToolResultParts() accepted %s content", string(content))
				}
			}
		})
	}
}

func TestClaudeToolResultPartsAllowsEmptyClientToolResult(t *testing.T) {
	t.Parallel()

	for _, isError := range []bool{false, true} {
		for _, content := range []json.RawMessage{nil, json.RawMessage(`null`)} {
			parts, err := claudeToolResultParts(claudeContentBlock{
				Type: "tool_result", ToolUseID: "call-1", Content: content, IsError: isError,
			})
			if err != nil {
				t.Fatalf("claudeToolResultParts() error = %v", err)
			}
			result, ok := parts[0].(ToolResultPart)
			if !ok || result.CallID != "call-1" || result.Output != "" ||
				(result.Status == ToolResultStatusFailed) != isError {
				t.Fatalf("client tool result = %#v", parts)
			}
		}
	}
}

func TestClaudeToolResultPartsPreservesCustomerEncryptedContent(t *testing.T) {
	t.Parallel()

	for _, blockType := range []string{"tool_result", "mcp_tool_result"} {
		parts, err := claudeToolResultParts(claudeContentBlock{
			Type: blockType, ToolUseID: "call-1",
			Content: json.RawMessage(`{"encrypted_content":"customer-value"}`),
		})
		if err != nil {
			t.Fatalf("claudeToolResultParts(%s) error = %v", blockType, err)
		}
		result := parts[0].(ToolResultPart)
		if !strings.Contains(result.Output, "customer-value") {
			t.Fatalf("%s output = %s, want customer field preserved", blockType, result.Output)
		}
	}
}

func TestClaudeToolImagesStayOutOfDurableTranscript(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, content string }{
		{"native image", `[{"type":"text","text":"region 0: stroke-a"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"large-base64-preview"}}]`},
		{"MCP image", `[{"type":"text","text":"region 0: stroke-a"},{"type":"image","mimeType":"image/png","data":"large-base64-preview"}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parts, err := claudeToolResultParts(
				claudeContentBlock{
					Type:      "tool_result",
					ToolUseID: "read",
					Content:   json.RawMessage(tt.content),
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			got := parts[0].(ToolResultPart).Output
			if strings.Contains(got, "large-base64-preview") || !strings.Contains(got, "stroke-a") ||
				!strings.Contains(got, "dataOmitted") {
				t.Fatalf("durable output = %s", got)
			}
		})
	}
	// Arbitrary structured customer data is not a provider image block.
	got := claudeToolResultOutput("tool_result", json.RawMessage(`{"type":"image","data":"customer-value"}`))
	if !strings.Contains(got, "customer-value") {
		t.Fatal("customer JSON changed")
	}
}

func TestClaudeToolResultPartsDoesNotInferCustomerErrorCode(t *testing.T) {
	t.Parallel()

	for _, blockType := range []string{"tool_result", "mcp_tool_result"} {
		parts, err := claudeToolResultParts(claudeContentBlock{
			Type: blockType, ToolUseID: "call-1",
			Content: json.RawMessage(`{"error_code":"customer-value"}`),
		})
		if err != nil {
			t.Fatalf("claudeToolResultParts(%s) error = %v", blockType, err)
		}
		result, ok := parts[0].(ToolResultPart)
		if !ok || result.Status != ToolResultStatusCompleted || len(parts) != 1 {
			t.Fatalf("%s result = %#v, want completed customer output", blockType, parts)
		}
	}
}

func TestClaudeToolCallPartRequiresObjectInput(t *testing.T) {
	t.Parallel()

	tests := []claudeContentBlock{
		{Type: "tool_use", ID: "call-1", Name: "read"},
		{Type: "server_tool_use", ID: "call-1", Name: "advisor"},
		{Type: "mcp_tool_use", ID: "call-1", ServerName: "github", Name: "search"},
	}
	for _, base := range tests {
		t.Run(base.Type, func(t *testing.T) {
			t.Parallel()
			for _, input := range []json.RawMessage{
				nil, json.RawMessage(`null`), json.RawMessage(`"value"`), json.RawMessage(`[]`),
			} {
				block := base
				block.Input = input
				if _, err := claudeToolCallPart(block); err == nil {
					t.Fatalf("claudeToolCallPart() accepted input %s", string(input))
				}
			}
			base.Input = json.RawMessage(`{}`)
			if _, err := claudeToolCallPart(base); err != nil {
				t.Fatalf("claudeToolCallPart() rejected empty object: %v", err)
			}
		})
	}
}

func TestClaudeMCPToolUseRequiresServerQualifiedIdentity(t *testing.T) {
	t.Parallel()

	_, err := claudeToolCallPart(claudeContentBlock{
		Type: "mcp_tool_use", ID: "mcp-1", Name: "search",
		Input: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("claudeToolCallPart() accepted MCP tool without server_name")
	}
}

func TestClaudeToolSearchResultDoesNotInventVariantName(t *testing.T) {
	t.Parallel()

	parts, err := claudeToolResultParts(claudeContentBlock{
		Type: "tool_search_tool_result", ToolUseID: "search-1",
		Content: json.RawMessage(`{"type":"tool_search_tool_search_result","tool_references":[]}`),
	})
	if err != nil {
		t.Fatalf("claudeToolResultParts() error = %v", err)
	}
	result := parts[0].(ToolResultPart)
	if result.Name != "" {
		t.Fatalf("tool-search result name = %q, want empty correlation-only name", result.Name)
	}
}

func TestClaudeHandleUserToolResult(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	ch := make(chan Event, 10)

	msg := claudeSDKMessage{
		Type: "user",
		Message: mustMarshal(t, claudeMessageContent{
			Role: "user",
			Content: []claudeContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: "call-1",
					Content:   mustMarshal(t, "file contents here"),
				},
			},
		}),
	}

	turn := b.handleUser(context.Background(), msg, testExecutionEventEmitter(ch), "tool-result-1")
	if turn.contractErr != nil || turn.sawAsyncLaunch {
		t.Fatalf("handleUser() = %+v, want ordinary tool result", turn)
	}

	select {
	case event := <-ch:
		item, ok := event.(CompletedItem)
		if !ok || item.Key != "tool-result-1" || item.Role != RoleTool || len(item.Parts) != 1 {
			t.Fatalf("unexpected event: %#v", event)
		}
		part, ok := item.Parts[0].(ToolResultPart)
		if !ok || part.CallID != "call-1" || part.Output != "file contents here" {
			t.Fatalf("unexpected part: %#v", item.Parts[0])
		}
	default:
		t.Fatal("expected message on channel")
	}
}

func TestClaudeHandleUserToolUse(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	events := make(chan Event, 1)
	msg := claudeSDKMessage{Type: "user", Message: mustMarshal(t, claudeMessageContent{
		ID: "user-tool-message", Role: "user", Content: []claudeContentBlock{{
			Type: "tool_use", ID: "call-1", Name: "read_file",
			Input: mustMarshal(t, map[string]any{"path": "README.md"}),
		}},
	})}

	turn := b.handleUser(context.Background(), msg, testExecutionEventEmitter(events), "fallback")
	if turn.contractErr != nil {
		t.Fatalf("handleUser() error = %v", turn.contractErr)
	}
	item := (<-events).(CompletedItem)
	call, ok := item.Parts[0].(ToolCallPart)
	if !ok || item.Role != RoleTool || call.CallID != "call-1" ||
		call.Name != "read_file" || call.Input["path"] != "README.md" {
		t.Fatalf("user tool item = %#v", item)
	}
}

func TestClaudeHandleUserRejectsTextBlockWithoutText(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	events := make(chan Event, 1)
	msg := claudeSDKMessage{Type: "user", Message: mustMarshal(t, claudeMessageContent{
		ID: "user-message", Role: "user", Content: []claudeContentBlock{
			{
				Type: "tool_result", ToolUseID: "call-1",
				Content: mustMarshal(t, "done"),
			},
			{Type: "text"},
		},
	})}

	turn := b.handleUser(context.Background(), msg, testExecutionEventEmitter(events), "fallback")
	if turn.contractErr == nil {
		t.Fatal("handleUser() accepted text block without text")
	}
	select {
	case event := <-events:
		t.Fatalf("malformed user content emitted partial item: %#v", event)
	default:
	}
}

func TestClaudeHandleAssistantRejectsMissingKnownContentFields(t *testing.T) {
	t.Parallel()

	for _, blockType := range []string{"text", "thinking"} {
		t.Run(blockType, func(t *testing.T) {
			t.Parallel()
			b := newClaudeProviderForTest(t)
			events := make(chan Event, 1)
			msg := claudeSDKMessage{Type: "assistant", Message: mustMarshal(t, claudeMessageContent{
				ID: "message-1", Role: "assistant", Content: []claudeContentBlock{
					{Type: "text", Text: "partial"},
					{Type: blockType},
				},
			})}
			turn := b.handleAssistant(
				context.Background(), msg, testExecutionEventEmitter(events),
				make(map[string]TokenUsage), "fallback",
			)
			if turn.contractErr == nil {
				t.Fatalf("handleAssistant() accepted %s without required content", blockType)
			}
			select {
			case event := <-events:
				t.Fatalf("malformed content emitted partial item: %#v", event)
			default:
			}
		})
	}
}

func TestClaudeHandleAssistantRejectsNullKnownContentFields(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"text", "thinking"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			b := newClaudeProviderForTest(t)
			events := make(chan Event, 1)
			msg := claudeSDKMessage{Type: "assistant", Message: json.RawMessage(
				`{"id":"message-1","role":"assistant","content":[` +
					`{"type":"text","text":"partial"},` +
					`{"type":"` + field + `","` + field + `":null}]}`,
			)}
			turn := b.handleAssistant(
				context.Background(), msg, testExecutionEventEmitter(events),
				make(map[string]TokenUsage), "fallback",
			)
			if turn.contractErr == nil {
				t.Fatalf("handleAssistant() accepted %s:null", field)
			}
			select {
			case event := <-events:
				t.Fatalf("null content emitted partial item: %#v", event)
			default:
			}
		})
	}
}

func TestClaudeHandleControlRequestAutoApproves(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)

	var written bytes.Buffer

	msg := claudeSDKMessage{
		Type:      "control_request",
		RequestID: "req-42",
		Request: mustMarshal(t, claudeControlRequestPayload{
			Subtype:  "tool_use",
			ToolName: "Bash",
			Input:    mustMarshal(t, map[string]any{"command": "ls"}),
		}),
	}

	b.handleControlRequest(msg, &written, ExecOptions{})

	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if resp["type"] != "control_response" {
		t.Fatalf("expected type control_response, got %v", resp["type"])
	}
	respInner := resp["response"].(map[string]any)
	if respInner["request_id"] != "req-42" {
		t.Fatalf("expected request_id req-42, got %v", respInner["request_id"])
	}
	innerResp := respInner["response"].(map[string]any)
	if innerResp["behavior"] != "allow" {
		t.Fatalf("expected behavior allow, got %v", innerResp["behavior"])
	}
	updatedInput := innerResp["updatedInput"].(map[string]any)
	if _, ok := updatedInput["run_in_background"]; ok {
		t.Fatal("did not expect run_in_background to be injected into ordinary tool input")
	}
}

func TestClaudeHandleControlRequestForcesBackgroundToolsForeground(t *testing.T) {
	t.Parallel()

	for _, toolName := range []string{"Bash", "Agent"} {
		t.Run(toolName, func(t *testing.T) {
			t.Parallel()

			b := newClaudeProviderForTest(t)

			var written bytes.Buffer

			msg := claudeSDKMessage{
				Type:      "control_request",
				RequestID: "req-42",
				Request: mustMarshal(t, claudeControlRequestPayload{
					Subtype:  "tool_use",
					ToolName: toolName,
					Input: mustMarshal(t, map[string]any{
						"command":           "sleep 60",
						"run_in_background": true,
					}),
				}),
			}

			b.handleControlRequest(msg, &written, ExecOptions{})

			var resp map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &resp); err != nil {
				t.Fatalf("unmarshal response: %v", err)
			}

			respInner := resp["response"].(map[string]any)
			innerResp := respInner["response"].(map[string]any)
			if innerResp["behavior"] != "allow" {
				t.Fatalf("expected behavior allow, got %v", innerResp["behavior"])
			}
			updatedInput := innerResp["updatedInput"].(map[string]any)
			if updatedInput["run_in_background"] != false {
				t.Fatalf("expected run_in_background=false, got %v", updatedInput["run_in_background"])
			}
			if updatedInput["command"] != "sleep 60" {
				t.Fatalf("expected original command to be preserved, got %v", updatedInput["command"])
			}
		})
	}
}

func TestClaudeHandleControlRequestConnectorOnlyAllowsExactMCPNamespace(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	tests := []struct {
		name     string
		tool     string
		behavior string
	}{
		{name: "selected namespace", tool: "mcp__calendar__events_list", behavior: "allow"},
		{name: "unselected namespace", tool: "mcp__drive__files_read", behavior: "deny"},
		{name: "native command", tool: "Bash", behavior: "deny"},
		{name: "native web", tool: "WebSearch", behavior: "deny"},
		{name: "native user input", tool: "AskUserQuestion", behavior: "deny"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var written bytes.Buffer
			msg := claudeSDKMessage{
				Type:      "control_request",
				RequestID: "request-1",
				Request: mustMarshal(t, claudeControlRequestPayload{
					Subtype: "tool_use", ToolName: test.tool,
					Input: mustMarshal(t, map[string]any{"run_in_background": true}),
				}),
			}
			b.handleControlRequest(msg, &written, ExecOptions{
				ToolPolicy:        ToolPolicyConnectorOnly,
				AllowedMCPServers: []string{"calendar"},
			})

			var response struct {
				Response struct {
					Response struct {
						Behavior     string         `json:"behavior"`
						UpdatedInput map[string]any `json:"updatedInput"`
					} `json:"response"`
				} `json:"response"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(written.Bytes()), &response); err != nil {
				t.Fatalf("decode control response: %v", err)
			}
			if response.Response.Response.Behavior != test.behavior {
				t.Fatalf("behavior = %q, want %q", response.Response.Response.Behavior, test.behavior)
			}
			if test.behavior == "allow" &&
				response.Response.Response.UpdatedInput["run_in_background"] != false {
				t.Fatalf("allowed Connector tool retained background execution: %#v", response)
			}
		})
	}
}

func TestClaudeHandleUserDetectsAsyncLaunchedToolResult(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	ch := make(chan Event, 10)

	msg := claudeSDKMessage{
		Type: "user",
		Message: mustMarshal(t, claudeMessageContent{
			Role: "user",
			Content: []claudeContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: "call-1",
					Content: mustMarshal(t, map[string]any{
						"status":  "async_launched",
						"message": "background task launched",
					}),
				},
			},
		}),
	}

	turn := b.handleUser(context.Background(), msg, testExecutionEventEmitter(ch), "tool-result-1")
	if turn.contractErr != nil || !turn.sawAsyncLaunch {
		t.Fatalf("handleUser() = %+v, want async launch", turn)
	}
}

func TestClaudeHandleUserIgnoresAsyncLaunchedTextOutput(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	ch := make(chan Event, 10)

	msg := claudeSDKMessage{
		Type: "user",
		Message: mustMarshal(t, claudeMessageContent{
			Role: "user",
			Content: []claudeContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: "call-1",
					Content: mustMarshal(t, map[string]any{
						"stdout": `fixture contained {"status":"async_launched"} as plain text`,
					}),
				},
			},
		}),
	}

	turn := b.handleUser(context.Background(), msg, testExecutionEventEmitter(ch), "tool-result-1")
	if turn.contractErr != nil || turn.sawAsyncLaunch {
		t.Fatalf("handleUser() = %+v, want ordinary text output", turn)
	}
}

func TestClaudeHandleAssistantInvalidJSON(t *testing.T) {
	t.Parallel()

	b := newClaudeProviderForTest(t)
	ch := make(chan Event, 10)

	msg := claudeSDKMessage{
		Type:    "assistant",
		Message: json.RawMessage(`invalid json`),
	}

	// Should not panic
	turn := b.handleAssistant(
		context.Background(),
		msg,
		testExecutionEventEmitter(ch),
		make(map[string]TokenUsage),
		"assistant-1",
	)
	output, tools := turn.text, turn.toolUses

	if output != "" {
		t.Fatalf("expected empty output for invalid JSON, got %q", output)
	}
	if tools != 0 {
		t.Fatalf("expected no tool uses for invalid JSON, got %d", tools)
	}
	select {
	case m := <-ch:
		t.Fatalf("expected no message, got %+v", m)
	default:
	}
}

func TestSendProviderMessageBackpressuresDurableMessagesAndDropsOnlyDeltas(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ch := make(chan Message)
	firstSent := make(chan bool, 1)
	go func() {
		firstSent <- sendProviderMessage(ctx, ch, Message{Type: MessageText, Content: "first"})
	}()
	first := <-ch
	if first.Content != "first" {
		t.Fatalf("expected 'first', got %q", first.Content)
	}
	select {
	case <-firstSent:
		t.Fatal("durable parser advanced before processing acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	first.acknowledgeProcessing()
	if delivered := <-firstSent; !delivered {
		t.Fatal("first durable message was not delivered")
	}

	durableSent := make(chan struct{})
	go func() {
		sendProviderMessage(ctx, ch, Message{Type: MessageText, Content: "second"})
		close(durableSent)
	}()
	select {
	case <-durableSent:
		t.Fatal("durable message was dropped instead of applying backpressure")
	case <-time.After(20 * time.Millisecond):
	}

	m := <-ch
	select {
	case <-durableSent:
		t.Fatal("durable sender advanced before processing acknowledgement")
	case <-time.After(20 * time.Millisecond):
	}
	if m.Content != "second" {
		t.Fatalf("expected 'second', got %q", m.Content)
	}
	m.acknowledgeProcessing()
	select {
	case <-durableSent:
	case <-time.After(time.Second):
		t.Fatal("durable sender remained blocked after processing acknowledgement")
	}

	buffered := make(chan Message, 1)
	buffered <- Message{Type: MessageText, Content: "third"}
	if !sendProviderMessage(ctx, buffered, Message{Type: messageItemDelta, Content: "disposable"}) {
		t.Fatal("a dropped disposable delta must not terminate its producer")
	}
	m = <-buffered
	if m.Content != "third" {
		t.Fatalf("expected 'third', got %q", m.Content)
	}
	select {
	case m := <-buffered:
		t.Fatalf("expected full-channel delta to be dropped, got %+v", m)
	default:
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if sendProviderMessage(cancelled, make(chan Message), Message{Type: MessageText}) {
		t.Fatal("cancelled durable send reported delivery without a receiver")
	}
}

func TestBuildClaudeArgsInheritsMCPByDefault(t *testing.T) {
	t.Parallel()

	args := buildClaudeArgs(ExecOptions{}, "", slog.Default())
	expected := []string{
		"-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--permission-mode", "bypassPermissions",
		"--disallowedTools", "AskUserQuestion",
	}

	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
	for i, want := range expected {
		if args[i] != want {
			t.Fatalf("expected args[%d] = %q, got %q", i, want, args[i])
		}
	}
}

func TestBuildClaudeArgsUsesStrictMCPForManagedConfig(t *testing.T) {
	t.Parallel()

	args := buildClaudeArgs(ExecOptions{McpConfig: json.RawMessage(`{}`)}, "", slog.Default())
	if !slices.Contains(args, "--strict-mcp-config") {
		t.Fatalf("managed MCP config must enable strict mode, got %v", args)
	}
}

func TestBuildClaudeArgsUsesPrivateSystemPromptFile(t *testing.T) {
	t.Parallel()

	const brief = "the entire boardly runtime brief"
	const promptPath = "/private/boardly/system-prompt.txt"
	args := buildClaudeArgs(
		ExecOptions{SystemPrompt: brief},
		promptPath,
		slog.Default(),
	)
	if !slices.Contains(args, "--append-system-prompt-file="+promptPath) {
		t.Fatalf("managed system prompt file missing from args: %v", args)
	}
	if slices.Contains(args, brief) {
		t.Fatalf("SystemPrompt leaked into args: %v", args)
	}
}

func TestBuildClaudeArgsOmitsEmptySystemPrompt(t *testing.T) {
	t.Parallel()

	args := buildClaudeArgs(ExecOptions{}, "", slog.Default())
	for _, arg := range args {
		if strings.HasPrefix(arg, "--append-system-prompt") ||
			strings.HasPrefix(arg, "--system-prompt") {
			t.Fatalf("empty SystemPrompt produced %q in args: %v", arg, args)
		}
	}
}

func TestBuildClaudeArgsBlocksSystemPromptOverrides(t *testing.T) {
	t.Parallel()

	const promptPath = "/private/boardly/system-prompt.txt"
	args := buildClaudeArgs(ExecOptions{CustomArgs: []string{
		"--system-prompt", "unsafe-inline",
		"--system-prompt-file=/tmp/unsafe-system.txt",
		"--append-system-prompt", "unsafe-append",
		"--append-system-prompt-file", "/tmp/unsafe-append.txt",
		"--verbose",
	}}, promptPath, slog.Default())

	joined := strings.Join(args, "\x00")
	for _, unsafe := range []string{
		"unsafe-inline",
		"/tmp/unsafe-system.txt",
		"unsafe-append",
		"/tmp/unsafe-append.txt",
	} {
		if strings.Contains(joined, unsafe) {
			t.Fatalf("custom system prompt override %q reached args: %v", unsafe, args)
		}
	}
	if !slices.Contains(args, "--append-system-prompt-file="+promptPath) {
		t.Fatalf("managed system prompt file missing from args: %v", args)
	}
}

func TestValidateClaudeConnectorOnlySettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		mode    os.FileMode
		want    string
	}{
		{
			name:    "exact managed settings",
			content: `{"disableAllHooks":true,"enabledPlugins":{},"permissions":{"ask":["*"]}}`,
			mode:    0o600,
		},
		{
			name:    "hooks remain enabled",
			content: `{"disableAllHooks":false,"enabledPlugins":{},"permissions":{"ask":["*"]}}`,
			mode:    0o600,
			want:    "disable all hooks",
		},
		{
			name:    "permission allow rule",
			content: `{"disableAllHooks":true,"enabledPlugins":{},"permissions":{"ask":["*"],"allow":["Bash"]}}`,
			mode:    0o600,
			want:    "unexpected field",
		},
		{
			name:    "world readable",
			content: `{"disableAllHooks":true,"enabledPlugins":{},"permissions":{"ask":["*"]}}`,
			mode:    0o644,
			want:    "permissions",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(test.content), test.mode); err != nil {
				t.Fatal(err)
			}
			err := validateClaudeConnectorOnlySettings(path)
			if test.want == "" && err != nil {
				t.Fatalf("validateClaudeConnectorOnlySettings() error = %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestArgsRequestBypassPermissions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "permission mode bypass",
			args: []string{"--permission-mode", "bypassPermissions"},
			want: true,
		},
		{
			name: "dangerous skip permissions",
			args: []string{"--dangerously-skip-permissions"},
			want: true,
		},
		{
			name: "neither",
			args: []string{"--model", "sonnet"},
			want: false,
		},
		{
			name: "permission mode default",
			args: []string{"--permission-mode", "default"},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := argsRequestBypassPermissions(tc.args); got != tc.want {
				t.Fatalf("argsRequestBypassPermissions(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestFilterCustomArgsBlocksProtocolFlags(t *testing.T) {
	t.Parallel()

	blocked := map[string]blockedArgMode{
		"--output-format":   blockedWithValue,
		"--permission-mode": blockedWithValue,
		"-p":                blockedStandalone,
	}
	logger := slog.Default()

	// Blocks flag with separate value
	result := filterCustomArgs([]string{"--output-format", "text", "--model", "o3"}, blocked, logger)
	if len(result) != 2 || result[0] != "--model" || result[1] != "o3" {
		t.Fatalf("expected [--model o3], got %v", result)
	}

	// Blocks flag=value form
	result = filterCustomArgs([]string{"--permission-mode=plan", "--verbose"}, blocked, logger)
	if len(result) != 1 || result[0] != "--verbose" {
		t.Fatalf("expected [--verbose], got %v", result)
	}

	// Blocks standalone short flags without consuming next arg
	result = filterCustomArgs([]string{"-p", "--max-turns", "10"}, blocked, logger)
	if len(result) != 2 || result[0] != "--max-turns" || result[1] != "10" {
		t.Fatalf("expected [--max-turns 10], got %v", result)
	}

	// Passes through non-blocked args
	result = filterCustomArgs([]string{"--model", "o3", "--max-turns", "50"}, blocked, logger)
	if len(result) != 4 {
		t.Fatalf("expected all 4 args to pass through, got %v", result)
	}

	// Handles nil blocked map
	result = filterCustomArgs([]string{"--anything"}, nil, logger)
	if len(result) != 1 {
		t.Fatalf("expected args to pass through with nil blocked map, got %v", result)
	}

	// Handles empty args
	result = filterCustomArgs(nil, blocked, logger)
	if result != nil {
		t.Fatalf("expected nil for nil input, got %v", result)
	}
}

func TestFilterCustomArgsStripsShellQuotes(t *testing.T) {
	t.Parallel()

	logger := slog.Default()

	// Single-quoted inline value: --deny-tool='write' → --deny-tool=write
	result := filterCustomArgs([]string{"--deny-tool='write'"}, nil, logger)
	if len(result) != 1 || result[0] != "--deny-tool=write" {
		t.Fatalf("expected [--deny-tool=write], got %v", result)
	}

	// Double-quoted inline value: --deny-tool="write" → --deny-tool=write
	result = filterCustomArgs([]string{`--deny-tool="write"`}, nil, logger)
	if len(result) != 1 || result[0] != "--deny-tool=write" {
		t.Fatalf("expected [--deny-tool=write], got %v", result)
	}

	// Standalone quoted value: 'write' → write
	result = filterCustomArgs([]string{"'write'"}, nil, logger)
	if len(result) != 1 || result[0] != "write" {
		t.Fatalf("expected [write], got %v", result)
	}

	// Non-flag assignments may use quotes semantically (for example Codex
	// `-c model="o3"`), so they must survive unchanged.
	result = filterCustomArgs([]string{`model="o3"`}, nil, logger)
	if len(result) != 1 || result[0] != `model="o3"` {
		t.Fatalf("expected [model=\"o3\"], got %v", result)
	}

	// Unquoted arg passes through unchanged
	result = filterCustomArgs([]string{"--deny-tool=write"}, nil, logger)
	if len(result) != 1 || result[0] != "--deny-tool=write" {
		t.Fatalf("expected [--deny-tool=write], got %v", result)
	}

	// Mismatched quotes are not stripped
	result = filterCustomArgs([]string{"--flag='val\""}, nil, logger)
	if len(result) != 1 || result[0] != "--flag='val\"" {
		t.Fatalf("mismatched quotes should not be stripped, got %v", result)
	}

	// Blocked flag with quoted value: the blocking still fires, the unquoted
	// form passes the flag-match, and the arg is dropped.
	blocked := map[string]blockedArgMode{"--output-format": blockedWithValue}
	result = filterCustomArgs([]string{"--output-format='json'", "--verbose"}, blocked, logger)
	if len(result) != 1 || result[0] != "--verbose" {
		t.Fatalf("quoted blocked flag should still be dropped, got %v", result)
	}
}

func TestBuildClaudeArgsPassesThroughCustomArgs(t *testing.T) {
	t.Parallel()

	args := buildClaudeArgs(ExecOptions{
		CustomArgs: []string{"--max-turns", "50", "--verbose"},
	}, "", slog.Default())

	// Custom args should appear at the end
	found := 0
	for i, a := range args {
		if a == "--max-turns" && i+1 < len(args) && args[i+1] == "50" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("expected --max-turns 50 in args: %v", args)
	}
}

func TestBuildClaudeArgsFiltersBlockedCustomArgs(t *testing.T) {
	t.Parallel()

	args := buildClaudeArgs(ExecOptions{
		CustomArgs: []string{"--output-format", "text", "--model", "o3"},
	}, "", slog.Default())

	// --model o3 should pass through
	foundModel := false
	for i, a := range args {
		if a == "--model" && i+1 < len(args) && args[i+1] == "o3" {
			foundModel = true
		}
		// Verify no duplicate --output-format with value "text"
		if a == "--output-format" && i+1 < len(args) && args[i+1] == "text" {
			t.Fatalf("blocked --output-format text should have been filtered: %v", args)
		}
	}
	if !foundModel {
		t.Fatalf("expected --model o3 in args but it was missing: %v", args)
	}
}

func TestBuildClaudeInputEncodesUserMessage(t *testing.T) {
	t.Parallel()

	data, err := buildClaudeInput("say pong")
	if err != nil {
		t.Fatalf("buildClaudeInput: %v", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("expected newline-terminated payload, got %q", data)
	}

	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["type"] != "user" {
		t.Fatalf("expected type user, got %v", payload["type"])
	}

	message, ok := payload["message"].(map[string]any)
	if !ok {
		t.Fatalf("expected message object, got %T", payload["message"])
	}
	if message["role"] != "user" {
		t.Fatalf("expected role user, got %v", message["role"])
	}

	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected one content block, got %v", message["content"])
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("expected content block object, got %T", content[0])
	}
	if block["type"] != "text" || block["text"] != "say pong" {
		t.Fatalf("unexpected content block: %v", block)
	}
}

func TestMergeEnvFiltersClaudeCodeVars(t *testing.T) {
	t.Parallel()

	env := mergeEnv([]string{
		"PATH=/usr/bin",
		"CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=cli",
		"CLAUDE_CODE_EXECPATH=/opt/claude",
		"CLAUDE_CODE_SESSION_ID=abc123",
		"CLAUDE_CODE_SSE_PORT=9999",
		"CLAUDECODEX=keep-me",
		"CLAUDE_CODE_GIT_BASH_PATH=C:\\Program Files\\Git\\bin\\bash.exe",
		"CLAUDE_CODE_USE_BEDROCK=1",
		"CLAUDE_CODE_TMPDIR=/custom/tmp",
		"BOARDLY_LLM_API_KEY=daemon-secret",
		"BOARDLY_SERVER_URL=https://daemon.example",
	}, map[string]string{
		"FOO":                "bar",
		"BOARDLY_SERVER_URL": "https://run.example",
		"BOARDLY_TOKEN":      "bat_run",
	})

	// Internal runtime/session markers must be stripped so the child does not
	// inherit the parent's identity or transport.
	filteredOut := []string{
		"CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=cli",
		"CLAUDE_CODE_EXECPATH=/opt/claude",
		"CLAUDE_CODE_SESSION_ID=abc123",
		"CLAUDE_CODE_SSE_PORT=9999",
		"BOARDLY_LLM_API_KEY=daemon-secret",
		"BOARDLY_SERVER_URL=https://daemon.example",
	}
	for _, entry := range env {
		for _, banned := range filteredOut {
			if entry == banned {
				t.Fatalf("expected internal Claude Code marker %q to be filtered, got %v", banned, env)
			}
		}
	}

	found := map[string]bool{}
	for _, entry := range env {
		found[entry] = true
	}

	if !found["PATH=/usr/bin"] {
		t.Fatalf("expected PATH to be preserved, got %v", env)
	}
	if !found["CLAUDECODEX=keep-me"] {
		t.Fatalf("expected unrelated env vars to be preserved, got %v", env)
	}
	// User-facing CLAUDE_CODE_* config must reach the child — stripping
	// CLAUDE_CODE_GIT_BASH_PATH is what broke Claude Code on Windows.
	if !found["CLAUDE_CODE_GIT_BASH_PATH=C:\\Program Files\\Git\\bin\\bash.exe"] {
		t.Fatalf("expected CLAUDE_CODE_GIT_BASH_PATH to be preserved, got %v", env)
	}
	if !found["CLAUDE_CODE_USE_BEDROCK=1"] {
		t.Fatalf("expected CLAUDE_CODE_USE_BEDROCK to be preserved, got %v", env)
	}
	// CLAUDE_CODE_TMPDIR is a documented user-configurable temp-dir override, not
	// an internal per-session marker, so it must reach the child.
	if !found["CLAUDE_CODE_TMPDIR=/custom/tmp"] {
		t.Fatalf("expected CLAUDE_CODE_TMPDIR to be preserved, got %v", env)
	}
	if !found["FOO=bar"] {
		t.Fatalf("expected extra env var to be appended, got %v", env)
	}
	if !found["BOARDLY_SERVER_URL=https://run.example"] || !found["BOARDLY_TOKEN=bat_run"] {
		t.Fatalf("expected explicit run BOARDLY_* values to be appended, got %v", env)
	}
}

func TestBuildEnvAppendsExtras(t *testing.T) {
	t.Parallel()

	env := buildEnv(map[string]string{"FOO": "bar", "BAZ": "qux"})
	found := 0
	for _, e := range env {
		if e == "FOO=bar" || e == "BAZ=qux" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("expected 2 extra env vars, found %d", found)
	}
}

func TestBuildEnvNilExtras(t *testing.T) {
	t.Parallel()

	env := buildEnv(nil)
	if len(env) == 0 {
		t.Fatal("expected at least system env vars")
	}
}

func TestEnvHasSandbox(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  []string
		want bool
	}{
		{name: "one", env: []string{"IS_SANDBOX=1"}, want: true},
		{name: "true", env: []string{"IS_SANDBOX=true"}, want: true},
		{name: "yes", env: []string{"IS_SANDBOX=yes"}, want: true},
		{name: "on", env: []string{"IS_SANDBOX=on"}, want: true},
		{name: "zero", env: []string{"IS_SANDBOX=0"}, want: false},
		{name: "false", env: []string{"IS_SANDBOX=false"}, want: false},
		{name: "empty", env: []string{"IS_SANDBOX="}, want: false},
		{name: "absent", env: []string{"PATH=/usr/bin"}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := envHasSandbox(tc.env); got != tc.want {
				t.Fatalf("envHasSandbox(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestClaudeRootSudoPreflight(t *testing.T) {
	t.Parallel()

	t.Run("sandbox bypass allowed", func(t *testing.T) {
		t.Parallel()

		err := claudeRootSudoPreflight(
			[]string{"--permission-mode", "bypassPermissions"},
			[]string{"IS_SANDBOX=1"},
		)
		if err != nil {
			t.Fatalf("expected sandboxed bypass to pass preflight, got %v", err)
		}
	})

	t.Run("non bypass allowed", func(t *testing.T) {
		t.Parallel()

		err := claudeRootSudoPreflight(
			[]string{"--permission-mode", "default"},
			nil,
		)
		if err != nil {
			t.Fatalf("expected non-bypass args to pass preflight, got %v", err)
		}
	})

	t.Run("root bypass without sandbox errors", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() != 0 {
			t.Skip("root-only preflight assertion")
		}

		err := claudeRootSudoPreflight(
			[]string{"--permission-mode", "bypassPermissions"},
			nil,
		)
		if err == nil {
			t.Fatal("expected root bypass without sandbox to fail preflight")
		}
		if !strings.Contains(err.Error(), "IS_SANDBOX") || !strings.Contains(err.Error(), "non-root") {
			t.Fatalf("expected actionable root guidance, got %q", err.Error())
		}
	})
}

func TestBuildClaudeArgsBlocksMcpConfig(t *testing.T) {
	t.Parallel()

	// --mcp-config is hardcoded by the daemon — it must not be overridable via custom_args.
	args := buildClaudeArgs(ExecOptions{
		CustomArgs: []string{"--mcp-config", "/tmp/evil.json", "--model", "o3"},
	}, "", slog.Default())

	for i, a := range args {
		if a == "--mcp-config" {
			t.Fatalf("--mcp-config should be blocked from custom_args, found at index %d: %v", i, args)
		}
		if a == "/tmp/evil.json" {
			t.Fatalf(
				"--mcp-config value should be consumed when blocking, but found it at index %d: %v",
				i,
				args,
			)
		}
	}

	// Non-blocked args should still pass through.
	foundModel := false
	for i, a := range args {
		if a == "--model" && i+1 < len(args) && args[i+1] == "o3" {
			foundModel = true
		}
	}
	if !foundModel {
		t.Fatalf("expected --model o3 in args after blocking --mcp-config: %v", args)
	}
}

func TestWriteMcpConfigToTemp(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"mcpServers":{"test":{"command":"echo","args":["hello"]}}}`)
	path, err := writeMcpConfigToTemp(raw)
	if err != nil {
		t.Fatalf("writeMcpConfigToTemp: %v", err)
	}

	// File should exist and contain exactly the raw JSON.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file %s: %v", path, err)
	}
	if !bytes.Equal(data, []byte(raw)) {
		t.Fatalf("expected %s, got %s", raw, data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat temp file %s: %v", path, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("temp MCP file permissions = %o, want no group/other access", info.Mode().Perm())
		}
	}

	// Cleanup should remove the temp directory and every related sidecar file.
	cleanupMcpConfigTemp(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected temp file to be removed, but it still exists")
	}
}

func TestResumeWasRejected(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		requested string
		emitted   string
		failed    bool
		texts     []string
		want      bool
	}{
		{
			// Verbatim from the field report against Claude Code 2.1.207
			// (zh-CN). This is the failure the whole signal exists for: the
			// session belongs to the account the user switched away from, and
			// claude echoes the requested id back rather than a fresh one.
			name:      "account binding 400 echoing the requested id is a rejection",
			requested: "sess-old",
			emitted:   "sess-old",
			failed:    true,
			texts:     []string{"400 此 session 已绑定另外的ai账号，请执行 /new 开启新 session"},
			want:      true,
		},
		{
			name:      "no conversation found on stderr is a rejection",
			requested: "sess-dead",
			emitted:   "sess-dead",
			failed:    true,
			texts:     []string{"No conversation found with session ID: sess-dead"},
			want:      true,
		},
		{
			name:      "a different emitted id means the resume did not land",
			requested: "sess-dead",
			emitted:   "fresh-new",
			failed:    true,
			want:      true,
		},

		// Failures a fresh session cannot cure. None of these may set the
		// flag: doing so would throw away a session the platform's own retry
		// is expected to resume.
		{
			name:      "mid-response network drop is not a rejection",
			requested: "sess-old",
			emitted:   "sess-old",
			failed:    true,
			texts:     []string{"API Error: Connection closed mid-response"},
			want:      false,
		},
		{
			name:      "rate limit is not a rejection",
			requested: "sess-old",
			emitted:   "sess-old",
			failed:    true,
			texts:     []string{"API Error: 429 rate_limit_error: rate limit exceeded"},
			want:      false,
		},
		{
			name:      "overloaded is not a rejection",
			requested: "sess-old",
			emitted:   "sess-old",
			failed:    true,
			texts:     []string{"API Error: 529 overloaded_error"},
			want:      false,
		},
		{
			name:      "quota exhaustion is not a rejection",
			requested: "sess-old",
			emitted:   "sess-old",
			failed:    true,
			texts:     []string{"API Error: 402 credit balance is too low"},
			want:      false,
		},
		{
			// Reported alongside the account-binding 400 and easy to conflate
			// with it. This one needs a re-login, not a new session.
			name:      "revoked oauth token is not a rejection",
			requested: "sess-old",
			emitted:   "sess-old",
			failed:    true,
			texts: []string{
				"401 Invalid authentication credentials: OAuth access token has been revoked",
			},
			want: false,
		},
		{
			name:      "a run that succeeded is never a rejection",
			requested: "sess-old",
			emitted:   "fresh-new",
			failed:    false,
			texts:     []string{"No conversation found with session ID: sess-old"},
			want:      false,
		},
		{
			name:      "no resume requested is never a rejection",
			requested: "",
			emitted:   "fresh-abc",
			failed:    true,
			texts:     []string{"No conversation found with session ID: whatever"},
			want:      false,
		},
		{
			// A failure before claude emitted anything. Empty is not
			// evidence of anything, which is why the daemon stopped reading
			// an empty SessionID as "the resume was rejected".
			name:      "failure with no emitted id and no matching text is not a rejection",
			requested: "sess-old",
			emitted:   "",
			failed:    true,
			texts:     []string{"exit status 1"},
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resumeWasRejected(tc.requested, tc.emitted, tc.failed, tc.texts...)
			if got != tc.want {
				t.Fatalf("resumeWasRejected(%q, %q, %v, %q) = %v, want %v",
					tc.requested, tc.emitted, tc.failed, tc.texts, got, tc.want)
			}
		})
	}
}

func TestResolveSessionID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		requested string
		emitted   string
		failed    bool
		stderr    string
		want      string
	}{
		{
			name:      "no resume requested propagates emitted",
			requested: "",
			emitted:   "fresh-abc",
			failed:    false,
			stderr:    "",
			want:      "fresh-abc",
		},
		{
			name:      "resume succeeded keeps matching id",
			requested: "sess-old",
			emitted:   "sess-old",
			failed:    false,
			stderr:    "",
			want:      "sess-old",
		},
		{
			name:      "resume succeeded but run failed mid-turn keeps id for later retry",
			requested: "sess-old",
			emitted:   "sess-old",
			failed:    true,
			stderr:    "",
			want:      "sess-old",
		},
		{
			name:      "resume did not land and run failed clears id so daemon fallback fires",
			requested: "sess-dead",
			emitted:   "fresh-new",
			failed:    true,
			stderr:    "",
			want:      "",
		},
		{
			name:      "resume not found stderr clears matching id so daemon fallback fires",
			requested: "sess-dead",
			emitted:   "sess-dead",
			failed:    true,
			stderr:    "No conversation found with session ID: sess-dead",
			want:      "",
		},
		{
			name:      "resume did not land but run succeeded keeps fresh id (defensive)",
			requested: "sess-dead",
			emitted:   "fresh-new",
			failed:    false,
			stderr:    "No conversation found with session ID: sess-dead",
			want:      "fresh-new",
		},
		{
			name:      "no emitted id leaves result empty",
			requested: "sess-old",
			emitted:   "",
			failed:    true,
			stderr:    "",
			want:      "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveSessionID(tc.requested, tc.emitted, tc.failed, tc.stderr)
			if got != tc.want {
				t.Fatalf("resolveSessionID(%q, %q, %v, %q) = %q, want %q",
					tc.requested, tc.emitted, tc.failed, tc.stderr, got, tc.want)
			}
		})
	}
}

func TestClaudeExecuteSurfacesStderrWhenChildExitsEarly(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	// Fake claude binary: reads the initial stdin frame so writeClaudeInput
	// succeeds, writes a canonical V8-abort line to stderr, then exits
	// non-zero before emitting any stream-json to stdout. This is the exact
	// failure mode that motivated the stderr tail — without sampling
	// stderrBuf.Tail() after cmd.Wait() returns, Result.Error would be a
	// useless "exit status 3".
	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		"echo \"FATAL ERROR: V8 abort: assertion failed Authorization: Bearer super-secret-token\" >&2\n" +
		"exit 3\n"
	writeTestExecutable(t, fakePath, []byte(script))

	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), providerFixtureWaitTimeout)
	defer cancel()
	session, err := provider.Execute(ctx, "prompt-ignored", ExecOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	results := executionResults(session)

	select {
	case result, ok := <-results:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		if result.Status != "failed" {
			t.Fatalf("expected status=failed, got %q (error=%q)", result.Status, result.Error)
		}
		if !strings.Contains(result.Error, "claude exited with error") {
			t.Fatalf("expected error to mention exit, got %q", result.Error)
		}
		if !strings.Contains(result.Error, "V8 abort: assertion failed") {
			t.Fatalf("expected error to include stderr hint, got %q", result.Error)
		}
		if !strings.Contains(result.Error, "claude stderr:") {
			t.Fatalf("expected stderr label in error, got %q", result.Error)
		}
		if strings.Contains(result.Error, "super-secret-token") {
			t.Fatalf("stderr secret reached Result.Error: %q", result.Error)
		}
	case <-time.After(providerFixtureWaitTimeout):
		t.Fatal("timeout waiting for result")
	}
}

func TestClaudeExecuteDeliversSystemPromptFromPrivateFileAndCleansItUp(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	const brief = "private Boardly runtime brief\nwith a second line"
	tempDir := t.TempDir()
	capturePath := filepath.Join(tempDir, "system-prompt-capture")
	fakePath := filepath.Join(tempDir, "claude")
	script := `#!/bin/sh
prompt_path=''
for arg in "$@"; do
  case "$arg" in
    --append-system-prompt-file=*) prompt_path=${arg#*=} ;;
  esac
done
if [ -z "$prompt_path" ] || [ ! -f "$prompt_path" ]; then
  echo "managed system prompt file missing" >&2
  exit 42
fi
case "$(uname -s)" in
  Darwin) file_mode=$(stat -f '%Lp' "$prompt_path"); dir_mode=$(stat -f '%Lp' "$(dirname "$prompt_path")") ;;
  *) file_mode=$(stat -c '%a' "$prompt_path"); dir_mode=$(stat -c '%a' "$(dirname "$prompt_path")") ;;
esac
{
  printf '%s\n' "$prompt_path"
  printf '%s\n' "$file_mode"
  printf '%s\n' "$dir_mode"
  cat "$prompt_path"
} > "$CAPTURE_PATH"
IFS= read -r _
echo '{"type":"result","subtype":"success","session_id":"session-1","result":"done"}'
`
	writeTestExecutable(t, fakePath, []byte(script))

	var logs bytes.Buffer
	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"CAPTURE_PATH": capturePath, "IS_SANDBOX": "1"},
		Logger:         slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	execution, err := provider.Execute(t.Context(), "perform the run", ExecOptions{
		SystemPrompt: brief,
		Timeout:      providerFixtureExecTimeout,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	result, _ := collectExecution(t, execution, providerFixtureWaitTimeout)
	if result.Status != ResultCompleted {
		t.Fatalf("result status = %q, want completed (error = %q)", result.Status, result.Error)
	}

	captured, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read system prompt capture: %v", err)
	}
	parts := strings.SplitN(string(captured), "\n", 4)
	if len(parts) != 4 {
		t.Fatalf("system prompt capture = %q, want path, two modes, and content", captured)
	}
	promptPath, fileMode, dirMode, gotBrief := parts[0], parts[1], parts[2], parts[3]
	if gotBrief != brief {
		t.Fatalf("system prompt content = %q, want %q", gotBrief, brief)
	}
	if fileMode != "600" || dirMode != "700" {
		t.Fatalf("system prompt file modes = file %s, directory %s; want 600 and 700", fileMode, dirMode)
	}
	if _, err := os.Stat(promptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("system prompt file remains after execution: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(promptPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("system prompt directory remains after execution: %v", err)
	}
	if strings.Contains(logs.String(), brief) {
		t.Fatal("system prompt content leaked into provider logs")
	}
}

func TestClaudeExecuteRedactsVendorLogFrames(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"log","log":{"level":"debug","message":"Authorization: Bearer super-secret-token"}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","session_id":"session-1","result":"done"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))

	var logs bytes.Buffer
	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})),
	})
	execution, err := provider.Execute(t.Context(), "prompt", ExecOptions{Timeout: providerFixtureExecTimeout})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for event := range execution.Events {
		if item, ok := event.(CompletedItem); ok {
			item.Acknowledge()
		}
	}
	if got := logs.String(); strings.Contains(got, "super-secret-token") ||
		!strings.Contains(got, "[REDACTED]") {
		t.Fatalf("vendor log redaction = %q", got)
	}
}

func TestClaudeExecuteEmitsOneOrderedEventStream(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"system","subtype":"init","session_id":"session-1"}'` + "\n" +
		`echo '{"type":"assistant","message":{"id":"message-1","role":"assistant","content":[{"type":"thinking","thinking":"considering"},{"type":"text","text":"answer"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","session_id":"session-1","result":"answer"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))

	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	execution, err := provider.Execute(t.Context(), "prompt", ExecOptions{Timeout: providerFixtureExecTimeout})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var got []Event
	for event := range execution.Events {
		got = append(got, event)
		if item, ok := event.(CompletedItem); ok {
			item.Acknowledge()
		}
	}
	if len(got) != 3 {
		t.Fatalf("len(events) = %d, want 3: %#v", len(got), got)
	}
	ready, ok := got[0].(SessionReady)
	if !ok || ready.SessionID != "session-1" {
		t.Fatalf("events[0] = %#v, want SessionReady(session-1)", got[0])
	}
	item, ok := got[1].(CompletedItem)
	if !ok {
		t.Fatalf("events[1] = %T, want CompletedItem", got[1])
	}
	if item.Key != "message-1" || item.Role != RoleAgent || len(item.Parts) != 2 {
		t.Fatalf("completed item = %#v, want keyed assistant with two parts", item)
	}
	if reasoning, ok := item.Parts[0].(ReasoningPart); !ok || reasoning.Text != "considering" {
		t.Fatalf("parts[0] = %#v, want reasoning part", item.Parts[0])
	}
	if text, ok := item.Parts[1].(TextPart); !ok || text.Text != "answer" {
		t.Fatalf("parts[1] = %#v, want text part", item.Parts[1])
	}
	finished, ok := got[2].(Finished)
	if !ok || finished.Result.Status != "completed" || finished.Result.Output != "answer" {
		t.Fatalf("events[2] = %#v, want completed Finished", got[2])
	}
}

func TestClaudeExecuteFailsClosedOnUnknownMixedAssistantContent(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"assistant","message":{"id":"message-1","role":"assistant","content":[{"type":"text","text":"partial"},{"type":"future_content","value":"not normalized"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","session_id":"session-1","result":"partial"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))
	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	execution, err := provider.Execute(t.Context(), "prompt", ExecOptions{Timeout: providerFixtureExecTimeout})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var got []Event
	for event := range execution.Events {
		got = append(got, event)
		if item, ok := event.(CompletedItem); ok {
			item.Acknowledge()
		}
	}
	if len(got) != 1 {
		t.Fatalf("events = %#v, want only failed Finished", got)
	}
	finished, ok := got[0].(Finished)
	if !ok || finished.Result.Status != "failed" ||
		!strings.Contains(finished.Result.Error, "unsupported assistant content block") {
		t.Fatalf("terminal event = %#v", got[0])
	}
}

func TestClaudeExecuteFailsClosedOnMalformedKnownUserResult(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"user","message":{"id":"message-1","role":"user","content":[{"type":"mcp_tool_result","tool_use_id":"mcp-1"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","session_id":"session-1","result":"done"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))
	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	execution, err := provider.Execute(t.Context(), "prompt", ExecOptions{Timeout: providerFixtureExecTimeout})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	result, events := collectExecution(t, execution, providerFixtureWaitTimeout)
	if len(events) != 1 || result.Status != "failed" ||
		!strings.Contains(result.Error, "mcp_tool_result block is missing content") {
		t.Fatalf("events = %#v, result = %+v", events, result)
	}
}

func TestClaudeExecuteFailsClosedOnMalformedStreamFrame(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"assistant","message":{"id":"message-1","role":"assistant","content":[{"type":"text","text":"durable prefix"}]}}'` + "\n" +
		`echo '{"type":"assistant","message":'` + "\n" +
		`echo '{"type":"result","subtype":"success","session_id":"session-1","result":"durable prefix"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))
	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	execution, err := provider.Execute(t.Context(), "prompt", ExecOptions{Timeout: providerFixtureExecTimeout})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	result, events := collectExecution(t, execution, providerFixtureWaitTimeout)
	if len(events) != 2 || result.Status != "failed" ||
		!strings.Contains(result.Error, "decode Claude stream frame") {
		t.Fatalf("events = %#v, result = %+v", events, result)
	}
}

func TestClaudeExecuteAcceptsStringUserContent(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"user","message":{"id":"message-1","role":"user","content":"injected provider context"}}'` + "\n" +
		`echo '{"type":"assistant","message":{"id":"message-2","role":"assistant","content":[{"type":"text","text":"done"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","session_id":"session-1","result":"done"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))
	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	execution, err := provider.Execute(t.Context(), "prompt", ExecOptions{Timeout: providerFixtureExecTimeout})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	result, events := collectExecution(t, execution, providerFixtureWaitTimeout)
	if len(events) != 2 || result.Status != "completed" || result.Output != "done" {
		t.Fatalf("events = %#v, result = %+v", events, result)
	}
}

func TestClaudeExecuteKeepsTerminalAfterLastItemReceiptOutlivesProviderTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"assistant","message":{"id":"message-1","role":"assistant","content":[{"type":"text","text":"durable"}]}}'` + "\n" +
		"sleep 10\n"
	writeTestExecutable(t, fakePath, []byte(script))
	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})
	execution, err := provider.Execute(t.Context(), "prompt", ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	event := <-execution.Events
	item, ok := event.(CompletedItem)
	if !ok {
		t.Fatalf("event = %#v, want CompletedItem", event)
	}
	time.Sleep(5200 * time.Millisecond)
	item.Acknowledge()
	result, _ := collectExecution(t, execution, providerFixtureWaitTimeout)
	if result.Status != "timeout" {
		t.Fatalf("result after delayed receipt = %+v", result)
	}
}

// End-to-end through the provider rather than the predicate alone: a fake
// claude reproduces the account-switch guardrail — it echoes the resumed
// session id back on the init frame, then fails the turn with the 400. The
// result must carry ResumeRejected so the daemon retries fresh, and must not
// persist the dead session id.
func TestClaudeExecuteReportsResumeRejectedOnAccountBinding(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"system","subtype":"init","session_id":"sess-old"}'` + "\n" +
		`echo '{"type":"result","subtype":"error","is_error":true,"session_id":"sess-old","result":"400 此 session 已绑定另外的ai账号，请执行 /new 开启新 session"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))

	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), providerFixtureWaitTimeout)
	defer cancel()
	session, err := provider.Execute(ctx, "prompt", ExecOptions{
		ResumeSessionID: "sess-old",
		Timeout:         providerFixtureExecTimeout,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	results := executionResults(session)

	select {
	case result, ok := <-results:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		if result.Status != "failed" {
			t.Fatalf("expected status=failed, got %q", result.Status)
		}
		if !result.ResumeRejected {
			t.Fatalf("expected ResumeRejected for the account-binding 400, got error=%q", result.Error)
		}
		// The session belongs to the previous account; keeping it would make
		// every later dispatch on this (agent, workdir) fail identically.
		if result.SessionID != "" {
			t.Fatalf("expected the dead session id to be dropped, got %q", result.SessionID)
		}
		// The provider's own wording has to survive so a matcher miss on some
		// other locale is diagnosable from the failure itself.
		if !strings.Contains(result.Error, "已绑定另外") {
			t.Fatalf("expected the raw provider error to be preserved, got %q", result.Error)
		}
	case <-time.After(providerFixtureWaitTimeout):
		t.Fatal("timeout waiting for result")
	}
}

// The counterpart: a resumed run that dies from a transient network fault
// must NOT be reported as a rejected resume, and must keep its session id so
// the platform's resume-safe retry can continue the conversation.
func TestClaudeExecuteKeepsSessionOnNetworkFailure(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		`echo '{"type":"system","subtype":"init","session_id":"sess-old"}'` + "\n" +
		`echo '{"type":"result","subtype":"error","is_error":true,"session_id":"sess-old","result":"API Error: Connection closed mid-response"}'` + "\n"
	writeTestExecutable(t, fakePath, []byte(script))

	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), providerFixtureWaitTimeout)
	defer cancel()
	session, err := provider.Execute(ctx, "prompt", ExecOptions{
		ResumeSessionID: "sess-old",
		Timeout:         providerFixtureExecTimeout,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	results := executionResults(session)

	select {
	case result, ok := <-results:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		if result.ResumeRejected {
			t.Fatal("a network drop must not be reported as a rejected resume")
		}
		if result.SessionID != "sess-old" {
			t.Fatalf("expected the session to survive a network failure, got %q", result.SessionID)
		}
	case <-time.After(providerFixtureWaitTimeout):
		t.Fatal("timeout waiting for result")
	}
}

func TestClaudeExecuteRecordsResultModelUsage(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	fakePath := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"session_id\":\"sess-result-usage\"}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"sess-result-usage\",\"result\":\"done\",\"modelUsage\":{\"zhipu/coding-plan\":{\"inputTokens\":123,\"outputTokens\":45,\"cacheReadInputTokens\":7,\"cacheCreationInputTokens\":11,\"costUSD\":0.01}}}'\n"
	writeTestExecutable(t, fakePath, []byte(script))

	provider := mustNewProvider(t, "claude", Config{
		ExecutablePath: fakePath,
		Env:            map[string]string{"IS_SANDBOX": "1"},
		Logger:         slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), providerFixtureWaitTimeout)
	defer cancel()
	session, err := provider.Execute(ctx, "prompt-ignored", ExecOptions{Timeout: providerFixtureExecTimeout})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	results := executionResults(session)

	select {
	case result, ok := <-results:
		if !ok {
			t.Fatal("result channel closed without a value")
		}
		usage, ok := result.Usage["zhipu/coding-plan"]
		if !ok {
			t.Fatalf("expected usage for zhipu/coding-plan, got %#v", result.Usage)
		}
		if usage.InputTokens != 123 || usage.OutputTokens != 45 || usage.CacheReadTokens != 7 ||
			usage.CacheWriteTokens != 11 {
			t.Fatalf("unexpected usage: %+v", usage)
		}
		if usage.RawJSON == "" || strings.Contains(usage.RawJSON, "zhipu/coding-plan") ||
			strings.Contains(usage.RawJSON, "result") {
			t.Fatalf("raw usage fragment = %q", usage.RawJSON)
		}
	case <-time.After(providerFixtureWaitTimeout):
		t.Fatal("timeout waiting for result")
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return data
}

func TestBuildClaudeArgsExtraArgsBeforeCustomArgsAndFiltersBoth(t *testing.T) {
	args := buildClaudeArgs(ExecOptions{
		ExtraArgs:  []string{"--output-format", "text", "--max-budget-usd", "1.00"},
		CustomArgs: []string{"--max-budget-usd", "2.00", "--permission-mode", "plan"},
	}, "", slog.Default())
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--output-format text") || strings.Contains(joined, "--permission-mode plan") {
		t.Fatalf("blocked args should be filtered from both layers: %v", args)
	}
	extraIdx, customIdx := -1, -1
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--max-budget-usd" && args[i+1] == "1.00" {
			extraIdx = i
		}
		if args[i] == "--max-budget-usd" && args[i+1] == "2.00" {
			customIdx = i
		}
	}
	if extraIdx == -1 || customIdx == -1 || extraIdx > customIdx {
		t.Fatalf("expected extra args before custom args, got %v", args)
	}
}

func TestBuildClaudeArgsManagedSkillSettingsWins(t *testing.T) {
	args := buildClaudeArgs(ExecOptions{
		ClaudeSettingsPath: "/tmp/boardly-claude-settings.json",
		ExtraArgs:          []string{"--settings", "/tmp/default.json"},
		CustomArgs:         []string{"--settings=/tmp/agent.json", "--max-turns", "7"},
	}, "", slog.Default())
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "default.json") || strings.Contains(joined, "agent.json") {
		t.Fatalf("competing settings args were not filtered: %v", args)
	}
	if !strings.Contains(joined, "--settings /tmp/boardly-claude-settings.json") {
		t.Fatalf("managed settings missing: %v", args)
	}
	if !strings.Contains(joined, "--max-turns 7") {
		t.Fatalf("unrelated custom arg was dropped: %v", args)
	}
}

func TestClaudeChannelFinalProofRequiresSuccessfulTerminalResult(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	for _, tc := range []struct {
		name, result string
		proof        bool
		want         int
	}{
		{name: "successful channel answer", result: `{"type":"result","subtype":"success","is_error":false,"result":"Final answer"}`, proof: true, want: 1},
		{name: "ordinary chat", result: `{"type":"result","subtype":"success","is_error":false,"result":"Final answer"}`, want: 0},
		{name: "error result", result: `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Failure"}`, proof: true, want: 0},
		{name: "empty result", result: `{"type":"result","subtype":"success","is_error":false,"result":""}`, proof: true, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "claude")
			script := "#!/bin/sh\nIFS= read -r _\n" + `echo '{"type":"assistant","message":{"id":"narration","role":"assistant","content":[{"type":"text","text":"Working on it"}]}}'` + "\necho '" + tc.result + "'\n"
			writeTestExecutable(t, path, []byte(script))
			provider := mustNewProvider(
				t,
				"claude",
				Config{
					ExecutablePath: path,
					Env:            map[string]string{"IS_SANDBOX": "1"},
					Logger:         slog.Default(),
				},
			)
			execution, err := provider.Execute(
				t.Context(),
				"hello",
				ExecOptions{Timeout: providerFixtureExecTimeout, RequireFinalAnswerProof: tc.proof},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, events := collectExecution(t, execution, providerFixtureWaitTimeout)
			got := 0
			for _, event := range events {
				if item, ok := event.(CompletedItem); ok {
					for _, part := range item.Parts {
						if text, ok := part.(TextPart); ok && text.Final {
							got++
							if text.Text != "Final answer" {
								t.Fatalf("published text = %q", text.Text)
							}
						}
					}
				}
			}
			if got != tc.want {
				t.Fatalf("proven final items = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBuildClaudeArgsLoadsRunSkillsOnlyOutsideConnectorLane(t *testing.T) {
	opts := ExecOptions{ClaudeSkillsRoot: "/tmp/run-skills"}
	args := strings.Join(buildClaudeArgs(opts, "", slog.Default()), " ")
	if !strings.Contains(args, "--add-dir /tmp/run-skills") {
		t.Fatalf("missing skills root: %s", args)
	}
	opts.ToolPolicy = ToolPolicyConnectorOnly
	args = strings.Join(buildClaudeArgs(opts, "", slog.Default()), " ")
	if strings.Contains(args, "run-skills") {
		t.Fatalf("schedule loaded skills: %s", args)
	}
}
