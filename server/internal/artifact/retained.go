package artifact

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type RetainedInput struct {
	CompanyID, ActorUserID                                      uuid.UUID
	ArtifactID                                                  *uuid.UUID
	ProjectID, OwnerUserID, ConversationID, TaskID, SourceRunID uuid.NullUUID
	Type, Title, Summary                                        string
	Payload                                                     json.RawMessage
	PrimaryAssetVersionID                                       uuid.NullUUID
}

type RetainedResult struct {
	ArtifactID uuid.UUID
	SnapshotID uuid.UUID
}

// RetainInTx saves immutable content under a stable identity. The caller owns
// the transaction and holds edit authority and the identity lock when updating
// existing work. Delivery creation and its evidence links belong to the caller.
func RetainInTx(ctx context.Context, q *generated.Queries, in RetainedInput) (RetainedResult, error) {
	kind := in.Type
	if kind == "text" {
		kind = "file"
	}
	if kind != "file" && kind != "media" && kind != "app" {
		return RetainedResult{}, ErrInvalid
	}
	var id uuid.UUID
	if in.ArtifactID == nil {
		owner := in.OwnerUserID
		if in.ProjectID.Valid {
			owner = uuid.NullUUID{}
		}
		row, err := q.CreateRetainedArtifact(ctx, generated.CreateRetainedArtifactParams{
			CompanyID: in.CompanyID, ProjectID: in.ProjectID, OwnerUserID: owner, Type: kind,
			Title: in.Title, CreatedByUserID: in.ActorUserID, ConversationID: in.ConversationID,
			TaskID: in.TaskID, SourceRunID: in.SourceRunID,
		})
		if err != nil {
			return RetainedResult{}, err
		}
		id = row.ID
	} else {
		row, err := q.GetArtifactIdentity(
			ctx,
			generated.GetArtifactIdentityParams{CompanyID: in.CompanyID, ID: *in.ArtifactID},
		)
		if err != nil {
			return RetainedResult{}, err
		}
		if row.Type != kind {
			return RetainedResult{}, ErrInvalid
		}
		id = row.ID
	}
	snapshot, err := q.CreateRetainedSnapshot(ctx, generated.CreateRetainedSnapshotParams{
		CompanyID: in.CompanyID, ArtifactID: id, ActorUserID: in.ActorUserID,
		Title: in.Title, ContentType: in.Type, Summary: in.Summary, Payload: in.Payload,
		PrimaryAssetVersionID: in.PrimaryAssetVersionID,
	})
	if err != nil {
		return RetainedResult{}, err
	}
	return RetainedResult{ArtifactID: id, SnapshotID: snapshot.ID}, nil
}
