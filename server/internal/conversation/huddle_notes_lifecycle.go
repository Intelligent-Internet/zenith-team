package conversation

import (
	"context"
	"errors"
	"time"

	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/jackc/pgx/v5"
)

func (s *DefaultHuddleNotesService) Reconcile(ctx context.Context) error {
	rows, err := s.options.Store.Queries().ListActiveHuddleNotes(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		intervals, err := s.options.Store.Queries().GetHuddleNotes(ctx, generated.GetHuddleNotesParams{CompanyID: row.CompanyID, HuddleID: row.HuddleID})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		attempts, err := s.options.Store.Queries().ListHuddleNotesIntervals(ctx, generated.ListHuddleNotesIntervalsParams{CompanyID: row.CompanyID, NotesID: intervals.ID})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if intervals.State == "stopped" || intervals.State == "interrupted" {
			err = s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
				h, e := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: row.CompanyID, HuddleID: row.HuddleID})
				if e != nil {
					return e
				}
				if h.State != "ended" {
					return nil
				}
				n, e := q.LockHuddleNotes(ctx, generated.LockHuddleNotesParams{CompanyID: row.CompanyID, HuddleID: row.HuddleID})
				if e != nil {
					return e
				}
				return s.stop(ctx, q, n, "ended", notesEndReason(n))
			})
			if err != nil {
				failures = append(failures, err)
			}
		}
		for _, i := range attempts {
			if i.State == "finished" {
				continue
			}
			var room string
			err = s.withInterval(ctx, i.ID, func(_ *generated.Queries, h generated.Huddle, _ generated.HuddleNote, _ generated.GetHuddleNotesIntervalRow) error {
				room = h.RoomName.String
				return nil
			})
			if err == nil {
				if i.State == "pending" && s.options.Dispatcher != nil {
					err = s.options.Dispatcher.DispatchHuddleNotes(ctx, room, i.ID)
				}
			} else if errors.Is(err, ErrHuddleNotesUnavailable) {
				err = s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
					h, e := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: row.CompanyID, HuddleID: row.HuddleID})
					if e != nil {
						return e
					}
					n, e := q.LockHuddleNotes(ctx, generated.LockHuddleNotesParams{CompanyID: row.CompanyID, HuddleID: row.HuddleID})
					if e != nil {
						return e
					}
					room = h.RoomName.String
					// Recheck the exact attempt: a person may already have restarted notes.
					current, e := q.GetHuddleNotesInterval(ctx, i.ID)
					if e != nil {
						return e
					}
					if current.State == "finished" {
						return nil
					}
					state, reason := "interrupted", "capture_unavailable"
					if n.State == "stopping" {
						reason = "finalization_incomplete"
					}
					if h.State == "ending" || h.State == "ended" {
						state, reason = "ended", notesEndReason(n)
					}
					return s.stop(ctx, q, n, state, reason)
				})
				if err == nil {
					s.signal(ctx, row.CompanyID, row.HuddleID)
					if s.options.Dispatcher != nil && room != "" {
						err = s.options.Dispatcher.RemoveHuddleNotes(ctx, room, i.ID)
					}
				}
			}
			if err != nil {
				failures = append(failures, err)
			}
		}
	}
	expired, err := s.options.Store.Queries().ListExpiredHuddleNotes(ctx)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, row := range expired {
		err = s.options.Store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			if _, e := q.LockHuddle(ctx, generated.LockHuddleParams{CompanyID: row.CompanyID, HuddleID: row.HuddleID}); e != nil {
				return e
			}
			n, e := q.LockHuddleNotes(ctx, generated.LockHuddleNotesParams{CompanyID: row.CompanyID, HuddleID: row.HuddleID})
			if e != nil {
				return e
			}
			if n.ExpiresAt.Time.After(time.Now()) {
				return nil
			}
			if e = q.PurgeHuddleNotesIntervals(ctx, generated.PurgeHuddleNotesIntervalsParams{CompanyID: row.CompanyID, NotesID: n.ID}); e != nil {
				return e
			}
			if e = q.PurgeHuddleNotesRecapRevisions(ctx, generated.PurgeHuddleNotesRecapRevisionsParams{CompanyID: row.CompanyID, NotesID: n.ID}); e != nil {
				return e
			}
			return q.ExpireHuddleNotes(ctx, generated.ExpireHuddleNotesParams{CompanyID: row.CompanyID, ID: n.ID})
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Keep incomplete-tail evidence when the call ends or recap generation seals it.
func notesEndReason(n generated.HuddleNote) string {
	if n.State == "stopping" || n.StopReason == "finalization_incomplete" {
		return "finalization_incomplete"
	}
	return "call_ended"
}
