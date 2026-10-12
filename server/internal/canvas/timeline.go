package canvas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Timeline reads the same immutable versions and review decisions as History.
// Seek pagination is stable when several decisions share a transaction timestamp.
func (s *DefaultCanvasService) Timeline(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	cursor string,
) (TimelinePage, error) {
	result := TimelinePage{Items: []TimelineEntry{}}
	var before struct {
		Time time.Time `json:"time"`
		ID   uuid.UUID `json:"id"`
	}
	if cursor != "" {
		if len(cursor) > 256 {
			return result, ErrInvalid
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &before) != nil || before.Time.IsZero() || before.ID == uuid.Nil {
			return result, ErrInvalid
		}
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		rows, err := q.ListCanvasTimeline(ctx, generated.ListCanvasTimelineParams{
			CompanyID: p.CompanyID, ProjectID: project, ActorUserID: p.UserID, FirstPage: cursor == "",
			BeforeTime: pgtype.Timestamptz{Time: before.Time, Valid: cursor != ""}, BeforeID: before.ID,
		})
		if err != nil {
			return err
		}
		result.Items, err = decodeRows[TimelineEntry](rows)
		if err != nil {
			return err
		}
		if len(result.Items) > 50 {
			result.Items = result.Items[:50]
			last := result.Items[49]
			before.Time, before.ID = last.CreatedAt, last.ID
			raw, err := json.Marshal(before)
			if err != nil {
				return err
			}
			next := base64.RawURLEncoding.EncodeToString(raw)
			result.NextCursor = &next
		}
		return nil
	})
	return result, err
}
