package execution

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

const TopicDeliverable = "deliverable"

const (
	EventDeliverableCreated    = "deliverable.created"
	EventDeliverableReviewed   = "deliverable.reviewed"
	EventDeliverableSuperseded = "deliverable.superseded"
)

type DeliverableEvent struct {
	RecipientUserIDs []uuid.UUID       `json:"-"`
	ID               uuid.UUID         `json:"id"`
	RunID            uuid.UUID         `json:"runId"`
	Status           DeliverableStatus `json:"status"`
	ItemCount        int64             `json:"itemCount,omitempty"`
}

func newDeliverableEvent(
	name string,
	run db.Run,
	deliverableID uuid.UUID,
	status DeliverableStatus,
	itemCount int64,
	actor events.Actor,
	createdAt time.Time,
) events.Event {
	return events.Event{
		EntityType: TopicDeliverable, Name: name, CompanyID: run.CompanyID,
		CreatedAt: createdAt.UTC(), RunID: run.ID,
		TaskID: util.NullUUIDToValue(run.TaskID), ConversationID: run.ConversationID,
		Actor: actor,
		Payload: DeliverableEvent{
			ID: deliverableID, RunID: run.ID, Status: status, ItemCount: itemCount,
		},
	}
}

func deliverableEventInTx(
	ctx context.Context, queries *db.Queries, outputID uuid.UUID, event events.Event,
) (events.Event, error) {
	recipients, err := queries.ListOutputAccessRecipients(ctx, db.ListOutputAccessRecipientsParams{
		CompanyID: event.CompanyID, OutputID: outputID,
	})
	if err != nil {
		return events.Event{}, fmt.Errorf("resolve Deliverable event recipients: %w", err)
	}
	payload, err := events.PayloadOf[DeliverableEvent](event)
	if err != nil {
		return events.Event{}, err
	}
	payload.RecipientUserIDs = recipients
	event.Payload = payload
	return event, nil
}
