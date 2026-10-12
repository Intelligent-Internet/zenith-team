package mailbox

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestMailboxChangedPayloadContract(t *testing.T) {
	t.Parallel()

	messageID := uuid.New()
	tests := []struct {
		name      string
		reason    ChangeReason
		messageID *uuid.UUID
		wantErr   bool
	}{
		{name: "configured", reason: ChangeReasonConfigured},
		{name: "message received", reason: ChangeReasonMessageReceived, messageID: &messageID},
		{name: "message read", reason: ChangeReasonMessageRead, messageID: &messageID},
		{name: "message removed", reason: ChangeReasonMessageRemoved, messageID: &messageID},
		{name: "unknown reason", reason: "unknown", wantErr: true},
		{
			name:      "configured with message",
			reason:    ChangeReasonConfigured,
			messageID: &messageID,
			wantErr:   true,
		},
		{name: "message change without message", reason: ChangeReasonMessageRead, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload, err := NewChangedPayload(test.reason, test.messageID)
			if test.wantErr {
				if err == nil {
					t.Fatal("NewChangedPayload() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewChangedPayload() error = %v", err)
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if len(encoded) == 0 {
				t.Fatal("json.Marshal() returned an empty payload")
			}
		})
	}
}
