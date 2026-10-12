package attachment

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

type attachmentLocator struct {
	ID        uuid.UUID
	CompanyID uuid.UUID
}

type pendingAttachmentCreate struct {
	RequestKey       uuid.UUID
	CompanyID        uuid.UUID
	UploadedByUserID uuid.UUID
	Filename         string
	ContentType      string
	SizeBytes        int64
	PendingTTL       time.Duration
}

type pendingAttachmentExpiry uint8

const (
	pendingAttachmentCurrent pendingAttachmentExpiry = iota + 1
	pendingAttachmentExpired
)

type pendingAttachmentResult struct {
	Record  attachmentRecord
	Created bool
	Expiry  pendingAttachmentExpiry
}

type attachmentCompletion struct {
	Locator          attachmentLocator
	UploadedByUserID uuid.UUID
	ObjectKey        string
	SizeBytes        int64
	ExpectedVersion  int64
	// RunActor is CompleteInput.RunActor: the Run that uploaded the file,
	// when its proof came with the upload.
	RunActor events.Actor
}

type attachmentCompletionAuthorization struct {
	Locator           attachmentLocator
	RequestedByUserID uuid.UUID
}

type attachmentDownloadAuthorization struct {
	Locator           attachmentLocator
	RequestedByUserID uuid.UUID
}

type attachmentCompletionOutcome uint8

const (
	attachmentCompletionApplied attachmentCompletionOutcome = iota + 1
	attachmentCompletionReplay
	attachmentCompletionExpired
	attachmentCompletionDeletionWon
)

type attachmentCompletionResult struct {
	Record  attachmentRecord
	Outcome attachmentCompletionOutcome
}

type attachmentDeletionReason string

const (
	attachmentDeletionIntegrity attachmentDeletionReason = "integrity_mismatch"
	attachmentDeletionExpired   attachmentDeletionReason = "upload_expired"
	attachmentDeletionAbandoned attachmentDeletionReason = "upload_abandoned"
	attachmentDeletionRequested attachmentDeletionReason = "user_requested"
)

type attachmentDeletionRequest struct {
	Locator          attachmentLocator
	UploadedByUserID *uuid.UUID
	ObjectKey        string
	Reason           attachmentDeletionReason
}

type attachmentDeletionFinalization struct {
	Locator         attachmentLocator
	ObjectKey       string
	ExpectedVersion int64
}

func pendingCreateMatchesRecord(create pendingAttachmentCreate, record attachmentRecord) bool {
	return create.RequestKey == record.RequestKey && create.CompanyID == record.CompanyID &&
		create.UploadedByUserID == record.UploadedByUserID &&
		create.Filename == record.Filename && create.ContentType == record.ContentType &&
		create.SizeBytes == record.SizeBytes
}

func validPendingCreate(create pendingAttachmentCreate) bool {
	if !util.ValidUUID(create.RequestKey) || !util.ValidUUID(create.CompanyID) ||
		!util.ValidUUID(create.UploadedByUserID) ||
		!validAttachmentSize(create.SizeBytes) ||
		create.PendingTTL < minimumPendingTTL || create.PendingTTL > maximumPendingTTL {
		return false
	}
	filename, filenameErr := normalizeFilename(create.Filename)
	contentType, contentTypeErr := normalizeContentType(create.ContentType)
	return filenameErr == nil && filename == create.Filename &&
		contentTypeErr == nil && contentType == create.ContentType
}

func validAttachmentAuthorization(
	ctx context.Context,
	q *generated.Queries,
	locator attachmentLocator,
	userID uuid.UUID,
) bool {
	return q != nil && ctx != nil &&
		util.ValidUUID(locator.ID) && util.ValidUUID(locator.CompanyID) && util.ValidUUID(userID)
}

func validAttachmentCleanupStoreCall(
	ctx context.Context,
	q *generated.Queries,
	batchSize int,
) bool {
	return q != nil && ctx != nil && batchSize >= 1 && batchSize <= maximumCleanupBatchSize
}

func validAttachmentCompletion(completion attachmentCompletion) bool {
	return util.ValidUUID(completion.Locator.ID) && util.ValidUUID(completion.Locator.CompanyID) &&
		util.ValidUUID(completion.UploadedByUserID) &&
		completion.ObjectKey == canonicalObjectKey(completion.Locator.CompanyID, completion.Locator.ID) &&
		validAttachmentSize(completion.SizeBytes) && completion.ExpectedVersion == 1
}

func completionMatchesRecord(completion attachmentCompletion, record attachmentRecord) bool {
	return completion.Locator.ID == record.ID && completion.Locator.CompanyID == record.CompanyID &&
		completion.UploadedByUserID == record.UploadedByUserID &&
		completion.ObjectKey == record.ObjectKey && completion.SizeBytes == record.SizeBytes
}

func validAttachmentDeletionRequest(request attachmentDeletionRequest) bool {
	if !util.ValidUUID(request.Locator.ID) || !util.ValidUUID(request.Locator.CompanyID) ||
		request.ObjectKey != canonicalObjectKey(request.Locator.CompanyID, request.Locator.ID) {
		return false
	}
	switch request.Reason {
	case attachmentDeletionRequested:
		return request.UploadedByUserID != nil && util.ValidUUID(*request.UploadedByUserID)
	case attachmentDeletionExpired, attachmentDeletionIntegrity:
		return request.UploadedByUserID == nil
	default:
		return false
	}
}

func validAttachmentDeletionFinalization(finalization attachmentDeletionFinalization) bool {
	return util.ValidUUID(finalization.Locator.ID) &&
		util.ValidUUID(finalization.Locator.CompanyID) &&
		finalization.ObjectKey == canonicalObjectKey(finalization.Locator.CompanyID, finalization.Locator.ID) &&
		finalization.ExpectedVersion > 0 && finalization.ExpectedVersion < protocol.MaxJSONSafeInteger
}

// deletionEffectProvenance attributes the finalize step, which is always the
// cleanup worker's: the row no longer carries who asked for the delete, and
// finalization runs in its own transaction long after they did. The authored
// fact — this user requested this deletion — belongs to the activity stream,
// recorded when the request was served.
func deletionEffectProvenance() (string, *uuid.UUID) {
	return "system", nil
}

func attachmentDurationMilliseconds(value time.Duration) (int64, error) {
	if value <= 0 || value > math.MaxInt64-time.Millisecond+1 {
		return 0, errors.New("Attachment duration is outside supported bounds")
	}
	milliseconds := int64(value / time.Millisecond)
	if value%time.Millisecond != 0 {
		milliseconds++
	}
	if milliseconds <= 0 {
		return 0, errors.New("Attachment duration is outside supported bounds")
	}
	return milliseconds, nil
}

func attachmentRecordFromDBRow(row generated.Attachment) attachmentRecord {
	return attachmentRecord{
		ID:                   row.ID,
		CompanyID:            row.CompanyID,
		UploadedByUserID:     row.UploadedByUserID,
		RequestKey:           row.RequestKey,
		ObjectKey:            row.ObjectKey,
		Filename:             row.Filename,
		ContentType:          row.ContentType,
		SizeBytes:            row.SizeBytes,
		Status:               Status(row.Status),
		UploadLeaseExpiresAt: util.TimestampToTime(row.UploadLeaseExpiresAt),
		CreatedAt:            row.CreatedAt.Time.UTC(),
		UpdatedAt:            row.UpdatedAt.Time.UTC(),
		Revision:             row.Version,
	}
}
