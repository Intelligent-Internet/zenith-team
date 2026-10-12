package execution

import (
	"context"
	"fmt"
	"time"

	"boardly.dev/boardly/server/internal/events"
	db "boardly.dev/boardly/server/pkg/db/generated"

	"github.com/google/uuid"
)

const (
	TopicArtifact              = "artifact"
	EventArtifactAccessChanged = "artifact.access_changed"
	EventArtifactChanged       = "artifact.changed"
)

type ArtifactAccessChanged struct {
	RecipientUserIDs []uuid.UUID `json:"-"`
}

// ArtifactChanged is an opaque invalidation. Readers check current access
// before loading publication state, including after a concurrent revocation.
type ArtifactChanged struct {
	RecipientUserIDs []uuid.UUID `json:"-"`
}

func publicationChangedInTx(ctx context.Context, q *db.Queries, row db.OutputPublication) (events.Event, error) {
	return artifactChangedInTx(ctx, q, row.CompanyID, row.OutputID, row.UpdatedAt.Time)
}

func artifactChangedInTx(
	ctx context.Context,
	q *db.Queries,
	companyID, outputID uuid.UUID,
	changedAt time.Time,
) (events.Event, error) {
	recipients, err := q.ListOutputAccessRecipients(ctx, db.ListOutputAccessRecipientsParams{
		CompanyID: companyID, OutputID: outputID,
	})
	if err != nil {
		return events.Event{}, fmt.Errorf("resolve Artifact event recipients: %w", err)
	}
	return events.Event{
		EntityType: TopicArtifact, Name: EventArtifactChanged, CompanyID: companyID,
		CreatedAt: changedAt, Actor: events.SystemActor(),
		Payload: ArtifactChanged{RecipientUserIDs: recipients},
	}, nil
}
