package auth

import (
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
)

// NewUserDisabledSessionRevokedEvent is the narrow cross-domain constructor
// used by account soft deletion.
func NewUserDisabledSessionRevokedEvent(
	revocation SessionRevocation,
	actorUserID uuid.UUID,
) events.Event {
	return newAuthSessionRevokedEvent(revocation, events.UserActor(actorUserID.String()))
}

func newAuthSessionRevokedEvent(
	revocation SessionRevocation,
	actor events.Actor,
) events.Event {
	return events.Event{
		EntityType: Topic,
		Name:       EventSessionRevoked,
		CreatedAt:  time.Now().UTC(),
		Actor:      actor,
		Payload: RevokedSession{
			SessionID: revocation.SessionID,
			Reason:    revocation.Reason,
			UserID:    revocation.UserID,
		},
	}
}
