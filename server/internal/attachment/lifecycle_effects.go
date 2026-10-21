package attachment

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// LifecycleEffect contains only public lifecycle state and actor provenance.
// Storage drivers, object keys, provider errors, and credentials never cross
// this boundary.
type LifecycleEffect struct {
	AttachmentID        uuid.UUID
	CompanyID           uuid.UUID
	UploadedByUserID    uuid.UUID
	CompletedAttachment *Response
	Revision            int64
	ActorKind           string
	ActorID             *uuid.UUID
	// RunActor, when set, names the Run that caused the transition: its
	// Agent, acting for the member who uploaded, in that Run. It
	// replaces ActorKind and ActorID.
	RunActor events.Actor
}

// writeCompletedEffect is the only Attachment wire-event producer, together
// with writeDeletedEffect below. It stages one closed wire event in the same
// transaction as the lifecycle transition. Object-store details never cross
// this boundary. The completed projection already names the company and the
// uploader, which is everything the browser plane routes it by.
func writeCompletedEffect(
	pending *[]events.Event,
	effect LifecycleEffect,
) {
	*pending = append(*pending, events.Event{
		EntityType: Topic,
		Name:       EventCompleted,
		CompanyID:  effect.CompanyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      attachmentEffectActor(effect),
		Payload:    *effect.CompletedAttachment,
	})
}

// writeDeletedEffect reads the current linked/unlinked route from normalized
// foreign keys in the same transaction as the lifecycle transition, and stages
// it onto the payload for the consumer that delivers.
func writeDeletedEffect(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	effect LifecycleEffect,
) error {
	route, err := q.GetAttachmentRealtimeRoute(ctx, generated.GetAttachmentRealtimeRouteParams{
		AttachmentID: effect.AttachmentID, CompanyID: effect.CompanyID,
	})
	if err != nil {
		return fmt.Errorf("load attachment deletion route: %w", err)
	}
	payload := attachmentDeletedPayload(effect, route)
	conversationID := uuid.Nil
	if payload.ConversationID != nil {
		conversationID = *payload.ConversationID
	}
	staged := []events.Event{{
		EntityType:     Topic,
		Name:           EventDeleted,
		CompanyID:      effect.CompanyID,
		CreatedAt:      time.Now().UTC(),
		ConversationID: conversationID,
		Actor:          attachmentEffectActor(effect),
		Payload:        payload,
	}}
	if err := auth.StampProjectAudiencesInTx(ctx, q, staged); err != nil {
		return err
	}
	*pending = append(*pending, staged...)
	return nil
}

// attachmentEffectActor names who caused the transition. A lifecycle step
// nobody asked for — the cleanup worker's finalize — is the system's.
func attachmentEffectActor(effect LifecycleEffect) events.Actor {
	if effect.RunActor.Type != "" {
		return effect.RunActor
	}
	switch effect.ActorKind {
	case "user":
		return events.UserActor(effect.ActorID.String())
	case "agent":
		return events.AgentActor(effect.ActorID.String())
	}
	return events.SystemActor()
}

// attachmentDeletedPayload carries the deletion's public fields plus the
// routing facts the browser plane branches on, so delivery never depends on
// decoding the wire payload or on a query issued after the fact. Link
// visibility decides the route: an attachment linked to no message stays
// private to its uploader, a Task chat's reaches that chat's Project readers
// (ADR 0017), and any other conversation is its creator's alone.
func attachmentDeletedPayload(
	effect LifecycleEffect,
	route generated.GetAttachmentRealtimeRouteRow,
) Removal {
	payload := Removal{
		AttachmentID:     effect.AttachmentID,
		Revision:         effect.Revision,
		CompanyID:        effect.CompanyID,
		UploadedByUserID: effect.UploadedByUserID,
	}
	if !route.MessageID.Valid {
		return payload
	}
	payload.MessageID = attachmentUUID(route.MessageID.UUID)
	payload.ConversationID = attachmentUUID(route.ConversationID.UUID)
	payload.ConversationKind = route.ConversationKind.String
	payload.ConversationCreatorUserID = route.ConversationCreatorUserID.UUID
	return payload
}

func attachmentUUID(value uuid.UUID) *uuid.UUID {
	cloned := value
	return &cloned
}
