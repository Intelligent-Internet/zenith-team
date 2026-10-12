package attachment

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAttachmentClosedErrorMappingDropsRawRepositoryAndStorageDetail(t *testing.T) {
	t.Parallel()

	const secret = "postgresql://admin:secret@private/attachments?bucket=access-key"
	if got := safeRepositoryError(context.Background(), errors.New(secret)); !errors.Is(got, ErrUnavailable) ||
		strings.Contains(got.Error(), "secret") || strings.Contains(got.Error(), "bucket") {
		t.Fatalf("safeRepositoryError(raw) = %v", got)
	}
	if got := safeStorageError(context.Background(), errors.New(secret)); !errors.Is(got, ErrStorageUnavailable) ||
		strings.Contains(got.Error(), "secret") || strings.Contains(got.Error(), "bucket") {
		t.Fatalf("safeStorageError(raw) = %v", got)
	}

	for name, expected := range map[string]error{
		"conflict":  ErrConflict,
		"not found": ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := safeRepositoryError(context.Background(), errors.Join(errors.New(secret), expected))
			if !errors.Is(got, expected) || strings.Contains(got.Error(), "secret") {
				t.Fatalf("safeRepositoryError() = %v, want only %v", got, expected)
			}
		})
	}
	if got := safeStorageError(
		context.Background(),
		errors.Join(errors.New(secret), ErrIntegrityMismatch),
	); !errors.Is(
		got,
		ErrIntegrityMismatch,
	) ||
		strings.Contains(got.Error(), "secret") {
		t.Fatalf("safeStorageError(integrity) = %v", got)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := safeRepositoryError(cancelled, errors.New(secret)); !errors.Is(got, context.Canceled) {
		t.Fatalf("safeRepositoryError(cancelled) = %v", got)
	}
	if got := safeStorageError(cancelled, errors.New(secret)); !errors.Is(got, context.Canceled) {
		t.Fatalf("safeStorageError(cancelled) = %v", got)
	}
}
