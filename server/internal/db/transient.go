package db

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsTransient identifies failures for which retrying an idempotent transaction
// is appropriate. An unknown commit must be reconciled before repeating effects.
func IsTransient(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCommitUnknown) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return true
	}
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) {
		return false
	}
	if strings.HasPrefix(databaseError.Code, "08") {
		return true
	}
	switch databaseError.Code {
	case "40001", "40P01", "55P03", "57014", "53300", "57P01", "57P02", "57P03":
		return true
	default:
		return false
	}
}
