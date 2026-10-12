package attachment

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// This file is the only place in the package that names a generated query or
// row type for the Attachment lifecycle. Every function takes the query set as
// a parameter — the caller decides whether it is Store.Queries() or the handle
// WithinTx yields.

// ReserveInTx creates a server-owned upload reservation so the owning domain can
// bind access lineage before any bytes become downloadable. Existing names a
// reservation already bound to that parent. The returned input's private request
// key must be used for Initiate, and must not be exposed through the public API.
func ReserveInTx(ctx context.Context, q *generated.Queries, input InitiateInput, existing *uuid.UUID) (InitiateInput, uuid.UUID, error) {
	if existing == nil {
		input.RequestKey = uuid.New()
	} else {
		record, err := getAttachment(ctx, q, attachmentLocator{ID: *existing, CompanyID: input.CompanyID})
		if err != nil {
			return InitiateInput{}, uuid.Nil, safeRepositoryError(ctx, err)
		}
		if record.UploadedByUserID != input.UploadedByUserID {
			return InitiateInput{}, uuid.Nil, ErrNotFound
		}
		input.RequestKey = record.RequestKey
	}
	normalized, err := normalizeInitiateInput(input)
	if err != nil {
		return InitiateInput{}, uuid.Nil, err
	}
	result, err := initiatePendingAttachment(ctx, q, pendingAttachmentCreate{
		RequestKey: normalized.RequestKey, CompanyID: normalized.CompanyID, UploadedByUserID: normalized.UploadedByUserID,
		Filename: normalized.Filename, ContentType: normalized.ContentType, SizeBytes: normalized.SizeBytes, PendingTTL: defaultPendingTTL,
	})
	if err != nil {
		return InitiateInput{}, uuid.Nil, safeRepositoryError(ctx, err)
	}
	if result.Expiry == pendingAttachmentExpired {
		return InitiateInput{}, uuid.Nil, ErrUploadExpired
	}
	if existing == nil && !result.Created {
		return InitiateInput{}, uuid.Nil, ErrConflict
	}
	return normalized, result.Record.ID, nil
}

func initiatePendingAttachment(
	ctx context.Context,
	q *generated.Queries,
	create pendingAttachmentCreate,
) (pendingAttachmentResult, error) {
	if q == nil || ctx == nil || !validPendingCreate(create) {
		return pendingAttachmentResult{}, ErrConflict
	}
	ttlMilliseconds, err := attachmentDurationMilliseconds(create.PendingTTL)
	if err != nil {
		return pendingAttachmentResult{}, ErrConflict
	}
	if err := lockAttachmentUserAuthority(ctx, q, create.CompanyID, create.UploadedByUserID); err != nil {
		return pendingAttachmentResult{}, err
	}
	row, err := q.InsertPendingAttachmentIfAbsent(ctx, generated.InsertPendingAttachmentIfAbsentParams{
		RequestKey: create.RequestKey, CompanyID: create.CompanyID,
		UploadedByUserID: create.UploadedByUserID,
		Filename:         create.Filename, ContentType: create.ContentType, SizeBytes: create.SizeBytes,
		PendingTtlMilliseconds: ttlMilliseconds,
	})
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		row, err = q.GetAttachmentByRequestKeyForUpdate(
			ctx,
			generated.GetAttachmentByRequestKeyForUpdateParams{
				CompanyID: create.CompanyID, UploadedByUserID: create.UploadedByUserID,
				RequestKey: create.RequestKey,
			},
		)
	}
	if err != nil {
		return pendingAttachmentResult{}, fmt.Errorf("create or lock Attachment: %w", err)
	}
	record := attachmentRecordFromDBRow(row)
	if record.Status == StatusDeleting || record.Status == StatusDeleted ||
		!pendingCreateMatchesRecord(create, record) {
		return pendingAttachmentResult{}, ErrConflict
	}
	expiry := pendingAttachmentCurrent
	if record.Status == StatusPending && !created {
		renewedRow, renewErr := q.RenewPendingAttachmentLease(
			ctx,
			generated.RenewPendingAttachmentLeaseParams{
				PendingTtlMilliseconds: ttlMilliseconds,
				AttachmentID:           record.ID,
				CompanyID:              create.CompanyID,
				UploadedByUserID:       create.UploadedByUserID,
				ExpectedUploadLeaseExpiresAt: pgtype.Timestamptz{
					Time:  record.UploadLeaseExpiresAt.UTC(),
					Valid: true,
				},
			},
		)
		switch {
		case errors.Is(renewErr, pgx.ErrNoRows):
			expiry = pendingAttachmentExpired
		case renewErr != nil:
			return pendingAttachmentResult{}, fmt.Errorf(
				"renew pending Attachment upload lease: %w",
				renewErr,
			)
		default:
			record = attachmentRecordFromDBRow(renewedRow)
		}
	}
	return pendingAttachmentResult{Record: record, Created: created, Expiry: expiry}, nil
}

func getAttachment(
	ctx context.Context,
	q *generated.Queries,
	locator attachmentLocator,
) (attachmentRecord, error) {
	if q == nil || ctx == nil ||
		!util.ValidUUID(locator.ID) || !util.ValidUUID(locator.CompanyID) {
		return attachmentRecord{}, ErrNotFound
	}
	row, err := q.GetAttachmentByLocator(ctx, generated.GetAttachmentByLocatorParams{
		AttachmentID: locator.ID, CompanyID: locator.CompanyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return attachmentRecord{}, ErrNotFound
	}
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("get Attachment: %w", err)
	}
	return attachmentRecordFromDBRow(row), nil
}

func authorizeAttachmentCompletion(
	ctx context.Context,
	q *generated.Queries,
	authorization attachmentCompletionAuthorization,
) (attachmentRecord, error) {
	if !validAttachmentAuthorization(ctx, q, authorization.Locator, authorization.RequestedByUserID) {
		return attachmentRecord{}, ErrNotFound
	}
	row, err := q.AuthorizeAttachmentCompletion(
		ctx,
		generated.AuthorizeAttachmentCompletionParams{
			ActorUserID:  authorization.RequestedByUserID,
			AttachmentID: authorization.Locator.ID,
			CompanyID:    authorization.Locator.CompanyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return attachmentRecord{}, ErrNotFound
	}
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("authorize Attachment completion: %w", err)
	}
	return attachmentRecordFromDBRow(row), nil
}

func authorizeAttachmentDownload(
	ctx context.Context,
	q *generated.Queries,
	authorization attachmentDownloadAuthorization,
) (attachmentRecord, error) {
	if !validAttachmentAuthorization(ctx, q, authorization.Locator, authorization.RequestedByUserID) {
		return attachmentRecord{}, ErrNotFound
	}
	row, err := q.AuthorizeAttachmentDownload(
		ctx,
		generated.AuthorizeAttachmentDownloadParams{
			ActorUserID:  authorization.RequestedByUserID,
			AttachmentID: authorization.Locator.ID,
			CompanyID:    authorization.Locator.CompanyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return attachmentRecord{}, ErrNotFound
	}
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("authorize Attachment download: %w", err)
	}
	return attachmentRecordFromDBRow(row), nil
}

// completePendingAttachment stages the public lifecycle wire event inside
// the exact transaction that marks the Attachment ready, so a rejected effect
// rolls the transition back with it.
func completePendingAttachment(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	completion attachmentCompletion,
) (attachmentCompletionResult, error) {
	if q == nil || ctx == nil || !validAttachmentCompletion(completion) {
		return attachmentCompletionResult{}, ErrConflict
	}
	if err := lockAttachmentUserAuthority(
		ctx,
		q,
		completion.Locator.CompanyID,
		completion.UploadedByUserID,
	); err != nil {
		return attachmentCompletionResult{}, err
	}
	row, err := q.GetAttachmentByIDForUpdate(ctx, completion.Locator.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return attachmentCompletionResult{}, ErrNotFound
	}
	if err != nil {
		return attachmentCompletionResult{}, fmt.Errorf("lock Attachment for completion: %w", err)
	}
	record := attachmentRecordFromDBRow(row)
	if record.CompanyID != completion.Locator.CompanyID || record.UploadedByUserID != completion.UploadedByUserID {
		return attachmentCompletionResult{}, ErrNotFound
	}
	if !completionMatchesRecord(completion, record) {
		return attachmentCompletionResult{}, ErrConflict
	}
	switch record.Status {
	case StatusReady:
		return attachmentCompletionResult{Record: record, Outcome: attachmentCompletionReplay}, nil
	case StatusDeleting, StatusDeleted:
		return attachmentCompletionResult{Record: record, Outcome: attachmentCompletionDeletionWon}, nil
	case StatusPending:
	default:
		return attachmentCompletionResult{}, ErrConflict
	}
	nowAt, err := q.GetAttachmentClock(ctx)
	if err != nil {
		return attachmentCompletionResult{}, fmt.Errorf("read Attachment completion clock: %w", err)
	}
	if record.UploadLeaseExpiresAt == nil || !nowAt.Time.Before(*record.UploadLeaseExpiresAt) {
		return attachmentCompletionResult{Record: record, Outcome: attachmentCompletionExpired}, nil
	}
	if record.Revision != completion.ExpectedVersion {
		return attachmentCompletionResult{}, ErrConflict
	}
	readyRow, err := q.MarkAttachmentReady(ctx, generated.MarkAttachmentReadyParams{
		NowAt:        pgtype.Timestamptz{Time: nowAt.Time.UTC(), Valid: true},
		AttachmentID: record.ID, CompanyID: record.CompanyID,
		UploadedByUserID: record.UploadedByUserID, ObjectKey: record.ObjectKey,
		SizeBytes: record.SizeBytes, ExpectedVersion: record.Revision,
	})
	if err != nil {
		return attachmentCompletionResult{}, fmt.Errorf("complete Attachment: %w", err)
	}
	ready := attachmentRecordFromDBRow(readyRow)
	response := attachmentDTOFromRecord(ready, PublicStatusReady)
	actorID := ready.UploadedByUserID
	writeCompletedEffect(pending, LifecycleEffect{
		AttachmentID: ready.ID, CompanyID: ready.CompanyID, UploadedByUserID: ready.UploadedByUserID,
		CompletedAttachment: &response, Revision: ready.Revision, ActorKind: "user", ActorID: &actorID,
		RunActor: completion.RunActor,
	})
	return attachmentCompletionResult{Record: ready, Outcome: attachmentCompletionApplied}, nil
}

func requestAttachmentDeletion(
	ctx context.Context,
	q *generated.Queries,
	request attachmentDeletionRequest,
) (attachmentRecord, error) {
	if q == nil || ctx == nil || !validAttachmentDeletionRequest(request) {
		return attachmentRecord{}, ErrConflict
	}
	if request.Reason == attachmentDeletionRequested {
		if err := lockAttachmentUserAuthority(
			ctx,
			q,
			request.Locator.CompanyID,
			*request.UploadedByUserID,
		); err != nil {
			return attachmentRecord{}, err
		}
	}
	row, err := q.GetAttachmentByIDForUpdate(ctx, request.Locator.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return attachmentRecord{}, ErrNotFound
	}
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("lock Attachment for deletion: %w", err)
	}
	record := attachmentRecordFromDBRow(row)
	if record.CompanyID != request.Locator.CompanyID {
		return attachmentRecord{}, ErrNotFound
	}
	if record.ObjectKey != request.ObjectKey {
		return attachmentRecord{}, ErrConflict
	}
	if request.Reason == attachmentDeletionRequested && record.UploadedByUserID != *request.UploadedByUserID {
		return attachmentRecord{}, ErrNotFound
	}
	if record.Status == StatusDeleting || record.Status == StatusDeleted {
		return record, nil
	}
	if record.Status != StatusPending && record.Status != StatusReady {
		return attachmentRecord{}, ErrConflict
	}
	nowAt, err := q.GetAttachmentClock(ctx)
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("read Attachment deletion clock: %w", err)
	}
	if request.Reason == attachmentDeletionExpired &&
		(record.Status != StatusPending || record.UploadLeaseExpiresAt == nil ||
			nowAt.Time.Before(*record.UploadLeaseExpiresAt)) {
		return attachmentRecord{}, ErrConflict
	}
	if request.Reason == attachmentDeletionIntegrity && record.Status != StatusPending {
		return attachmentRecord{}, ErrConflict
	}
	deletingRow, err := q.MarkAttachmentDeleting(ctx, generated.MarkAttachmentDeletingParams{
		NowAt:           pgtype.Timestamptz{Time: nowAt.Time.UTC(), Valid: true},
		AttachmentID:    record.ID,
		CompanyID:       record.CompanyID,
		ObjectKey:       record.ObjectKey,
		ExpectedVersion: record.Revision,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return attachmentRecord{}, ErrConflict
	}
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("request Attachment deletion: %w", err)
	}
	return attachmentRecordFromDBRow(deletingRow), nil
}

// finalizeAttachmentDeletion stages the terminal wire event inside the
// exact transaction that marks the Attachment deleted.
func finalizeAttachmentDeletion(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	finalization attachmentDeletionFinalization,
) (attachmentRecord, error) {
	if q == nil || ctx == nil || !validAttachmentDeletionFinalization(finalization) {
		return attachmentRecord{}, ErrConflict
	}
	row, err := q.GetAttachmentByIDForUpdate(ctx, finalization.Locator.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return attachmentRecord{}, ErrNotFound
	}
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("lock Attachment deletion finalization: %w", err)
	}
	record := attachmentRecordFromDBRow(row)
	if record.CompanyID != finalization.Locator.CompanyID {
		return attachmentRecord{}, ErrNotFound
	}
	if record.ObjectKey != finalization.ObjectKey {
		return attachmentRecord{}, ErrConflict
	}
	if record.Status == StatusDeleted {
		if record.Revision != finalization.ExpectedVersion+1 {
			return attachmentRecord{}, ErrConflict
		}
		return record, nil
	}
	if record.Status != StatusDeleting || record.Revision != finalization.ExpectedVersion {
		return attachmentRecord{}, ErrConflict
	}
	nowAt, err := q.GetAttachmentClock(ctx)
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("read Attachment deletion finalization clock: %w", err)
	}
	deletedRow, err := q.MarkAttachmentDeleted(ctx, generated.MarkAttachmentDeletedParams{
		NowAt:        pgtype.Timestamptz{Time: nowAt.Time.UTC(), Valid: true},
		AttachmentID: record.ID, CompanyID: record.CompanyID,
		ObjectKey: record.ObjectKey, ExpectedVersion: record.Revision,
	})
	if err != nil {
		return attachmentRecord{}, fmt.Errorf("finalize Attachment deletion: %w", err)
	}
	deleted := attachmentRecordFromDBRow(deletedRow)
	actorKind, actorID := deletionEffectProvenance()
	if err := writeDeletedEffect(ctx, q, pending, LifecycleEffect{
		AttachmentID: deleted.ID, CompanyID: deleted.CompanyID, UploadedByUserID: deleted.UploadedByUserID,
		Revision: deleted.Revision, ActorKind: actorKind, ActorID: actorID,
	}); err != nil {
		return attachmentRecord{}, fmt.Errorf("write Attachment deletion effects: %w", err)
	}
	return deleted, nil
}

func lockAttachmentUserAuthority(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	userID uuid.UUID,
) error {
	if _, err := q.LockAttachmentCompanyAuthority(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock Attachment Company authority: %w", err)
	}
	_, err := q.LockAttachmentActiveMember(ctx, generated.LockAttachmentActiveMemberParams{
		CompanyID: companyID, ActorUserID: userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock Attachment member authority: %w", err)
	}
	return nil
}
