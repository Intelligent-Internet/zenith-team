package company

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// maxCompanyReferenceDocuments bounds one registration call. A founder handing
// over background reading is describing their company, not filing an archive;
// the limit is about the attention of the Agent that must read them.
const maxCompanyReferenceDocuments = 10

// ReferenceDocument is one founder-supplied file the Company keeps as source
// material. It names an Attachment rather than copying it: object storage
// already owns the bytes.
type ReferenceDocument struct {
	AttachmentID uuid.UUID `json:"attachmentId"`
}

// ReferenceService owns the founder-supplied documents a Company keeps for its
// Agents to read. There is no read surface: an Agent receives them in its
// frozen Run brief and the daemon materializes them into the Company
// workspace, so nothing on a screen reads this back.
type ReferenceService interface {
	AddReferenceDocuments(
		context.Context,
		auth.UserPrincipal,
		[]uuid.UUID,
	) ([]ReferenceDocument, error)
}

var _ ReferenceService = (*DefaultReferenceService)(nil)

type DefaultReferenceService struct {
	store *db.Store
}

func NewReferenceService(store *db.Store) (*DefaultReferenceService, error) {
	if store == nil {
		return nil, errors.New("Company reference document database store is required")
	}
	return &DefaultReferenceService{store: store}, nil
}

// AddReferenceDocuments registers already-uploaded Attachments as the
// Company's reference material, in one transaction. The set is written whole
// because a founder hands over a folder rather than a file at a time, and a
// partial write would leave an Agent reading an incomplete brief while
// believing it had everything.
//
// Registration is what saves these uploads from the abandoned-upload sweeper,
// so it must happen for every file the founder expects an Agent to read.
func (s *DefaultReferenceService) AddReferenceDocuments(
	ctx context.Context,
	principal auth.UserPrincipal,
	attachmentIDs []uuid.UUID,
) ([]ReferenceDocument, error) {
	attachmentIDs, err := normalizeReferenceDocumentInput(attachmentIDs)
	if err != nil {
		return nil, err
	}
	var result []ReferenceDocument
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		result = result[:0]
		if err := authorizeMilestoneMutation(
			ctx,
			tx,
			principal,
			auth.PermissionCompanyResourcesManage,
		); err != nil {
			return err
		}
		for _, attachmentID := range attachmentIDs {
			row, err := tx.RegisterCompanyReferenceDocument(
				ctx,
				generated.RegisterCompanyReferenceDocumentParams{
					CompanyID:    principal.CompanyID,
					AttachmentID: attachmentID,
					ActorUserID:  principal.UserID,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				// The insert selects from a ready Attachment the caller
				// uploaded that no chat message, queued message or Project
				// holds, so no row means the id names someone else's file, a
				// file already shared elsewhere or an upload that never
				// completed. Each is the caller's error (ADR 0017).
				return fmt.Errorf(
					"%w: reference document is not your own unshared upload",
					ErrInvalidInput,
				)
			} else if err != nil {
				return fmt.Errorf("register Company reference document: %w", err)
			}
			result = append(result, ReferenceDocument{AttachmentID: row.AttachmentID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// normalizeReferenceDocumentInput rejects an empty or oversized set and
// collapses repeats, so the transaction below writes each file once.
func normalizeReferenceDocumentInput(attachmentIDs []uuid.UUID) ([]uuid.UUID, error) {
	if len(attachmentIDs) == 0 || len(attachmentIDs) > maxCompanyReferenceDocuments {
		return nil, fmt.Errorf(
			"%w: reference documents must name between 1 and %d files",
			ErrInvalidInput, maxCompanyReferenceDocuments,
		)
	}
	seen := make(map[uuid.UUID]struct{}, len(attachmentIDs))
	unique := make([]uuid.UUID, 0, len(attachmentIDs))
	for _, attachmentID := range attachmentIDs {
		if attachmentID == uuid.Nil {
			return nil, fmt.Errorf("%w: reference document id is invalid", ErrInvalidInput)
		}
		if _, repeated := seen[attachmentID]; repeated {
			continue
		}
		seen[attachmentID] = struct{}{}
		unique = append(unique, attachmentID)
	}
	return unique, nil
}
