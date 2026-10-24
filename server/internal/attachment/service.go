// Package attachment implements the transport-independent attachment
// lifecycle after caller authorization. It owns no HTTP or authentication
// types and exposes no object-store location or credential detail.
package attachment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/storage"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/redact"
)

// ObjectStorage is the attachment domain port for durable object bytes.
// Provider coordinates and object keys never cross the public API boundary.
type ObjectStorage interface {
	Upload(context.Context, string, io.Reader, int64, string, string) error
	DeleteObject(context.Context, string) error
	DeleteKeys(context.Context, []string) error
	GetReader(context.Context, string) (io.ReadCloser, error)
	PresignGet(context.Context, string, time.Duration) (string, error)
}

const (
	defaultPendingTTL                     = time.Hour
	minimumPendingTTL                     = 20 * time.Minute
	maximumPendingTTL                     = 24 * time.Hour
	minimumAttachmentUploadBytesPerSecond = int64(128 << 10)
	attachmentUploadBaseTimeout           = 2 * time.Minute
)

// AttachmentService owns the attachment lifecycle after caller
// authorization: initiation, completion, download, deletion, and the
// terminal cleanup of a deleting row.
type AttachmentService interface {
	Initiate(context.Context, InitiateInput, io.Reader) (InitiateResult, error)
	Complete(context.Context, CompleteInput) (CompleteResult, error)
	Download(context.Context, DownloadInput) (DownloadResult, error)
	DownloadResolved(context.Context, ResolvedDownloadInput) (DownloadResult, error)
	DownloadForRun(context.Context, RunDownloadInput) (DownloadResult, error)
	ShareResolved(context.Context, ResolvedShareInput) (ShareResult, error)
	Delete(context.Context, DeleteInput) error
	Cleanup(context.Context, CleanupInput) error
	Format(fmt.State, rune)
	GoString() string
	LogValue() slog.Value
}

var _ AttachmentService = (*DefaultAttachmentService)(nil)

type DefaultAttachmentService struct {
	store      *db.Store
	bus        *events.Bus
	objects    ObjectStorage
	pendingTTL time.Duration
}

type ServiceOptions struct {
	Store      *db.Store
	Bus        *events.Bus
	Objects    ObjectStorage
	PendingTTL time.Duration
}

type InitiateResult struct {
	Attachment Response `json:"attachment"`
	Created    bool     `json:"created"`
}

type CompleteResult struct {
	Attachment Response `json:"attachment"`
	Changed    bool     `json:"changed"`
}

type DownloadResult struct {
	Attachment  Response      `json:"attachment"`
	Body        io.ReadCloser `json:"-"`
	NotModified bool          `json:"-"`
}

type ShareResult struct {
	URL       string
	ExpiresAt time.Time
}

func NewAttachmentService(options ServiceOptions) (*DefaultAttachmentService, error) {
	if options.PendingTTL == 0 {
		options.PendingTTL = defaultPendingTTL
	}
	if options.Store == nil || options.Bus == nil || nilDependency(options.Objects) ||
		options.PendingTTL < minimumPendingTTL ||
		options.PendingTTL > maximumPendingTTL {
		return nil, ErrInvalidServiceConfiguration
	}
	// The store is only touched per call, so a zero-value store still
	// constructs (pinned by cmd/server/main_test.go).
	return &DefaultAttachmentService{
		store: options.Store, bus: options.Bus, objects: options.Objects,
		pendingTTL: options.PendingTTL,
	}, nil
}

func (s *DefaultAttachmentService) Initiate(
	ctx context.Context,
	input InitiateInput,
	source io.Reader,
) (InitiateResult, error) {
	if !validServiceCall(ctx, s) || nilDependency(source) {
		return InitiateResult{}, ErrInvalidInput
	}
	normalized, err := normalizeInitiateInput(input)
	if err != nil {
		return InitiateResult{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, attachmentUploadTimeout(normalized.SizeBytes))
	defer cancel()
	ctx = operationCtx
	create := pendingAttachmentCreate{
		RequestKey: normalized.RequestKey, CompanyID: normalized.CompanyID,
		UploadedByUserID: normalized.UploadedByUserID,
		Filename:         normalized.Filename, ContentType: normalized.ContentType,
		SizeBytes: normalized.SizeBytes, PendingTTL: s.pendingTTL,
	}
	var initiated pendingAttachmentResult
	if err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		var txErr error
		initiated, txErr = initiatePendingAttachment(ctx, tx, create)
		return txErr
	}); err != nil {
		return InitiateResult{}, safeRepositoryError(ctx, err)
	}
	record := initiated.Record
	if record.Status == StatusDeleted {
		return InitiateResult{}, ErrConflict
	}
	if initiated.Expiry == pendingAttachmentExpired {
		if cleanupErr := s.requestDeletionAndCleanup(
			ctx,
			record,
			attachmentDeletionExpired,
		); cleanupErr != nil {
			return InitiateResult{}, cleanupErr
		}
		return InitiateResult{}, ErrUploadExpired
	}
	publicStatus := PublicStatusPending
	if record.Status == StatusReady {
		publicStatus = PublicStatusReady
	}
	response := attachmentDTOFromRecord(record, publicStatus)
	result := InitiateResult{Attachment: response, Created: initiated.Created}
	if record.Status == StatusReady {
		return result, nil
	}

	err = s.objects.Upload(
		ctx, record.ObjectKey, source, record.SizeBytes, record.ContentType, record.Filename,
	)
	if errors.Is(err, storage.ErrAlreadyExists) {
		if verifyErr := s.verifyStoredContent(ctx, record); verifyErr != nil {
			if errors.Is(verifyErr, ErrIntegrityMismatch) {
				if cleanupErr := s.requestDeletionAndCleanup(
					ctx,
					record,
					attachmentDeletionIntegrity,
				); cleanupErr != nil {
					return InitiateResult{}, cleanupErr
				}
			}
			return InitiateResult{}, verifyErr
		}
		return result, nil
	}
	if errors.Is(err, storage.ErrSizeMismatch) {
		if cleanupErr := s.requestDeletionAndCleanup(
			ctx,
			record,
			attachmentDeletionIntegrity,
		); cleanupErr != nil {
			return InitiateResult{}, cleanupErr
		}
		return InitiateResult{}, ErrIntegrityMismatch
	}
	if err != nil {
		return InitiateResult{}, safeStorageError(ctx, "upload", err)
	}
	return result, nil
}

func attachmentUploadTimeout(sizeBytes int64) time.Duration {
	uploadSeconds := (sizeBytes + minimumAttachmentUploadBytesPerSecond - 1) /
		minimumAttachmentUploadBytesPerSecond
	return attachmentUploadBaseTimeout + time.Duration(uploadSeconds)*time.Second
}

func (s *DefaultAttachmentService) verifyStoredContent(ctx context.Context, record attachmentRecord) error {
	reader, err := s.objects.GetReader(ctx, record.ObjectKey)
	if err != nil {
		closeAttachmentReaderBestEffort(reader)
		if errors.Is(err, storage.ErrNotFound) {
			return ErrUploadIncomplete
		}
		return safeStorageError(ctx, "verify", err)
	}
	if nilDependency(reader) {
		return storageUnavailable(ctx, "verify", errNoObjectReader)
	}
	limited := &io.LimitedReader{R: reader, N: record.SizeBytes + 1}
	actualSize, readErr := io.Copy(io.Discard, limited)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return safeStorageError(ctx, "verify", errors.Join(readErr, closeErr))
	}
	if actualSize != record.SizeBytes {
		return ErrIntegrityMismatch
	}
	return nil
}

func (s *DefaultAttachmentService) Complete(ctx context.Context, input CompleteInput) (CompleteResult, error) {
	if !validServiceCall(ctx, s) || !validCompleteInput(input) {
		return CompleteResult{}, ErrInvalidInput
	}
	record, err := authorizeAttachmentCompletion(ctx, s.store.Queries(), attachmentCompletionAuthorization{
		Locator:           attachmentLocator{ID: input.ID, CompanyID: input.CompanyID},
		RequestedByUserID: input.UploadedByUserID,
	})
	if err != nil {
		return CompleteResult{}, safeRepositoryError(ctx, err)
	}
	if record.Status == StatusReady {
		return CompleteResult{Attachment: attachmentDTOFromRecord(record, PublicStatusReady)}, nil
	}
	if record.Status != StatusPending {
		return CompleteResult{}, ErrNotFound
	}
	if err := s.verifyStoredContent(ctx, record); err != nil {
		if errors.Is(err, ErrIntegrityMismatch) {
			if cleanupErr := s.requestDeletionAndCleanup(
				ctx,
				record,
				attachmentDeletionIntegrity,
			); cleanupErr != nil {
				return CompleteResult{}, cleanupErr
			}
		}
		return CompleteResult{}, err
	}
	var completion attachmentCompletionResult
	var pendingEvents []events.Event
	if err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pendingEvents = pendingEvents[:0]
		var txErr error
		completion, txErr = completePendingAttachment(ctx, tx, &pendingEvents, attachmentCompletion{
			Locator:          attachmentLocator{ID: record.ID, CompanyID: record.CompanyID},
			UploadedByUserID: record.UploadedByUserID, ObjectKey: record.ObjectKey,
			SizeBytes: record.SizeBytes, ExpectedVersion: record.Revision,
			RunActor: input.RunActor,
		})
		return txErr
	}); err != nil {
		return CompleteResult{}, safeRepositoryError(ctx, err)
	}
	for _, group := range events.GroupByTopic(pendingEvents) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	switch completion.Outcome {
	case attachmentCompletionExpired:
		if cleanupErr := s.requestDeletionAndCleanup(
			ctx,
			completion.Record,
			attachmentDeletionExpired,
		); cleanupErr != nil {
			return CompleteResult{}, cleanupErr
		}
		return CompleteResult{}, ErrUploadExpired
	case attachmentCompletionDeletionWon:
		return CompleteResult{}, ErrNotFound
	case attachmentCompletionApplied, attachmentCompletionReplay:
		return CompleteResult{
			Attachment: attachmentDTOFromRecord(completion.Record, PublicStatusReady),
			Changed:    completion.Outcome == attachmentCompletionApplied,
		}, nil
	default:
		return CompleteResult{}, ErrUnavailable
	}
}

func (s *DefaultAttachmentService) Download(ctx context.Context, input DownloadInput) (DownloadResult, error) {
	if !validServiceCall(ctx, s) || !validDownloadInput(input) {
		return DownloadResult{}, ErrNotFound
	}
	record, err := authorizeAttachmentDownload(ctx, s.store.Queries(), attachmentDownloadAuthorization{
		Locator:           attachmentLocator{ID: input.ID, CompanyID: input.CompanyID},
		RequestedByUserID: input.RequestedByUserID,
	})
	if err != nil {
		return DownloadResult{}, safeRepositoryError(ctx, err)
	}
	return s.downloadRecord(ctx, record)
}

// DownloadResolved opens bytes after a caller such as the Deliverable domain
// has resolved access to this exact Attachment. It intentionally does not
// repeat generic message/uploader authorization. The caller resolves authority
// over an exact resource (a delivery,
// Artifact snapshot, Board or public capture), or identifies the Run authorizer
// whose current delivery grants must be checked before opening storage.
func (s *DefaultAttachmentService) DownloadResolved(
	ctx context.Context,
	input ResolvedDownloadInput,
) (DownloadResult, error) {
	if !validServiceCall(ctx, s) || !util.ValidUUID(input.ID) || !util.ValidUUID(input.CompanyID) {
		return DownloadResult{}, ErrNotFound
	}
	if !input.ResourceAccessResolved {
		inaccessible, err := s.store.Queries().HasInaccessibleOutputAttachment(ctx,
			generated.HasInaccessibleOutputAttachmentParams{
				AttachmentID: input.ID,
				CompanyID:    input.CompanyID,
				ActorUserID:  input.RequestedByUserID,
			})
		if err != nil {
			return DownloadResult{}, safeRepositoryError(ctx, err)
		}
		if inaccessible {
			return DownloadResult{}, ErrNotFound
		}
	}
	record, err := getAttachment(
		ctx,
		s.store.Queries(),
		attachmentLocator{ID: input.ID, CompanyID: input.CompanyID},
	)
	if err != nil {
		return DownloadResult{}, safeRepositoryError(ctx, err)
	}
	if record.Status == StatusReady && input.KnownRevision > 0 && input.KnownRevision == record.Revision {
		return DownloadResult{
			Attachment:  attachmentDTOFromRecord(record, PublicStatusReady),
			NotModified: true,
		}, nil
	}
	return s.downloadRecord(ctx, record)
}

// DownloadForRun opens a file for the daemon running a Run, after the caller
// has checked that the Run's frozen context lists it. The file must still be
// readable by the Run's authorizer now (ADR 0017): a Project file needs that
// person's Project access, and a frozen Project document retired since stays
// readable to them; a chat file follows the chat's readers; a Company
// reference document that is nothing else is open to every active member.
// Anything else, including a Run with no authorizer, is ErrNotFound.
func (s *DefaultAttachmentService) DownloadForRun(ctx context.Context, input RunDownloadInput) (DownloadResult, error) {
	if !validServiceCall(ctx, s) || !util.ValidUUID(input.ID) || !util.ValidUUID(input.CompanyID) ||
		!util.ValidUUID(input.RunID) || !util.ValidUUID(input.AuthorizedByUserID) {
		return DownloadResult{}, ErrNotFound
	}
	row, err := s.store.Queries().
		AuthorizeRunAttachmentDownload(ctx, generated.AuthorizeRunAttachmentDownloadParams{
			AttachmentID: input.ID, CompanyID: input.CompanyID,
			RunID: input.RunID, AuthorizerUserID: input.AuthorizedByUserID,
			FrozenProjectDocument: input.FrozenProjectDocument,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return DownloadResult{}, ErrNotFound
	}
	if err != nil {
		return DownloadResult{}, safeRepositoryError(ctx, err)
	}
	return s.downloadRecord(ctx, attachmentRecordFromDBRow(row))
}

// ShareResolved creates a temporary object URL after a caller such as the
// Social domain has resolved access to this exact Attachment. Output attachments
// cannot use storage URLs because those URLs bypass revocable Artifact access.
func (s *DefaultAttachmentService) ShareResolved(
	ctx context.Context,
	input ResolvedShareInput,
) (ShareResult, error) {
	if !validServiceCall(ctx, s) || !util.ValidUUID(input.ID) || !util.ValidUUID(input.CompanyID) {
		return ShareResult{}, ErrNotFound
	}
	inaccessible, err := s.store.Queries().HasInaccessibleOutputAttachment(ctx,
		generated.HasInaccessibleOutputAttachmentParams{
			AttachmentID: input.ID, CompanyID: input.CompanyID,
		})
	if err != nil {
		return ShareResult{}, safeRepositoryError(ctx, err)
	}
	if inaccessible {
		return ShareResult{}, ErrNotFound
	}
	record, err := getAttachment(
		ctx,
		s.store.Queries(),
		attachmentLocator(input),
	)
	if err != nil {
		return ShareResult{}, safeRepositoryError(ctx, err)
	}
	if record.Status == StatusPending {
		return ShareResult{}, ErrUploadIncomplete
	}
	if record.Status != StatusReady {
		return ShareResult{}, ErrNotFound
	}
	issuedAt := time.Now().UTC()
	url, err := s.objects.PresignGet(ctx, record.ObjectKey, time.Hour)
	if err == nil && url == "" {
		err = errors.New("presign returned no URL")
	}
	if err != nil {
		return ShareResult{}, safeStorageError(ctx, "presign", err)
	}
	return ShareResult{URL: url, ExpiresAt: issuedAt.Add(time.Hour)}, nil
}

func (s *DefaultAttachmentService) downloadRecord(
	ctx context.Context,
	record attachmentRecord,
) (DownloadResult, error) {
	if record.Status == StatusPending {
		return DownloadResult{}, ErrUploadIncomplete
	}
	if record.Status != StatusReady {
		return DownloadResult{}, ErrNotFound
	}
	response := attachmentDTOFromRecord(record, PublicStatusReady)
	reader, err := s.objects.GetReader(ctx, record.ObjectKey)
	if err != nil {
		closeAttachmentReaderBestEffort(reader)
		return DownloadResult{}, safeStorageError(ctx, "download", err)
	}
	if nilDependency(reader) {
		return DownloadResult{}, storageUnavailable(ctx, "download", errNoObjectReader)
	}
	return DownloadResult{Attachment: response, Body: &safeAttachmentReader{ctx: ctx, reader: reader}}, nil
}

func closeAttachmentReaderBestEffort(reader io.ReadCloser) {
	if !nilDependency(reader) {
		_ = reader.Close()
	}
}

func (s *DefaultAttachmentService) Delete(ctx context.Context, input DeleteInput) error {
	if !validServiceCall(ctx, s) || !validDeleteInput(input) {
		return ErrInvalidInput
	}
	record, err := s.loadRecord(ctx, attachmentLocator{ID: input.ID, CompanyID: input.CompanyID})
	if err != nil {
		return err
	}
	if record.UploadedByUserID != input.UploadedByUserID {
		return ErrNotFound
	}
	if record.Status == StatusDeleted {
		return nil
	}
	return s.requestDeletionAndCleanup(ctx, record, attachmentDeletionRequested)
}

func (s *DefaultAttachmentService) Cleanup(ctx context.Context, input CleanupInput) error {
	if !validServiceCall(ctx, s) || !util.ValidUUID(input.ID) || !util.ValidUUID(input.CompanyID) {
		return ErrInvalidInput
	}
	record, err := getAttachment(ctx, s.store.Queries(), attachmentLocator(input))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return safeRepositoryError(ctx, err)
	}
	if record.Status == StatusDeleted {
		return nil
	}
	if record.Status != StatusDeleting {
		return ErrConflict
	}
	return s.cleanupDeleting(ctx, record)
}

func (s *DefaultAttachmentService) loadRecord(
	ctx context.Context,
	locator attachmentLocator,
) (attachmentRecord, error) {
	record, err := getAttachment(ctx, s.store.Queries(), locator)
	if err != nil {
		return attachmentRecord{}, safeRepositoryError(ctx, err)
	}
	return record, nil
}

func (s *DefaultAttachmentService) requestDeletionAndCleanup(
	ctx context.Context,
	record attachmentRecord,
	reason attachmentDeletionReason,
) error {
	var requestedByUserID *uuid.UUID
	if reason == attachmentDeletionRequested {
		requestedByUserID = &record.UploadedByUserID
	}
	var deleting attachmentRecord
	if err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		var txErr error
		deleting, txErr = requestAttachmentDeletion(ctx, tx, attachmentDeletionRequest{
			Locator: attachmentLocator{ID: record.ID, CompanyID: record.CompanyID},
			UploadedByUserID: cloneUUID(
				requestedByUserID,
			),
			ObjectKey: record.ObjectKey, Reason: reason,
		})
		return txErr
	}); err != nil {
		return safeRepositoryError(ctx, err)
	}
	if deleting.Status == StatusDeleted {
		return nil
	}
	return s.cleanupDeleting(ctx, deleting)
}

func (s *DefaultAttachmentService) cleanupDeleting(ctx context.Context, record attachmentRecord) error {
	if record.Status != StatusDeleting || record.Revision >= protocol.MaxJSONSafeInteger {
		return ErrConflict
	}
	if err := s.objects.DeleteObject(ctx, record.ObjectKey); err != nil {
		return safeStorageError(ctx, "delete", err)
	}
	var pendingEvents []events.Event
	if err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pendingEvents = pendingEvents[:0]
		_, txErr := finalizeAttachmentDeletion(ctx, tx, &pendingEvents, attachmentDeletionFinalization{
			Locator:   attachmentLocator{ID: record.ID, CompanyID: record.CompanyID},
			ObjectKey: record.ObjectKey, ExpectedVersion: record.Revision,
		})
		return txErr
	}); err != nil {
		return safeRepositoryError(ctx, err)
	}
	for _, group := range events.GroupByTopic(pendingEvents) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return nil
}

// validCompleteInput, validDownloadInput and validDeleteInput each report
// whether a lifecycle call names a real attachment, a real company, and a real
// actor — the triple every authorization query below is keyed by, and which
// therefore has to hold before one is issued.
//
// None of the three is decided at the boundary. The attachment and company ids
// reach the handler as a path segment and a raw query parameter rather than as
// fields of a bound DTO, the actor is the authenticated principal, and the one
// body that does carry a company id (completeAttachmentRequest) declares no
// `binding` tag. They stay three functions rather than one because the input
// types are distinct and each caller answers a different error.
func validCompleteInput(input CompleteInput) bool {
	return util.ValidUUID(input.ID) && util.ValidUUID(input.CompanyID) &&
		util.ValidUUID(input.UploadedByUserID)
}

func validDownloadInput(input DownloadInput) bool {
	return util.ValidUUID(input.ID) && util.ValidUUID(input.CompanyID) &&
		util.ValidUUID(input.RequestedByUserID)
}

func validDeleteInput(input DeleteInput) bool {
	return util.ValidUUID(input.ID) && util.ValidUUID(input.CompanyID) &&
		util.ValidUUID(input.UploadedByUserID)
}

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

type safeAttachmentReader struct {
	ctx    context.Context
	reader io.ReadCloser
}

func (r *safeAttachmentReader) Read(destination []byte) (int, error) {
	if r == nil || nilDependency(r.reader) {
		return 0, ErrStorageUnavailable
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	read, err := r.reader.Read(destination)
	if err == nil || errors.Is(err, io.EOF) {
		return read, err
	}
	if contextErr := r.ctx.Err(); contextErr != nil {
		return read, contextErr
	}
	return read, storageUnavailable(r.ctx, "download stream", err)
}

func (r *safeAttachmentReader) Close() error {
	if r == nil || nilDependency(r.reader) {
		return ErrStorageUnavailable
	}
	if err := r.reader.Close(); err != nil {
		return storageUnavailable(r.ctx, "download stream", err)
	}
	return nil
}

func safeRepositoryError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	switch {
	case errors.Is(err, ErrConflict):
		return ErrConflict
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	default:
		return ErrUnavailable
	}
}

// safeStorageError maps a storage failure onto the closed errors the API
// answers with, logging the cause of an unavailable store (storageUnavailable).
func safeStorageError(ctx context.Context, operation string, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrIntegrityMismatch) {
		return ErrIntegrityMismatch
	}
	return storageUnavailable(ctx, operation, err)
}

// errNoObjectReader is a storage call that reported success with no reader.
var errNoObjectReader = errors.New("object storage returned no reader")

// storageUnavailable logs why object storage failed and returns the
// ErrStorageUnavailable the client sees, which says nothing about why. The
// S3 adapter has already reduced a provider failure to its operation and
// class (authentication_failed, provider_unavailable, provider_failure), so
// no endpoint, bucket, key or signed URL reaches the message; it is redacted
// again in case a credential slipped through. Object bytes never travel in
// an error.
func storageUnavailable(ctx context.Context, operation string, cause error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	slog.ErrorContext(ctx, "attachment storage failed",
		"request_id", util.ContextRequestID(ctx),
		"operation", operation,
		"error_type", fmt.Sprintf("%T", cause),
		"error", redact.Redact(cause.Error()),
	)
	return ErrStorageUnavailable
}

func validServiceCall(ctx context.Context, service *DefaultAttachmentService) bool {
	return service != nil && ctx != nil && service.store != nil && !nilDependency(service.objects)
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	kind := reflect.ValueOf(value).Kind()
	switch kind {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}
