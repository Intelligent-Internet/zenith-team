package company

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// PlanService applies the Company Plan to Company Tasks and their dependencies.
type PlanService interface {
	ApplyCompanyPlan(context.Context, auth.UserPrincipal, PlanArtifact) (PlanApplyResult, error)
}

var _ PlanService = (*DefaultPlanService)(nil)

// DefaultPlanService reads and writes the Work Plan tables through the query
// set the store hands out: Store.Queries() for a plain read, and the handle
// WithinTx yields for anything that has to land in one commit. Helpers below
// that take the handle as a parameter therefore serve both without knowing
// which they got.
type DefaultPlanService struct {
	store          *db.Store
	bus            *events.Bus
	inboxPresenter inbox.Presenter
}

// NewPlanService accepts a nil inboxPresenter, as NewTaskService does: an
// apply raises the same Inbox items a Task write would, and their toast hint
// is a best-effort post-commit presentation that inbox.PresentCreated skips
// when no presenter is wired.
func NewPlanService(
	store *db.Store,
	bus *events.Bus,
	inboxPresenter inbox.Presenter,
) (*DefaultPlanService, error) {
	if store == nil {
		return nil, errors.New("Work Plan database store is required")
	}
	if bus == nil {
		return nil, errors.New("Work Plan event bus is required")
	}
	return &DefaultPlanService{store: store, bus: bus, inboxPresenter: inboxPresenter}, nil
}

// lockPlanApplier takes the canonical Company and membership locks and
// returns the applier's role while they stay an active member, so a
// concurrent revocation or role change serializes behind the apply instead
// of racing it. The Company lock is the one every grant, visibility, role
// and membership change takes, so the applier's Project access holds still
// for the rest of the apply.
func lockPlanApplier(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
) (auth.CompanyRole, error) {
	if _, err := q.LockCompanyForMembershipMutation(
		ctx, principal.CompanyID,
	); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrPlanNotFound
	} else if err != nil {
		return "", fmt.Errorf("lock Company for Work Plan mutation: %w", err)
	}
	membership, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: principal.CompanyID,
		UserID:    principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return "", ErrPlanNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock Work Plan actor membership: %w", err)
	}
	return auth.CompanyRole(membership.Role), nil
}

func mapPlanMutationError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrPlanConflict)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503":
			return fmt.Errorf("%s: %w", operation, ErrPlanNotFound)
		case "23505", "23514":
			return fmt.Errorf("%s: %w", operation, ErrPlanConflict)
		}
	}
	return mapUnexpected(operation, err)
}

func mapUnexpected(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
