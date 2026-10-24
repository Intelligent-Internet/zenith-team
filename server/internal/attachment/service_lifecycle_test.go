package attachment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

func TestAttachmentClosedErrorMappingDropsRawRepositoryAndStorageDetail(t *testing.T) {
	t.Parallel()

	const secret = "postgresql://admin:secret@private/attachments?bucket=access-key"
	if got := safeRepositoryError(context.Background(), errors.New(secret)); !errors.Is(got, ErrUnavailable) ||
		strings.Contains(got.Error(), "secret") || strings.Contains(got.Error(), "bucket") {
		t.Fatalf("safeRepositoryError(raw) = %v", got)
	}
	if got := safeStorageError(
		context.Background(),
		"upload",
		errors.New(secret),
	); !errors.Is(got, ErrStorageUnavailable) ||
		strings.Contains(got.Error(), "secret") ||
		strings.Contains(got.Error(), "bucket") {
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
		"verify",
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
	if got := safeStorageError(cancelled, "upload", errors.New(secret)); !errors.Is(got, context.Canceled) {
		t.Fatalf("safeStorageError(cancelled) = %v", got)
	}
}

// The client only ever sees attachment_storage_unavailable, so the server log
// is where the cause lives: at error level, quoting the request id the error
// envelope carried, and with credentials redacted. Not parallel: it swaps the
// default logger the service writes to.
func TestStorageFailureLogsItsCauseWithTheRequestID(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const requestID = "01a169d8-408c-745b-87a5-9e9304d950a1"
	ctx := util.WithRequestID(context.Background(), requestID)
	cause := errors.New("create S3 object: authentication_failed (s3://AKIAABCDEFGHIJKLMNOP:hunter2@bucket)")
	if got := safeStorageError(ctx, "upload", cause); !errors.Is(got, ErrStorageUnavailable) ||
		strings.Contains(got.Error(), "authentication_failed") {
		t.Fatalf("safeStorageError() = %v, want the bare ErrStorageUnavailable", got)
	}

	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("log line %q is not one JSON object: %v", logs.String(), err)
	}
	for key, want := range map[string]string{
		"level":      "ERROR",
		"msg":        "attachment storage failed",
		"request_id": requestID,
		"operation":  "upload",
	} {
		if line[key] != want {
			t.Errorf("%s = %v, want %q", key, line[key], want)
		}
	}
	logged, _ := line["error"].(string)
	if !strings.Contains(logged, "authentication_failed") ||
		strings.Contains(logged, "AKIAABCDEFGHIJKLMNOP") || strings.Contains(logged, "hunter2") {
		t.Fatalf("logged error = %q, want the class without the credential", logged)
	}
}
