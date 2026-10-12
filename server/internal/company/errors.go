package company

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrUnauthenticated = errors.New("user is not authenticated")
	ErrInvalidInput    = errors.New("invalid company input")
	ErrConflict        = errors.New("company create conflict")
	ErrDeleteConflict  = errors.New("company deletion conflict")
	ErrNotFound        = errors.New("company not found")
	ErrForbidden       = errors.New("company role lacks the required permission")
	ErrLastOwner       = errors.New("Company must retain an active owner")
)

var (
	ErrProactiveModeInvalidInput         = errors.New("proactive mode input is invalid")
	ErrProactiveModeConflict             = errors.New("proactive mode mutation conflict")
	ErrProactiveModeConversationRequired = errors.New("proactive mode requires an owner CEO conversation")
)

// Company resources and the Work Plan keep their own not-found/conflict/
// invalid sentinels because the transport maps them to different client-facing
// messages and codes than the Company sentinels above; only the authentication
// and owner sentinels, which map identically, are shared.
var (
	ErrResourceInvalidInput = errors.New("invalid Company resource input")
	ErrResourceConflict     = errors.New("Company resource mutation conflict")
	ErrResourceNotFound     = errors.New("Company resource or Company not found")
)

var (
	ErrPlanInvalidInput = errors.New("invalid Work Plan input")
	ErrPlanConflict     = errors.New("Work Plan mutation conflict")
	ErrPlanNotFound     = errors.New("Work Plan entity or Company not found")
)

// ErrPlanTaskDeletePermissionRequired refuses a Company Plan that deletes
// another member's open Task for an applier who is neither its assignee nor
// a tasks.assign holder (ADR 0014, ADR 0017). Moving such a Task to another
// Agent or out of review answers the Task domain's own refusals, as a Task
// PATCH does.
var ErrPlanTaskDeletePermissionRequired = errors.New(
	"only the Task's assignee or a tasks.assign holder may delete it",
)

// mapMutationError classifies a write failure. A compare-and-swap query that
// matched no row, a unique violation, and a check violation are all the same
// thing to a caller: the row moved under it.
func mapMutationError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrResourceConflict)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23505", "23514":
			return fmt.Errorf("%s: %w", operation, ErrResourceConflict)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
