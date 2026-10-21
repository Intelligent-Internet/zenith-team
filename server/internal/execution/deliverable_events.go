package execution

import "github.com/google/uuid"

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
