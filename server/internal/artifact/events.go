package artifact

import "github.com/google/uuid"

const (
	Topic              = "artifact"
	EventAccessChanged = "artifact.access_changed"
	EventChanged       = "artifact.changed"
)

type AccessChanged struct {
	RecipientUserIDs []uuid.UUID `json:"-"`
}

// Changed invalidates retained presentation or live-work metadata. Recipients
// recheck current access before reading; this event carries no content.
type Changed struct {
	RecipientUserIDs []uuid.UUID `json:"-"`
}
