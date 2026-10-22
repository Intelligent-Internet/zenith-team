package artifact

import (
	"context"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// ShareInTx participates in the caller's message transaction. The caller holds
// the destination Conversation lock; the Artifact's owner remains unchanged.
func ShareInTx(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, id, conversation uuid.UUID) error {
	access, err := auth.LockArtifactAccess(ctx, q, p, id)
	if err != nil {
		return err
	}
	if !access.Read {
		return ErrNotFound
	}
	if !access.Manage {
		return ErrForbidden
	}
	allowed, err := q.CanShareOutputToConversation(ctx, generated.CanShareOutputToConversationParams{
		CompanyID: p.CompanyID, ConversationID: conversation, ActorUserID: p.UserID,
	})
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotFound
	}
	if err = q.ShareArtifactWithConversation(ctx, generated.ShareArtifactWithConversationParams{
		CompanyID: p.CompanyID, ArtifactID: id, ConversationID: conversation, GrantedByUserID: p.UserID,
	}); err != nil {
		return err
	}
	_, err = q.AdvanceArtifactAccess(ctx, generated.AdvanceArtifactAccessParams{CompanyID: p.CompanyID, ID: id})
	return err
}
