package canvas

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// PurgeExpiredPagePreviews removes one bounded batch of transient generations.
// Read and write queries enforce expiry independently of this retention pass.
func (s *DefaultPageService) PurgeExpiredPagePreviews(ctx context.Context) (int64, error) {
	return s.store.Queries().DeleteExpiredPageSourcePreviews(ctx)
}

// PagePreviewRef binds a transient generation to its Run and destination Page.
// These identities never grant authority to mutate the canonical document.
type PagePreviewRef struct {
	RunID        uuid.UUID
	PageID       uuid.UUID
	GenerationID uuid.UUID
}

type PagePreviewCreateInput struct {
	RequestKey      uuid.UUID `json:"requestKey"      binding:"required"`
	ProtocolVersion int       `json:"protocolVersion" binding:"required,oneof=1"`
	LanguageVersion int       `json:"languageVersion" binding:"required,oneof=1"`
}

type PagePreviewGeneration struct {
	ID              uuid.UUID          `json:"id"`
	ProjectID       *uuid.UUID         `json:"projectId,omitempty"`
	PageID          uuid.UUID          `json:"pageId"`
	RunID           uuid.UUID          `json:"runId"`
	ConversationID  uuid.UUID          `json:"conversationId"`
	ProtocolVersion int                `json:"protocolVersion"`
	LanguageVersion int                `json:"languageVersion"`
	Revision        int64              `json:"revision"`
	Source          string             `json:"source"`
	Discarded       bool               `json:"discarded"`
	ExpiresAt       time.Time          `json:"expiresAt"`
	Preview         wiki.SourcePreview `json:"preview"`
}

func (s *DefaultPageService) CreatePagePreviewForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	ref PagePreviewRef,
	input PagePreviewCreateInput,
) (PagePreviewGeneration, error) {
	var result PagePreviewGeneration
	var notification *events.Event
	if ref.RunID == uuid.Nil || ref.PageID == uuid.Nil || ref.GenerationID != uuid.Nil ||
		input.RequestKey == uuid.Nil ||
		input.ProtocolVersion != 1 ||
		input.LanguageVersion != 1 {
		return result, ErrInvalid
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		project, err := admitPagePreview(ctx, q, p, ref)
		if err != nil {
			return err
		}
		row, err := q.GetPageSourcePreviewReceipt(
			ctx,
			generated.GetPageSourcePreviewReceiptParams{
				CompanyID:  p.CompanyID,
				RunID:      ref.RunID,
				RequestKey: input.RequestKey,
			},
		)
		if err == nil {
			if row.PageID != ref.PageID {
				return ErrConflict
			}
			result = s.compilePagePreview(row)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if s.authoring.Disabled {
			return wiki.ErrUIAuthoringDisabled
		}
		row, err = q.CreatePageSourcePreview(
			ctx,
			generated.CreatePageSourcePreviewParams{
				CompanyID:   p.CompanyID,
				ProjectID:   project,
				PageID:      ref.PageID,
				RunID:       ref.RunID,
				ActorUserID: p.UserID,
				RequestKey:  input.RequestKey,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLimit
		}
		if err != nil {
			return err
		}
		result = s.compilePagePreview(row)
		notification, err = pagePreviewChangeEvent(ctx, q, row)
		return err
	})
	if err == nil && notification != nil {
		s.bus.Publish(ctx, Topic, *notification)
	}
	return result, mapWrite(err)
}

func (s *DefaultPageService) ReadPagePreviewForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	ref PagePreviewRef,
) (PagePreviewGeneration, error) {
	var result PagePreviewGeneration
	if ref.RunID == uuid.Nil || ref.PageID == uuid.Nil || ref.GenerationID == uuid.Nil {
		return result, ErrInvalid
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		project, err := admitPagePreview(ctx, q, p, ref)
		if err != nil {
			return err
		}
		row, err := q.GetPageSourcePreview(
			ctx,
			generated.GetPageSourcePreviewParams{
				CompanyID: p.CompanyID,
				ProjectID: nullable(&project),
				PageID:    ref.PageID,
				ID:        ref.GenerationID,
			},
		)
		if err != nil {
			return err
		}
		if row.RunID != ref.RunID {
			return ErrNotFound
		}
		result = s.compilePagePreview(row)
		return nil
	})
	return result, mapWrite(err)
}

func (s *DefaultPageService) PatchPagePreviewForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	ref PagePreviewRef,
	input wiki.SourcePreviewPatch,
) (result PagePreviewGeneration, resultErr error) {
	started := time.Now()
	outcome := "accepted"
	defer func() {
		if s.previewObserver == nil {
			return
		}
		if resultErr != nil {
			var evaluation *wiki.UIEvaluationError
			switch {
			case errors.Is(resultErr, ErrConflict):
				outcome = "conflict"
			case errors.Is(resultErr, ErrLimit):
				outcome = "limit"
			case errors.Is(resultErr, ErrNotFound),
				errors.Is(resultErr, ErrForbidden),
				errors.Is(resultErr, wiki.ErrUIAuthoringDisabled):
				outcome = "denied"
			case errors.Is(resultErr, ErrInvalid),
				errors.Is(resultErr, wiki.ErrPageInvalidInput),
				errors.As(resultErr, &evaluation):
				outcome = "invalid"
			default:
				outcome = "error"
			}
		}
		s.previewObserver("patch", outcome, time.Since(started), len(input.Text))
	}()
	var notification *events.Event
	if ref.RunID == uuid.Nil || ref.PageID == uuid.Nil || ref.GenerationID == uuid.Nil {
		return result, ErrInvalid
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := admitPagePreview(ctx, q, p, ref); err != nil {
			return err
		}
		row, err := q.LockPageSourcePreview(
			ctx,
			generated.LockPageSourcePreviewParams{
				CompanyID: p.CompanyID,
				RunID:     ref.RunID,
				ID:        ref.GenerationID,
			},
		)
		if err != nil {
			return err
		}
		if row.PageID != ref.PageID {
			return ErrNotFound
		}
		current := wiki.SourcePreviewState{
			Revision:        row.Revision,
			Source:          row.Source,
			Discarded:       row.Discarded,
			LastRequestKey:  row.LastRequestKey.UUID,
			LastRequestHash: row.LastRequestHash.String,
		}
		next, err := wiki.ApplySourcePreviewPatch(current, input)
		if err != nil {
			var evaluation *wiki.UIEvaluationError
			if errors.As(err, &evaluation) {
				switch evaluation.Code {
				case "preview.conflict":
					return ErrConflict
				case "preview.limit", "preview.size":
					return ErrLimit
				}
			}
			return err
		}
		// An exact retry neither consumes another revision nor extends retention.
		if next.Revision == current.Revision {
			outcome = "replayed"
			result = s.compilePagePreview(row)
			return nil
		}
		// Disposal remains available while authoring is suspended.
		if s.authoring.Disabled && !next.Discarded {
			return wiki.ErrUIAuthoringDisabled
		}
		row, err = q.UpdatePageSourcePreview(
			ctx,
			generated.UpdatePageSourcePreviewParams{
				CompanyID:        p.CompanyID,
				RunID:            ref.RunID,
				ID:               ref.GenerationID,
				ExpectedRevision: current.Revision,
				Source:           next.Source,
				Discarded:        next.Discarded,
				RequestKey:       next.LastRequestKey,
				RequestHash:      next.LastRequestHash,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		result = pagePreviewResult(row, next.Preview)
		if next.Discarded {
			outcome = "discarded"
		}
		notification, err = pagePreviewChangeEvent(ctx, q, row)
		return err
	})
	if err == nil && notification != nil {
		s.bus.Publish(ctx, Topic, *notification)
	}
	return result, mapWrite(err)
}

// Lock order is the existing Page Run admission order, then Page, then preview.
// Even reconnect reads prove the live Run's member and execution generation.
func admitPagePreview(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	ref PagePreviewRef,
) (uuid.UUID, error) {
	if err := checkDocumentAgentPage(ctx, q, p, ref.RunID, ref.PageID); err != nil {
		return uuid.Nil, err
	}
	project, _, err := admitPageRun(ctx, q, p, ref.RunID, true)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := editableProjectPage(ctx, q, p, project, ref.PageID); err != nil {
		return uuid.Nil, err
	}
	return project, nil
}

func (s *DefaultPageService) compilePagePreview(row generated.ProjectPageSourcePreview) PagePreviewGeneration {
	started := time.Now()
	preview := wiki.SourcePreview{Diagnostics: []wiki.SourceDiagnostic{}}
	if !row.Discarded {
		preview = wiki.PreviewSource(row.Source, int(row.LanguageVersion))
	}
	outcome := "empty"
	switch {
	case row.Discarded:
		outcome = "discarded"
	case preview.Complete:
		outcome = "complete"
	case preview.Content != nil:
		outcome = "partial"
	}
	if s.previewObserver != nil {
		s.previewObserver("compile", outcome, time.Since(started), len(row.Source))
	}
	return pagePreviewResult(row, preview)
}

func pagePreviewResult(row generated.ProjectPageSourcePreview, preview wiki.SourcePreview) PagePreviewGeneration {
	return PagePreviewGeneration{
		ID:              row.ID,
		ProjectID:       outputSource(row.ProjectID),
		PageID:          row.PageID,
		RunID:           row.RunID,
		ConversationID:  row.ConversationID,
		ProtocolVersion: int(row.ProtocolVersion),
		LanguageVersion: int(row.LanguageVersion),
		Revision:        row.Revision,
		Source:          row.Source,
		Discarded:       row.Discarded,
		ExpiresAt:       row.ExpiresAt.Time,
		Preview:         preview,
	}
}

// PagePreviewMemberRef identifies a destination Page and one retained generation.
// Members may inspect an ended Run's preview until expiry, but cannot mutate it.
type (
	PagePreviewMemberRef struct{ ProjectID, PageID, GenerationID uuid.UUID }
	PagePreviewListInput struct{ ProjectID, PageID, RunID uuid.UUID }
	PagePreviewSummary   struct {
		ID             uuid.UUID  `json:"id"`
		ProjectID      *uuid.UUID `json:"projectId,omitempty"`
		PageID         uuid.UUID  `json:"pageId"`
		RunID          uuid.UUID  `json:"runId"`
		ConversationID uuid.UUID  `json:"conversationId"`
		Revision       int64      `json:"revision"`
		Discarded      bool       `json:"discarded"`
		ExpiresAt      time.Time  `json:"expiresAt"`
	}
)

func (s *DefaultPageService) ReadPagePreview(
	ctx context.Context,
	p auth.UserPrincipal,
	ref PagePreviewMemberRef,
) (PagePreviewGeneration, error) {
	var result PagePreviewGeneration
	if ref.PageID == uuid.Nil || ref.GenerationID == uuid.Nil {
		return result, ErrInvalid
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		ref.ProjectID, err = pageReviewProject(
			ctx,
			q,
			p,
			pageReviewScope{ProjectID: ref.ProjectID, PageID: ref.PageID},
		)
		if err != nil {
			return err
		}
		if err := admitPagePreviewMember(ctx, q, p, ref); err != nil {
			return err
		}
		row, err := q.GetPageSourcePreviewForMember(
			ctx,
			generated.GetPageSourcePreviewForMemberParams{
				CompanyID:   p.CompanyID,
				ProjectID:   ref.ProjectID,
				PageID:      ref.PageID,
				ID:          ref.GenerationID,
				ActorUserID: p.UserID,
			},
		)
		if err != nil {
			return err
		}
		result = s.compilePagePreview(row)
		return nil
	})
	return result, mapWrite(err)
}

func (s *DefaultPageService) ListPagePreviews(
	ctx context.Context,
	p auth.UserPrincipal,
	in PagePreviewListInput,
) ([]PagePreviewSummary, error) {
	result := []PagePreviewSummary{}
	if in.PageID == uuid.Nil || in.RunID == uuid.Nil {
		return result, ErrInvalid
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		in.ProjectID, err = pageReviewProject(
			ctx,
			q,
			p,
			pageReviewScope{ProjectID: in.ProjectID, PageID: in.PageID},
		)
		if err != nil {
			return err
		}
		if err := admitPagePreviewMember(
			ctx,
			q,
			p,
			PagePreviewMemberRef{ProjectID: in.ProjectID, PageID: in.PageID},
		); err != nil {
			return err
		}
		rows, err := q.ListPageSourcePreviewsForMember(
			ctx,
			generated.ListPageSourcePreviewsForMemberParams{
				CompanyID:   p.CompanyID,
				ProjectID:   in.ProjectID,
				PageID:      in.PageID,
				RunID:       in.RunID,
				ActorUserID: p.UserID,
			},
		)
		if err != nil {
			return err
		}
		for _, row := range rows {
			result = append(
				result,
				PagePreviewSummary{
					ID:             row.ID,
					ProjectID:      outputSource(row.ProjectID),
					PageID:         row.PageID,
					RunID:          row.RunID,
					ConversationID: row.ConversationID,
					Revision:       row.Revision,
					Discarded:      row.Discarded,
					ExpiresAt:      row.ExpiresAt.Time,
				},
			)
		}
		return nil
	})
	return result, mapWrite(err)
}

func admitPagePreviewMember(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	ref PagePreviewMemberRef,
) error {
	if err := authorizePageReview(
		ctx,
		q,
		p,
		pageReviewScope{ProjectID: ref.ProjectID, PageID: ref.PageID},
	); err != nil {
		return err
	}
	_, err := editableProjectPage(ctx, q, p, ref.ProjectID, ref.PageID)
	return err
}
