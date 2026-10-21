package canvas

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// A Whiteboard Artifact's unrevoked public link grants reads only. Recheck it
// in the read's snapshot for each scene, hydration and file request.
func publicBoardRead(ctx context.Context, q *generated.Queries, token uuid.UUID) (boardRecord, error) {
	row, err := q.ReadPublicBoard(ctx, token)
	return boardRecord{Board: row.Board, Title: row.Title}, boardError(err)
}

func (s *DefaultBoardService) PublicSnapshot(ctx context.Context, token uuid.UUID) (BoardSnapshot, error) {
	var result BoardSnapshot
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		b, err := publicBoardRead(ctx, q, token)
		if err != nil {
			return err
		}
		result, err = boardSnapshotInTx(ctx, q, auth.UserPrincipal{CompanyID: b.CompanyID}, b, 0)
		return err
	})
	return result, boardError(err)
}

func (s *DefaultBoardService) PublicHydrate(
	ctx context.Context,
	token uuid.UUID,
	in BoardHydrateInput,
) (BoardHydration, error) {
	var result BoardHydration
	// A reader never edits, so it gets plain text and ink without CRDT state.
	in.StateVectors = nil
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		b, err := publicBoardRead(ctx, q, token)
		if err != nil {
			return err
		}
		result, err = boardHydrateInTx(ctx, q, b, in, false)
		return err
	})
	return result, boardError(err)
}

func (s *DefaultBoardMediaService) PublicFileDownload(
	ctx context.Context,
	token, attachmentID uuid.UUID,
	preview bool,
) (attachment.DownloadResult, error) {
	var companyID, file uuid.UUID
	// Resolve the link and the placement in one short read, then open the file
	// after the transaction: a download never holds a connection while it
	// waits for another or for object storage.
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		b, err := publicBoardRead(ctx, q, token)
		if err != nil {
			return err
		}
		row, err := q.GetPublicBoardFile(
			ctx,
			generated.GetPublicBoardFileParams{
				CompanyID:    b.CompanyID,
				BoardID:      b.ID,
				AttachmentID: attachmentID,
			},
		)
		companyID, file = b.CompanyID, boardFileVariant(row.AttachmentID, row.PreviewAttachmentID, preview)
		return err
	})
	if err != nil {
		return attachment.DownloadResult{}, boardError(err)
	}
	result, err := s.attachments.DownloadResolved(
		ctx,
		attachment.ResolvedDownloadInput{ID: file, CompanyID: companyID},
	)
	if errors.Is(err, attachment.ErrNotFound) {
		err = ErrBoardNotFound
	}
	return result, err
}
