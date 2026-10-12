package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	dbgen "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	defaultPublicationPollInterval = 3 * time.Second
	minimumPublicationPollInterval = 100 * time.Millisecond
	maximumPublicationPollInterval = time.Hour
	defaultPublicationLease        = 30 * time.Minute
	minimumPublicationLease        = time.Minute
	maximumPublicationLease        = time.Hour
	defaultPublicationMaxAttempts  = 3
)

var errPublicationProcessing = errors.New("Output publication processing failed")

type PublicationIssue string

const (
	PublicationIssueClaimFailed   PublicationIssue = "claim_failed"
	PublicationIssueSourceFailed  PublicationIssue = "source_failed"
	PublicationIssuePublishFailed PublicationIssue = "publish_failed"
	PublicationIssueDeleteFailed  PublicationIssue = "delete_failed"
	PublicationIssueStoreFailed   PublicationIssue = "store_failed"
)

type PublicationIssueReport struct {
	Issue         PublicationIssue
	PublicationID uuid.UUID
	Attempt       int
	ProviderType  PublicationProviderType
	Err           error
}

type PublicationObjectStore interface {
	GetReader(context.Context, string) (io.ReadCloser, error)
}

type PublicationWorkerOptions struct {
	Bus          *events.Bus
	Routes       PublicationRoutes
	PollInterval time.Duration
	Lease        time.Duration
	MaxAttempts  int
	OnIssue      func(PublicationIssueReport)
}

type PublicationWorker struct {
	store       *db.Store
	objectStore PublicationObjectStore
	providers   PublicationProviderCatalog
	options     PublicationWorkerOptions
}

func NewPublicationWorker(
	store *db.Store,
	objectStore PublicationObjectStore,
	providers PublicationProviderCatalog,
	options PublicationWorkerOptions,
) (*PublicationWorker, error) {
	if store == nil || objectStore == nil || providers == nil || options.Bus == nil ||
		!normalizePublicationWorkerOptions(&options) {
		return nil, ErrPublicationInvalidConfiguration
	}
	return &PublicationWorker{
		store: store, objectStore: objectStore, providers: providers, options: options,
	}, nil
}

func (w *PublicationWorker) Run(ctx context.Context) error {
	if !validPublicationWorkerCall(ctx, w) {
		return ErrPublicationInvalidConfiguration
	}
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := w.ProcessOnce(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			w.observeIssue(publicationIssueReport(err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *PublicationWorker) ProcessOnce(ctx context.Context) (bool, error) {
	if !validPublicationWorkerCall(ctx, w) {
		return false, ErrPublicationInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var pending []events.Event
	var claim dbgen.ClaimOutputPublicationWorkRow
	expired, claimed := false, false
	leaseID := uuid.New()
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
		rows, err := q.FailExpiredOutputPublications(ctx, dbgen.FailExpiredOutputPublicationsParams{
			FailureCode:   string(PublicationFailureProviderUnavailable),
			FailureDetail: publicationFailureDetail(PublicationFailureProviderUnavailable),
			MaxAttempts:   int32(w.options.MaxAttempts),
		})
		if err != nil {
			return err
		}
		expired = len(rows) > 0
		claim, err = q.ClaimOutputPublicationWork(ctx, dbgen.ClaimOutputPublicationWorkParams{
			MaxAttempts: int32(w.options.MaxAttempts), LeaseID: leaseID,
			LeaseMilliseconds: w.options.Lease.Milliseconds(),
		})
		if err == nil {
			claimed = true
			rows = append(rows, dbgen.OutputPublication(claim))
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return errors.Join(errPublicationClaimFailed, err)
		}
		for _, row := range rows {
			event, err := publicationChangedInTx(ctx, q, row)
			if err != nil {
				return err
			}
			pending = append(pending, event)
		}
		return nil
	})
	if err != nil {
		return false, errors.Join(errPublicationStoreFailed, err)
	}
	w.options.Bus.Publish(ctx, TopicArtifact, pending...)
	if !claimed {
		return expired, nil
	}
	if claim.Status == string(PublicationStatusRouting) {
		return true, wrapPublicationWorkError(claim, w.routePublication(ctx, claim))
	}
	provider, providerOK := resolvePublicationProvider(
		w.providers,
		publicationProviderKeyFromClaim(claim),
	)
	if !providerOK {
		return true, wrapPublicationWorkError(claim, w.failClaim(
			ctx,
			claim,
			leaseID,
			PublicationFailureProviderUnavailable,
			ErrPublicationDisabled,
		))
	}
	if claim.Status == string(PublicationStatusUnpublishing) {
		return true, wrapPublicationWorkError(
			claim, w.deletePublication(ctx, provider, claim, leaseID),
		)
	}
	return true, wrapPublicationWorkError(claim, w.publish(ctx, provider, claim, leaseID))
}

// Persist the state and its audience together; provider calls stay outside the
// transaction. Cancellation recovery passes its detached, bounded context here.
func (w *PublicationWorker) mutate(
	ctx context.Context,
	write func(*dbgen.Queries) (dbgen.OutputPublication, error),
) error {
	var pending events.Event
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
		row, err := write(q)
		if err != nil {
			return err
		}
		pending, err = publicationChangedInTx(ctx, q, row)
		return err
	})
	if err != nil {
		return err
	}
	w.options.Bus.Publish(ctx, TopicArtifact, pending)
	return nil
}

var (
	errPublicationClaimFailed   = errors.New("claim Output publication work")
	errPublicationSourceFailed  = errors.New("read Output publication source")
	errPublicationPublishFailed = errors.New("publish Output")
	errPublicationDeleteFailed  = errors.New("delete Output publication")
	errPublicationStoreFailed   = errors.New("persist Output publication state")
)

func (w *PublicationWorker) publish(
	ctx context.Context,
	provider PublicationProvider,
	claim dbgen.ClaimOutputPublicationWorkRow,
	leaseID uuid.UUID,
) error {
	sourceRow, err := w.store.Queries().GetOutputPublicationSource(
		ctx,
		dbgen.GetOutputPublicationSourceParams{
			PublicationID: claim.ID,
			LeaseID:       leaseID,
		},
	)
	if err != nil {
		code := publicationSourceLookupFailureCode(err)
		cause := errors.Join(errPublicationStoreFailed, err)
		if code == PublicationFailureSourceInvalid {
			cause = errors.Join(errPublicationSourceFailed, err)
		}
		return w.failClaim(
			ctx,
			claim,
			leaseID,
			code,
			cause,
		)
	}
	source, err := w.objectStore.GetReader(ctx, sourceRow.ObjectKey)
	if err != nil {
		return w.handlePublicationFailure(
			ctx,
			claim,
			leaseID,
			PublicationFailureProviderUnavailable,
			true,
			false,
			errors.Join(errPublicationSourceFailed, err),
			errPublicationSourceFailed,
		)
	}
	if err := w.mutate(ctx, func(q *dbgen.Queries) (dbgen.OutputPublication, error) {
		return q.MarkOutputPublicationProviderResourcesPossible(
			ctx,
			dbgen.MarkOutputPublicationProviderResourcesPossibleParams{
				PublicationID: claim.ID,
				LeaseID:       leaseID,
			},
		)
	}); err != nil {
		closeErr := source.Close()
		return errors.Join(errPublicationStoreFailed, err, closeErr)
	}
	result, publishErr := provider.Publish(ctx, PublicationProviderRequest{
		PublicationID: claim.ID.String(), ServiceName: claim.ServiceName,
		Attempt:      int(claim.AttemptCount),
		SourceSize:   sourceRow.AttachmentSizeBytes,
		SourceSHA256: nil,
	}, source, func(progressCtx context.Context, progress PublicationProviderProgress) error {
		return w.recordProgress(progressCtx, claim.ID, leaseID, progress)
	})
	closeErr := source.Close()
	if publishErr == nil && closeErr != nil {
		publishErr = errors.Join(errPublicationSourceFailed, closeErr)
	}
	if publishErr != nil {
		return w.handleProviderFailure(ctx, claim, leaseID, publishErr, false)
	}
	if err := validatePublicationResult(result); err != nil {
		return w.failClaim(ctx, claim, leaseID, PublicationFailureProviderUnavailable, err)
	}
	if err := w.mutate(ctx, func(q *dbgen.Queries) (dbgen.OutputPublication, error) {
		return q.CompleteOutputPublication(ctx,
			dbgen.CompleteOutputPublicationParams{
				SourceRef:      optionalPublicationText(result.SourceRef),
				SourceTemplate: optionalPublicationText(result.SourceTemplate),
				BuildRef:       optionalPublicationText(result.BuildRef),
				ArtifactRef:    optionalPublicationText(result.ArtifactRef),
				ArtifactDigest: optionalPublicationText(result.ArtifactDigest),
				ServiceRef:     result.ServiceRef,
				PublicUrl:      result.ServiceURI,
				PublicationID:  claim.ID, LeaseID: leaseID,
			})
	}); err != nil {
		return errors.Join(errPublicationStoreFailed, err)
	}
	return nil
}

func (w *PublicationWorker) deletePublication(
	ctx context.Context,
	provider PublicationProvider,
	claim dbgen.ClaimOutputPublicationWorkRow,
	leaseID uuid.UUID,
) error {
	if claim.CustomHostname.Valid {
		if !publicationRoutesAvailable(w.options.Routes, claim.RouteBindingID.String) {
			return w.failClaim(
				ctx,
				claim,
				leaseID,
				PublicationFailureProviderUnavailable,
				ErrPublicationSubdomainDisabled,
			)
		}
		if err := w.options.Routes.Delete(ctx, claim.CustomHostname.String); err != nil {
			return w.handleProviderFailure(ctx, claim, leaseID, err, true)
		}
	}
	if err := provider.Delete(ctx, PublicationProviderDeleteRequest{
		PublicationID: claim.ID.String(),
		ServiceName:   claim.ServiceName,
		Resources:     publicationResourcesFromClaim(claim),
	}); err != nil {
		return w.handleProviderFailure(ctx, claim, leaseID, err, true)
	}
	if err := w.mutate(ctx, func(q *dbgen.Queries) (dbgen.OutputPublication, error) {
		return q.CompleteOutputUnpublication(ctx,
			dbgen.CompleteOutputUnpublicationParams{
				PublicationID: claim.ID, LeaseID: leaseID,
			})
	}); err != nil {
		return errors.Join(errPublicationStoreFailed, err)
	}
	return nil
}

func (w *PublicationWorker) recordProgress(
	ctx context.Context,
	publicationID, leaseID uuid.UUID,
	progress PublicationProviderProgress,
) error {
	status := PublicationStatus(progress.Stage)
	if status != PublicationStatusBuilding && status != PublicationStatusDeploying {
		return ErrPublicationStoreUnavailable
	}
	if !validPublicationProviderResources(progress.PublicationProviderResources, false) {
		return ErrPublicationStoreUnavailable
	}
	err := w.mutate(ctx, func(q *dbgen.Queries) (dbgen.OutputPublication, error) {
		return q.UpdateOutputPublicationProgress(ctx,
			dbgen.UpdateOutputPublicationProgressParams{
				Status:            string(status),
				SourceRef:         optionalPublicationText(progress.SourceRef),
				SourceTemplate:    optionalPublicationText(progress.SourceTemplate),
				BuildRef:          optionalPublicationText(progress.BuildRef),
				ArtifactRef:       optionalPublicationText(progress.ArtifactRef),
				ArtifactDigest:    optionalPublicationText(progress.ArtifactDigest),
				LeaseMilliseconds: w.options.Lease.Milliseconds(),
				PublicationID:     publicationID, LeaseID: leaseID,
			})
	})
	if err != nil {
		return errors.Join(errPublicationStoreFailed, err)
	}
	return nil
}

func (w *PublicationWorker) handleProviderFailure(
	ctx context.Context,
	claim dbgen.ClaimOutputPublicationWorkRow,
	leaseID uuid.UUID,
	err error,
	deleting bool,
) error {
	code, retryable := PublicationFailureProviderUnavailable, true
	var failure *PublicationProviderFailure
	if errors.As(err, &failure) {
		code = failure.Code
		retryable = failure.Retryable
	}
	issue := errPublicationPublishFailed
	if deleting {
		issue = errPublicationDeleteFailed
	}
	return w.handlePublicationFailure(
		ctx,
		claim,
		leaseID,
		code,
		retryable,
		deleting,
		err,
		issue,
	)
}

func (w *PublicationWorker) handlePublicationFailure(
	ctx context.Context,
	claim dbgen.ClaimOutputPublicationWorkRow,
	leaseID uuid.UUID,
	code PublicationFailureCode,
	retryable, deleting bool,
	cause, issue error,
) error {
	if ctx.Err() != nil {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		status := PublicationStatusQueued
		if deleting {
			status = PublicationStatusUnpublishing
		}
		if retryErr := w.mutate(recoveryCtx, func(q *dbgen.Queries) (dbgen.OutputPublication, error) {
			return q.ReleaseOutputPublicationLease(recoveryCtx,
				dbgen.ReleaseOutputPublicationLeaseParams{
					Status:        string(status),
					PublicationID: claim.ID, LeaseID: leaseID,
				})
		}); retryErr != nil {
			return errors.Join(ctx.Err(), errPublicationStoreFailed, retryErr)
		}
		return ctx.Err()
	}
	if retryable && int(claim.AttemptCount) < w.options.MaxAttempts {
		status := PublicationStatusQueued
		if deleting {
			status = PublicationStatusUnpublishing
		}
		if retryErr := w.mutate(ctx, func(q *dbgen.Queries) (dbgen.OutputPublication, error) {
			return q.RetryOutputPublication(ctx,
				dbgen.RetryOutputPublicationParams{
					Status: string(
						status,
					),
					RetryMilliseconds: publicationRetryDelay(claim.AttemptCount).Milliseconds(),
					PublicationID:     claim.ID,
					LeaseID:           leaseID,
				})
		}); retryErr != nil {
			return errors.Join(errPublicationStoreFailed, retryErr)
		}
		return errors.Join(issue, errPublicationProcessing, cause)
	}
	return errors.Join(issue, w.failClaim(ctx, claim, leaseID, code, cause))
}

func (w *PublicationWorker) failClaim(
	ctx context.Context,
	claim dbgen.ClaimOutputPublicationWorkRow,
	leaseID uuid.UUID,
	code PublicationFailureCode,
	cause error,
) error {
	if publicationFailureDetail(code) == "" {
		code = PublicationFailureProviderUnavailable
	}
	if err := w.mutate(ctx, func(q *dbgen.Queries) (dbgen.OutputPublication, error) {
		return q.FailOutputPublication(ctx, dbgen.FailOutputPublicationParams{
			FailureCode: string(code), FailureDetail: publicationFailureDetail(code),
			PublicationID: claim.ID, LeaseID: leaseID,
		})
	}); err != nil {
		return errors.Join(errPublicationStoreFailed, err)
	}
	return errors.Join(cause, errPublicationProcessing)
}

func normalizePublicationWorkerOptions(options *PublicationWorkerOptions) bool {
	if options == nil {
		return false
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultPublicationPollInterval
	}
	if options.Lease == 0 {
		options.Lease = defaultPublicationLease
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = defaultPublicationMaxAttempts
	}
	return options.PollInterval >= minimumPublicationPollInterval &&
		options.PollInterval <= maximumPublicationPollInterval &&
		options.Lease >= minimumPublicationLease && options.Lease <= maximumPublicationLease &&
		options.MaxAttempts >= 1 && options.MaxAttempts <= 10
}

func validPublicationWorkerCall(ctx context.Context, worker *PublicationWorker) bool {
	return ctx != nil && worker != nil && worker.store != nil &&
		worker.objectStore != nil && worker.providers != nil
}

func publicationSourceLookupFailureCode(err error) PublicationFailureCode {
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicationFailureSourceInvalid
	}
	return PublicationFailureProviderUnavailable
}

func validatePublicationResult(
	result PublicationProviderResult,
) error {
	if !validPublicationProviderResources(result.PublicationProviderResources, true) ||
		!validPublicationServiceURI(result.ServiceURI) {
		return ErrPublicationStoreUnavailable
	}
	return nil
}

func publicationProviderKeyFromClaim(
	claim dbgen.ClaimOutputPublicationWorkRow,
) PublicationProviderKey {
	return PublicationProviderKey{
		Type: PublicationProviderType(claim.ProviderType), BindingID: claim.ProviderBindingID,
	}
}

func publicationResourcesFromClaim(
	claim dbgen.ClaimOutputPublicationWorkRow,
) PublicationProviderResources {
	return PublicationProviderResources{
		SourceRef:      claim.SourceRef.String,
		SourceTemplate: claim.SourceTemplate.String,
		BuildRef:       claim.BuildRef.String,
		ArtifactRef:    claim.ArtifactRef.String,
		ArtifactDigest: claim.ArtifactDigest.String,
		ServiceRef:     claim.ServiceRef.String,
	}
}

func validPublicationProviderResources(
	resources PublicationProviderResources,
	requireService bool,
) bool {
	for _, value := range []string{
		resources.SourceRef,
		resources.BuildRef,
		resources.ArtifactRef,
		resources.ServiceRef,
	} {
		if len(value) > 2048 {
			return false
		}
	}
	if len(resources.SourceTemplate) > 128 ||
		resources.ArtifactDigest != "" &&
			!validPublicationImageDigest(resources.ArtifactDigest) {
		return false
	}
	return !requireService || resources.ServiceRef != ""
}

func validPublicationImageDigest(value string) bool {
	digest, found := strings.CutPrefix(value, "sha256:")
	if !found {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size
}

func validPublicationServiceURI(value string) bool {
	if len(value) == 0 || len(value) > 2048 {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.Fragment == ""
}

func publicationRetryDelay(attempt int32) time.Duration {
	delay := 5 * time.Second
	for current := int32(1); current < attempt && delay < time.Minute; current++ {
		delay *= 2
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

func optionalPublicationText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func publicationIssueForError(err error) PublicationIssue {
	switch {
	case errors.Is(err, errPublicationClaimFailed):
		return PublicationIssueClaimFailed
	case errors.Is(err, errPublicationSourceFailed):
		return PublicationIssueSourceFailed
	case errors.Is(err, errPublicationDeleteFailed):
		return PublicationIssueDeleteFailed
	case errors.Is(err, errPublicationStoreFailed):
		return PublicationIssueStoreFailed
	default:
		return PublicationIssuePublishFailed
	}
}

type publicationWorkError struct {
	publicationID uuid.UUID
	attempt       int
	providerType  PublicationProviderType
	err           error
}

func (e *publicationWorkError) Error() string { return e.err.Error() }

func (e *publicationWorkError) Unwrap() error { return e.err }

func wrapPublicationWorkError(claim dbgen.ClaimOutputPublicationWorkRow, err error) error {
	if err == nil {
		return nil
	}
	return &publicationWorkError{
		publicationID: claim.ID,
		attempt:       int(claim.AttemptCount),
		providerType:  PublicationProviderType(claim.ProviderType),
		err:           err,
	}
}

func publicationIssueReport(err error) PublicationIssueReport {
	report := PublicationIssueReport{Issue: publicationIssueForError(err), Err: err}
	var workErr *publicationWorkError
	if errors.As(err, &workErr) {
		report.PublicationID = workErr.publicationID
		report.Attempt = workErr.attempt
		report.ProviderType = workErr.providerType
	}
	return report
}

func (w *PublicationWorker) observeIssue(report PublicationIssueReport) {
	if w == nil || w.options.OnIssue == nil {
		return
	}
	defer func() { _ = recover() }()
	w.options.OnIssue(report)
}
