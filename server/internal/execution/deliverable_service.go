package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/artifact"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/studiocanvas"
)

type DeliverableService interface {
	ReadArtifactForRun(context.Context, auth.UserPrincipal, RunOrigin, uuid.UUID) (DeliverableOutput, error)
	AttachStudioCanvas(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		studiocanvas.Canvas,
	) (SavedStudioCanvas, error)
	Present(
		context.Context,
		auth.UserPrincipal,
		RunOrigin,
		DeliverablePresentRequest,
	) (DeliverablePresentResult, error)
	FetchRunResult(context.Context, auth.UserPrincipal, uuid.UUID) (DeliverableRunResult, error)
	FetchRunArtifactResult(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
	) (DeliverableRunResult, error)
	ListTaskResults(context.Context, auth.UserPrincipal, uuid.UUID, TaskResultListOptions) (TaskResultPage, error)
	ListConversationArtifacts(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		ArtifactListOptions,
	) (ConversationArtifactPage, error)
	ListArtifacts(context.Context, auth.UserPrincipal, CompanyArtifactListOptions) (ArtifactPage, error)
	ResolveArtifacts(context.Context, auth.UserPrincipal, []uuid.UUID) ([]ArtifactRunResult, error)
	ResolveArtifactFileAttachment(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		int,
	) (uuid.UUID, error)
	SetArtifactVisibility(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		ArtifactVisibilityInput,
	) (ArtifactAccess, error)
	RevokeArtifactConversation(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		ArtifactConversationInput,
	) (ArtifactAccess, error)
	GetPublicArtifact(context.Context, uuid.UUID) (PublicArtifact, error)
	ResolvePublicArtifactFile(context.Context, uuid.UUID, int) (uuid.UUID, uuid.UUID, error)
	ListUnownedArtifacts(context.Context, auth.UserPrincipal) ([]UnownedArtifact, error)
	ClaimUnownedArtifact(context.Context, auth.UserPrincipal, uuid.UUID, int64) (ArtifactDetail, error)
	GetArtifact(context.Context, auth.UserPrincipal, uuid.UUID) (ArtifactDetail, error)
	ChangeArtifactGrant(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		*ArtifactPermission,
		int64,
	) (ArtifactAccess, error)
}

var _ DeliverableService = (*DefaultDeliverableService)(nil)

type DefaultDeliverableService struct {
	store *db.Store
	bus   *events.Bus
}

type DeliverableServiceOptions struct {
	Store *db.Store
	Bus   *events.Bus
}

func NewDeliverableService(options DeliverableServiceOptions) (*DefaultDeliverableService, error) {
	if options.Store == nil || options.Bus == nil {
		return nil, ErrDeliverableInvalidConfiguration
	}
	return &DefaultDeliverableService{
		store: options.Store, bus: options.Bus,
	}, nil
}

func (s *DefaultDeliverableService) Present(
	ctx context.Context,
	principal auth.UserPrincipal,
	origin RunOrigin,
	request DeliverablePresentRequest,
) (DeliverablePresentResult, error) {
	if !validDeliverableService(s) || origin.RunID == uuid.Nil ||
		origin.CompanyID == uuid.Nil || origin.AgentID == uuid.Nil {
		return DeliverablePresentResult{}, ErrDeliverableInvalidInput
	}
	normalized, err := normalizeDeliverablePresentation(request)
	if err != nil {
		return DeliverablePresentResult{}, err
	}

	var result DeliverablePresentResult
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *dbgen.Queries) error {
		pending = pending[:0]
		_, active, lockErr := lockActiveMember(ctx, tx, origin.CompanyID, principal.UserID)
		if lockErr != nil {
			return lockErr
		}
		if !active {
			return ErrDeliverableNotFound
		}
		if normalized.PresentationID == nil {
			var artifactID uuid.NullUUID
			if normalized.ArtifactID != nil {
				artifactID = uuid.NullUUID{UUID: *normalized.ArtifactID, Valid: true}
			}
			projects, err := tx.ListRetainedPresentationProjects(
				ctx,
				dbgen.ListRetainedPresentationProjectsParams{
					CompanyID: origin.CompanyID, RunID: origin.RunID, ArtifactID: artifactID,
				},
			)
			if err != nil {
				return err
			}
			// Canvas mutations lock Canvas before Artifact. Follow that order even
			// when re-delivering work owned by a different Project.
			for _, project := range projects {
				if _, err = auth.LockProjectAccess(ctx, tx, principal, project); err != nil {
					if errors.Is(err, auth.ErrProjectNotVisible) {
						return ErrDeliverableNotFound
					}
					return err
				}
			}
			for _, project := range projects {
				if _, err = tx.EnsureProjectCanvas(
					ctx,
					dbgen.EnsureProjectCanvasParams{
						CompanyID: origin.CompanyID,
						ProjectID: project,
					},
				); err != nil {
					return err
				}
			}
		}
		if normalized.ArtifactID != nil {
			access, err := auth.LockArtifactAccess(ctx, tx, principal, *normalized.ArtifactID)
			if err != nil {
				return err
			}
			if !access.Read {
				return ErrDeliverableNotFound
			}
			if !access.Edit {
				return ErrDeliverableForbidden
			}
		}
		if normalized.PresentationID != nil {
			if err := canvas.AdmitPresentationRunInTx(ctx, tx, principal, origin.RunID); err != nil {
				return mapPresentationError(err)
			}
		}
		run, err := tx.LockRunForOutputPresentation(
			ctx,
			dbgen.LockRunForOutputPresentationParams{
				RunID: origin.RunID, CompanyID: origin.CompanyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeliverableNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Run for output presentation: %w", err)
		}
		if run.AgentID != origin.AgentID || run.ConversationID != origin.ConversationID {
			return ErrDeliverableForbidden
		}

		existing, err := tx.GetOutputByRunFingerprint(
			ctx,
			dbgen.GetOutputByRunFingerprintParams{
				CompanyID:               origin.CompanyID,
				RunID:                   origin.RunID,
				PresentationFingerprint: normalized.fingerprint,
			},
		)
		if err == nil {
			result = DeliverablePresentResult{OutputID: existing.ID}
			snapshots, err := tx.ListDeliverySnapshots(
				ctx,
				dbgen.ListDeliverySnapshotsParams{CompanyID: origin.CompanyID, OutputID: existing.ID},
			)
			if err != nil {
				return err
			}
			if len(snapshots) > 0 {
				result.ArtifactID = &snapshots[0].ArtifactID
				result.SnapshotID = &snapshots[0].ID
			}
			canvasEvent, err := canvas.RegisterOutputInTx(ctx, tx, canvas.OutputRegistrationInput{
				CompanyID: origin.CompanyID, OutputID: existing.ID, ActorUserID: principal.UserID,
			})
			if canvasEvent != nil {
				pending = append(pending, *canvasEvent)
			}
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load replayed Output: %w", err)
		}
		if run.Status != "running" {
			return ErrDeliverableConflict
		}
		presentationNumber := int64(1)
		latest, err := tx.GetLatestOutputByRunForUpdate(
			ctx,
			dbgen.GetLatestOutputByRunForUpdateParams{
				CompanyID: origin.CompanyID,
				RunID:     origin.RunID,
			},
		)
		if err == nil {
			if latest.PresentationNumber >= 9007199254740991 {
				return ErrDeliverableConflict
			}
			presentationNumber = latest.PresentationNumber + 1
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load latest Output: %w", err)
		}

		var previewURL *string
		if normalized.previewURL != "" {
			previewURL = &normalized.previewURL
		}
		output, err := tx.InsertOutput(ctx, dbgen.InsertOutputParams{
			Type:               string(normalized.Type),
			CompanyID:          origin.CompanyID,
			RunID:              origin.RunID,
			PresentationNumber: presentationNumber,
			Title:              normalized.Title, Summary: normalized.Summary,
			SubmissionNotes:         util.PtrToText(normalized.SubmissionNotes),
			PresentationFingerprint: normalized.fingerprint,
			PreviewUrl:              util.PtrToText(previewURL),
			Payload:                 normalized.payload,
		})
		if err != nil {
			return fmt.Errorf("insert Output: %w", err)
		}
		var primaryAsset uuid.UUID
		for _, item := range normalized.Items {
			assetVersion, err := resolvePresentedAssetVersion(
				ctx, tx, origin, principal.UserID, item,
			)
			if err != nil {
				return err
			}
			native, nativeErr := tx.GetProjectPresentationForAttachment(
				ctx,
				dbgen.GetProjectPresentationForAttachmentParams{
					CompanyID:          origin.CompanyID,
					ExportAttachmentID: uuid.NullUUID{UUID: assetVersion.attachmentID, Valid: true},
				},
			)
			if nativeErr == nil &&
				(normalized.PresentationID == nil || *normalized.PresentationID != native || item.Role != DeliverableItemRolePrimary) {
				return ErrDeliverableInvalidInput
			}
			if nativeErr != nil && !errors.Is(nativeErr, pgx.ErrNoRows) {
				return nativeErr
			}
			if item.Role == DeliverableItemRolePrimary &&
				!artifactPrimaryContentMatches(normalized.Type, assetVersion.contentType) {
				return ErrDeliverableInvalidInput
			}
			if item.Role == DeliverableItemRolePrimary {
				primaryAsset = assetVersion.id
			}
			if _, err := tx.InsertOutputItem(ctx, dbgen.InsertOutputItemParams{
				CompanyID: origin.CompanyID, OutputID: output.ID,
				Ordinal: int32(item.ordinal), Role: string(item.Role), Label: item.Label,
				AssetVersionID: assetVersion.id,
			}); err != nil {
				return fmt.Errorf("insert Output item: %w", err)
			}
		}
		if normalized.PresentationID != nil {
			if err = canvas.BindPresentationOutputInTx(
				ctx,
				tx,
				principal,
				canvas.PresentationOutputInput{
					RunID:                 origin.RunID,
					PresentationID:        *normalized.PresentationID,
					OutputID:              output.ID,
					PrimaryAssetVersionID: primaryAsset,
				},
			); err != nil {
				return mapPresentationError(err)
			}
		}
		result = DeliverablePresentResult{OutputID: output.ID, Created: true}
		if normalized.PresentationID != nil {
			snapshots, err := tx.ListDeliverySnapshots(
				ctx,
				dbgen.ListDeliverySnapshotsParams{CompanyID: origin.CompanyID, OutputID: output.ID},
			)
			if err != nil {
				return err
			}
			result.ArtifactID = &snapshots[0].ArtifactID
			result.SnapshotID = &snapshots[0].ID
		}
		if normalized.PresentationID == nil {
			projectID := output.ProjectID
			if !output.TaskID.Valid {
				projectID = uuid.NullUUID{}
			}
			retained, err := artifact.RetainInTx(ctx, tx, artifact.RetainedInput{
				CompanyID:      origin.CompanyID,
				ActorUserID:    principal.UserID,
				ArtifactID:     normalized.ArtifactID,
				ProjectID:      projectID,
				OwnerUserID:    output.OwnerUserID,
				ConversationID: output.ConversationID,
				TaskID:         output.TaskID,
				SourceRunID:    output.SourceRunID,
				Type:           output.Type,
				Title:          output.Title,
				Summary:        output.Summary,
				Payload:        output.Payload,
				PrimaryAssetVersionID: uuid.NullUUID{
					UUID:  primaryAsset,
					Valid: primaryAsset != uuid.Nil,
				},
			})
			if errors.Is(err, artifact.ErrInvalid) {
				return ErrDeliverableInvalidInput
			}
			if err != nil {
				return err
			}
			if err = tx.RetainDeliverySnapshotItems(ctx, dbgen.RetainDeliverySnapshotItemsParams{
				CompanyID: origin.CompanyID, OutputID: output.ID, SnapshotID: retained.SnapshotID,
			}); err != nil {
				return err
			}
			if err = tx.LinkDeliverySnapshot(ctx, dbgen.LinkDeliverySnapshotParams{
				CompanyID:  origin.CompanyID,
				OutputID:   output.ID,
				SnapshotID: retained.SnapshotID,
				Ordinal:    0,
			}); err != nil {
				return err
			}
			result.ArtifactID = &retained.ArtifactID
			result.SnapshotID = &retained.SnapshotID
		}
		canvasEvent, err := canvas.RegisterOutputInTx(ctx, tx, canvas.OutputRegistrationInput{
			CompanyID: origin.CompanyID, OutputID: output.ID, ActorUserID: principal.UserID,
		})
		if canvasEvent != nil {
			pending = append(pending, *canvasEvent)
		}
		return err
	})
	if err != nil {
		return DeliverablePresentResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

type presentedAssetVersion struct {
	id           uuid.UUID
	contentType  string
	attachmentID uuid.UUID
}

func resolvePresentedAssetVersion(
	ctx context.Context,
	tx *dbgen.Queries,
	origin RunOrigin,
	userID uuid.UUID,
	item DeliverablePresentItemRequest,
) (presentedAssetVersion, error) {
	if item.AssetVersionID != nil {
		version, err := tx.GetReadyAssetVersionForOutputForShare(
			ctx,
			dbgen.GetReadyAssetVersionForOutputForShareParams{
				AssetVersionID: *item.AssetVersionID, CompanyID: origin.CompanyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) ||
			err == nil && (!version.SourceRunID.Valid || version.SourceRunID.UUID != origin.RunID) {
			return presentedAssetVersion{}, ErrDeliverableTargetUnavailable
		}
		if err != nil {
			return presentedAssetVersion{}, fmt.Errorf("load presented Asset version: %w", err)
		}
		return presentedAssetVersion{
			id:           version.ID,
			contentType:  version.ContentType,
			attachmentID: version.AttachmentID,
		}, nil
	}
	if item.AttachmentID == nil {
		return presentedAssetVersion{}, ErrDeliverableInvalidInput
	}
	versionID, contentType, err := promoteAttachmentInTx(
		ctx, tx, origin.CompanyID, userID, origin.RunID, item.Label, *item.AttachmentID,
	)
	return presentedAssetVersion{id: versionID, contentType: contentType, attachmentID: *item.AttachmentID}, err
}

func promoteAttachmentInTx(
	ctx context.Context,
	tx *dbgen.Queries,
	companyID, userID uuid.UUID,
	runID uuid.UUID,
	label string,
	attachmentID uuid.UUID,
) (uuid.UUID, string, error) {
	existing, err := tx.GetAssetVersionByAttachment(ctx, dbgen.GetAssetVersionByAttachmentParams{
		CompanyID: companyID, AttachmentID: attachmentID,
	})
	if err == nil {
		if (!existing.SourceRunID.Valid || existing.SourceRunID.UUID != runID) ||
			existing.IntegrityStatus != "verified" {
			return uuid.Nil, "", ErrDeliverableTargetUnavailable
		}
		return existing.ID, existing.ContentType, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", fmt.Errorf("load generated Asset version: %w", err)
	}
	attachment, err := tx.GetReadyAttachmentForOutputForShare(
		ctx,
		dbgen.GetReadyAttachmentForOutputForShareParams{
			AttachmentID: attachmentID, CompanyID: companyID, UserID: userID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ErrDeliverableTargetUnavailable
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("load presented Attachment: %w", err)
	}
	asset, err := tx.InsertGeneratedAsset(ctx, dbgen.InsertGeneratedAssetParams{
		CompanyID: companyID, Title: label, MediaClass: mediaClass(attachment.ContentType),
		CreatedByUserID: userID,
	})
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("insert generated Asset: %w", err)
	}
	version, err := tx.InsertGeneratedAssetVersion(ctx, dbgen.InsertGeneratedAssetVersionParams{
		CompanyID: companyID, AssetID: asset.ID, AttachmentID: attachmentID,
		SourceRunID: runID, Filename: attachment.Filename,
		ContentType: attachment.ContentType, SizeBytes: attachment.SizeBytes,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ErrDeliverableTargetUnavailable
	}
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("insert generated Asset version: %w", err)
	}
	return version.ID, version.ContentType, nil
}

func mediaClass(contentType string) string {
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case contentType == "application/pdf":
		return "document"
	case strings.HasPrefix(contentType, "text/"):
		return "text"
	default:
		return "file"
	}
}

func (s *DefaultDeliverableService) FetchRunResult(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
) (DeliverableRunResult, error) {
	if !validDeliverableService(s) || runID == uuid.Nil {
		return DeliverableRunResult{}, ErrDeliverableInvalidInput
	}
	return fetchDeliverableRunResult(ctx, s.store.Queries(), principal, runID, nil)
}

func (s *DefaultDeliverableService) FetchRunArtifactResult(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	outputID uuid.UUID,
) (DeliverableRunResult, error) {
	if !validDeliverableService(s) || runID == uuid.Nil || outputID == uuid.Nil {
		return DeliverableRunResult{}, ErrDeliverableInvalidInput
	}
	return fetchDeliverableRunResult(ctx, s.store.Queries(), principal, runID, &outputID)
}

func fetchDeliverableRunResult(
	ctx context.Context,
	queries *dbgen.Queries,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	selectedOutputID *uuid.UUID,
) (DeliverableRunResult, error) {
	row, err := queries.FetchRunDeliverable(ctx, dbgen.FetchRunDeliverableParams{
		UserID: principal.UserID, RunID: runID, CompanyID: principal.CompanyID,
		OutputID: util.NullUUIDFromPtr(selectedOutputID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliverableRunResult{}, ErrDeliverableNotFound
	}
	if err != nil {
		return DeliverableRunResult{}, fmt.Errorf("fetch Run result: %w", err)
	}
	result := DeliverableRunResult{
		RuntimeID: ViewerRuntimeID(row.RuntimeID, row.RuntimeOwnerUserID, principal.UserID),
		RunStatus: row.RunStatus,
	}
	if !row.OutputID.Valid {
		if selectedOutputID != nil {
			return DeliverableRunResult{}, ErrDeliverableNotFound
		}
		return result, nil
	}
	outputID := row.OutputID.UUID
	if err = canvas.PresentationOutputAccessInTx(ctx, queries, principal, outputID); err != nil {
		return DeliverableRunResult{}, mapPresentationError(err)
	}
	itemsByOutput, err := loadOutputItems(
		ctx, queries, principal.CompanyID, principal.UserID, []uuid.UUID{outputID},
	)
	if err != nil {
		return DeliverableRunResult{}, err
	}
	output := outputFromProjection(
		row.Title.String,
		row.Summary.String,
		util.TextToPtr(row.SubmissionNotes),
		util.TextToPtr(row.PreviewUrl),
		row.OutputCreatedAt.Time,
		itemsByOutput[outputID],
	)
	output.Type = ArtifactType(row.OutputType.String)
	output.ID = outputID
	result.Output = &output
	level, err := queries.GetOutputAccessForManage(ctx, dbgen.GetOutputAccessForManageParams{
		CompanyID: principal.CompanyID, OutputID: outputID, ActorUserID: principal.UserID,
	})
	if err != nil {
		return DeliverableRunResult{}, fmt.Errorf("resolve Artifact access: %w", err)
	}
	source, sourceErr := queries.GetOutputStudioCanvas(
		ctx,
		dbgen.GetOutputStudioCanvasParams{OutputID: outputID, CompanyID: principal.CompanyID},
	)
	if sourceErr == nil {
		saved := SavedStudioCanvas{AddedAt: source.CreatedAt.Time}
		if err := json.Unmarshal(source.Canvas, &saved.Canvas); err != nil {
			return DeliverableRunResult{}, fmt.Errorf("decode Studio source: %w", err)
		}
		result.StudioCanvas = &saved
	} else if !errors.Is(sourceErr, pgx.ErrNoRows) {
		return DeliverableRunResult{}, fmt.Errorf("read Studio source: %w", sourceErr)
	}
	result.CanAttachStudioCanvas = sourceErr != nil && level >= 3 &&
		auth.CompanyRole(row.CallerRole).Can(auth.PermissionResultsReview) &&
		(output.Type == ArtifactTypeMedia || output.Type == ArtifactTypeFile)
	for _, item := range output.Items {
		if item.ContentType == "application/vnd.zenith-studio.canvas+json" {
			result.CanAttachStudioCanvas = false
		}
	}
	if !row.DeliverableID.Valid {
		return result, nil
	}
	deliverable := deliverableFromProjection(
		row.DeliverableID.UUID, runID, row.DeliverableStatus.String, output,
		row.SubmittedAt.Time, util.TimestampToTime(row.ReviewedAt),
		util.TextToPtr(row.ReviewFeedback), row.DeliverableVersion.Int64,
	)
	result.Deliverable = &deliverable

	return result, nil
}

func (s *DefaultDeliverableService) ResolveArtifactFileAttachment(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	outputID uuid.UUID,
	ordinal int,
) (uuid.UUID, error) {
	if !validDeliverableService(s) || outputID == uuid.Nil ||
		ordinal < 0 || ordinal >= 32 {
		return uuid.Nil, ErrDeliverableNotFound
	}
	attachmentID, err := s.store.Queries().ResolveOutputFileAttachment(
		ctx,
		dbgen.ResolveOutputFileAttachmentParams{
			UserID: principal.UserID, CompanyID: principal.CompanyID,
			RunID: runID, OutputID: outputID, Ordinal: int32(ordinal),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrDeliverableNotFound
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve Output file: %w", err)
	}
	if err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{},
		func(q *dbgen.Queries) error { return canvas.PresentationOutputAccessInTx(ctx, q, principal, outputID) },
	); err != nil {
		return uuid.Nil, mapPresentationError(err)
	}
	return attachmentID, nil
}

func mapPresentationError(err error) error {
	switch {
	case errors.Is(err, canvas.ErrNotFound):
		return ErrDeliverableNotFound
	case errors.Is(err, canvas.ErrForbidden):
		return ErrDeliverableForbidden
	case errors.Is(err, canvas.ErrInvalid):
		return ErrDeliverableInvalidInput
	case errors.Is(err, canvas.ErrConflict):
		return ErrDeliverableConflict
	default:
		return err
	}
}

type outputContent struct {
	actorType       string
	actorUserID     *uuid.UUID
	actorAgentID    *uuid.UUID
	freshness       string
	liveURL         *string
	items           []dbgen.ListOutputItemsRow
	canvasURL       *string
	presentationURL *string
}

func loadOutputItems(
	ctx context.Context,
	queries *dbgen.Queries,
	companyID, actorUserID uuid.UUID,
	outputIDs []uuid.UUID,
) (map[uuid.UUID]outputContent, error) {
	itemsByOutput := make(map[uuid.UUID]outputContent, len(outputIDs))
	for _, outputID := range outputIDs {
		itemsByOutput[outputID] = outputContent{
			items:     []dbgen.ListOutputItemsRow{},
			freshness: "not_applicable",
		}
	}
	attribution, err := queries.ListDeliveryAttribution(
		ctx,
		dbgen.ListDeliveryAttributionParams{CompanyID: companyID, OutputIds: outputIDs},
	)
	if err != nil {
		return nil, err
	}
	for _, row := range attribution {
		content := itemsByOutput[row.ID]
		content.actorType, content.actorUserID, content.actorAgentID = row.ActorType, util.NullUUIDPtr(
			row.ActorUserID,
		), util.NullUUIDPtr(
			row.ActorAgentID,
		)
		itemsByOutput[row.ID] = content
	}
	rows, err := queries.ListOutputItems(ctx, dbgen.ListOutputItemsParams{
		CompanyID: companyID, OutputIds: outputIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list Output items: %w", err)
	}
	for _, row := range rows {
		content := itemsByOutput[row.OutputID]
		content.items = append(content.items, row)
		itemsByOutput[row.OutputID] = content
	}
	destinations, err := queries.ListOutputCanvasDestinations(ctx, dbgen.ListOutputCanvasDestinationsParams{
		CompanyID: companyID, OutputIds: outputIDs, ActorUserID: actorUserID,
	})
	if err != nil {
		return nil, fmt.Errorf("list Output canvas destinations: %w", err)
	}
	for _, destination := range destinations {
		content := itemsByOutput[destination.OutputID]
		url := fmt.Sprintf(
			"/projects/%s/canvas?artifactId=%s&versionId=%s",
			destination.ProjectID.UUID,
			destination.ArtifactID,
			destination.ID,
		)
		content.canvasURL = &url
		itemsByOutput[destination.OutputID] = content
	}
	presentations, err := queries.ListProjectPresentationDestinations(
		ctx,
		dbgen.ListProjectPresentationDestinationsParams{
			CompanyID:   companyID,
			OutputIds:   outputIDs,
			ActorUserID: actorUserID,
		},
	)
	if err != nil {
		return nil, err
	}
	for _, row := range presentations {
		content := itemsByOutput[row.OutputID]
		url := fmt.Sprintf("/projects/%s/pages?pageId=%s&presentationId=%s", row.ProjectID, row.PageID, row.ID)
		content.presentationURL = &url
		itemsByOutput[row.OutputID] = content
	}
	freshness, err := queries.ListOutputDocumentFreshness(
		ctx,
		dbgen.ListOutputDocumentFreshnessParams{
			CompanyID:   companyID,
			ActorUserID: actorUserID,
			OutputIds:   outputIDs,
		},
	)
	if err != nil {
		return nil, err
	}
	for _, row := range freshness {
		content := itemsByOutput[row.OutputID]
		if !row.CanReadLive {
			if content.freshness != "stale" {
				content.freshness = "unavailable"
			}
		} else {
			if row.Stale {
				content.freshness = "stale"
			} else if content.freshness == "not_applicable" {
				content.freshness = "current"
			}
			if row.ProjectID.Valid {
				url := fmt.Sprintf("/projects/%s/pages?pageId=%s", row.ProjectID.UUID, row.PageID)
				content.liveURL = &url
			} else if row.SourceOutputID.Valid {
				url := fmt.Sprintf(
					"/companies/%s/artifacts/%s?view=canvas",
					companyID,
					row.SourceOutputID.UUID,
				)
				content.liveURL = &url
			}
		}
		itemsByOutput[row.OutputID] = content
	}
	return itemsByOutput, nil
}

func outputFromProjection(
	title, summary string,
	notes *string,
	_ *string,
	createdAt time.Time,
	content outputContent,
) DeliverableOutput {
	rows := content.items
	items := make([]DeliverableOutputItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, DeliverableOutputItem{
			ID: row.OutputItemID, AssetVersionID: row.AssetVersionID,
			ContentType: row.ContentType, SizeBytes: row.SizeBytes,
			Ordinal: int(row.Ordinal), Role: DeliverableItemRole(row.Role), Label: row.Label,
			Kind: "file", DisplayName: row.Filename, Detail: formatDeliverableBytes(row.SizeBytes),
		})
	}
	return DeliverableOutput{
		Freshness: content.freshness, LiveURL: content.liveURL,
		ActorType: content.actorType, ActorUserID: content.actorUserID, ActorAgentID: content.actorAgentID,
		Title:           title,
		Summary:         summary,
		SubmissionNotes: notes,
		Items:           items,
		CreatedAt:       createdAt.UTC(),
		CanvasURL:       content.canvasURL,
		PresentationURL: content.presentationURL,
	}
}

func deliverableFromProjection(
	id uuid.UUID,
	runID uuid.UUID,
	status string,
	output DeliverableOutput,
	submittedAt time.Time,
	reviewedAt *time.Time,
	feedback *string,
	revision int64,
) Deliverable {
	return Deliverable{
		ID:             id,
		RunID:          util.NullUUIDPtr(uuid.NullUUID{UUID: runID, Valid: runID != uuid.Nil}),
		Status:         DeliverableStatus(status),
		Output:         output,
		SubmittedAt:    submittedAt.UTC(),
		ReviewedAt:     reviewedAt,
		ReviewFeedback: feedback,
		Revision:       revision,
	}
}

func formatDeliverableBytes(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/float64(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/float64(1<<10))
	default:
		return fmt.Sprintf("%d bytes", size)
	}
}

func validDeliverableService(service *DefaultDeliverableService) bool {
	return service != nil && service.store != nil && service.bus != nil
}

// lockActiveMember takes the canonical Company then acting-membership locks.
// Separate statements give a waiter a fresh READ COMMITTED membership snapshot
// after a concurrent company or membership mutation commits.
func lockActiveMember(
	ctx context.Context,
	queries *dbgen.Queries,
	companyID, userID uuid.UUID,
) (auth.CompanyRole, bool, error) {
	if _, err := queries.LockCompanyForMembershipMutation(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	} else if err != nil {
		return "", false, fmt.Errorf("lock Company for Output mutation: %w", err)
	}
	membership, err := queries.GetCompanyMemberForUpdate(
		ctx,
		dbgen.GetCompanyMemberForUpdateParams{CompanyID: companyID, UserID: userID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lock acting Company member for Output mutation: %w", err)
	}
	if membership.Status != "active" {
		return "", false, nil
	}
	return auth.CompanyRole(membership.Role), true, nil
}
