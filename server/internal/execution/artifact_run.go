package execution

import (
	"context"
	"errors"
	"log/slog"

	"boardly.dev/boardly/server/internal/auth"

	"github.com/google/uuid"
)

// ReadArtifactForRun resolves the exact immutable Output, rechecking its
// revocable grants for the Run's Authorizer. Every Run, in a direct chat, a
// Task chat or tagged in a channel or group DM, reads an Artifact exactly
// when its Authorizer reads it through the person's own path, never more,
// and no chat audience narrows it (D31, Access TECH B10). A scheduled Run
// reads none.
func (s *DefaultDeliverableService) ReadArtifactForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	origin RunOrigin,
	id uuid.UUID,
) (DeliverableOutput, error) {
	if origin.CompanyID != p.CompanyID || origin.AuthorizedByUserID == nil ||
		*origin.AuthorizedByUserID != p.UserID ||
		origin.ConversationKind == "schedule" {
		logRunArtifactRefusal(ctx, origin, id, "run_proof_invalid")
		return DeliverableOutput{}, ErrRunProofInvalid
	}
	artifact, err := s.GetArtifact(ctx, p, id)
	switch {
	case errors.Is(err, ErrDeliverableNotFound):
		logRunArtifactRefusal(ctx, origin, id, "authorizer_cannot_read")
		return DeliverableOutput{}, err
	case errors.Is(err, ErrDeliverableForbidden):
		logRunArtifactRefusal(ctx, origin, id, "authorizer_cannot_read_sources")
		return DeliverableOutput{}, err
	case err != nil:
		return DeliverableOutput{}, err
	}
	return *artifact.Result.Output, nil
}

// logRunArtifactRefusal records why a Run was refused an Artifact, naming
// the Run and the Artifact only, never its content.
func logRunArtifactRefusal(ctx context.Context, origin RunOrigin, id uuid.UUID, reason string) {
	slog.InfoContext(ctx, "run artifact read refused",
		"run_id", origin.RunID.String(),
		"artifact_id", id.String(),
		"conversation_kind", origin.ConversationKind,
		"reason", reason,
	)
}
