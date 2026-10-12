package task

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
)

const (
	taskMergeReconcileInterval = 5 * time.Second
	taskMergeReconcileLimit    = 100
)

// MergeReconciler backfills durable merge jobs for Tasks that were already in
// review when Proactive Mode was enabled. Run completion owns the fast path.
type MergeReconciler struct {
	store  *db.Store
	logger *slog.Logger
}

func NewMergeReconciler(store *db.Store, logger *slog.Logger) (*MergeReconciler, error) {
	if store == nil {
		return nil, errors.New("Task merge reconciler store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &MergeReconciler{store: store, logger: logger}, nil
}

func (r *MergeReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(taskMergeReconcileInterval)
	defer ticker.Stop()
	for {
		if err := r.reconcile(ctx); err != nil && ctx.Err() == nil {
			r.logger.WarnContext(ctx, "Task merge reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *MergeReconciler) reconcile(ctx context.Context) error {
	for range taskMergeReconcileLimit {
		if _, err := r.store.Queries().
			RefreshNextPendingTaskMergeJobAuthority(ctx, auth.RolesWith(auth.PermissionCompanyProactiveModeManage)); err == nil {
			continue
		} else if !errors.Is(
			err,
			pgx.ErrNoRows,
		) {
			return fmt.Errorf("refresh proactive Task merge authority: %w", err)
		}
		if _, err := r.store.Queries().
			InsertNextProactiveTaskMergeJob(ctx, auth.RolesWith(auth.PermissionCompanyProactiveModeManage)); errors.Is(
			err,
			pgx.ErrNoRows,
		) {
			return nil
		} else if err != nil {
			return fmt.Errorf("backfill proactive Task merge job: %w", err)
		}
	}
	return nil
}
