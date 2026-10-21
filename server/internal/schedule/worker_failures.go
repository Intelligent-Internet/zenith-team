package schedule

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type firingFailureCount struct {
	occurrence time.Time
	attempts   int
	touched    time.Time
}

func escalationEligible(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var networkError net.Error
	if errors.As(err, &networkError) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "55P03" || strings.HasPrefix(pgErr.Code, "08")) {
		return false
	}
	return true
}

func (w *FiringWorker) countFailure(row generated.ListDueScheduleLockTargetsRow) int {
	w.failureMu.Lock()
	defer w.failureMu.Unlock()
	if w.failures == nil {
		w.failures = make(map[uuid.UUID]firingFailureCount)
	}
	now := time.Now()
	if !now.Before(w.nextFailurePrune) {
		for id, item := range w.failures {
			if now.Sub(item.touched) > 30*time.Minute {
				delete(w.failures, id)
			}
		}
		w.nextFailurePrune = now.Add(time.Minute)
	}
	item := w.failures[row.ScheduleID]
	if !item.occurrence.Equal(row.NextOccurrenceAt.Time) {
		item = firingFailureCount{occurrence: row.NextOccurrenceAt.Time}
	}
	item.attempts++
	item.touched = now
	if len(w.failures) < 10000 || item.attempts > 1 {
		w.failures[row.ScheduleID] = item
	}
	return item.attempts
}

func (w *FiringWorker) escalateFailure(ctx context.Context, candidate generated.ListDueScheduleLockTargetsRow) error {
	row, err := w.store.Queries().GetScheduleForCreator(ctx, generated.GetScheduleForCreatorParams{
		CompanyID:   candidate.CompanyID,
		ScheduleID:  candidate.ScheduleID,
		ActorUserID: candidate.CreatorUserID,
		ActorRoles:  auth.RolesWith(auth.PermissionSchedulesManage),
	})
	if err != nil {
		return err
	}
	if !row.NextOccurrenceAt.Time.Equal(candidate.NextOccurrenceAt.Time) {
		return nil
	}
	current, err := responseWithSources(ctx, w.store.Queries(), row)
	if err != nil {
		return err
	}
	err = w.recordMaterializationFailure(
		ctx,
		materializationFailure{
			Schedule:     current,
			OccurrenceAt: candidate.NextOccurrenceAt.Time,
			ServerTime:   candidate.ServerTime.Time,
			Reason:       ReasonFiringFailed,
		},
	)
	if err == nil {
		w.observeFiring("sweep", firingObservation{outcome: "needs_attention", reason: ReasonFiringFailed})
	}
	return err
}
