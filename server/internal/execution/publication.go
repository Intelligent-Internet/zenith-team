package execution

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	artifactdomain "github.com/Intelligent-Internet/zenith-team/server/internal/artifact"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const maximumPublicationSourceBytes = int64(100 << 20)

type PublicationStatus string

const (
	PublicationStatusQueued       PublicationStatus = "queued"
	PublicationStatusUploading    PublicationStatus = "uploading"
	PublicationStatusBuilding     PublicationStatus = "building"
	PublicationStatusDeploying    PublicationStatus = "deploying"
	PublicationStatusRouting      PublicationStatus = "routing"
	PublicationStatusPublished    PublicationStatus = "published"
	PublicationStatusUnpublishing PublicationStatus = "unpublishing"
	PublicationStatusUnpublished  PublicationStatus = "unpublished"
	PublicationStatusFailed       PublicationStatus = "failed"
)

type PublicationFailureCode string

const (
	PublicationFailureSourceInvalid       PublicationFailureCode = "source_invalid"
	PublicationFailureBuildFailed         PublicationFailureCode = "build_failed"
	PublicationFailureDeployFailed        PublicationFailureCode = "deploy_failed"
	PublicationFailureProviderUnavailable PublicationFailureCode = "provider_unavailable"
)

var (
	ErrPublicationInvalidConfiguration = errors.New("Publication service configuration is invalid")
	ErrPublicationInvalidInput         = errors.New("Publication input is invalid")
	ErrPublicationForbidden            = errors.New("Publication is forbidden")
	ErrPublicationNotFound             = errors.New("Publication target was not found")
	ErrPublicationConflict             = errors.New("Publication conflicts with current state")
	ErrPublicationNotPublishable       = errors.New("Output is not publishable")
	ErrPublicationApprovalRequired     = errors.New("Output approval is required before publication")
	ErrPublicationDisabled             = errors.New("Publication is not configured")
	ErrPublicationStoreUnavailable     = errors.New("Publication store is unavailable")
)

type PublicationCreateInput struct {
	ConfirmPublicExposure bool `json:"confirmPublicExposure"`
}

type PublicationUnpublishInput struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

type Publication struct {
	ID            uuid.UUID               `json:"id"`
	OutputID      uuid.UUID               `json:"outputId"`
	ProviderType  PublicationProviderType `json:"providerType"`
	Status        PublicationStatus       `json:"status"`
	URL           *string                 `json:"url,omitempty"`
	CustomURL     *string                 `json:"customUrl,omitempty"`
	Subdomain     *string                 `json:"subdomain,omitempty"`
	FailureCode   *PublicationFailureCode `json:"failureCode,omitempty"`
	FailureDetail *string                 `json:"failureDetail,omitempty"`
	PublishedAt   *time.Time              `json:"publishedAt,omitempty"`
	UnpublishedAt *time.Time              `json:"unpublishedAt,omitempty"`
	CreatedAt     time.Time               `json:"createdAt"`
	UpdatedAt     time.Time               `json:"updatedAt"`
	Revision      int64                   `json:"revision"`
}

type PublicationView struct {
	Publication  *Publication                 `json:"publication"`
	CanPublish   bool                         `json:"canPublish"`
	CanUnpublish bool                         `json:"canUnpublish"`
	Subdomain    *PublicationSubdomainOptions `json:"subdomain,omitempty"`
}

type PublicationCreateResult struct {
	Publication Publication `json:"publication"`
	Created     bool        `json:"created"`
}

// PublicationService is the complete exported publication surface.
type PublicationService interface {
	Fetch(context.Context, auth.UserPrincipal, uuid.UUID) (PublicationView, error)
	SetSubdomain(context.Context, auth.UserPrincipal, uuid.UUID, PublicationSubdomainInput) (Publication, error)
	Create(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		PublicationCreateInput,
	) (PublicationCreateResult, error)
	Unpublish(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		PublicationUnpublishInput,
	) (Publication, error)
}

var _ PublicationService = (*DefaultPublicationService)(nil)

type DefaultPublicationService struct {
	store       *db.Store
	bus         *events.Bus
	providers   PublicationProviderCatalog
	routes      PublicationRoutes
	allowPublic bool
}

type PublicationServiceOptions struct {
	Store       *db.Store
	Bus         *events.Bus
	Providers   PublicationProviderCatalog
	Routes      PublicationRoutes
	AllowPublic bool
}

func NewPublicationService(options PublicationServiceOptions) (*DefaultPublicationService, error) {
	if options.Store == nil || options.Bus == nil {
		return nil, ErrPublicationInvalidConfiguration
	}
	if options.Providers != nil {
		if _, ok := defaultPublicationProvider(options.Providers); !ok {
			return nil, ErrPublicationInvalidConfiguration
		}
	}
	return &DefaultPublicationService{
		store:       options.Store,
		bus:         options.Bus,
		providers:   options.Providers,
		routes:      options.Routes,
		allowPublic: options.AllowPublic,
	}, nil
}

func (s *DefaultPublicationService) Fetch(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
) (PublicationView, error) {
	if !validPublicationRequest(ctx, s, principal, runID) {
		return PublicationView{}, ErrPublicationInvalidInput
	}
	authority, err := s.publicationAuthority(ctx, principal, runID)
	if err != nil {
		return PublicationView{}, err
	}
	_, defaultAvailable := defaultPublicationProvider(s.providers)
	view := PublicationView{CanPublish: defaultAvailable && s.publicationPolicy(authority, principal) == nil}
	if s.routes != nil {
		view.Subdomain = &PublicationSubdomainOptions{BaseDomain: s.routes.BaseDomain()}
	}
	row, err := s.store.Queries().GetOutputPublicationByOutput(ctx, dbgen.GetOutputPublicationByOutputParams{
		CompanyID: principal.CompanyID, OutputID: authority.OutputID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return PublicationView{}, fmt.Errorf("read Output publication: %w", err)
	}
	publication := publicationFromRow(row)
	view.Publication = &publication
	_, rowProviderAvailable := resolvePublicationProvider(s.providers, publicationProviderKeyFromRow(row))
	_, retryProviderAvailable := publicationRetryProvider(s.providers, row)
	view.CanPublish = s.publicationPolicy(authority, principal) == nil && retryProviderAvailable &&
		publicationCanRetry(row)
	view.CanUnpublish = rowProviderAvailable && publicationCanUnpublish(row) &&
		auth.CompanyRole(authority.CallerRole).Can(auth.PermissionResultsPublish)
	if row.CustomHostname.Valid && !publicationRoutesAvailable(s.routes, row.RouteBindingID.String) {
		view.CanPublish, view.CanUnpublish = false, false
	}
	if view.Subdomain != nil {
		if row.CustomHostname.Valid {
			_, view.Subdomain.BaseDomain, _ = strings.Cut(row.CustomHostname.String, ".")
		}
		view.Subdomain.CanSet = rowProviderAvailable && s.publicationPolicy(authority, principal) == nil &&
			(!row.CustomHostname.Valid || publicationRoutesAvailable(s.routes, row.RouteBindingID.String)) &&
			publicationCanSetSubdomain(row)
	}
	return view, nil
}

func (s *DefaultPublicationService) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	input PublicationCreateInput,
) (PublicationCreateResult, error) {
	if !validPublicationRequest(ctx, s, principal, runID) || !input.ConfirmPublicExposure {
		return PublicationCreateResult{}, ErrPublicationInvalidInput
	}
	providerKey, providerAvailable := defaultPublicationProvider(s.providers)
	if !providerAvailable {
		return PublicationCreateResult{}, ErrPublicationDisabled
	}
	authority, err := s.publicationAuthority(ctx, principal, runID)
	if err != nil {
		return PublicationCreateResult{}, err
	}
	if err := s.publicationPolicy(authority, principal); err != nil {
		return PublicationCreateResult{}, err
	}

	result := PublicationCreateResult{}
	var changed events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
		if err := authorizePublicationOwnerInTx(ctx, q, principal); err != nil {
			return err
		}
		lockedAuthority, authorityErr := getPublicationAuthority(ctx, q, principal, runID)
		if authorityErr != nil {
			return authorityErr
		}
		if policyErr := s.publicationPolicy(lockedAuthority, principal); policyErr != nil {
			return policyErr
		}
		existing, existingErr := q.GetOutputPublicationByOutputForUpdate(
			ctx,
			dbgen.GetOutputPublicationByOutputForUpdateParams{
				CompanyID: principal.CompanyID, OutputID: lockedAuthority.OutputID,
			},
		)
		if existingErr == nil {
			if existing.OutputItemID != lockedAuthority.OutputItemID {
				return ErrPublicationConflict
			}
			if publicationCanRetry(existing) {
				retryProvider, available := publicationRetryProvider(s.providers, existing)
				if !available {
					return ErrPublicationDisabled
				}
				if existing.CustomHostname.Valid &&
					!publicationRoutesAvailable(s.routes, existing.RouteBindingID.String) {
					return ErrPublicationSubdomainDisabled
				}
				existing, existingErr = q.RequeueOutputPublication(
					ctx,
					dbgen.RequeueOutputPublicationParams{
						RequestedByUserID: principal.UserID,
						ProviderType:      string(retryProvider.Type),
						ProviderBindingID: retryProvider.BindingID,
						PublicationID:     existing.ID,
						CompanyID:         principal.CompanyID,
						OutputID:          lockedAuthority.OutputID,
						OutputItemID:      lockedAuthority.OutputItemID,
					},
				)
				if existingErr != nil {
					return fmt.Errorf("requeue Output publication: %w", existingErr)
				}
				result.Created = true
				changed, existingErr = publicationChangedInTx(ctx, q, existing)
				if existingErr != nil {
					return existingErr
				}
			}
			result.Publication = publicationFromRow(existing)
			return nil
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return fmt.Errorf("lock Output publication: %w", existingErr)
		}
		row, insertErr := q.InsertOutputPublication(ctx, dbgen.InsertOutputPublicationParams{
			CompanyID: principal.CompanyID, OutputID: lockedAuthority.OutputID,
			OutputItemID: lockedAuthority.OutputItemID, RequestedByUserID: principal.UserID,
			ServiceName:  publicationServiceName(lockedAuthority.OutputID),
			ProviderType: string(providerKey.Type), ProviderBindingID: providerKey.BindingID,
		})
		if errors.Is(insertErr, pgx.ErrNoRows) {
			row, insertErr = q.GetOutputPublicationByOutputForUpdate(
				ctx,
				dbgen.GetOutputPublicationByOutputForUpdateParams{
					CompanyID: principal.CompanyID, OutputID: lockedAuthority.OutputID,
				},
			)
			if insertErr == nil {
				result = PublicationCreateResult{Publication: publicationFromRow(row)}
				return nil
			}
		}
		if insertErr != nil {
			return fmt.Errorf("insert Output publication: %w", insertErr)
		}
		result = PublicationCreateResult{Publication: publicationFromRow(row), Created: true}
		changed, insertErr = publicationChangedInTx(ctx, q, row)
		return insertErr
	})
	if err != nil {
		return PublicationCreateResult{}, err
	}
	if result.Created {
		s.bus.Publish(ctx, artifactdomain.Topic, changed)
	}
	return result, nil
}

func (s *DefaultPublicationService) Unpublish(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	input PublicationUnpublishInput,
) (Publication, error) {
	if !validPublicationRequest(ctx, s, principal, runID) || input.ExpectedRevision < 1 ||
		input.ExpectedRevision > protocol.MaxJSONSafeInteger {
		return Publication{}, ErrPublicationInvalidInput
	}
	if s.providers == nil {
		return Publication{}, ErrPublicationDisabled
	}
	var result Publication
	var changed events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *dbgen.Queries) error {
		if err := authorizePublicationOwnerInTx(ctx, q, principal); err != nil {
			return err
		}
		authority, authorityErr := getPublicationAuthority(ctx, q, principal, runID)
		if authorityErr != nil {
			return authorityErr
		}
		row, rowErr := q.GetOutputPublicationByOutputForUpdate(
			ctx,
			dbgen.GetOutputPublicationByOutputForUpdateParams{
				CompanyID: principal.CompanyID, OutputID: authority.OutputID,
			},
		)
		if errors.Is(rowErr, pgx.ErrNoRows) {
			return ErrPublicationNotFound
		}
		if rowErr != nil {
			return fmt.Errorf("lock Output publication: %w", rowErr)
		}
		if row.Version != input.ExpectedRevision || !publicationCanUnpublish(row) {
			return ErrPublicationConflict
		}
		if _, ok := resolvePublicationProvider(s.providers, publicationProviderKeyFromRow(row)); !ok {
			return ErrPublicationDisabled
		}
		if row.CustomHostname.Valid && !publicationRoutesAvailable(s.routes, row.RouteBindingID.String) {
			return ErrPublicationSubdomainDisabled
		}
		row, rowErr = q.MarkOutputPublicationUnpublishing(ctx, dbgen.MarkOutputPublicationUnpublishingParams{
			RequestedByUserID: principal.UserID, PublicationID: row.ID, CompanyID: principal.CompanyID,
			OutputID: authority.OutputID, ExpectedVersion: input.ExpectedRevision,
		})
		if rowErr != nil {
			return fmt.Errorf("mark Output publication unpublishing: %w", rowErr)
		}
		result = publicationFromRow(row)
		changed, rowErr = publicationChangedInTx(ctx, q, row)
		return rowErr
	})
	if err != nil {
		return Publication{}, err
	}
	s.bus.Publish(ctx, artifactdomain.Topic, changed)
	return result, nil
}

func (s *DefaultPublicationService) publicationAuthority(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
) (dbgen.GetPublicationAuthorityRow, error) {
	return getPublicationAuthority(ctx, s.store.Queries(), principal, runID)
}

func getPublicationAuthority(
	ctx context.Context,
	q *dbgen.Queries,
	principal auth.UserPrincipal,
	runID uuid.UUID,
) (dbgen.GetPublicationAuthorityRow, error) {
	row, err := q.GetPublicationAuthority(ctx, dbgen.GetPublicationAuthorityParams{
		UserID: principal.UserID, RunID: runID, CompanyID: principal.CompanyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.GetPublicationAuthorityRow{}, ErrPublicationNotFound
	}
	if err != nil {
		return dbgen.GetPublicationAuthorityRow{}, fmt.Errorf("read publication authority: %w", err)
	}
	return row, nil
}

func (s *DefaultPublicationService) publicationPolicy(
	authority dbgen.GetPublicationAuthorityRow,
	principal auth.UserPrincipal,
) error {
	if !s.allowPublic {
		return ErrPublicationDisabled
	}
	return publicationPolicy(authority, principal)
}

func publicationPolicy(authority dbgen.GetPublicationAuthorityRow, principal auth.UserPrincipal) error {
	if authority.CallerAccessLevel < 3 ||
		!auth.CompanyRole(authority.CallerRole).Can(auth.PermissionResultsPublish) {
		return ErrPublicationForbidden
	}
	webApp := authority.OutputType == string(ArtifactTypeApp)
	if (!webApp && authority.RunStatus != "completed") || authority.AttachmentStatus != "ready" ||
		!util.ValidUUID(authority.OutputID) || !util.ValidUUID(authority.OutputItemID) ||
		authority.ObjectKey == "" || authority.SizeBytes < 1 ||
		authority.SizeBytes > maximumPublicationSourceBytes ||
		!publicationZIP(authority.Filename, authority.ContentType) {
		return ErrPublicationNotPublishable
	}
	if authority.TaskID.Valid {
		if authority.ConversationKind != "task" || (!webApp && (!authority.DeliverableStatus.Valid ||
			authority.DeliverableStatus.String != string(DeliverableStatusApproved))) {
			return ErrPublicationApprovalRequired
		}
		return nil
	}
	if (authority.ConversationKind != "direct" && authority.ConversationKind != "schedule") ||
		!authority.ConversationCreatedByUserID.Valid ||
		authority.ConversationCreatedByUserID.UUID != principal.UserID {
		return ErrPublicationForbidden
	}
	return nil
}

func authorizePublicationOwnerInTx(ctx context.Context, q *dbgen.Queries, principal auth.UserPrincipal) error {
	role, active, err := lockActiveMember(ctx, q, principal.CompanyID, principal.UserID)
	if err != nil {
		return err
	}
	if !active || !role.Can(auth.PermissionResultsPublish) {
		return ErrPublicationForbidden
	}
	return nil
}

func publicationFromRow(row dbgen.OutputPublication) Publication {
	var subdomain, customURL *string
	if row.CustomHostname.Valid {
		name, _, _ := strings.Cut(row.CustomHostname.String, ".")
		subdomain = &name
		if row.Status == string(PublicationStatusPublished) && !row.RoutePending {
			url := "https://" + row.CustomHostname.String
			customURL = &url
		}
	}
	var failureCode *PublicationFailureCode
	if row.FailureCode.Valid {
		code := PublicationFailureCode(row.FailureCode.String)
		failureCode = &code
	}
	var publicURL *string
	if row.Status == string(PublicationStatusPublished) {
		publicURL = util.TextToPtr(row.PublicUrl)
	}
	return Publication{
		ID:            row.ID,
		OutputID:      row.OutputID,
		ProviderType:  PublicationProviderType(row.ProviderType),
		Status:        PublicationStatus(row.Status),
		URL:           publicURL,
		CustomURL:     customURL,
		Subdomain:     subdomain,
		FailureCode:   failureCode,
		FailureDetail: util.TextToPtr(row.FailureDetail),
		PublishedAt:   util.TimestampToTime(row.PublishedAt),
		UnpublishedAt: util.TimestampToTime(row.UnpublishedAt),
		CreatedAt:     row.CreatedAt.Time.UTC(),
		UpdatedAt:     row.UpdatedAt.Time.UTC(),
		Revision:      row.Version,
	}
}

func publicationCanRetry(row dbgen.OutputPublication) bool {
	if row.Status == string(PublicationStatusUnpublished) {
		return !row.ProviderResourcesMayExist
	}
	if row.Status != string(PublicationStatusFailed) || !row.FailureCode.Valid {
		return false
	}
	switch PublicationFailureCode(row.FailureCode.String) {
	case PublicationFailureProviderUnavailable, PublicationFailureDeployFailed:
		return true
	default:
		return false
	}
}

func publicationRetryProvider(
	catalog PublicationProviderCatalog,
	row dbgen.OutputPublication,
) (PublicationProviderKey, bool) {
	if row.ProviderResourcesMayExist {
		key := publicationProviderKeyFromRow(row)
		_, available := resolvePublicationProvider(catalog, key)
		return key, available
	}
	return defaultPublicationProvider(catalog)
}

func publicationCanUnpublish(row dbgen.OutputPublication) bool {
	return row.ProviderResourcesMayExist && (row.Status == string(PublicationStatusPublished) ||
		row.Status == string(PublicationStatusFailed))
}

func publicationProviderKeyFromRow(row dbgen.OutputPublication) PublicationProviderKey {
	return PublicationProviderKey{Type: PublicationProviderType(row.ProviderType), BindingID: row.ProviderBindingID}
}

func normalizePublicationCreateInput(input PublicationCreateInput) (PublicationCreateInput, error) {
	if !input.ConfirmPublicExposure {
		return PublicationCreateInput{}, ErrPublicationInvalidInput
	}
	return input, nil
}

func publicationFailureDetail(code PublicationFailureCode) string {
	switch code {
	case PublicationFailureSourceInvalid:
		return "The ZIP is not a supported application source archive."
	case PublicationFailureBuildFailed:
		return "The application source could not be built."
	case PublicationFailureDeployFailed:
		return "The hosting provider could not start this application."
	case PublicationFailureProviderUnavailable:
		return "The hosting provider is temporarily unavailable. Try publishing again."
	default:
		return ""
	}
}

func validPublicationRequest(
	ctx context.Context,
	service *DefaultPublicationService,
	_ auth.UserPrincipal,
	runID uuid.UUID,
) bool {
	return ctx != nil && service != nil && service.store != nil &&
		runID != uuid.Nil
}

func publicationZIP(filename, contentType string) bool {
	return strings.EqualFold(filepath.Ext(strings.TrimSpace(filename)), ".zip") &&
		(strings.EqualFold(strings.TrimSpace(contentType), "application/zip") ||
			strings.EqualFold(strings.TrimSpace(contentType), "application/x-zip-compressed"))
}

func publicationServiceName(outputID uuid.UUID) string {
	return "zenith-team-" + strings.ReplaceAll(outputID.String(), "-", "")
}
