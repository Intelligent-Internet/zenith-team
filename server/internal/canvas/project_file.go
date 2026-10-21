package canvas

import (
	"context"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type projectFileInput struct {
	Principal               auth.UserPrincipal
	ProjectID, AttachmentID uuid.UUID
	Title                   string
}

// registerProjectFileInTx registers a human upload without manufacturing Run provenance.
// The caller holds the Company, membership and Project canvas mutation locks.
func registerProjectFileInTx(ctx context.Context, q *generated.Queries, in projectFileInput) (uuid.UUID, error) {
	if err := auth.RequireProjectPermissionHeld(
		ctx,
		q,
		in.Principal.CompanyID,
		in.ProjectID,
		in.Principal.UserID,
		auth.ProjectPermissionFilesManage,
	); err != nil {
		return uuid.Nil, err
	}
	file, err := q.LockCanvasUpload(
		ctx,
		generated.LockCanvasUploadParams{
			CompanyID:        in.Principal.CompanyID,
			ID:               in.AttachmentID,
			UploadedByUserID: in.Principal.UserID,
		},
	)
	if err != nil {
		return uuid.Nil, err
	}
	// A file placed on a Board is read by the Board's audience; filing the same
	// upload here would not narrow that, so the Project wants its own copy.
	onBoard, err := q.AttachmentOnBoard(
		ctx,
		generated.AttachmentOnBoardParams{CompanyID: in.Principal.CompanyID, AttachmentID: file.ID},
	)
	if err != nil {
		return uuid.Nil, err
	}
	if onBoard {
		return uuid.Nil, ErrForbidden
	}
	asset, err := q.InsertGeneratedAsset(
		ctx,
		generated.InsertGeneratedAssetParams{
			CompanyID:       in.Principal.CompanyID,
			Title:           in.Title,
			MediaClass:      "file",
			CreatedByUserID: in.Principal.UserID,
		},
	)
	if err != nil {
		return uuid.Nil, err
	}
	if err = q.RegisterCanvasFileAsset(
		ctx,
		generated.RegisterCanvasFileAssetParams{
			CompanyID: in.Principal.CompanyID,
			ProjectID: in.ProjectID,
			AssetID:   asset.ID,
		},
	); err != nil {
		return uuid.Nil, err
	}
	version, err := q.InsertCanvasFileVersion(
		ctx,
		generated.InsertCanvasFileVersionParams{
			CompanyID:    in.Principal.CompanyID,
			AssetID:      asset.ID,
			AttachmentID: file.ID,
			ActorUserID:  in.Principal.UserID,
		},
	)
	return version.ID, err
}
