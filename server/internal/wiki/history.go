package wiki

import (
	"context"
	"encoding/json"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/reearth/ygo/crdt"
)

// DocumentChangeInput records an accepted save, never a client-declared author.
// The caller holds the page/draft lock and owns the mutation transaction.
type DocumentChangeInput struct {
	CompanyID, PageID     uuid.UUID
	ActorID               uuid.UUID
	ActorType, ChangeType string
	Sequence              int64
	Before, State         []byte
}

func RecordDocumentChangeInTx(ctx context.Context, q *generated.Queries, in DocumentChangeInput) error {
	exists, err := q.DocumentHistoryExists(
		ctx,
		generated.DocumentHistoryExistsParams{CompanyID: in.CompanyID, PageID: in.PageID},
	)
	if err != nil {
		return err
	}
	if !exists && len(in.Before) > 0 {
		if err = q.RecordDocumentChange(
			ctx,
			generated.RecordDocumentChangeParams{
				CompanyID:  in.CompanyID,
				PageID:     in.PageID,
				Sequence:   in.Sequence - 1,
				ActorType:  "system",
				ChangeType: "initial",
				Delta:      []byte{},
				Checkpoint: in.Before,
			},
		); err != nil {
			return err
		}
	}
	var delta, checkpoint []byte
	checkpointDue := len(in.Before) == 0
	if !checkpointDue {
		latest, e := q.DocumentHistoryCheckpoint(
			ctx,
			generated.DocumentHistoryCheckpointParams{
				CompanyID: in.CompanyID,
				PageID:    in.PageID,
				Sequence:  in.Sequence,
			},
		)
		if e != nil {
			return e
		}
		checkpointDue = in.Sequence-latest.Sequence >= 64
	}
	if checkpointDue {
		delta, checkpoint = []byte{}, in.State
	} else {
		_, vector, e := BlockStateDelta(in.Before, nil)
		if e != nil {
			return e
		}
		delta, _, err = BlockStateDelta(in.State, vector)
		if err != nil {
			return err
		}
	}
	actor := uuid.NullUUID{UUID: in.ActorID, Valid: in.ActorID != uuid.Nil}
	return q.RecordDocumentChange(
		ctx,
		generated.RecordDocumentChangeParams{
			CompanyID:  in.CompanyID,
			PageID:     in.PageID,
			Sequence:   in.Sequence,
			ActorType:  in.ActorType,
			ActorID:    actor,
			ChangeType: in.ChangeType,
			Delta:      delta,
			Checkpoint: checkpoint,
		},
	)
}

type HistoryPoint struct {
	CompanyID, PageID uuid.UUID
	Sequence          int64
}

// ReadDocumentHistoryInTx reconstructs from at most 63 deltas after a checkpoint.
func ReadDocumentHistoryInTx(ctx context.Context, q *generated.Queries, in HistoryPoint) (Document, error) {
	if _, err := q.GetDocumentChange(
		ctx,
		generated.GetDocumentChangeParams{CompanyID: in.CompanyID, PageID: in.PageID, Sequence: in.Sequence},
	); err != nil {
		return Document{}, err
	}
	checkpoint, err := q.DocumentHistoryCheckpoint(
		ctx,
		generated.DocumentHistoryCheckpointParams{
			CompanyID: in.CompanyID,
			PageID:    in.PageID,
			Sequence:  in.Sequence,
		},
	)
	if err != nil {
		return Document{}, err
	}
	deltas, err := q.DocumentHistoryDeltas(
		ctx,
		generated.DocumentHistoryDeltasParams{
			CompanyID:  in.CompanyID,
			PageID:     in.PageID,
			Sequence:   checkpoint.Sequence,
			Sequence_2: in.Sequence,
		},
	)
	if err != nil {
		return Document{}, err
	}
	doc := crdt.New()
	defer doc.Destroy()
	if err = crdt.ApplyUpdateV1(doc, checkpoint.Checkpoint, nil); err != nil {
		return Document{}, err
	}
	for _, delta := range deltas {
		if err = crdt.ApplyUpdateV1(doc, delta, nil); err != nil {
			return Document{}, err
		}
	}
	state, root, markdown, err := MergeBlocks(doc.EncodeStateAsUpdate(), nil)
	if err != nil {
		return Document{}, err
	}
	content, err := json.Marshal(root)
	return Document{
		PageID:   in.PageID,
		Sequence: in.Sequence,
		State:    state,
		Content:  content,
		Markdown: markdown,
	}, err
}

// ReplaceDocumentContent retains CRDT causality so old clients cannot resurrect
// the pre-restore tree by sending an already acknowledged update.
func ReplaceDocumentContent(state []byte, root BlockNode) ([]byte, BlockNode, string, error) {
	doc := crdt.New()
	defer doc.Destroy()
	if err := crdt.ApplyUpdateV1(doc, state, nil); err != nil {
		return nil, BlockNode{}, "", err
	}
	fragment := doc.GetXmlFragment("content")
	doc.Transact(func(tx *crdt.Transaction) {
		fragment.Delete(tx, 0, fragment.Len())
		appendBlockNodes(tx, fragment, 0, root.Content)
	})
	return MergeBlocks(doc.EncodeStateAsUpdate(), nil)
}
