package attachment

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNormalizeInitiateInputCanonicalizesPublicMetadataAndObjectKey(t *testing.T) {
	input := InitiateInput{
		RequestKey: attachmentTestRequestKey, CompanyID: attachmentTestCompanyID,
		UploadedByUserID: attachmentTestUserID, Filename: "  design brief.txt  ",
		ContentType: "Text/Plain", SizeBytes: 18,
	}
	normalized, err := normalizeInitiateInput(input)
	if err != nil {
		t.Fatalf("normalizeInitiateInput() error = %v", err)
	}
	if normalized.Filename != "design brief.txt" || normalized.ContentType != "text/plain" {
		t.Fatalf("normalized metadata = %#v", normalized)
	}
	if key := canonicalObjectKey(normalized.CompanyID, attachmentTestID); key !=
		"companies/0190f5ef-78cd-7a34-8a12-0123456789ab/attachments/0190f5ef-78cd-7b45-8b23-123456789abc" {
		t.Fatalf("canonicalObjectKey() = %q", key)
	}
}

func TestNormalizeInitiateInputRejectsMalformedMetadata(t *testing.T) {
	valid := InitiateInput{
		RequestKey: attachmentTestRequestKey, CompanyID: attachmentTestCompanyID,
		UploadedByUserID: attachmentTestUserID, Filename: "report.txt",
		ContentType: "text/plain", SizeBytes: 1,
	}
	for name, mutate := range map[string]func(*InitiateInput){
		"nil request key":  func(value *InitiateInput) { value.RequestKey = uuid.Nil },
		"path filename":    func(value *InitiateInput) { value.Filename = "../secret.txt" },
		"control filename": func(value *InitiateInput) { value.Filename = "bad\nname.txt" },
		"invalid type":     func(value *InitiateInput) { value.ContentType = "text/plain; charset=utf-8" },
		"negative size":    func(value *InitiateInput) { value.SizeBytes = -1 },
		"oversized":        func(value *InitiateInput) { value.SizeBytes = (100 << 20) + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if normalized, err := normalizeInitiateInput(candidate); normalized != (InitiateInput{}) ||
				!errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeInitiateInput() = %#v, %v", normalized, err)
			}
		})
	}
}

func TestAttachmentResponseOmitsInternalObjectStorageData(t *testing.T) {
	createdAt := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(time.Hour)
	record := attachmentRecord{
		ID: attachmentTestID, CompanyID: attachmentTestCompanyID,
		UploadedByUserID: attachmentTestUserID,
		RequestKey:       attachmentTestRequestKey,
		ObjectKey:        canonicalObjectKey(attachmentTestCompanyID, attachmentTestID),
		Filename:         "design brief.txt", ContentType: "text/plain", SizeBytes: 18,
		Status: StatusPending, UploadLeaseExpiresAt: &expiresAt,
		CreatedAt: createdAt, UpdatedAt: createdAt, Revision: 1,
	}
	response := attachmentDTOFromRecord(record, PublicStatusPending)
	if response.Revision != 1 || response.Status != PublicStatusPending {
		t.Fatalf("Response = %#v", response)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"objectKey", "sha256", "storageDriver", "storageBinding", "generation"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Response exposed %q: %s", forbidden, encoded)
		}
	}
	// There is no completedAt on the wire: an attachment that reached 'ready'
	// did so at updatedAt, and the authored history is the activity stream's.
	if strings.Contains(string(encoded), "completedAt") {
		t.Fatalf("Response still carries completedAt: %s", encoded)
	}
}

var (
	attachmentTestID         = uuid.MustParse("0190f5ef-78cd-7b45-8b23-123456789abc")
	attachmentTestRequestKey = uuid.MustParse("0190f5ef-78cd-7d67-8d45-3456789abcde")
	attachmentTestCompanyID  = uuid.MustParse("0190f5ef-78cd-7a34-8a12-0123456789ab")
	attachmentTestUserID     = uuid.MustParse("0190f5ef-78cd-7c56-8c34-23456789abcd")
)
