package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type (
	OutputDocumentRef struct{ OutputID, ItemID uuid.UUID }
	DocumentSyncInput struct {
		Update       []byte
		BaseSequence int64
	}
)

// A target resolves an entry point to one saved page. Document merge, block IDs,
// recovery and comment reconciliation do not depend on where that page is opened.
type documentTarget struct{ ProjectID, VersionID, OutputID, ItemID uuid.UUID }

func (t documentTarget) resolve(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	write bool,
) (generated.WikiPage, bool, error) {
	var id uuid.UUID
	var canEdit bool
	if t.OutputID != uuid.Nil {
		allowed, err := authorizeOutputDocument(ctx, q, p, t.OutputID, write)
		if err != nil {
			return generated.WikiPage{}, false, err
		}
		canEdit = allowed
		doc, err := q.GetOutputDocument(
			ctx,
			generated.GetOutputDocumentParams{
				CompanyID:    p.CompanyID,
				OutputID:     t.OutputID,
				OutputItemID: t.ItemID,
			},
		)
		if err != nil {
			return generated.WikiPage{}, false, lookup(err)
		}
		id = doc.PageID
	} else {
		access, err := authorize(ctx, q, p, t.ProjectID, write)
		if err != nil {
			return generated.WikiPage{}, false, err
		}
		if _, err = canVersionSource(ctx, q, p, t.ProjectID, t.VersionID); err != nil {
			return generated.WikiPage{}, false, err
		}
		v, err := q.GetCanvasVersion(
			ctx,
			generated.GetCanvasVersionParams{
				CompanyID: p.CompanyID,
				ProjectID: t.ProjectID,
				ID:        t.VersionID,
			},
		)
		if err != nil {
			return generated.WikiPage{}, false, lookup(err)
		}
		if !v.WikiPageID.Valid {
			return generated.WikiPage{}, false, ErrInvalid
		}
		id = v.WikiPageID.UUID
		canEdit = access.Can(auth.ProjectPermissionTasksWrite)
	}
	page, err := q.GetCanvasWikiPage(ctx, generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: id})
	if err != nil {
		return page, false, lookup(err)
	}
	if page.SourceOutputID.Valid {
		allowed, e := authorizeOutputDocument(ctx, q, p, page.SourceOutputID.UUID, write)
		if e != nil {
			return page, false, e
		}
		canEdit = allowed
	}
	if !page.ProjectID.Valid && !page.SourceOutputID.Valid {
		canEdit = false
	}
	pendingCreation, err := q.PagePendingCreation(
		ctx,
		generated.PagePendingCreationParams{CompanyID: p.CompanyID, WikiPageID: nullable(&id)},
	)
	if err != nil {
		return page, false, err
	}
	canEdit = canEdit && !pendingCreation
	if write && !canEdit {
		return page, false, ErrForbidden
	}
	if page.Status != "active" {
		return page, false, ErrNotFound
	}
	return page, canEdit, nil
}

func (t documentTarget) changed(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	sequence int64,
	page uuid.UUID,
) (*events.Event, error) {
	owner, err := q.GetCanvasWikiPage(ctx, generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: page})
	if err != nil {
		return nil, lookup(err)
	}
	if owner.SourceOutputID.Valid {
		t.OutputID = owner.SourceOutputID.UUID
	}
	if t.OutputID != uuid.Nil {
		readers, err := q.OutputDocumentReaders(
			ctx,
			generated.OutputDocumentReadersParams{CompanyID: p.CompanyID, OutputID: t.OutputID},
		)
		if err != nil {
			return nil, err
		}
		return &events.Event{
			EntityType: Topic,
			Name:       EventDocumentChanged,
			CompanyID:  p.CompanyID,
			Actor:      events.UserActor(p.UserID.String()),
			CreatedAt:  time.Now().UTC(),
			Payload: OutputDocumentChanged{
				ProjectFrame: auth.ProjectFrame{Audience: &auth.ProjectAudience{UserIDs: readers}},
				OutputID:     t.OutputID,
				PageID:       page,
				Sequence:     sequence,
			},
		}, nil
	}
	return changeEvent(ctx, q, p, t.ProjectID, sequence, page)
}

func validateDocumentReferences(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.NullUUID,
	root wiki.BlockNode,
) error {
	if project.Valid {
		return validateBlockReferences(ctx, q, p, project.UUID, root)
	}
	return wiki.ValidateStandaloneBlocks(root)
}

func (s *DefaultPageService) ReadOutputDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	ref OutputDocumentRef,
	input DocumentReadInput,
) (Document, error) {
	return s.readDocument(ctx, p, documentTarget{OutputID: ref.OutputID, ItemID: ref.ItemID}, input)
}

func (s *DefaultPageService) SyncOutputDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	ref OutputDocumentRef,
	input DocumentSyncInput,
) (Document, error) {
	return s.syncDocument(
		ctx,
		p,
		documentTarget{OutputID: ref.OutputID, ItemID: ref.ItemID},
		input.Update,
		input.BaseSequence,
	)
}

func (s *DefaultPageService) ReserveOutputBlockIDs(
	ctx context.Context,
	p auth.UserPrincipal,
	ref OutputDocumentRef,
	count int32,
) ([]uuid.UUID, error) {
	return s.reserveDocumentBlockIDs(ctx, p, documentTarget{OutputID: ref.OutputID, ItemID: ref.ItemID}, count)
}

func (s *DefaultPageService) CheckpointOutputDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	ref OutputDocumentRef,
	sequence int64,
) (uuid.UUID, error) {
	var revision uuid.UUID
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		target := documentTarget{OutputID: ref.OutputID, ItemID: ref.ItemID}
		page, _, err := target.resolve(ctx, q, p, true)
		if err != nil {
			return err
		}
		if _, err = q.LockCanvasWikiPage(
			ctx,
			generated.LockCanvasWikiPageParams{CompanyID: p.CompanyID, ID: page.ID},
		); err != nil {
			return lookup(err)
		}
		draft, err := q.LockWikiDraft(
			ctx,
			generated.LockWikiDraftParams{CompanyID: p.CompanyID, PageID: page.ID},
		)
		if err != nil {
			return err
		}
		if draft.HeadSeq != sequence {
			return ErrConflict
		}
		existing, e := q.OutputDocumentCheckpoint(
			ctx,
			generated.OutputDocumentCheckpointParams{
				CompanyID:  p.CompanyID,
				PageID:     page.ID,
				ThroughSeq: sequence,
			},
		)
		if e == nil {
			revision = existing
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		revision, err = wiki.CheckpointCanvasPageInTx(
			ctx,
			q,
			wiki.CanvasCheckpointInput{
				Principal: p,
				PageID:    page.ID,
				Body:      draft.Markdown,
				Content:   draft.Content,
				Sequence:  sequence,
			},
		)
		return err
	})
	return revision, mapWrite(err)
}

func (s *DefaultPageService) OutputDocumentVersion(
	ctx context.Context,
	p auth.UserPrincipal,
	output, revision uuid.UUID,
) (DocumentVersion, error) {
	var result DocumentVersion
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorizeOutputDocument(ctx, q, p, output, false); err != nil {
			return err
		}
		row, err := q.GetOutputDocumentVersion(
			ctx,
			generated.GetOutputDocumentVersionParams{
				CompanyID: p.CompanyID,
				OutputID:  output,
				ID:        revision,
			},
		)
		if err != nil {
			return lookup(err)
		}
		result = DocumentVersion{Body: row.Body, Content: row.BlockContent}
		return nil
	})
	return result, err
}

type DocumentVersion struct {
	Body    string          `json:"body"`
	Content json.RawMessage `json:"content"`
}
