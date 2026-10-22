package execution

import (
	"context"
	"fmt"
	"time"

	artifactdomain "github.com/Intelligent-Internet/zenith-team/server/internal/artifact"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
)

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
		EntityType: artifactdomain.Topic, Name: artifactdomain.EventChanged, CompanyID: companyID,
		CreatedAt: changedAt, Actor: events.SystemActor(),
		Payload: artifactdomain.Changed{RecipientUserIDs: recipients},
	}, nil
}
