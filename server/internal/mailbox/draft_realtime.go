package mailbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type mailboxDraftChange = DraftChangeReason

const (
	mailboxDraftChangeCreated mailboxDraftChange = DraftChangeReasonCreated
	mailboxDraftChangeUpdated mailboxDraftChange = DraftChangeReasonUpdated
	mailboxDraftChangeQueued  mailboxDraftChange = DraftChangeReasonQueued
	mailboxDraftChangeSent    mailboxDraftChange = DraftChangeReasonSent
	mailboxDraftChangeFailed  mailboxDraftChange = DraftChangeReasonFailed
)

func newDraftChangedEvent(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	conversationID uuid.UUID,
	actor events.Actor,
	draft Draft,
	reason mailboxDraftChange,
	conversationKind string,
	conversationCreatedByUserID *uuid.UUID,
	occurredAt time.Time,
) (events.Event, error) {
	status := DraftWireStatus(draft.Status)
	payload, err := NewDraftChangedPayload(
		draft.GroupID, &draft.ID, status, draft.RecipientCount, reason,
		conversationKind, conversationCreatedByUserID,
	)
	if err != nil {
		return events.Event{}, fmt.Errorf("build Agent Mail realtime payload: %w", err)
	}
	return buildDraftChangedEvent(
		ctx, queries,
		companyID, conversationID, actor, payload, occurredAt,
	)
}

func newDraftDeletedEvent(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	conversationID uuid.UUID,
	actor events.Actor,
	groupID uuid.UUID,
	draftID uuid.UUID,
	recipientCount int,
	conversationKind string,
	conversationCreatedByUserID *uuid.UUID,
	occurredAt time.Time,
) (events.Event, error) {
	payload, err := NewDraftChangedPayload(
		groupID, &draftID, DraftWireStatusDeleted,
		recipientCount, DraftChangeReasonDeleted,
		conversationKind, conversationCreatedByUserID,
	)
	if err != nil {
		return events.Event{}, fmt.Errorf("build deleted Agent Mail realtime payload: %w", err)
	}
	return buildDraftChangedEvent(
		ctx, queries,
		companyID, conversationID, actor, payload, occurredAt,
	)
}

func buildDraftChangedEvent(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	conversationID uuid.UUID,
	actor events.Actor,
	payload DraftChangedPayload,
	occurredAt time.Time,
) (events.Event, error) {
	if !util.ValidUUID(companyID) || !util.ValidUUID(conversationID) || occurredAt.IsZero() {
		return events.Event{}, errors.New("Agent Mail event identity is invalid")
	}
	if !actor.Valid() {
		return events.Event{}, errors.New("Agent Mail event actor is invalid")
	}
	recipients, err := mailboxAudience(ctx, queries, companyID)
	if err != nil {
		return events.Event{}, err
	}
	if payload.ConversationKind == "task" {
		audience, err := auth.ConversationFrameAudienceInTx(ctx, queries, companyID, conversationID)
		if err != nil {
			return events.Event{}, err
		}
		if audience == nil {
			return events.Event{}, errors.New("missing Agent Mail Project audience")
		}
		if !audience.CompanyWide {
			recipients = slices.DeleteFunc(
				recipients,
				func(id uuid.UUID) bool { return !slices.Contains(audience.UserIDs, id) },
			)
		}
	} else {
		recipients = slices.DeleteFunc(recipients, func(id uuid.UUID) bool {
			return payload.ConversationCreatedByUserID == nil || id != *payload.ConversationCreatedByUserID
		})
	}
	payload.Audience = &auth.ProjectAudience{UserIDs: recipients}
	return events.Event{
		EntityType: Topic, Name: EventDraftChanged, CompanyID: companyID,
		CreatedAt: occurredAt.UTC(), ConversationID: conversationID, Actor: actor, Payload: payload,
	}, nil
}
