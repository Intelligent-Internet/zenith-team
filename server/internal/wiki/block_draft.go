package wiki

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/reearth/ygo/crdt"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// AllocateBlockTreeInTx remaps identities when importing or branching a document.
// Entity identities are reserved by PostgreSQL, never minted by a client.
func AllocateBlockTreeInTx(ctx context.Context, q *generated.Queries, company, page uuid.UUID, root *BlockNode) error {
	count := 0
	AssignBlockIDs(root, func() string { count++; return "" })
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

// BlockDraftInTx upgrades legacy Markdown once while retaining its original
// CRDT bytes and text. The page lock serializes first-open initialization.
// Historical revisions are not rewritten by this migration.
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
	draft, err := q.LockWikiDraft(ctx, generated.LockWikiDraftParams{CompanyID: company, PageID: page})
	if errors.Is(err, pgx.ErrNoRows) {
		current, e := q.GetCanvasWikiPage(ctx, generated.GetCanvasWikiPageParams{CompanyID: company, ID: page})
		if e != nil {
			return draft, e
		}
		revision, e := q.GetCanvasWikiRevision(
			ctx,
			generated.GetCanvasWikiRevisionParams{
				CompanyID: company,
				PageID:    page,
				ID:        current.CurrentRevisionID.UUID,
			},
		)
		if e != nil {
			return draft, e
		}
		doc := crdt.New()
		defer doc.Destroy()
		text := doc.GetText("markdown")
		doc.Transact(func(txn *crdt.Transaction) { text.Insert(txn, 0, revision.Body, nil) })
		draft, err = q.CreateCanvasDraft(
			ctx,
			generated.CreateCanvasDraftParams{
				CompanyID: company,
				PageID:    page,
				State:     doc.EncodeStateAsUpdate(),
				Markdown:  revision.Body,
			},
		)
	}
	if err != nil || draft.SchemaVersion == 1 {
		return draft, err
	}
	root := BlocksFromMarkdown(draft.Markdown)
	if err = AllocateBlockTreeInTx(ctx, q, company, page, &root); err != nil {
		return draft, err
	}
	state, err := EncodeBlocks(root)
	if err != nil {
		return draft, err
	}
	state, root, _, err = MergeBlocks(state, nil)
	if err != nil {
		return draft, err
	}
	content, err := json.Marshal(root)
	if err != nil {
		return draft, err
	}
	return q.UpgradeWikiDraft(
		ctx,
		generated.UpgradeWikiDraftParams{CompanyID: company, PageID: page, State: state, Content: content},
	)
}
