package mailbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMailboxDraftChangedPayloadIsConversationScopedAndPIIFree(t *testing.T) {
	t.Parallel()

	groupID, draftID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	creatorID := uuid.Must(uuid.NewV7())
	payload, err := NewDraftChangedPayload(
		groupID, &draftID, DraftWireStatusSending, 20, DraftChangeReasonQueued,
		"direct", &creatorID,
	)
	if err != nil {
		t.Fatalf("NewDraftChangedPayload() error = %v", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, pii := range []string{"@", "subject", "body", "from", "provider"} {
		if strings.Contains(strings.ToLower(string(encoded)), pii) {
			t.Fatalf("draft realtime payload contains forbidden token %q: %s", pii, encoded)
		}
	}
	if payload.ConversationKind != "direct" || payload.ConversationCreatedByUserID == nil ||
		*payload.ConversationCreatedByUserID != creatorID {
		t.Fatalf("draft realtime routing facts = %+v", payload)
	}
	if strings.Contains(string(encoded), "conversationKind") ||
		strings.Contains(string(encoded), creatorID.String()) {
		t.Fatalf("draft realtime payload exposed internal routing facts: %s", encoded)
	}
}

func TestMailboxDraftChangedPayloadRejectsInvalidStatusCountAndReason(t *testing.T) {
	t.Parallel()

	groupID := uuid.Must(uuid.NewV7())
	for _, payload := range []DraftChangedPayload{
		{GroupID: groupID, ConversationKind: "task", Status: "unknown", RecipientCount: 1, Reason: DraftChangeReasonQueued},
		{GroupID: groupID, ConversationKind: "task", Status: DraftWireStatusDraft, RecipientCount: 101, Reason: DraftChangeReasonCreated},
		{GroupID: groupID, ConversationKind: "task", Status: DraftWireStatusDraft, RecipientCount: 1, Reason: "provider_secret"},
	} {
		if err := validateDraftChangedPayload(payload); err == nil {
			t.Fatalf("validateDraftChangedPayload(%+v) error = nil", payload)
		}
	}
}
