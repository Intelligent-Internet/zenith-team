package canvas

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// PageThreads reads working-Page discussions independently of saved versions.
func (s *DefaultPageService) PageThreads(
	ctx context.Context,
	p auth.UserPrincipal,
	project, pageID uuid.UUID,
) ([]Thread, error) {
	result := []Thread{}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		if _, err := editableProjectPage(ctx, q, p, project, pageID); err != nil {
			return err
		}
		rows, err := q.ListReviewThreads(
			ctx,
			generated.ListReviewThreadsParams{
				CompanyID: p.CompanyID,
				ProjectID: nullable(&project),
				PageID:    nullable(&pageID),
			},
		)
		if err != nil {
			return err
		}
		result, err = decodeRows[Thread](rows)
		return err
	})
	return result, mapWrite(err)
}
