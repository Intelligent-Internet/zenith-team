package canvas

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// BoardSweep reports what one retention pass removed.
type BoardSweep struct {
	Items         int64
	Edges         int64
	ReleasedFiles int64
	Receipts      int64
}

// Retention horizons. Tombstones, retry receipts and registered but unplaced
// files outlive every undo and local recovery path.
const (
	boardTombstoneRetention = 30 * 24 * time.Hour
	boardUnplacedFileGrace  = 30 * 24 * time.Hour
	boardReceiptRetention   = 30 * 24 * time.Hour
	boardSweepBatch         = 1000
)

// Sweep applies Board retention. Tombstones older than 30 days are past every
// undo and recovery path and leave the Board; a file no item places any longer
// is released once its registration is as old, so ordinary attachment cleanup
// reclaims its bytes. Retry receipts leave after 30 days. Each step drains its
// backlog in short batches, each its own transaction, skipping rows another
// replica's sweep or a live edit holds, within one pass's time budget.
func (s *DefaultBoardService) Sweep(ctx context.Context, now time.Time) (BoardSweep, error) {
	var result BoardSweep
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tombstones := pgtype.Timestamptz{Time: now.Add(-boardTombstoneRetention), Valid: true}
	files := pgtype.Timestamptz{Time: now.Add(-boardUnplacedFileGrace), Valid: true}
	receipts := pgtype.Timestamptz{Time: now.Add(-boardReceiptRetention), Valid: true}
	steps := []struct {
		count *int64
		run   func(*generated.Queries) (int64, error)
	}{
		{&result.Items, func(q *generated.Queries) (int64, error) {
			return q.PurgeBoardTombstones(ctx, generated.PurgeBoardTombstonesParams{Cutoff: tombstones, BatchSize: boardSweepBatch})
		}},
		{&result.Edges, func(q *generated.Queries) (int64, error) {
			return q.PurgeBoardEdgeTombstones(
				ctx,
				generated.PurgeBoardEdgeTombstonesParams{Cutoff: tombstones, BatchSize: boardSweepBatch},
			)
		}},
		{&result.ReleasedFiles, func(q *generated.Queries) (int64, error) {
			return q.ReleaseUnplacedBoardFiles(ctx, generated.ReleaseUnplacedBoardFilesParams{Cutoff: files, BatchSize: boardSweepBatch})
		}},
		{&result.Receipts, func(q *generated.Queries) (int64, error) {
			return q.PurgeBoardReceipts(ctx, generated.PurgeBoardReceiptsParams{Cutoff: receipts, BatchSize: boardSweepBatch})
		}},
		{&result.Receipts, func(q *generated.Queries) (int64, error) {
			return q.PurgeBoardCreateReceipts(
				ctx,
				generated.PurgeBoardCreateReceiptsParams{Cutoff: receipts, BatchSize: boardSweepBatch},
			)
		}},
	}
	for _, step := range steps {
		for {
			var removed int64
			if err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
				var err error
				removed, err = step.run(q)
				return err
			}); err != nil {
				return result, err
			}
			*step.count += removed
			if removed < boardSweepBatch {
				break
			}
		}
	}
	return result, nil
}
