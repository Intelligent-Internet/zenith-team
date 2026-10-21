package conversation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// CreatedWhiteboard names the Whiteboard Artifact made in an Agent or Task
// chat and the Company it belongs to.
type CreatedWhiteboard struct {
	CompanyID  uuid.UUID
	ArtifactID uuid.UUID
}

// CreateWhiteboard makes a Whiteboard in an Agent or Task chat. Unlike a group
// chat, the chat gets no message for it: every turn there asks the Agent (an
// item carries its Run), so the Whiteboard joins the chat's Artifacts and each
// later Run of the chat is told of it. The member owns it and shares it like
// any Artifact. A Task chat asks project.tasks.write, as a message there does.
func (s *DefaultConversationService) CreateWhiteboard(
	ctx context.Context,
	principal auth.UserPrincipal,
	target MessageTarget,
	title string,
) (CreatedWhiteboard, error) {
	if !validMessageTarget(target) {
		return CreatedWhiteboard{}, ErrNotFound
	}
	var created CreatedWhiteboard
	var pending events.Event
	err := s.store.WithinTx(ctx, conversationWriteTxOptions, func(tx *generated.Queries) error {
		resolved, err := resolveMessageTarget(ctx, tx, principal.UserID, target)
		if err != nil {
			return err
		}
		if resolved.DocumentThreadID != nil {
			return ErrNotFound
		}
		if err := lockActiveMembership(ctx, tx, resolved.CompanyID, principal.UserID); err != nil {
			return err
		}
		if err := lockConversation(
			ctx, tx, principal.UserID, resolved.ConversationID, resolved.CompanyID,
		); err != nil {
			return err
		}
		if err := requireTaskChatWrite(ctx, tx, principal, resolved); err != nil {
			return err
		}
		member := principal
		member.CompanyID = resolved.CompanyID
		id, err := canvas.CreateWhiteboardInTx(ctx, tx, member, resolved.ConversationID, title)
		if errors.Is(err, canvas.ErrBoardInvalid) {
			return ErrInvalidInput
		}
		if err != nil {
			return err
		}
		created = CreatedWhiteboard{CompanyID: resolved.CompanyID, ArtifactID: id}
		// Only its owner reads it yet: their other windows refresh the
		// chat's Artifacts.
		pending = events.Event{
			EntityType: execution.TopicArtifact,
			Name:       execution.EventArtifactChanged,
			CompanyID:  resolved.CompanyID,
			CreatedAt:  time.Now().UTC(),
			Actor:      events.UserActor(principal.UserID.String()),
			Payload:    execution.ArtifactChanged{RecipientUserIDs: []uuid.UUID{principal.UserID}},
		}
		return nil
	})
	if err != nil {
		return CreatedWhiteboard{}, fmt.Errorf("create chat Whiteboard: %w", err)
	}
	s.bus.Publish(ctx, execution.TopicArtifact, pending)
	return created, nil
}
