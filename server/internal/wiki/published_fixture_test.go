package wiki_test

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

// publishedWikiFixture keeps the existing published-reader scenarios focused:
// fixtures explicitly edit the canonical draft and publish it before reading.
type (
	publishedWikiFixture struct{ *wiki.DefaultWikiService }
	updatePageInput      struct {
		ExpectedRevision     int64
		Title, Body, Summary string
		CollectionID         *uuid.UUID
	}
)

func (s *publishedWikiFixture) CreatePage(
	ctx context.Context,
	p auth.UserPrincipal,
	in wiki.CreatePageInput,
) (wiki.PageResponse, error) {
	page, err := s.DefaultWikiService.CreatePage(ctx, p, in)
	if err != nil {
		return page, err
	}
	doc, err := s.ReadDocument(ctx, p, page.ID, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		return page, err
	}
	return s.PublishPage(
		ctx,
		p,
		page.ID,
		wiki.PublishPageInput{ExpectedRevision: page.Revision, ExpectedSequence: doc.Sequence},
	)
}

func (s *publishedWikiFixture) UpdatePage(
	ctx context.Context,
	p auth.UserPrincipal,
	id uuid.UUID,
	in updatePageInput,
) (wiki.PageResponse, error) {
	page, err := s.GetPage(ctx, p, id)
	if err != nil {
		return page, err
	}
	if page.Revision != in.ExpectedRevision {
		return wiki.PageResponse{}, wiki.ErrPageConflict
	}
	doc, err := s.ReadDocument(ctx, p, id, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		return wiki.PageResponse{}, err
	}
	doc, err = s.UpdateDraftMetadata(
		ctx,
		p,
		id,
		wiki.DraftMetadataInput{
			ExpectedSequence: doc.Sequence,
			Title:            in.Title,
			Summary:          in.Summary,
			CollectionID:     in.CollectionID,
		},
	)
	if err != nil {
		return wiki.PageResponse{}, err
	}
	var old wiki.BlockNode
	if err = json.Unmarshal(doc.Content, &old); err != nil {
		return wiki.PageResponse{}, err
	}
	root := wiki.BlocksFromMarkdown(in.Body)
	count := 0
	wiki.AssignBlockIDs(&root, func() string { count++; return "" })
	var ids []uuid.UUID
	for len(ids) < count {
		batch, err := s.ReserveBlockIDs(ctx, p, id, int32(min(256, count-len(ids))))
		if err != nil {
			return wiki.PageResponse{}, err
		}
		ids = append(ids, batch...)
	}
	index := 0
	wiki.AssignBlockIDs(&root, func() string { next := ids[index]; index++; return next.String() })
	var ops []wiki.BlockOperation
	for i := range root.Content {
		ops = append(ops, wiki.BlockOperation{Type: "insert", Content: &root.Content[i]})
	}
	for _, node := range old.Content {
		ops = append(
			ops,
			wiki.BlockOperation{
				Type:         "remove",
				BlockID:      node.Attrs["blockId"].(string),
				ExpectedHash: wiki.BlockHash(node),
			},
		)
	}
	update, _, err := wiki.ApplyBlockOperations(doc.State, ops)
	if err != nil {
		return wiki.PageResponse{}, err
	}
	doc, err = s.SyncDocument(ctx, p, id, wiki.SyncDocumentInput{Update: update, BaseSequence: doc.Sequence})
	if err != nil {
		return wiki.PageResponse{}, err
	}
	return s.PublishPage(
		ctx,
		p,
		id,
		wiki.PublishPageInput{ExpectedRevision: page.Revision, ExpectedSequence: doc.Sequence},
	)
}

func (s *publishedWikiFixture) RestoreRevision(
	ctx context.Context,
	p auth.UserPrincipal,
	id, revision uuid.UUID,
	expected int64,
) (wiki.PageResponse, error) {
	doc, err := s.ReadDocument(ctx, p, id, wiki.DocumentReadInput{Since: -1})
	if err != nil {
		return wiki.PageResponse{}, err
	}
	doc, err = s.DefaultWikiService.RestoreRevision(
		ctx,
		p,
		id,
		revision,
		wiki.RestoreRevisionInput{ExpectedRevision: expected, ExpectedSequence: doc.Sequence},
	)
	if err != nil {
		return wiki.PageResponse{}, err
	}
	return s.PublishPage(
		ctx,
		p,
		id,
		wiki.PublishPageInput{ExpectedRevision: expected, ExpectedSequence: doc.Sequence},
	)
}
