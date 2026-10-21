package mailbox

import (
	"errors"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

// DraftWireStatus is the small non-PII state projected by Agent Mail realtime
// hints. Deleted is a wire tombstone, not a persisted draft state.
type DraftWireStatus string

const (
	DraftWireStatusDraft   DraftWireStatus = "draft"
	DraftWireStatusSending DraftWireStatus = "sending"
	DraftWireStatusSent    DraftWireStatus = "sent"
	DraftWireStatusFailed  DraftWireStatus = "failed"
	DraftWireStatusDeleted DraftWireStatus = "deleted"
)

type DraftChangeReason string

const (
	DraftChangeReasonCreated DraftChangeReason = "created"
	DraftChangeReasonUpdated DraftChangeReason = "updated"
	DraftChangeReasonDeleted DraftChangeReason = "deleted"
	DraftChangeReasonQueued  DraftChangeReason = "queued"
	DraftChangeReasonSent    DraftChangeReason = "sent"
	DraftChangeReasonFailed  DraftChangeReason = "failed"
)

// DraftChangedPayload deliberately excludes every address and content field.
// Clients refetch the authorized Conversation projection by ID.
type DraftChangedPayload struct {
	GroupID                     uuid.UUID         `json:"groupId"`
	DraftID                     *uuid.UUID        `json:"draftId"`
	Status                      DraftWireStatus   `json:"status"`
	RecipientCount              int               `json:"recipientCount"`
	Reason                      DraftChangeReason `json:"reason"`
	ConversationKind            string            `json:"-"`
	ConversationCreatedByUserID *uuid.UUID        `json:"-"`
	// ProjectFrame carries only Mailbox permission holders who can read
	// the Conversation, including its Project boundary (ADR 0017).
	auth.ProjectFrame
}

func (p DraftChangedPayload) ProjectFrameScope() (uuid.UUID, bool) {
	return uuid.Nil, p.ConversationKind == "task"
}

func (p DraftChangedPayload) WithProjectAudience(audience *auth.ProjectAudience) any {
	p.Audience = audience
	return p
}

// The router finds this payload by interface; the compiler checks it.
var _ auth.ProjectFramePayload = DraftChangedPayload{}

func NewDraftChangedPayload(
	groupID uuid.UUID,
	draftID *uuid.UUID,
	status DraftWireStatus,
	recipientCount int,
	reason DraftChangeReason,
	conversationKind string,
	conversationCreatedByUserID *uuid.UUID,
) (DraftChangedPayload, error) {
	payload := DraftChangedPayload{
		GroupID: groupID, DraftID: draftID, Status: status,
		RecipientCount: recipientCount, Reason: reason,
		ConversationKind:            conversationKind,
		ConversationCreatedByUserID: conversationCreatedByUserID,
	}
	if err := validateDraftChangedPayload(payload); err != nil {
		return DraftChangedPayload{}, err
	}
	return payload, nil
}

func validateDraftChangedPayload(payload DraftChangedPayload) error {
	if payload.GroupID == uuid.Nil || payload.DraftID == nil || *payload.DraftID == uuid.Nil {
		return errors.New("Mailbox draft change requires group and draft IDs")
	}
	if payload.RecipientCount < 1 || payload.RecipientCount > 100 {
		return errors.New("Mailbox draft recipient count is invalid")
	}
	switch payload.ConversationKind {
	case "task":
	case "direct", "schedule":
		if payload.ConversationCreatedByUserID == nil || *payload.ConversationCreatedByUserID == uuid.Nil {
			return errors.New("Mailbox draft Conversation creator is invalid")
		}
	default:
		return errors.New("Mailbox draft Conversation kind is invalid")
	}
	switch payload.Status {
	case DraftWireStatusDraft, DraftWireStatusSending, DraftWireStatusSent,
		DraftWireStatusFailed, DraftWireStatusDeleted:
	default:
		return errors.New("Mailbox draft status is invalid")
	}
	switch payload.Reason {
	case DraftChangeReasonCreated, DraftChangeReasonUpdated, DraftChangeReasonDeleted,
		DraftChangeReasonQueued, DraftChangeReasonSent, DraftChangeReasonFailed:
		return nil
	default:
		return errors.New("Mailbox draft change reason is invalid")
	}
}
