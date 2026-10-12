package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// References are frozen into new messages after replay checks. No sharing grant
// is created in a creator-private Agent chat.
func artifactMessageContent(
	ctx context.Context,
	q *generated.Queries,
	actorUserID, companyID uuid.UUID,
	input SendInput,
) (string, error) {
	for _, id := range input.ArtifactIDs {
		_, err := q.GetArtifactForMessage(ctx, generated.GetArtifactForMessageParams{
			CompanyID: companyID, ActorUserID: actorUserID, OutputID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", fmt.Errorf("read message Artifact: %w", err)
		}
	}
	content := formatArtifactMessage(input)
	if !validMessageContent(content) {
		return "", ErrInvalidInput
	}
	return content, nil
}

// Replay compares the saved IDs without depending on mutable Artifact metadata or access.
func messageContentMatches(input SendInput, stored string) bool {
	return formatArtifactMessage(input) == stored
}

func formatArtifactMessage(input SendInput) string {
	if len(input.ArtifactIDs) == 0 {
		return input.Content
	}
	var content strings.Builder
	content.WriteString(input.Content)
	content.WriteString("\n\nReferenced artifacts:")
	for _, id := range input.ArtifactIDs {
		fmt.Fprintf(&content, "\n- artifactId: %s", id)
	}
	return content.String()
}
