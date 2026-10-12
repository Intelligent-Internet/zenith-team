package protocol

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestRunTranscriptWirePayloads(t *testing.T) {
	t.Parallel()

	parts := []RunItemPart{
		{Type: RunItemPartTypeText, Text: "Done."},
		{
			Type:   RunItemPartTypeToolCall,
			CallID: "call-1",
			Name:   "read_file",
			Input:  json.RawMessage(`{"path":"README.md"}`),
		},
	}
	transcriptParts := []RunTranscriptPart{
		RunTextPart{Type: RunTextPartTypeText, Text: "Done."},
		RunToolCallPart{
			Type: RunToolCallPartTypeToolCall, CallID: "call-1", Name: "read_file",
			Input: json.RawMessage(`{"path":"README.md"}`),
		},
	}
	tests := []struct {
		name string
		got  any
		want string
	}{
		{
			name: "daemon append request",
			got: RunTranscriptAppendRequest{
				RunID:   uuid.MustParse("00000000-0000-4000-8000-000000004711"),
				ItemKey: "item-1",
				Role:    RunItemRoleAgent,
				Parts:   parts,
			},
			want: `{"runId":"00000000-0000-4000-8000-000000004711","itemKey":"item-1","role":"agent","parts":[{"type":"text","text":"Done."},{"type":"toolCall","callId":"call-1","name":"read_file","input":{"path":"README.md"}}]}`,
		},
		{
			name: "post-commit receipt",
			got: RunTranscriptAppendReceipt{
				ID:          "row-1",
				RunID:       uuid.MustParse("00000000-0000-4000-8000-000000004711"),
				ItemKey:     "item-1",
				Seq:         3,
				ThroughSeq:  3,
				ItemDigest:  "item-digest",
				CommittedAt: "2026-08-21T00:00:00Z",
			},
			want: `{"id":"row-1","runId":"00000000-0000-4000-8000-000000004711","itemKey":"item-1","seq":3,"throughSeq":3,"itemDigest":"item-digest","committedAt":"2026-08-21T00:00:00Z"}`,
		},
		{
			name: "browser durable item event",
			got: RunMessagePayload{
				ID:             "row-1",
				RunID:          uuid.MustParse("00000000-0000-4000-8000-000000004711"),
				ConversationID: "conversation-1",
				ItemKey:        "item-1",
				Seq:            3,
				Role:           RunItemRoleAgent,
				Parts:          transcriptParts,
				ItemDigest:     "item-digest",
				ThroughSeq:     3,
				CreatedAt:      "2026-08-21T00:00:00Z",
			},
			want: `{"id":"row-1","runId":"00000000-0000-4000-8000-000000004711","conversationId":"conversation-1","itemKey":"item-1","seq":3,"role":"agent","parts":[{"type":"text","text":"Done."},{"type":"toolCall","callId":"call-1","name":"read_file","input":{"path":"README.md"}}],"itemDigest":"item-digest","throughSeq":3,"createdAt":"2026-08-21T00:00:00Z"}`,
		},
		{
			// The reconciliation body bug R1 needs: the server's own head,
			// not an echo of the proof the daemon just sent.
			name: "terminal head mismatch",
			got: RunTerminalHeadMismatch{
				RunID:      uuid.MustParse("00000000-0000-4000-8000-000000004711"),
				ThroughSeq: 9,
			},
			want: `{"runId":"00000000-0000-4000-8000-000000004711","throughSeq":9}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tt.got)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("payload = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRunMessagePayloadContainsOnlyStructuredTranscriptFields(t *testing.T) {
	t.Parallel()

	typeOfPayload := reflect.TypeOf(RunMessagePayload{})
	got := make([]string, 0, typeOfPayload.NumField())
	for index := range typeOfPayload.NumField() {
		got = append(got, typeOfPayload.Field(index).Name)
	}
	want := []string{
		"ID", "RunID", "ConversationID", "ItemKey",
		"Seq", "Role", "Parts", "ItemDigest", "ThroughSeq",
		"CreatedAt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunMessagePayload fields = %v, want %v", got, want)
	}
}

// Run IDs travel as UUID strings; transcript sequences retain the JSON-safe
// integer ceiling enforced by the allocator in conversation_items.sql.
func TestRunTranscriptNumbersSurviveTheJSONSafeCeiling(t *testing.T) {
	t.Parallel()

	receipt := RunTranscriptAppendReceipt{
		RunID:      uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		Seq:        MaxJSONSafeInteger,
		ThroughSeq: MaxJSONSafeInteger,
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	var decoded RunTranscriptAppendReceipt
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal receipt: %v", err)
	}
	if decoded != receipt {
		t.Fatalf("receipt round trip = %+v, want %+v", decoded, receipt)
	}
	// The exact decimal digits, so a float64 detour anywhere in the encoder
	// would show up as 9007199254740992 or an exponent form.
	const ceiling = "9007199254740991"
	if want := `{"id":"","runId":"00000000-0000-4000-8000-000000004711","itemKey":"","seq":` + ceiling +
		`,"throughSeq":` + ceiling +
		`,"itemDigest":"","committedAt":""}`; string(encoded) != want {
		t.Fatalf("receipt = %s, want %s", encoded, want)
	}
}
