package commerce

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

var (
	ErrManifestNotFound       = errors.New("Commerce manifest not found")
	ErrManifestConflict       = errors.New("Commerce manifest conflicts with existing state")
	ErrManifestNotApprovable  = errors.New("Commerce manifest is not approvable")
	ErrApprovalExpired        = errors.New("recent owner authentication is required")
	ErrCatalogDisabled        = errors.New("Commerce catalog and Checkout are disabled")
	ErrSiteSigningKeyRequired = errors.New("Commerce Site signing key is required")
	ErrSiteAuthority          = errors.New("Commerce Site authority verification failed")
	ErrSiteNotFound           = errors.New("Commerce Site not found")
	ErrGatewayBusy            = errors.New("Commerce operation is already in progress")
	ErrGatewayRecovery        = errors.New("Commerce operation requires provider reconciliation")
)

type SubmitManifestInput struct {
	Environment Environment
	RawManifest []byte
}

type TrustedManifestContext struct {
	CompanyID      uuid.UUID
	ConversationID uuid.UUID
	TaskID         uuid.UUID
	AgentID        uuid.UUID
	RunID          uuid.UUID
	RequestKey     uuid.UUID
}

type ManifestSubmission struct {
	ManifestID           uuid.UUID       `json:"manifestId"`
	ApprovalID           uuid.UUID       `json:"approvalId"`
	SiteID               uuid.UUID       `json:"siteId"`
	Digest               string          `json:"digest"`
	State                string          `json:"state"`
	VerificationPath     string          `json:"verificationPath"`
	VerificationDocument json.RawMessage `json:"verificationDocument"`
	CreatedAt            time.Time       `json:"createdAt"`
}

type ManifestStatus struct {
	ManifestID     uuid.UUID  `json:"manifestId"`
	SiteID         uuid.UUID  `json:"siteId"`
	TaskID         uuid.UUID  `json:"taskId"`
	RunID          uuid.UUID  `json:"runId"`
	Digest         string     `json:"digest"`
	State          string     `json:"state"`
	ApplyErrorCode *string    `json:"applyErrorCode,omitempty"`
	ApprovedAt     *time.Time `json:"approvedAt,omitempty"`
	AppliedAt      *time.Time `json:"appliedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// ManifestReview is the provider-free owner review projection. It exposes the
// exact canonical payment proposal covered by Digest, but no Stripe object or
// credential.
type ManifestReview struct {
	ManifestStatus
	Environment Environment         `json:"environment"`
	Manifest    Manifest            `json:"manifest"`
	SigningKeys []SigningKeySummary `json:"signingKeys"`
}

type ManifestCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type ManifestListOptions struct {
	Limit  int32
	Before *ManifestCursor
}

type ManifestPage struct {
	Items []ManifestReview
	Next  *ManifestCursor
}

type ManifestStatusInput struct {
	TaskID         uuid.UUID
	ConversationID uuid.UUID
	ManifestID     uuid.UUID
}

func (s *DefaultCommerceService) SubmitManifest(
	ctx context.Context,
	principal auth.UserPrincipal,
	trusted TrustedManifestContext,
	input SubmitManifestInput,
) (ManifestSubmission, error) {
	if err := validateMemberPrincipal(principal); err != nil {
		return ManifestSubmission{}, err
	}
	if !validTrustedManifestContext(trusted) || principal.CompanyID != trusted.CompanyID ||
		!input.Environment.valid() {
		return ManifestSubmission{}, ErrInvalidInput
	}
	document, err := ParseManifest(input.RawManifest, input.Environment)
	if err != nil {
		return ManifestSubmission{}, ErrInvalidInput
	}
	configuration := s.environments[input.Environment]
	if !configuration.Configured || configuration.Gateway == nil {
		return ManifestSubmission{}, ErrProviderUnavailable
	}
	var manifest generated.CommerceManifest
	var approvalID uuid.UUID
	var pending []events.Event
	var inboxResults []inbox.CreateResult
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		pending = pending[:0]
		inboxResults = inboxResults[:0]
		if _, lockErr := lockMember(ctx, queries, principal); lockErr != nil {
			return lockErr
		}
		if _, lockErr := queries.LockCommerceManifestConversation(
			ctx,
			generated.LockCommerceManifestConversationParams{
				CompanyID: trusted.CompanyID, ConversationID: trusted.ConversationID,
			},
		); lockErr != nil {
			if errors.Is(lockErr, pgx.ErrNoRows) {
				return ErrAccessForbidden
			}
			return fmt.Errorf("lock Commerce manifest Conversation: %w", lockErr)
		}
		run, lockErr := queries.LockCommerceManifestRun(
			ctx,
			generated.LockCommerceManifestRunParams{
				CompanyID: trusted.CompanyID, RunID: trusted.RunID, TaskID: trusted.TaskID,
				ConversationID: trusted.ConversationID, AgentID: trusted.AgentID,
			},
		)
		if lockErr != nil {
			if errors.Is(lockErr, pgx.ErrNoRows) {
				return ErrAccessForbidden
			}
			return fmt.Errorf("lock Commerce manifest Run: %w", lockErr)
		}
		task, lockErr := queries.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{
				CompanyID: trusted.CompanyID, TaskID: trusted.TaskID,
			},
		)
		if lockErr != nil {
			if errors.Is(lockErr, pgx.ErrNoRows) {
				return ErrAccessForbidden
			}
			return fmt.Errorf("lock Commerce manifest Task: %w", lockErr)
		}
		if run.Status != "running" || !run.TaskID.Valid || run.TaskID.UUID != trusted.TaskID ||
			!run.TaskExecutionGeneration.Valid ||
			run.TaskExecutionGeneration.Int64 != task.ExecutionGeneration ||
			task.Status != "in_progress" || task.ConversationID != trusted.ConversationID {
			return ErrAccessForbidden
		}

		existing, replayErr := queries.GetCommerceManifestByRunRequestKey(
			ctx,
			generated.GetCommerceManifestByRunRequestKeyParams{
				CompanyID: trusted.CompanyID, RunID: trusted.RunID,
				RequestKey: trusted.RequestKey,
			},
		)
		if replayErr == nil {
			if existing.Environment != string(input.Environment) ||
				existing.CommerceManifest.TaskID != trusted.TaskID ||
				subtle.ConstantTimeCompare(existing.CommerceManifest.Digest, document.Digest[:]) != 1 ||
				!canonicalJSONEqual(existing.CommerceManifest.CanonicalJson, document.CanonicalJSON) {
				return ErrManifestConflict
			}
			manifest = existing.CommerceManifest
			requested, createdInboxResults, _, requestErr := approval.RequestCommerceManifestPublishInTx(
				ctx,
				queries,
				&pending,
				commerceManifestApprovalRequest(
					principal,
					trusted,
					manifest.ID,
					document,
					input.Environment,
					s.now(),
				),
			)
			if requestErr != nil {
				return commerceManifestApprovalRequestError(
					"request Commerce manifest approval replay",
					requestErr,
				)
			}
			inboxResults = append(inboxResults, createdInboxResults...)
			approvalID = requested.ID
			return nil
		}
		if !errors.Is(replayErr, pgx.ErrNoRows) {
			return fmt.Errorf("get Commerce manifest replay: %w", replayErr)
		}
		account, err := queries.GetCommerceAccountByCompanyEnvironment(
			ctx,
			generated.GetCommerceAccountByCompanyEnvironmentParams{
				CompanyID: principal.CompanyID, Environment: string(input.Environment),
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProviderSetupRequired
		}
		if err != nil {
			return fmt.Errorf("get Commerce account for manifest: %w", err)
		}
		if err := validateReadyCommerceAccount(account, s.allowedCountries); err != nil {
			return err
		}
		accepted, err := queries.GetCommerceTermsAcceptance(
			ctx,
			generated.GetCommerceTermsAcceptanceParams{
				CompanyID: principal.CompanyID, TermsVersion: s.terms.Version,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTermsOutdated
		}
		if err != nil {
			return fmt.Errorf("get Commerce terms for manifest: %w", err)
		}
		if !equalDigest(accepted.TermsSha256, s.terms.SHA256) {
			return ErrTermsConflict
		}

		site, err := queries.InsertCommerceDraftSite(ctx, generated.InsertCommerceDraftSiteParams{
			CompanyID: principal.CompanyID, CommerceAccountID: account.ID,
			Environment: string(input.Environment), SiteAlias: document.Manifest.Site.Alias,
			Origin:                  document.Manifest.Site.Origin,
			VerificationTokenSha256: document.Digest[:],
			PortalReturnPath:        document.Manifest.Site.PortalReturnPath,
			CreatedByUserID:         principal.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			site, err = queries.GetCommerceSiteByAlias(ctx, generated.GetCommerceSiteByAliasParams{
				CompanyID: principal.CompanyID, Environment: string(input.Environment),
				SiteAlias: document.Manifest.Site.Alias,
			})
		}
		if err != nil {
			return fmt.Errorf("insert Commerce draft Site: %w", err)
		}
		if !commerceSiteMatchesManifestBinding(
			site,
			principal.CompanyID,
			account.ID,
			input.Environment,
			document.Manifest.Site,
		) {
			return ErrManifestConflict
		}
		manifest, err = queries.InsertCommerceManifest(ctx, generated.InsertCommerceManifestParams{
			CompanyID: principal.CompanyID, SiteID: site.ID,
			TaskID: trusted.TaskID, RunID: trusted.RunID, RequestKey: trusted.RequestKey,
			CanonicalJson: document.CanonicalJSON, Digest: document.Digest[:],
		})
		if errors.Is(err, pgx.ErrNoRows) {
			manifest, err = queries.GetCommerceManifestBySiteRunDigest(
				ctx,
				generated.GetCommerceManifestBySiteRunDigestParams{
					CompanyID: principal.CompanyID, SiteID: site.ID, RunID: trusted.RunID,
					Digest: document.Digest[:],
				},
			)
		}
		if err != nil {
			return fmt.Errorf("insert Commerce manifest: %w", err)
		}
		if manifest.TaskID != trusted.TaskID || manifest.RunID != trusted.RunID ||
			manifest.RequestKey != trusted.RequestKey ||
			subtle.ConstantTimeCompare(manifest.Digest, document.Digest[:]) != 1 ||
			!canonicalJSONEqual(manifest.CanonicalJson, document.CanonicalJSON) {
			return ErrManifestConflict
		}
		requested, createdInboxResults, _, err := approval.RequestCommerceManifestPublishInTx(
			ctx,
			queries,
			&pending,
			commerceManifestApprovalRequest(
				principal,
				trusted,
				manifest.ID,
				document,
				input.Environment,
				s.now(),
			),
		)
		if err != nil {
			return commerceManifestApprovalRequestError(
				"request Commerce manifest approval",
				err,
			)
		}
		inboxResults = append(inboxResults, createdInboxResults...)
		approvalID = requested.ID
		return nil
	})
	if err != nil {
		return ManifestSubmission{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, inboxResult := range inboxResults {
		inbox.PresentCreated(s.inboxPresenter, inboxResult)
	}
	result := ManifestSubmission{
		ManifestID: manifest.ID, ApprovalID: approvalID, SiteID: manifest.SiteID,
		Digest: hex.EncodeToString(manifest.Digest), State: manifest.State,
		CreatedAt: manifest.CreatedAt.Time.UTC(),
	}
	verification, verificationErr := verificationDocument(document)
	if verificationErr != nil {
		return ManifestSubmission{}, verificationErr
	}
	result.VerificationPath = "/.well-known/boardly-site-verification.json"
	result.VerificationDocument = verification
	return result, nil
}

func commerceManifestApprovalRequestError(operation string, err error) error {
	switch {
	case errors.Is(err, approval.ErrApprovalConflict):
		return errors.Join(ErrManifestConflict, err)
	case errors.Is(err, approval.ErrApprovalInvalidInput):
		return errors.Join(ErrInvalidStore, err)
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

func commerceManifestApprovalRequest(
	principal auth.UserPrincipal,
	trusted TrustedManifestContext,
	manifestID uuid.UUID,
	document ManifestDocument,
	environment Environment,
	now time.Time,
) approval.CommerceManifestPublishRequest {
	digest := hex.EncodeToString(document.Digest[:])
	var preview strings.Builder
	preview.WriteString("**Environment:** `")
	preview.WriteString(markdownInlineCode(string(environment)))
	preview.WriteString("`\n\n**Site:** `")
	preview.WriteString(markdownInlineCode(document.Manifest.Site.Alias))
	preview.WriteString("` · `")
	preview.WriteString(markdownInlineCode(document.Manifest.Site.Origin))
	preview.WriteString("`\n\n**Manifest digest:** `")
	preview.WriteString(digest)
	preview.WriteString("`\n\n**Offers**\n")
	for _, offer := range document.Manifest.Offers {
		preview.WriteString("\n- **")
		preview.WriteString(markdownText(offer.DisplayName))
		preview.WriteString("** · `")
		preview.WriteString(markdownInlineCode(offer.Alias))
		preview.WriteString("` · ")
		_, _ = fmt.Fprintf(&preview, "%d %s minor units", offer.Amount, offer.Currency)
	}
	return approval.CommerceManifestPublishRequest{
		CompanyID:      trusted.CompanyID,
		ManifestID:     manifestID,
		TaskID:         trusted.TaskID,
		ConversationID: trusted.ConversationID,
		AgentID:        trusted.AgentID,
		ActorUserID:    principal.UserID,
		OriginRunID:    trusted.RunID,
		RequestKey:     trusted.RequestKey,
		Digest:         digest,
		Summary: fmt.Sprintf(
			"Review %d %s payment offer(s) for %s.",
			len(document.Manifest.Offers),
			environment,
			document.Manifest.Site.Alias,
		),
		PreviewMarkdown: preview.String(),
		Now:             now,
	}
}

func markdownInlineCode(value string) string {
	return strings.ReplaceAll(value, "`", "\\`")
}

func markdownText(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
		"`", "\\`",
	)
	return replacer.Replace(value)
}

func commerceSiteMatchesManifestBinding(
	site generated.CommerceSite,
	companyID uuid.UUID,
	accountID uuid.UUID,
	environment Environment,
	manifestSite ManifestSite,
) bool {
	if site.CompanyID != companyID || site.CommerceAccountID != accountID ||
		site.Environment != string(environment) || site.SiteAlias != manifestSite.Alias ||
		site.Origin != manifestSite.Origin ||
		site.PortalReturnPath != manifestSite.PortalReturnPath {
		return false
	}
	return true
}

func (s *DefaultCommerceService) ManifestStatus(
	ctx context.Context,
	principal auth.UserPrincipal,
	input ManifestStatusInput,
) (ManifestStatus, error) {
	if err := validateMemberPrincipal(principal); err != nil {
		return ManifestStatus{}, err
	}
	if input.TaskID == uuid.Nil || input.ConversationID == uuid.Nil || input.ManifestID == uuid.Nil {
		return ManifestStatus{}, ErrInvalidInput
	}
	if err := authorizeMemberRead(ctx, s.store.Queries(), principal); err != nil {
		return ManifestStatus{}, err
	}
	row, err := s.store.Queries().GetCommerceManifestByTask(
		ctx,
		generated.GetCommerceManifestByTaskParams{
			CompanyID: principal.CompanyID, TaskID: input.TaskID,
			ConversationID: input.ConversationID, ManifestID: input.ManifestID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ManifestStatus{}, ErrManifestNotFound
	}
	if err != nil {
		return ManifestStatus{}, fmt.Errorf("get Commerce manifest status: %w", err)
	}
	if err := authorizeMemberRead(ctx, s.store.Queries(), principal); err != nil {
		return ManifestStatus{}, err
	}
	return manifestStatusFromRow(row.CommerceManifest), nil
}

func (s *DefaultCommerceService) ListManifests(
	ctx context.Context,
	principal auth.UserPrincipal,
	options ManifestListOptions,
) (ManifestPage, error) {
	if err := validatePrincipalPermission(principal, auth.PermissionCommerceRead); err != nil {
		return ManifestPage{}, err
	}
	limit := options.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || options.Before != nil &&
		(options.Before.CreatedAt.IsZero() || options.Before.ID == uuid.Nil) {
		return ManifestPage{}, ErrInvalidInput
	}
	if err := authorizeRead(ctx, s.store.Queries(), principal, auth.PermissionCommerceRead); err != nil {
		return ManifestPage{}, err
	}
	params := generated.ListCommerceManifestsForOwnerParams{
		CompanyID: principal.CompanyID,
		PageSize:  limit + 1,
	}
	if options.Before != nil {
		params.HasCursor = true
		params.BeforeCreatedAt = pgtype.Timestamptz{
			Time:  options.Before.CreatedAt.UTC(),
			Valid: true,
		}
		params.BeforeID = options.Before.ID
	}
	rows, err := s.store.Queries().ListCommerceManifestsForOwner(ctx, params)
	if err != nil {
		return ManifestPage{}, fmt.Errorf("list Commerce manifests: %w", err)
	}
	hasNext := len(rows) > int(limit)
	if hasNext {
		rows = rows[:limit]
	}
	result := make([]ManifestReview, 0, len(rows))
	for _, row := range rows {
		document, parseErr := ParseManifest(
			row.CommerceManifest.CanonicalJson,
			Environment(row.Environment),
		)
		if parseErr != nil || subtle.ConstantTimeCompare(
			document.Digest[:],
			row.CommerceManifest.Digest,
		) != 1 {
			return ManifestPage{}, ErrInvalidStore
		}
		keys, keyErr := s.store.Queries().ListCommerceSiteSigningKeys(
			ctx,
			generated.ListCommerceSiteSigningKeysParams{
				CompanyID: principal.CompanyID,
				SiteID:    row.CommerceManifest.SiteID,
			},
		)
		if keyErr != nil {
			return ManifestPage{}, fmt.Errorf("list Commerce Site signing keys: %w", keyErr)
		}
		result = append(result, ManifestReview{
			ManifestStatus: manifestStatusFromRow(row.CommerceManifest),
			Environment:    Environment(row.Environment),
			Manifest:       document.Manifest,
			SigningKeys:    signingKeySummaries(keys),
		})
	}
	if err := authorizeRead(ctx, s.store.Queries(), principal, auth.PermissionCommerceRead); err != nil {
		return ManifestPage{}, err
	}
	page := ManifestPage{Items: result}
	if hasNext {
		last := rows[len(rows)-1].CommerceManifest
		page.Next = &ManifestCursor{CreatedAt: last.CreatedAt.Time.UTC(), ID: last.ID}
	}
	return page, nil
}

func validTrustedManifestContext(trusted TrustedManifestContext) bool {
	return trusted.CompanyID != uuid.Nil && trusted.ConversationID != uuid.Nil &&
		trusted.TaskID != uuid.Nil && trusted.AgentID != uuid.Nil && trusted.RunID != uuid.Nil &&
		trusted.RequestKey != uuid.Nil
}

func validateMemberPrincipal(principal auth.UserPrincipal) error {
	if principal.UserID == uuid.Nil || principal.SessionID == uuid.Nil ||
		principal.CompanyID == uuid.Nil ||
		(principal.AuthKind != auth.TokenKindSession && principal.AuthKind != auth.TokenKindBearer) {
		return ErrUnauthenticated
	}
	if !principal.Role.Valid() {
		return ErrAccessForbidden
	}
	return nil
}

func validateReadyCommerceAccount(
	account generated.CommerceAccount,
	allowedCountries map[string]struct{},
) error {
	if account.Status != string(StatusReady) || !account.ChargesEnabled ||
		!account.ProviderAccountID.Valid || !providerIDPattern.MatchString(account.ProviderAccountID.String) ||
		!account.Country.Valid {
		return ErrProviderSetupRequired
	}
	if _, allowed := allowedCountries[account.Country.String]; !allowed {
		return ErrProviderSetupRequired
	}
	return nil
}

func verificationDocument(document ManifestDocument) (json.RawMessage, error) {
	raw, err := json.Marshal(struct {
		SchemaVersion  int    `json:"schemaVersion"`
		SiteAlias      string `json:"siteAlias"`
		ManifestDigest string `json:"manifestDigest"`
	}{
		SchemaVersion:  manifestSchemaVersion,
		SiteAlias:      document.Manifest.Site.Alias,
		ManifestDigest: hex.EncodeToString(document.Digest[:]),
	})
	if err != nil {
		return nil, fmt.Errorf("encode Commerce Site verification document: %w", err)
	}
	return raw, nil
}

func canonicalJSONEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func manifestStatusFromRow(row generated.CommerceManifest) ManifestStatus {
	status := ManifestStatus{
		ManifestID: row.ID, SiteID: row.SiteID, TaskID: row.TaskID, RunID: row.RunID,
		Digest: hex.EncodeToString(row.Digest), State: row.State,
		ApprovedAt: manifestUTCTimePointer(row.ApprovedAt),
		AppliedAt:  manifestUTCTimePointer(row.AppliedAt),
		CreatedAt:  row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(),
	}
	if row.ApplyErrorCode.Valid {
		value := row.ApplyErrorCode.String
		status.ApplyErrorCode = &value
	}
	return status
}

func manifestUTCTimePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time.UTC()
	return &result
}
