package wiki

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// AllocateBlockTreeInTx remaps identities when importing or branching a document.
// Entity identities are reserved by PostgreSQL, never minted by a client.
func AllocateBlockTreeInTx(ctx context.Context, q *generated.Queries, company, page uuid.UUID, root *BlockNode) error {
	count := 0
	var countNodes func(BlockNode)
	countNodes = func(node BlockNode) {
		if node.Type != "doc" && !inlineBlockNode(node.Type) {
			count++
		}
		for _, child := range node.Content {
			countNodes(child)
		}
	}
	countNodes(*root)
	if count < 1 || count > 10000 {
		return ErrPageInvalidInput
	}
	ids, err := q.ReserveWikiBlockIDs(ctx, generated.ReserveWikiBlockIDsParams{
		CompanyID: company, PageID: page, Count: int32(count),
	})
	if err != nil {
		return err
	}
	index := 0
	AssignBlockIDs(root, func() string { id := ids[index]; index++; return id.String() })
	return nil
}

// BlockDraftInTx locks the Page before its native document draft.
func BlockDraftInTx(
	ctx context.Context,
	q *generated.Queries,
	company, page uuid.UUID,
) (generated.WikiCanvasDraft, error) {
	if _, err := q.LockCanvasWikiPage(
		ctx,
		generated.LockCanvasWikiPageParams{CompanyID: company, ID: page},
	); err != nil {
		return generated.WikiCanvasDraft{}, err
	}
	return q.LockWikiDraft(ctx, generated.LockWikiDraftParams{CompanyID: company, PageID: page})
}

func createBlockDraftInTx(
	ctx context.Context,
	q *generated.Queries,
	company, page uuid.UUID,
	markdown string,
) (generated.WikiCanvasDraft, error) {
	root := BlocksFromMarkdown(markdown)
	if err := AllocateBlockTreeInTx(ctx, q, company, page, &root); err != nil {
		return generated.WikiCanvasDraft{}, err
	}
	state, err := EncodeBlocks(root)
	if err != nil {
		return generated.WikiCanvasDraft{}, err
	}
	state, root, _, err = MergeBlocks(state, nil)
	if err != nil {
		return generated.WikiCanvasDraft{}, err
	}
	content, err := json.Marshal(root)
	if err != nil {
		return generated.WikiCanvasDraft{}, err
	}
	return q.CreateCanvasDraft(
		ctx,
		generated.CreateCanvasDraftParams{
			CompanyID:     company,
			PageID:        page,
			State:         state,
			Markdown:      markdown,
			Content:       content,
			SchemaVersion: int32(UIDocumentSchema(root)),
		},
	)
}
