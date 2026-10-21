package execution

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func TestMapTranscriptRowsRejectsUndecodableParts(t *testing.T) {
	t.Parallel()

	_, err := MapTranscriptRows([]db.ConversationItem{{Parts: []byte(`{"type":"text"}`)}})
	if err == nil {
		t.Fatal("MapTranscriptRows accepted a non-array parts document")
	}
}

// A Run's slice of the one stream also holds the user's own words, and the
// run-detail queries are what exclude them. This seam does not exclude them a
// second time — it refuses the row — so a query that stopped excluding it fails
// loudly here instead of being absorbed into a page that is short by one and
// carries a cursor pointing at nothing.
func TestMapTranscriptRowsRefusesTheRunsUserMessage(t *testing.T) {
	t.Parallel()

	_, err := MapTranscriptRows([]db.ConversationItem{
		{Type: "user_message", Content: "do the thing"},
	})
	if err == nil {
		t.Fatal("MapTranscriptRows accepted a row with no provider parts")
	}
	if !strings.Contains(err.Error(), "user_message") {
		t.Fatalf("error = %v, want it to name the row a run-detail read should have excluded", err)
	}
}

// What it does map is every row it is handed, provider shape intact.
func TestMapTranscriptRowsMapsProviderItems(t *testing.T) {
	t.Parallel()

	items, err := MapTranscriptRows([]db.ConversationItem{
		{
			RunID: uuid.NullUUID{
				UUID:  uuid.MustParse("00000000-0000-4000-8000-000000004711"),
				Valid: true,
			},
			Type:    "agent_text",
			ItemKey: pgtype.Text{String: "item-1", Valid: true},
			Seq:     7,
			Parts:   []byte(`[{"type":"text","text":"done"}]`),
		},
	})
	if err != nil {
		t.Fatalf("MapTranscriptRows() error = %v", err)
	}
	if len(items) != 1 || items[0].RunID != uuid.MustParse("00000000-0000-4000-8000-000000004711") ||
		items[0].ItemKey != "item-1" ||
		items[0].Seq != 7 ||
		items[0].Role != protocol.RunItemRoleAgent {
		t.Fatalf("transcript entries = %+v, want the provider item", items)
	}
}

// The one stream stores `type`, and type is the whole visibility rule: only
// 'agent_text' reaches the chat page. This is where an agent's raw tool output
// would leak into a user's chat if the mapping were wrong.
func TestTranscriptItemTypeAndContentDerivesVisibilityFromTheItem(t *testing.T) {
	t.Parallel()

	text := func(value string) protocol.RunItemPart {
		return protocol.RunItemPart{Type: protocol.RunItemPartTypeText, Text: value}
	}
	reasoning := protocol.RunItemPart{
		Type: protocol.RunItemPartTypeReasoning, Text: "thinking",
	}
	toolCall := protocol.RunItemPart{
		Type: protocol.RunItemPartTypeToolCall, CallID: "call-1", Name: "read_file",
	}
	toolResult := protocol.RunItemPart{
		Type: protocol.RunItemPartTypeToolResult, CallID: "call-1", Output: "contents",
	}
	failure := protocol.RunItemPart{
		Type: protocol.RunItemPartTypeError, Text: "boom",
	}

	tests := []struct {
		name        string
		role        protocol.RunItemRole
		parts       []protocol.RunItemPart
		wantType    string
		wantContent string
	}{
		{
			name: "agent text is the only visible type",
			role: protocol.RunItemRoleAgent, parts: []protocol.RunItemPart{text("Hello.")},
			wantType: "agent_text", wantContent: "Hello.",
		},
		{
			// The daemon nominates its terminal final item by this exact
			// test — agent role, a non-empty text part — so an item it
			// nominates must be stored as agent_text or the completion
			// cannot find it and the Run wedges.
			name: "reasoning and tool calls beside the answer do not hide it",
			role: protocol.RunItemRoleAgent,
			parts: []protocol.RunItemPart{
				reasoning, toolCall, text("Hello, "), text("world."),
			},
			wantType: "agent_text", wantContent: "Hello, world.",
		},
		{
			name: "a tool's prose is not the agent speaking",
			role: protocol.RunItemRoleTool, parts: []protocol.RunItemPart{text("raw output")},
			wantType: "reasoning", wantContent: "raw output",
		},
		{
			name: "reasoning alone stays run detail",
			role: protocol.RunItemRoleAgent, parts: []protocol.RunItemPart{reasoning},
			wantType: "reasoning", wantContent: "thinking",
		},
		{
			name: "tool call",
			role: protocol.RunItemRoleAgent, parts: []protocol.RunItemPart{toolCall},
			wantType: "tool_call", wantContent: "read_file",
		},
		{
			name: "tool result",
			role: protocol.RunItemRoleTool, parts: []protocol.RunItemPart{toolResult},
			wantType: "tool_result", wantContent: "contents",
		},
		{
			name: "error",
			role: protocol.RunItemRoleSystem, parts: []protocol.RunItemPart{failure},
			wantType: "error", wantContent: "boom",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			gotType, gotContent := transcriptItemTypeAndContent(test.role, test.parts)
			if gotType != test.wantType || gotContent != test.wantContent {
				t.Fatalf("type/content = %q/%q, want %q/%q",
					gotType, gotContent, test.wantType, test.wantContent)
			}
		})
	}
}

func TestNormalizeTranscriptItemBuildsCanonicalRedactedContent(t *testing.T) {
	t.Parallel()

	input := TranscriptItemInput{
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{
			{
				Type: protocol.RunItemPartTypeText,
				Text: "credential AKIAABCDEFGHIJKLMNOP",
			},
			{
				Type:   protocol.RunItemPartTypeToolCall,
				CallID: "call-1",
				Name:   "read_file",
				Input: json.RawMessage(
					`{ "z": 1, "a": "AKIAABCDEFGHIJKLMNOP", "password": "hunter2", "apiKey": "short-secret" }`,
				),
			},
		},
	}

	got, err := normalizeTranscriptItem(input)
	if err != nil {
		t.Fatalf("normalizeTranscriptItem() error = %v", err)
	}
	if got.itemKey != "item-1" {
		t.Fatalf("item key = %q, want item-1", got.itemKey)
	}
	wantParts := `[{"type":"text","text":"credential [redacted:aws-key]"},{"type":"toolCall","callId":"call-1","name":"read_file","input":{"a":"[redacted:aws-key]","apiKey":"[redacted:json-secret]","password":"[redacted:json-secret]","z":1}}]`
	if string(got.partsJSON) != wantParts {
		t.Fatalf("parts JSON = %s, want %s", got.partsJSON, wantParts)
	}
	wantContent := `{"itemKey":"item-1","role":"agent","parts":` + wantParts + `}`
	wantDigest := sha256.Sum256([]byte(wantContent))
	if got.contentDigest != wantDigest {
		t.Fatalf("content digest = %x, want %x", got.contentDigest, wantDigest)
	}

	// The normalized item owns its immutable copy; caller mutation after the
	// seam cannot change the bytes or event parts that will be committed.
	input.Parts[0].Text = "mutated"
	input.Parts[1].Input[0] = '['
	if got.parts[0].Text != "credential [redacted:aws-key]" ||
		string(
			got.parts[1].Input,
		) != `{"a":"[redacted:aws-key]","apiKey":"[redacted:json-secret]","password":"[redacted:json-secret]","z":1}` {
		t.Fatalf("normalized parts changed with caller input: %+v", got.parts)
	}
}

func TestNormalizeTranscriptItemPreservesEmailAndRedactsCredentials(t *testing.T) {
	t.Parallel()

	got, err := normalizeTranscriptItem(TranscriptItemInput{
		ItemKey: "mail-draft-reply",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeText,
			Text: "Created a draft for alice@example.com. password=hunter2", Final: true,
		}},
	})
	if err != nil {
		t.Fatalf("normalizeTranscriptItem() error = %v", err)
	}
	want := "Created a draft for alice@example.com. password=[redacted:kv-secret]"
	if got.content != want || got.parts[0].Text != want {
		t.Fatalf("reply = %q, part = %q, want %q", got.content, got.parts[0].Text, want)
	}
}

func TestNormalizeTranscriptItemMarksAProvenFinalAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		parts []protocol.RunItemPart
		want  bool
	}{
		{name: "final text", parts: []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "Done.", Final: true}}, want: true},
		{name: "commentary", parts: []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "Working on it."}}},
	} {
		got, err := normalizeTranscriptItem(
			TranscriptItemInput{ItemKey: "item-1", Role: protocol.RunItemRoleAgent, Parts: tc.parts},
		)
		if err != nil {
			t.Fatalf("%s: normalizeTranscriptItem() error = %v", tc.name, err)
		}
		if got.finalAnswer != tc.want {
			t.Fatalf("%s: finalAnswer = %v, want %v", tc.name, got.finalAnswer, tc.want)
		}
	}
}

func TestNormalizeTranscriptItemDefaultsMissingToolInputToObject(t *testing.T) {
	t.Parallel()

	got, err := normalizeTranscriptItem(TranscriptItemInput{
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeToolCall, CallID: "call-1", Name: "tool",
		}},
	})
	if err != nil {
		t.Fatalf("normalizeTranscriptItem() error = %v", err)
	}
	if input := string(got.parts[0].Input); input != `{}` {
		t.Fatalf("normalized tool input = %s, want {}", input)
	}
}

func TestNormalizeTranscriptItemRedactsStructuredToolResultKeys(t *testing.T) {
	t.Parallel()

	got, err := normalizeTranscriptItem(TranscriptItemInput{
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleTool,
		Parts: []protocol.RunItemPart{
			{
				Type:   protocol.RunItemPartTypeToolResult,
				CallID: "call-1",
				Name:   "tool",
				Output: ` { "email": "alice@example.com", "safe" : 1, "token" : ["alpha", "beta"], "password" : {"raw":"hunter2"} } `,
			},
		},
	})
	if err != nil {
		t.Fatalf("normalizeTranscriptItem() error = %v", err)
	}
	want := `{"email":"alice@example.com","password":"[redacted:json-secret]","safe":1,"token":"[redacted:json-secret]"}`
	if output := got.parts[0].Output; output != want {
		t.Fatalf("tool result = %s, want %s", output, want)
	}
}

func TestNormalizeTranscriptItemUsesSemanticObjectOrderForReplay(t *testing.T) {
	t.Parallel()

	first, err := normalizeTranscriptItem(TranscriptItemInput{
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeToolCall, CallID: "call-1", Name: "tool",
			Input: json.RawMessage(`{"b":2,"a":1}`),
		}},
	})
	if err != nil {
		t.Fatalf("normalize first item: %v", err)
	}
	second, err := normalizeTranscriptItem(TranscriptItemInput{
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeToolCall, CallID: "call-1", Name: "tool",
			Input: json.RawMessage(` { "a" : 1, "b" : 2 } `),
		}},
	})
	if err != nil {
		t.Fatalf("normalize second item: %v", err)
	}
	if first.contentDigest != second.contentDigest {
		t.Fatalf(
			"semantic JSON replay diverged: first %x, second %x",
			first.contentDigest, second.contentDigest,
		)
	}
}

func TestNormalizeTranscriptItemRejectsInvalidShapes(t *testing.T) {
	t.Parallel()

	valid := func() TranscriptItemInput {
		return TranscriptItemInput{
			ItemKey: "item-1",
			Role:    protocol.RunItemRoleAgent,
			Parts: []protocol.RunItemPart{{
				Type: protocol.RunItemPartTypeText, Text: "done",
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(*TranscriptItemInput)
	}{
		{name: "blank item key", mutate: func(input *TranscriptItemInput) {
			input.ItemKey = " \t\n"
		}},
		{name: "item key with surrounding whitespace", mutate: func(input *TranscriptItemInput) {
			input.ItemKey = " item-1 "
		}},
		{name: "oversized item key", mutate: func(input *TranscriptItemInput) {
			input.ItemKey = strings.Repeat("k", 513)
		}},
		{name: "unknown role", mutate: func(input *TranscriptItemInput) {
			input.Role = protocol.RunItemRole("vendor")
		}},
		{name: "no parts", mutate: func(input *TranscriptItemInput) {
			input.Parts = nil
		}},
		{name: "too many parts", mutate: func(input *TranscriptItemInput) {
			input.Parts = make([]protocol.RunItemPart, 129)
		}},
		{name: "empty text", mutate: func(input *TranscriptItemInput) {
			input.Parts[0].Text = ""
		}},
		{name: "text with tool field", mutate: func(input *TranscriptItemInput) {
			input.Parts[0].CallID = "call-1"
		}},
		{name: "tool call without identity", mutate: func(input *TranscriptItemInput) {
			input.Parts[0] = protocol.RunItemPart{Type: protocol.RunItemPartTypeToolCall}
		}},
		{name: "tool call with invalid JSON", mutate: func(input *TranscriptItemInput) {
			input.Parts[0] = protocol.RunItemPart{
				Type: protocol.RunItemPartTypeToolCall, CallID: "call-1", Name: "tool",
				Input: json.RawMessage(`{"broken":`),
			}
		}},
		{name: "tool call with null input", mutate: func(input *TranscriptItemInput) {
			input.Parts[0] = protocol.RunItemPart{
				Type: protocol.RunItemPartTypeToolCall, CallID: "call-1", Name: "tool",
				Input: json.RawMessage(`null`),
			}
		}},
		{name: "tool call with array input", mutate: func(input *TranscriptItemInput) {
			input.Parts[0] = protocol.RunItemPart{
				Type: protocol.RunItemPartTypeToolCall, CallID: "call-1", Name: "tool",
				Input: json.RawMessage(`[]`),
			}
		}},
		{name: "tool call with scalar input", mutate: func(input *TranscriptItemInput) {
			input.Parts[0] = protocol.RunItemPart{
				Type: protocol.RunItemPartTypeToolCall, CallID: "call-1", Name: "tool",
				Input: json.RawMessage(`"value"`),
			}
		}},
		{name: "tool result without call id", mutate: func(input *TranscriptItemInput) {
			input.Parts[0] = protocol.RunItemPart{
				Type: protocol.RunItemPartTypeToolResult, Output: "done",
			}
		}},
		{name: "tool result with unknown status", mutate: func(input *TranscriptItemInput) {
			input.Parts[0] = protocol.RunItemPart{
				Type: protocol.RunItemPartTypeToolResult, CallID: "call-1",
				Status: protocol.RunToolResultStatus("vendor_status"),
			}
		}},
		{name: "tool result with negative duration", mutate: func(input *TranscriptItemInput) {
			negative := int64(-1)
			input.Parts[0] = protocol.RunItemPart{
				Type: protocol.RunItemPartTypeToolResult, CallID: "call-1",
				DurationMs: &negative,
			}
		}},
		{name: "error without text", mutate: func(input *TranscriptItemInput) {
			input.Parts[0] = protocol.RunItemPart{Type: protocol.RunItemPartTypeError}
		}},
		{name: "unknown part type", mutate: func(input *TranscriptItemInput) {
			input.Parts[0].Type = protocol.RunItemPartType("vendor")
		}},
		{name: "oversized item", mutate: func(input *TranscriptItemInput) {
			input.Parts[0].Text = strings.Repeat("x", protocol.RunTranscriptMaxItemBytes+1)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := valid()
			test.mutate(&input)
			if _, err := normalizeTranscriptItem(input); !errors.Is(err, ErrTranscriptItemInvalid) {
				t.Fatalf("normalizeTranscriptItem() error = %v, want ErrTranscriptItemInvalid", err)
			}
		})
	}
}
