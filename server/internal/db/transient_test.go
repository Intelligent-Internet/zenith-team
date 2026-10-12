package db

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransient(t *testing.T) {
	for _, code := range []string{"40001", "40P01", "55P03", "57014", "53300", "57P01", "57P02", "57P03", "08006"} {
		t.Run(code, func(t *testing.T) {
			if !IsTransient(fmt.Errorf("query: %w", &pgconn.PgError{Code: code})) {
				t.Fatal("expected retryable error")
			}
		})
	}
	for _, err := range []error{context.DeadlineExceeded, ErrCommitUnknown} {
		if !IsTransient(err) {
			t.Fatalf("expected retryable %v", err)
		}
	}
	for _, err := range []error{nil, context.Canceled, errors.New("invalid input"), &pgconn.PgError{Code: "23505"}} {
		if IsTransient(err) {
			t.Fatalf("unexpected retryable %v", err)
		}
	}
}
