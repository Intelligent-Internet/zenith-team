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
		SchemaVersion int
		Update        []byte
		BaseSequence  int64
	}
)

// A target resolves an entry point to one saved page. Document merge, block IDs,
// recovery and comment reconciliation do not depend on where that page is opened.
type documentTarget struct{ ProjectID, VersionID, PageID uuid.UUID }

func (t documentTarget) resolve(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	write bool,
) (generated.WikiPage, bool, error) {
	id := t.PageID
	var canEdit bool
	switch {
	case id != uuid.Nil:
	default:
		_, err := authorize(ctx, q, p, t.ProjectID, false)
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
	}
	page, err := q.GetCanvasWikiPage(ctx, generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: id})
	if err != nil {
		return page, false, lookup(err)
	}
	canEdit, err = authorizeArtifactContent(ctx, q, p, page.ID, write)
	if err != nil {
		return page, false, err
	}
	if page.ArtifactType == "wiki" {
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

// Live content uses Artifact authority, independently of its entry point.
func authorizeArtifactContent(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	id uuid.UUID,
	write bool,
) (bool, error) {
	resolve := auth.ResolveArtifactAccess
	if write {
		resolve = auth.LockArtifactAccess
	}
	access, err := resolve(ctx, q, p, id)
	if err != nil {
		return false, err
	}
	if !access.Read {
		return false, ErrNotFound
	}
	if write && !access.Edit {
		return false, ErrForbidden
	}
	return access.Edit, nil
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
	if owner.ProjectID.Valid {
		return changeEvent(ctx, q, p, owner.ProjectID.UUID, sequence, page)
	}
	readers, err := q.ListArtifactReaders(
		ctx,
		generated.ListArtifactReadersParams{CompanyID: p.CompanyID, ArtifactID: page},
	)
	if err != nil {
		return nil, err
	}
	return &events.Event{
		EntityType: Topic, Name: EventDocumentChanged, CompanyID: p.CompanyID,
		Actor: events.UserActor(p.UserID.String()), CreatedAt: time.Now().UTC(),
		Payload: DocumentChanged{
			ProjectFrame: auth.ProjectFrame{Audience: &auth.ProjectAudience{UserIDs: readers}},
			PageID:       page, Sequence: sequence,
		},
	}, nil
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

func (s *DefaultPageService) CheckpointPageDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	page uuid.UUID,
	sequence int64,
) (uuid.UUID, error) {
	return s.checkpointDocument(ctx, p, documentTarget{PageID: page}, sequence)
}

func (s *DefaultPageService) checkpointDocument(
	ctx context.Context,
	p auth.UserPrincipal,
	target documentTarget,
	sequence int64,
) (uuid.UUID, error) {
	var revision uuid.UUID
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
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
		existing, e := q.PageDocumentCheckpoint(
			ctx,
			generated.PageDocumentCheckpointParams{
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
		if err := authorizeOutputDocument(ctx, q, p, output, false); err != nil {
			return err
		}
		row, err := q.GetOutputDocumentVersion(
			ctx,
			generated.GetOutputDocumentVersionParams{
				ActorUserID: p.UserID,
				CompanyID:   p.CompanyID,
				OutputID:    output,
				ID:          revision,
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
