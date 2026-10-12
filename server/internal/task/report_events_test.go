package task

import (
	"testing"

	"github.com/google/uuid"
)

func TestTaskReportReadyPayloadValidation(t *testing.T) {
	t.Parallel()

	payload, err := NewReportReadyPayload(uuid.New(), uuid.New(), uuid.New(), 4)
	if err != nil {
		t.Fatalf("NewReportReadyPayload() error = %v", err)
	}
	if err := validateReportReadyPayload(payload); err != nil {
		t.Fatalf("validateReportReadyPayload() error = %v", err)
	}
	payload.TaskRevision = 0
	if err := validateReportReadyPayload(payload); err == nil {
		t.Fatal("validateReportReadyPayload() accepted version 0")
	}
}

func TestTaskReportChangedPayloadValidation(t *testing.T) {
	t.Parallel()

	payload, err := NewReportChangedPayload(uuid.New(), uuid.New(), 5)
	if err != nil {
		t.Fatalf("NewReportChangedPayload() error = %v", err)
	}
	if err := validateReportChangedPayload(payload); err != nil {
		t.Fatalf("validateReportChangedPayload() error = %v", err)
	}
	payload.TaskID = "not-a-uuid"
	if err := validateReportChangedPayload(payload); err == nil {
		t.Fatal("validateReportChangedPayload() accepted an invalid task ID")
	}
}
