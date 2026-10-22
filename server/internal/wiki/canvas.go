package wiki

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type CanvasPageInput struct {
	Authoring           UIAuthoringPolicy
	Principal           auth.UserPrincipal
	ProjectID           uuid.UUID
	TaskID, SourceRunID *uuid.UUID
	Theme               string
	Title, Body         string
	Content             *BlockNode
	SnapshotContent     *BlockNode
	SourceOutputID      *uuid.UUID
	HistoryAgentID      *uuid.UUID
}

// CreateCanvasPageInTx participates in the caller's Company/membership transaction.
func CreateCanvasPageInTx(ctx context.Context, q *generated.Queries, in CanvasPageInput) (uuid.UUID, uuid.UUID, error) {
	title, body, summary, err := normalizePageInput(in.Title, in.Body, "")
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	owner := uuid.NullUUID{}
	if in.ProjectID == uuid.Nil {
		owner = uuid.NullUUID{UUID: in.Principal.UserID, Valid: true}
	}
	if in.SourceOutputID != nil {
		access, e := auth.OutputDocumentAccessInTx(ctx, q, in.Principal, *in.SourceOutputID)
		if e != nil {
			return uuid.Nil, uuid.Nil, e
		}
		if !access.Edit {
			return uuid.Nil, uuid.Nil, ErrPageForbidden
		}
		ownership, e := q.GetOutputDocumentOwnership(
			ctx,
			generated.GetOutputDocumentOwnershipParams{
				CompanyID: in.Principal.CompanyID,
				ID:        *in.SourceOutputID,
			},
		)
		if e != nil {
			return uuid.Nil, uuid.Nil, e
		}
		if ownership.ProjectID.Valid {
			in.ProjectID = ownership.ProjectID.UUID
			owner = uuid.NullUUID{}
		} else {
			in.ProjectID = uuid.Nil
			owner = uuid.NullUUID{UUID: in.Principal.UserID, Valid: true}
		}
		for _, content := range []*BlockNode{in.Content, in.SnapshotContent} {
			if content != nil {
				if err := ValidateStandaloneBlocks(*content); err != nil {
					return uuid.Nil, uuid.Nil, err
				}
			}
		}
	} else {
		if err = auth.RequireProjectPermissionHeld(
			ctx,
			q,
			in.Principal.CompanyID,
			in.ProjectID,
			in.Principal.UserID,
			auth.ProjectPermissionTasksWrite,
		); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
	}
	for _, content := range []*BlockNode{in.Content, in.SnapshotContent} {
		if content != nil {
			if err := in.Authoring.Check(0, *content); err != nil {
				return uuid.Nil, uuid.Nil, err
			}
		}
	}
	id, err := q.CreateWikiPage(
		ctx,
		generated.CreateWikiPageParams{
			CompanyID:       in.Principal.CompanyID,
			Type:            "page",
			ProjectID:       uuid.NullUUID{UUID: in.ProjectID, Valid: in.ProjectID != uuid.Nil},
			OwnerUserID:     owner,
			TaskID:          nullableUUID(in.TaskID),
			SourceRunID:     nullableUUID(in.SourceRunID),
			Title:           title,
			Summary:         summary,
			CreatedByUserID: in.Principal.UserID,
		},
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	if in.Theme == "" {
		in.Theme = "default"
	}
	if err = q.SetProjectPageScope(
		ctx,
		generated.SetProjectPageScopeParams{
			CompanyID: in.Principal.CompanyID,
			ID:        id,
			ProjectID: uuid.NullUUID{UUID: in.ProjectID, Valid: in.ProjectID != uuid.Nil},
			Theme:     in.Theme,
		},
	); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if in.SourceOutputID != nil {
		if err = q.SetWikiOutputSource(ctx, generated.SetWikiOutputSourceParams{
			CompanyID:      in.Principal.CompanyID,
			ID:             id,
			SourceOutputID: uuid.NullUUID{UUID: *in.SourceOutputID, Valid: true},
		}); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
	}
	number, err := q.AllocateWikiRevisionNumber(
		ctx,
		generated.AllocateWikiRevisionNumberParams{CompanyID: in.Principal.CompanyID, ID: id},
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	revision, err := q.CreateWikiRevision(
		ctx,
		generated.CreateWikiRevisionParams{
			CompanyID:    in.Principal.CompanyID,
			PageID:       id,
			Number:       number,
			Title:        title,
			Body:         body,
			Summary:      summary,
			AuthorUserID: in.Principal.UserID,
		},
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	_, err = q.SetWikiPageCurrentRevision(
		ctx,
		generated.SetWikiPageCurrentRevisionParams{
			CompanyID:       in.Principal.CompanyID,
			PageID:          id,
			Title:           title,
			Summary:         summary,
			RevisionID:      revision.ID,
			ExpectedVersion: 0,
		},
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if err = replaceLinks(ctx, q, in.Principal.CompanyID, id, body); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	draft, err := createBlockDraftInTx(ctx, q, in.Principal.CompanyID, id, body)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if in.Content != nil {
		root := *in.Content
		if err = AllocateBlockTreeInTx(ctx, q, in.Principal.CompanyID, id, &root); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		state, e := EncodeBlocks(root)
		if e != nil {
			return uuid.Nil, uuid.Nil, e
		}
		state, root, _, e = MergeBlocks(state, nil)
		if e != nil {
			return uuid.Nil, uuid.Nil, e
		}
		content, e := json.Marshal(root)
		if e != nil {
			return uuid.Nil, uuid.Nil, e
		}
		_, err = q.UpdateWikiBlockDraft(
			ctx,
			generated.UpdateWikiBlockDraftParams{
				SchemaVersion: int32(UIDocumentSchema(root)),
				CompanyID:     in.Principal.CompanyID,
				PageID:        id,
				State:         state,
				Content:       content,
				Markdown:      BlocksMarkdown(root),
				HeadSeq:       draft.HeadSeq,
			},
		)
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		draft.Content = content
		draft.Markdown = BlocksMarkdown(root)
	}
	if in.SnapshotContent != nil {
		var live BlockNode
		if err = json.Unmarshal(draft.Content, &live); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		ids := BlockIDs(live)
		snapshot := *in.SnapshotContent
		index := 0
		AssignBlockIDs(&snapshot, func() string { id := ids[index]; index++; return id.String() })
		state, err := EncodeBlocks(snapshot)
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		_, snapshot, _, err = MergeBlocks(state, nil)
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		draft.Content, err = json.Marshal(snapshot)
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		draft.Markdown = BlocksMarkdown(snapshot)
	}
	if err = q.SetWikiRevisionBlocks(
		ctx,
		generated.SetWikiRevisionBlocksParams{
			CompanyID:    in.Principal.CompanyID,
			ID:           revision.ID,
			BlockContent: draft.Content,
			Body:         draft.Markdown,
		},
	); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	current, err := q.GetCanvasDraft(
		ctx,
		generated.GetCanvasDraftParams{CompanyID: in.Principal.CompanyID, PageID: id},
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if err = q.RecordCanvasCheckpoint(ctx, generated.RecordCanvasCheckpointParams{
		CompanyID: in.Principal.CompanyID, PageID: id, RevisionID: revision.ID, ThroughSeq: current.HeadSeq,
	}); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	actorType, actorID := "user", in.Principal.UserID
	if in.HistoryAgentID != nil {
		actorType, actorID = "agent", *in.HistoryAgentID
	}
	if err = RecordDocumentChangeInTx(
		ctx,
		q,
		DocumentChangeInput{
			CompanyID:  in.Principal.CompanyID,
			PageID:     id,
			ActorID:    actorID,
			ActorType:  actorType,
			ChangeType: "initial",
			Sequence:   current.HeadSeq,
			State:      current.State,
		},
	); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return id, revision.ID, nil
}

type CanvasCheckpointInput struct {
	Principal auth.UserPrincipal
	PageID    uuid.UUID
	Body      string
	Sequence  int64
	Content   json.RawMessage
}

// CheckpointCanvasPageInTx creates an unpublished immutable revision of a saved draft.
func CheckpointCanvasPageInTx(ctx context.Context, q *generated.Queries, in CanvasCheckpointInput) (uuid.UUID, error) {
	access, err := auth.LockArtifactAccess(ctx, q, in.Principal, in.PageID)
	if err != nil {
		return uuid.Nil, err
	}
	if !access.Read {
		return uuid.Nil, ErrPageNotFound
	}
	if !access.Edit {
		return uuid.Nil, ErrPageForbidden
	}
	page, err := q.LockCanvasWikiPage(
		ctx,
		generated.LockCanvasWikiPageParams{CompanyID: in.Principal.CompanyID, ID: in.PageID},
	)
	if err != nil {
		return uuid.Nil, err
	}
	if page.Status != "active" {
		return uuid.Nil, ErrPageArchived
	}
	detail, err := q.GetCanvasWikiPage(
		ctx,
		generated.GetCanvasWikiPageParams{CompanyID: in.Principal.CompanyID, ID: in.PageID},
	)
	if err != nil {
		return uuid.Nil, err
	}
	if detail.ArtifactType != "page" {
		return uuid.Nil, ErrPageForbidden
	}
	title, body, summary, err := normalizePageInput(detail.Title, in.Body, detail.Summary)
	if err != nil {
		return uuid.Nil, err
	}
	number, err := q.AllocateWikiRevisionNumber(
		ctx,
		generated.AllocateWikiRevisionNumberParams{CompanyID: in.Principal.CompanyID, ID: in.PageID},
	)
	if err != nil {
		return uuid.Nil, err
	}
	revision, err := q.CreateWikiRevision(
		ctx,
		generated.CreateWikiRevisionParams{
			CompanyID:    in.Principal.CompanyID,
			PageID:       in.PageID,
			Number:       number,
			Title:        title,
			Body:         body,
			Summary:      summary,
			CollectionID: detail.CollectionID,
			AuthorUserID: in.Principal.UserID,
		},
	)
	if err != nil {
		return uuid.Nil, err
	}
	if len(in.Content) != 0 {
		if err = q.SetWikiRevisionBlocks(
			ctx,
			generated.SetWikiRevisionBlocksParams{
				CompanyID:    in.Principal.CompanyID,
				ID:           revision.ID,
				BlockContent: in.Content,
				Body:         in.Body,
			},
		); err != nil {
			return uuid.Nil, err
		}
	}
	err = q.RecordCanvasCheckpoint(
		ctx,
		generated.RecordCanvasCheckpointParams{
			CompanyID:  in.Principal.CompanyID,
			PageID:     in.PageID,
			RevisionID: revision.ID,
			ThroughSeq: in.Sequence,
		},
	)
	return revision.ID, err
}
