package attachment

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestAttachmentLifecycleTypesRedactFmtGoStringAndSlog(t *testing.T) {
	const secret = "attachment-private-path-bucket-credential"
	service, objects := newAttachmentUnitService(t)
	payload := []byte(secret)
	input := validAttachmentInitiateInput(payload)
	attachmentID := attachmentTestID
	expiresAt := attachmentTestNow.Add(defaultPendingTTL)
	record := attachmentRecord{
		ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
		RequestKey: input.RequestKey,
		ObjectKey:  canonicalObjectKey(input.CompanyID, attachmentID),
		Filename:   secret + ".txt", ContentType: input.ContentType, SizeBytes: input.SizeBytes,
		Status: StatusPending, UploadLeaseExpiresAt: &expiresAt,
		CreatedAt: attachmentTestNow, UpdatedAt: attachmentTestNow, Revision: 1,
	}
	response := Response{
		ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID,
		Filename: secret, ContentType: input.ContentType, SizeBytes: input.SizeBytes,
		Status:    PublicStatusPending,
		CreatedAt: attachmentTestNow, UpdatedAt: attachmentTestNow, Revision: 1,
	}
	create := pendingAttachmentCreate{
		RequestKey: input.RequestKey, CompanyID: input.CompanyID,
		UploadedByUserID: input.UploadedByUserID, Filename: secret,
		ContentType: input.ContentType, SizeBytes: input.SizeBytes,
		PendingTTL: defaultPendingTTL,
	}
	values := []any{
		service,
		*service,
		ServiceOptions{Objects: objects},
		input,
		CompleteInput{ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID},
		DownloadInput{ID: attachmentID, CompanyID: input.CompanyID, RequestedByUserID: input.UploadedByUserID},
		DeleteInput{ID: attachmentID, CompanyID: input.CompanyID, UploadedByUserID: input.UploadedByUserID},
		CleanupInput{ID: attachmentID, CompanyID: input.CompanyID},
		response,
		InitiateResult{Attachment: response, Created: true},
		CompleteResult{Attachment: response, Changed: true},
		DownloadResult{Attachment: response, Body: &secretAttachmentReadCloser{secret: secret}},
		record,
		attachmentLocator{ID: attachmentID, CompanyID: input.CompanyID},
		create,
		pendingAttachmentResult{Record: record, Created: true, Expiry: pendingAttachmentCurrent},
		attachmentCompletion{Locator: attachmentLocator{ID: attachmentID, CompanyID: input.CompanyID}},
		attachmentCompletionResult{Record: record, Outcome: attachmentCompletionExpired},
		attachmentDeletionRequest{
			Locator:   attachmentLocator{ID: attachmentID, CompanyID: input.CompanyID},
			ObjectKey: record.ObjectKey,
		},
		attachmentDeletionFinalization{
			Locator:   attachmentLocator{ID: attachmentID, CompanyID: input.CompanyID},
			ObjectKey: record.ObjectKey,
		},
	}
	idText := attachmentID.String()
	for _, value := range values {
		goStringer, ok := value.(fmt.GoStringer)
		if !ok {
			t.Errorf("value %T does not implement fmt.GoStringer", value)
		} else {
			assertAttachmentRedacted(t, value, goStringer.GoString(), secret, idText)
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%q"} {
			assertAttachmentRedacted(t, value, fmt.Sprintf(format, value), secret, idText)
		}
		var output bytes.Buffer
		slog.New(slog.NewJSONHandler(&output, nil)).Info("attachment", slog.Any("value", value))
		assertAttachmentRedacted(t, value, output.String(), secret, idText)
	}
}

func assertAttachmentRedacted(t *testing.T, value any, rendered, secret, id string) {
	t.Helper()
	if !strings.Contains(rendered, "[REDACTED]") || strings.Contains(rendered, secret) ||
		strings.Contains(rendered, id) {
		t.Errorf("value %T was not redacted: %s", value, rendered)
	}
}

type secretAttachmentReadCloser struct{ secret string }

func (*secretAttachmentReadCloser) Read([]byte) (int, error) { return 0, io.EOF }

func (*secretAttachmentReadCloser) Close() error { return nil }
