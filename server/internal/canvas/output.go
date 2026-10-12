package canvas

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type OutputRegistrationInput struct {
	CompanyID, OutputID, ActorUserID uuid.UUID
}

// RegisterOutputInTx adds a saved Task Output to its immutable Task Project's
// library and places its initial card without granting access. The caller holds Company,
// membership and Run locks, owns the transaction and publishes the returned event
// only after commit. Direct and Schedule Outputs have no Project destination.
func RegisterOutputInTx(
	ctx context.Context, q *generated.Queries, in OutputRegistrationInput,
) (*events.Event, error) {
	source, err := q.GetCanvasOutputSource(
		ctx,
		generated.GetCanvasOutputSourceParams{CompanyID: in.CompanyID, ID: in.OutputID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	board, err := q.EnsureProjectCanvas(
		ctx,
		generated.EnsureProjectCanvasParams{CompanyID: in.CompanyID, ProjectID: source.ProjectID},
	)
	if err != nil {
		return nil, err
	}
	presentation, err := q.GetProjectPresentationForOutput(
		ctx,
		generated.GetProjectPresentationForOutputParams{
			CompanyID: in.CompanyID,
			OutputID:  nullable(&in.OutputID),
		},
	)
	if err == nil {
		return registerNativeOutput(
			ctx,
			q,
			nativeOutputRegistration{
				input:   in,
				project: source.ProjectID,
				board:   board.ID,
				page:    presentation.PageID,
			},
		)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// Include archived registrations: replaying an Output must not undo a user's archive.
	_, err = q.GetCanvasRegisteredOutput(ctx, generated.GetCanvasRegisteredOutputParams{
		CompanyID: in.CompanyID, ProjectID: source.ProjectID, OutputID: nullable(&in.OutputID),
	})
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var artifact generated.ProjectArtifact
	fresh := true
	if fresh {
		count, err := q.CountCanvasArtifacts(ctx, source.ProjectID)
		if err != nil {
			return nil, err
		}
		if count >= 2000 {
			return nil, ErrLimit
		}
		artifact, err = q.CreateCanvasArtifact(
			ctx,
			generated.CreateCanvasArtifactParams{
				CompanyID:       in.CompanyID,
				ProjectID:       source.ProjectID,
				Title:           source.Title,
				CreatedByUserID: in.ActorUserID,
			},
		)
		if err != nil {
			return nil, err
		}
	}

	number, err := q.AllocateCanvasVersion(ctx, artifact.ID)
	if err != nil {
		return nil, err
	}
	version, err := q.CreateCanvasVersion(ctx, generated.CreateCanvasVersionParams{
		CompanyID: in.CompanyID, ProjectID: source.ProjectID, ArtifactID: artifact.ID, Number: number,
		SourceType: "output", OutputID: nullable(&in.OutputID), CreatedByUserID: in.ActorUserID,
	})
	if err != nil {
		return nil, err
	}
	var items []uuid.UUID
	if fresh {
		items, err = q.PlaceCanvasOutputArtifact(
			ctx,
			generated.PlaceCanvasOutputArtifactParams{
				CompanyID:   in.CompanyID,
				ProjectID:   source.ProjectID,
				CanvasID:    board.ID,
				ArtifactID:  artifact.ID,
				VersionID:   version.ID,
				ActorUserID: in.ActorUserID,
			},
		)
		if err != nil {
			return nil, err
		}
	}

	// A board full of manually duplicated cards must not block saving an Output.
	// The artifact still appears in Plan, Files, and Review at the placement limit.
	if items == nil {
		items = []uuid.UUID{}
	}
	sequence, err := q.AdvanceCanvasHead(
		ctx,
		generated.AdvanceCanvasHeadParams{CompanyID: in.CompanyID, ProjectID: source.ProjectID},
	)
	if err != nil {
		return nil, err
	}
	changes, err := json.Marshal(Changes{
		Items: items, Artifacts: []uuid.UUID{artifact.ID}, Edges: []uuid.UUID{},
		Removed: []uuid.UUID{}, ReviewVersionIDs: []uuid.UUID{},
	})
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(Result{ID: version.ID, Sequence: sequence})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte("registerOutput:" + in.OutputID.String()))
	if err = q.InsertCanvasReceipt(ctx, generated.InsertCanvasReceiptParams{
		CompanyID: in.CompanyID, ProjectID: source.ProjectID, Seq: sequence, ActorUserID: in.ActorUserID,
		RequestKey: in.OutputID, RequestHash: hash[:], Changes: changes, Result: result,
	}); err != nil {
		return nil, err
	}
	return changeEvent(
		ctx,
		q,
		auth.UserPrincipal{CompanyID: in.CompanyID, UserID: in.ActorUserID},
		source.ProjectID,
		sequence,
		uuid.Nil,
	)
}

type nativeOutputRegistration struct {
	input                OutputRegistrationInput
	project, board, page uuid.UUID
}

func registerNativeOutput(
	ctx context.Context,
	q *generated.Queries,
	in nativeOutputRegistration,
) (*events.Event, error) {
	version, err := q.GetProjectPageVersion(
		ctx,
		generated.GetProjectPageVersionParams{
			CompanyID: in.input.CompanyID,
			ProjectID: in.project,
			ID:        in.page,
		},
	)
	if err != nil {
		return nil, lookup(err)
	}
	artifact, err := q.LockCanvasArtifact(
		ctx,
		generated.LockCanvasArtifactParams{
			CompanyID: in.input.CompanyID,
			ProjectID: in.project,
			ID:        version.ArtifactID,
		},
	)
	if err != nil {
		return nil, err
	}
	if artifact.PendingCreation {
		return nil, nil
	}
	placed, err := q.CanvasArtifactEverPlaced(
		ctx,
		generated.CanvasArtifactEverPlacedParams{
			CompanyID:  in.input.CompanyID,
			ProjectID:  in.project,
			ArtifactID: nullable(&version.ArtifactID),
		},
	)
	if err != nil || placed {
		return nil, err
	}
	_, err = q.PlaceCanvasOutputArtifact(
		ctx,
		generated.PlaceCanvasOutputArtifactParams{
			CompanyID:   in.input.CompanyID,
			ProjectID:   in.project,
			CanvasID:    in.board,
			ArtifactID:  version.ArtifactID,
			VersionID:   version.ID,
			ActorUserID: in.input.ActorUserID,
		},
	)
	if err != nil {
		return nil, err
	}
	sequence, err := q.AdvanceCanvasHead(
		ctx,
		generated.AdvanceCanvasHeadParams{CompanyID: in.input.CompanyID, ProjectID: in.project},
	)
	if err != nil {
		return nil, err
	}
	return changeEvent(
		ctx,
		q,
		auth.UserPrincipal{CompanyID: in.input.CompanyID, UserID: in.input.ActorUserID},
		in.project,
		sequence,
		uuid.Nil,
	)
}
