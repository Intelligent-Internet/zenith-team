package mailbox

import (
	"errors"

	"github.com/google/uuid"
)

// Topic is the Mailbox domain's event stream.
const Topic = "mailbox"

const (
	EventChanged      = "mailbox.changed"
	EventDraftChanged = "mailbox.draft_changed"
)

// ChangeReason identifies which authoritative Mailbox projection needs to be
// refetched without carrying email metadata on the realtime plane.
type ChangeReason string

const (
	ChangeReasonConfigured      ChangeReason = "configured"
	ChangeReasonMessageReceived ChangeReason = "message_received"
	ChangeReasonMessageRead     ChangeReason = "message_read"
	ChangeReasonMessageRemoved  ChangeReason = "message_removed"
)

// ChangedPayload is the strict payload for a permission-scoped Mailbox refetch
// hint.
type ChangedPayload struct {
	RecipientUserIDs []uuid.UUID  `json:"-"`
	Reason           ChangeReason `json:"reason"`
	MessageID        *uuid.UUID   `json:"messageId,omitempty"`
}

// NewChangedPayload constructs and validates a Mailbox refetch hint.
func NewChangedPayload(
	reason ChangeReason,
	messageID *uuid.UUID,
) (ChangedPayload, error) {
	payload := ChangedPayload{Reason: reason, MessageID: messageID}
	if err := validateChangedPayload(payload); err != nil {
		return ChangedPayload{}, err
	}
	return payload, nil
}

func validateChangedPayload(payload ChangedPayload) error {
	switch payload.Reason {
	case ChangeReasonConfigured:
		if payload.MessageID != nil {
			return errors.New("configured Mailbox change must not carry a message ID")
		}
		return nil
	case ChangeReasonMessageReceived,
		ChangeReasonMessageRead,
		ChangeReasonMessageRemoved:
		if payload.MessageID == nil || *payload.MessageID == uuid.Nil {
			return errors.New("Mailbox message change requires a message ID")
		}
		return nil
	default:
		return errors.New("Mailbox change reason is invalid")
	}
}
