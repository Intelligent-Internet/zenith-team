package tools

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxCommerceManifestBytes = 64 << 10

type CommerceAPI interface {
	FetchRunJSON(context.Context, string, any) error
	PostRunJSON(context.Context, string, string, any, any) error
}

type CommerceToolService interface {
	CommerceAccountStatus(context.Context, CommerceAccountStatusInput) (CommerceAccountStatusResult, error)
	SubmitCommerceManifest(context.Context, SubmitCommerceManifestInput) (SubmitCommerceManifestResult, error)
	CommerceManifestStatus(context.Context, CommerceManifestStatusInput) (CommerceManifestStatusResult, error)
}

var _ CommerceToolService = (*DefaultCommerceToolService)(nil)

type DefaultCommerceToolService struct {
	api     CommerceAPI
	context RunContext
}

func NewCommerceToolService(api CommerceAPI, runContext RunContext) (*DefaultCommerceToolService, error) {
	if api == nil || validateRunContext(runContext) != nil ||
		!canonicalUUID(runContext.CompanyID) ||
		!canonicalUUID(runContext.ConversationID) ||
		!canonicalUUID(runContext.TaskID) ||
		!canonicalUUID(runContext.AgentID) ||
		!canonicalUUID(runContext.RunID) {
		return nil, errors.New("Zenith Team Commerce Task context is unavailable")
	}
	return &DefaultCommerceToolService{api: api, context: runContext}, nil
}

type CommerceEnvironment string

const (
	CommerceEnvironmentSandbox CommerceEnvironment = "sandbox"
	CommerceEnvironmentLive    CommerceEnvironment = "live"
)

type CommerceOfferType string

const (
	CommerceOfferTypeOneTime      CommerceOfferType = "one_time"
	CommerceOfferTypeSubscription CommerceOfferType = "subscription"
)

type CommerceInterval string

const (
	CommerceIntervalMonth CommerceInterval = "month"
	CommerceIntervalYear  CommerceInterval = "year"
)

type CommerceAccountStatusInput struct{}

type CommerceIntegrationRequirements struct {
	CurrentlyDue    int32      `json:"currentlyDue"`
	EventuallyDue   int32      `json:"eventuallyDue"`
	PastDue         int32      `json:"pastDue"`
	CurrentDeadline *time.Time `json:"currentDeadline,omitempty"`
}

type CommerceIntegrationAccount struct {
	Environment      CommerceEnvironment             `json:"environment"`
	Configured       bool                            `json:"configured"`
	Status           string                          `json:"status"`
	ManifestReady    bool                            `json:"manifestReady"`
	ChargesEnabled   bool                            `json:"chargesEnabled"`
	PayoutsEnabled   bool                            `json:"payoutsEnabled"`
	ActionRequired   bool                            `json:"actionRequired"`
	Country          *string                         `json:"country,omitempty"`
	Requirements     CommerceIntegrationRequirements `json:"requirements"`
	SyncStatus       string                          `json:"syncStatus"`
	LastReconciledAt *time.Time                      `json:"lastReconciledAt,omitempty"`
}

type CommerceAccountStatusResult struct {
	TermsAccepted bool                         `json:"termsAccepted"`
	Accounts      []CommerceIntegrationAccount `json:"accounts"`
}

type CommerceManifest struct {
	SchemaVersion int                     `json:"schemaVersion" jsonschema:"Closed Zenith Team Commerce manifest schema version. Must be 2."`
	Site          CommerceManifestSite    `json:"site"          jsonschema:"Website origin and Customer Portal return path."`
	Offers        []CommerceManifestOffer `json:"offers"        jsonschema:"Complete fixed-price quantity-1 catalog. One to one hundred offers."`
}

type CommerceManifestSite struct {
	Alias            string `json:"alias"            jsonschema:"Stable site alias owned by this manifest."`
	Origin           string `json:"origin"           jsonschema:"Canonical HTTPS origin, or an exact HTTP loopback origin for local sandbox development."`
	PortalReturnPath string `json:"portalReturnPath" jsonschema:"Relative site path used after Customer Portal. Absolute URLs are forbidden."`
}

type CommerceManifestOffer struct {
	Alias        string            `json:"alias"               jsonschema:"Stable offer alias used by server-side checkout(offerAlias)."`
	DisplayName  string            `json:"displayName"         jsonschema:"Customer-visible product name."`
	Type         CommerceOfferType `json:"type"                jsonschema:"Offer type. one_time forbids planGroup and interval; subscription requires both."`
	PlanGroup    string            `json:"planGroup,omitempty" jsonschema:"Subscription-only immutable product group shared by monthly and yearly Prices."`
	Entitlement  string            `json:"entitlement"         jsonschema:"Site-defined entitlement key granted after authoritative payment events."`
	Amount       int64             `json:"amount"              jsonschema:"Positive integer amount in currency minor units. Runtime callers cannot override it."`
	Currency     string            `json:"currency"            jsonschema:"Three-letter lowercase currency code."`
	Interval     CommerceInterval  `json:"interval,omitempty"  jsonschema:"Subscription-only billing interval: month or year. Omit for one_time."`
	AutomaticTax bool              `json:"automaticTax"        jsonschema:"Whether Stripe automatic tax is enabled for this offer."`
	SuccessPath  string            `json:"successPath"         jsonschema:"Relative processing-page path. Redirect never grants access."`
	CancelPath   string            `json:"cancelPath"          jsonschema:"Relative cancellation path."`
}

type SubmitCommerceManifestInput struct {
	Environment CommerceEnvironment `json:"environment" jsonschema:"Zenith Team Commerce environment selected by the user: sandbox or live."`
	Manifest    CommerceManifest    `json:"manifest"    jsonschema:"Complete boardly-commerce.json V2 document. This creates an immutable draft only; an owner must Approve & Publish it."`
}

type CommerceSiteVerificationDocument struct {
	SchemaVersion  int    `json:"schemaVersion"`
	SiteAlias      string `json:"siteAlias"`
	ManifestDigest string `json:"manifestDigest"`
}

type SubmitCommerceManifestResult struct {
	ManifestID           string                           `json:"manifestId"`
	ApprovalID           string                           `json:"approvalId"`
	ApprovalStatus       string                           `json:"approvalStatus"`
	ReviewPath           string                           `json:"reviewPath"`
	Instruction          string                           `json:"instruction"`
	SiteID               string                           `json:"siteId"`
	Digest               string                           `json:"digest"`
	State                string                           `json:"state"`
	VerificationPath     string                           `json:"verificationPath"`
	VerificationDocument CommerceSiteVerificationDocument `json:"verificationDocument"`
	CreatedAt            time.Time                        `json:"createdAt"`
}

type CommerceManifestStatusInput struct {
	ManifestID string `json:"manifestId" jsonschema:"Manifest ID returned by commerce_manifest_submit."`
}

type CommerceManifestStatusResult struct {
	ManifestID     string     `json:"manifestId"`
	SiteID         string     `json:"siteId"`
	Digest         string     `json:"digest"`
	State          string     `json:"state"`
	ApplyErrorCode *string    `json:"applyErrorCode,omitempty"`
	ApprovedAt     *time.Time `json:"approvedAt,omitempty"`
	AppliedAt      *time.Time `json:"appliedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type submitCommerceManifestRequest struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Environment   CommerceEnvironment `json:"environment"`
	Manifest      CommerceManifest    `json:"manifest"`
}

type submitCommerceManifestResponse struct {
	ManifestID           string          `json:"manifestId"`
	ApprovalID           string          `json:"approvalId"`
	SiteID               string          `json:"siteId"`
	Digest               string          `json:"digest"`
	State                string          `json:"state"`
	VerificationPath     string          `json:"verificationPath"`
	VerificationDocument json.RawMessage `json:"verificationDocument"`
	CreatedAt            time.Time       `json:"createdAt"`
}

type commerceManifestStatusResponse struct {
	ManifestID     string     `json:"manifestId"`
	SiteID         string     `json:"siteId"`
	TaskID         string     `json:"taskId"`
	RunID          uuid.UUID  `json:"runId"`
	Digest         string     `json:"digest"`
	State          string     `json:"state"`
	ApplyErrorCode *string    `json:"applyErrorCode,omitempty"`
	ApprovedAt     *time.Time `json:"approvedAt,omitempty"`
	AppliedAt      *time.Time `json:"appliedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type commerceAccountStatusResponse struct {
	TermsAccepted bool                                 `json:"termsAccepted"`
	Accounts      []commerceIntegrationAccountResponse `json:"accounts"`
}

type commerceIntegrationAccountResponse struct {
	Environment      CommerceEnvironment                     `json:"environment"`
	Configured       bool                                    `json:"configured"`
	Status           string                                  `json:"status"`
	ManifestReady    bool                                    `json:"manifestReady"`
	ChargesEnabled   bool                                    `json:"chargesEnabled"`
	PayoutsEnabled   bool                                    `json:"payoutsEnabled"`
	ActionRequired   bool                                    `json:"actionRequired"`
	Country          *string                                 `json:"country"`
	Requirements     commerceIntegrationRequirementsResponse `json:"requirements"`
	SyncStatus       string                                  `json:"syncStatus"`
	LastReconciledAt *time.Time                              `json:"lastReconciledAt"`
}

type commerceIntegrationRequirementsResponse struct {
	CurrentlyDue    int32      `json:"currentlyDue"`
	EventuallyDue   int32      `json:"eventuallyDue"`
	PastDue         int32      `json:"pastDue"`
	CurrentDeadline *time.Time `json:"currentDeadline"`
}

func (s *DefaultCommerceToolService) CommerceAccountStatus(
	ctx context.Context,
	_ CommerceAccountStatusInput,
) (CommerceAccountStatusResult, error) {
	if s == nil || s.api == nil {
		return CommerceAccountStatusResult{}, errors.New(
			"Zenith Team Commerce Task context is unavailable",
		)
	}
	path := fmt.Sprintf(
		"/companies/%s/commerce/account-status",
		url.PathEscape(s.context.CompanyID),
	)
	var response commerceAccountStatusResponse
	if err := s.api.FetchRunJSON(ctx, path, &response); err != nil {
		return CommerceAccountStatusResult{}, fmt.Errorf("get Commerce account status: %w", err)
	}
	if !validCommerceAccountStatusResponse(response) {
		return CommerceAccountStatusResult{}, errors.New(
			"Zenith Team Commerce account status response is invalid",
		)
	}
	result := CommerceAccountStatusResult{
		TermsAccepted: response.TermsAccepted,
		Accounts:      make([]CommerceIntegrationAccount, 0, len(response.Accounts)),
	}
	for _, account := range response.Accounts {
		result.Accounts = append(result.Accounts, CommerceIntegrationAccount{
			Environment: account.Environment, Configured: account.Configured,
			Status: account.Status, ManifestReady: account.ManifestReady,
			ChargesEnabled: account.ChargesEnabled, PayoutsEnabled: account.PayoutsEnabled,
			ActionRequired: account.ActionRequired, Country: account.Country,
			Requirements: CommerceIntegrationRequirements{
				CurrentlyDue:    account.Requirements.CurrentlyDue,
				EventuallyDue:   account.Requirements.EventuallyDue,
				PastDue:         account.Requirements.PastDue,
				CurrentDeadline: account.Requirements.CurrentDeadline,
			},
			SyncStatus: account.SyncStatus, LastReconciledAt: account.LastReconciledAt,
		})
	}
	return result, nil
}

func validCommerceAccountStatusResponse(response commerceAccountStatusResponse) bool {
	if len(response.Accounts) != 2 ||
		response.Accounts[0].Environment != CommerceEnvironmentSandbox ||
		response.Accounts[1].Environment != CommerceEnvironmentLive {
		return false
	}
	for _, account := range response.Accounts {
		if !validCommerceIntegrationAccount(account, response.TermsAccepted) {
			return false
		}
	}
	return true
}

func validCommerceIntegrationAccount(
	account commerceIntegrationAccountResponse,
	termsAccepted bool,
) bool {
	if !validCommerceAccountState(account.Status) ||
		!validCommerceSyncStatus(account.SyncStatus) ||
		account.Requirements.CurrentlyDue < 0 || account.Requirements.CurrentlyDue > 1024 ||
		account.Requirements.EventuallyDue < 0 || account.Requirements.EventuallyDue > 1024 ||
		account.Requirements.PastDue < 0 || account.Requirements.PastDue > 1024 ||
		account.Requirements.CurrentDeadline != nil && account.Requirements.CurrentDeadline.IsZero() ||
		account.LastReconciledAt != nil && account.LastReconciledAt.IsZero() ||
		!validOptionalCommerceCountry(account.Country) {
		return false
	}
	if !account.ManifestReady {
		return true
	}
	return termsAccepted && account.Configured && account.Status == "ready" &&
		account.ChargesEnabled && account.PayoutsEnabled && !account.ActionRequired &&
		account.Country != nil && account.SyncStatus == "current" &&
		account.LastReconciledAt != nil && account.Requirements.CurrentlyDue == 0 &&
		account.Requirements.PastDue == 0
}

func validCommerceAccountState(status string) bool {
	switch status {
	case "not_started", "provisioning", "onboarding", "pending_review", "ready",
		"action_required", "restricted", "draining", "disconnected":
		return true
	default:
		return false
	}
}

func validCommerceSyncStatus(status string) bool {
	switch status {
	case "not_started", "pending", "current", "stale":
		return true
	default:
		return false
	}
}

func validOptionalCommerceCountry(country *string) bool {
	if country == nil {
		return true
	}
	value := *country
	return len(value) == 2 && value[0] >= 'A' && value[0] <= 'Z' &&
		value[1] >= 'A' && value[1] <= 'Z'
}

func (s *DefaultCommerceToolService) SubmitCommerceManifest(
	ctx context.Context,
	input SubmitCommerceManifestInput,
) (SubmitCommerceManifestResult, error) {
	if s == nil || s.api == nil {
		return SubmitCommerceManifestResult{}, errors.New("Zenith Team Commerce Task context is unavailable")
	}
	if input.Environment != CommerceEnvironmentSandbox && input.Environment != CommerceEnvironmentLive {
		return SubmitCommerceManifestResult{}, errors.New("Commerce environment must be sandbox or live")
	}
	if input.Manifest.SchemaVersion != 2 || strings.TrimSpace(input.Manifest.Site.Alias) == "" ||
		strings.TrimSpace(input.Manifest.Site.Origin) == "" ||
		strings.TrimSpace(input.Manifest.Site.PortalReturnPath) == "" ||
		len(input.Manifest.Offers) == 0 {
		return SubmitCommerceManifestResult{}, errors.New("Commerce manifest is required")
	}
	manifest, err := json.Marshal(input.Manifest)
	if err != nil || len(manifest) > maxCommerceManifestBytes {
		return SubmitCommerceManifestResult{}, errors.New("Commerce manifest is invalid or exceeds 64 KiB")
	}
	path := fmt.Sprintf(
		"/companies/%s/commerce/manifests",
		url.PathEscape(s.context.CompanyID),
	)
	request := submitCommerceManifestRequest{
		SchemaVersion: 1,
		Environment:   input.Environment,
		Manifest:      input.Manifest,
	}
	requestKey, err := runRequestKey("commerce_manifest_submit", s.context, request)
	if err != nil {
		return SubmitCommerceManifestResult{}, err
	}
	var response submitCommerceManifestResponse
	if err := s.api.PostRunJSON(ctx, path, requestKey, request, &response); err != nil {
		return SubmitCommerceManifestResult{}, fmt.Errorf("submit Commerce manifest draft: %w", err)
	}
	if !validCommerceResponseIdentity(response.ManifestID, response.SiteID, response.Digest) ||
		!canonicalUUID(response.ApprovalID) ||
		response.State != "draft" || response.CreatedAt.IsZero() {
		return SubmitCommerceManifestResult{}, errors.New("Zenith Team Commerce manifest response is invalid")
	}
	if response.VerificationPath != "/.well-known/boardly-site-verification.json" ||
		len(response.VerificationDocument) == 0 {
		return SubmitCommerceManifestResult{}, errors.New("Zenith Team Commerce manifest response is invalid")
	}
	decoded, decodeErr := decodeCommerceVerificationDocument(response.VerificationDocument)
	if decodeErr != nil || !validCommerceVerificationDocument(
		decoded,
		input.Manifest.Site,
		response.Digest,
	) {
		return SubmitCommerceManifestResult{}, errors.New("Zenith Team Commerce verification document is invalid")
	}
	reviewQuery := url.Values{
		"agentId":          []string{s.context.AgentID},
		"approvalId":       []string{response.ApprovalID},
		"conversationId":   []string{s.context.ConversationID},
		"conversationKind": []string{"task"},
		"manifestId":       []string{response.ManifestID},
		"taskId":           []string{s.context.TaskID},
	}
	instruction := "A Review & publish payment setup approval is attached to this Task. " +
		"End this Run now and do not call ask_human for this approval. " +
		"Zenith Team automatically starts a continuation Run after the owner publishes the manifest."
	loopbackSandbox := input.Environment == CommerceEnvironmentSandbox &&
		strings.HasPrefix(input.Manifest.Site.Origin, "http://")
	if loopbackSandbox {
		instruction = "A Review & publish payment setup approval is attached to this Task, " +
			"but it cannot be published until the local signing key is registered. " +
			"Persist verificationDocument at verificationPath, then immediately start " +
			"`zen commerce dev --manifest boardly-commerce.json -- <site command>` " +
			"from this Task Run. Keep that wrapper in the foreground and keep this Run active " +
			"while the owner publishes and tests; do not end the Run while the signing key is missing."
	}
	return SubmitCommerceManifestResult{
		ManifestID:           response.ManifestID,
		ApprovalID:           response.ApprovalID,
		ApprovalStatus:       "pending",
		ReviewPath:           "/revenue?" + reviewQuery.Encode(),
		Instruction:          instruction,
		SiteID:               response.SiteID,
		Digest:               response.Digest,
		State:                response.State,
		VerificationPath:     response.VerificationPath,
		VerificationDocument: decoded,
		CreatedAt:            response.CreatedAt,
	}, nil
}

func decodeCommerceVerificationDocument(
	document json.RawMessage,
) (CommerceSiteVerificationDocument, error) {
	if err := validateUniqueJSONKeys(json.NewDecoder(bytes.NewReader(document))); err != nil {
		return CommerceSiteVerificationDocument{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var result CommerceSiteVerificationDocument
	if err := decoder.Decode(&result); err != nil {
		return CommerceSiteVerificationDocument{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return CommerceSiteVerificationDocument{}, err
	}
	return result, nil
}

func validCommerceVerificationDocument(
	document CommerceSiteVerificationDocument,
	site CommerceManifestSite,
	digest string,
) bool {
	if document.SchemaVersion != 2 || document.SiteAlias != site.Alias ||
		document.ManifestDigest != digest {
		return false
	}
	return true
}

func (s *DefaultCommerceToolService) CommerceManifestStatus(
	ctx context.Context,
	input CommerceManifestStatusInput,
) (CommerceManifestStatusResult, error) {
	if s == nil || s.api == nil {
		return CommerceManifestStatusResult{}, errors.New("Zenith Team Commerce Task context is unavailable")
	}
	if !canonicalUUID(input.ManifestID) {
		return CommerceManifestStatusResult{}, errors.New("Commerce manifest ID is invalid")
	}
	path := fmt.Sprintf(
		"/companies/%s/commerce/manifests/%s",
		url.PathEscape(s.context.CompanyID),
		url.PathEscape(input.ManifestID),
	)
	var response commerceManifestStatusResponse
	if err := s.api.FetchRunJSON(ctx, path, &response); err != nil {
		return CommerceManifestStatusResult{}, fmt.Errorf("get Commerce manifest status: %w", err)
	}
	if response.ManifestID != input.ManifestID || response.TaskID != s.context.TaskID ||
		!canonicalUUID(response.TaskID) || response.RunID == uuid.Nil {
		return CommerceManifestStatusResult{}, errors.New(
			"Zenith Team Commerce manifest response context is invalid",
		)
	}
	if !validCommerceResponseIdentity(response.ManifestID, response.SiteID, response.Digest) ||
		!validCommerceManifestState(
			response.State,
		) || response.CreatedAt.IsZero() || response.UpdatedAt.IsZero() {
		return CommerceManifestStatusResult{}, errors.New(
			"Zenith Team Commerce manifest status response is invalid",
		)
	}
	return CommerceManifestStatusResult{
		ManifestID:     response.ManifestID,
		SiteID:         response.SiteID,
		Digest:         response.Digest,
		State:          response.State,
		ApplyErrorCode: response.ApplyErrorCode,
		ApprovedAt:     response.ApprovedAt,
		AppliedAt:      response.AppliedAt,
		CreatedAt:      response.CreatedAt,
		UpdatedAt:      response.UpdatedAt,
	}, nil
}

func validCommerceResponseIdentity(manifestID, siteID, digest string) bool {
	if !canonicalUUID(manifestID) || !canonicalUUID(siteID) || len(digest) != 64 ||
		digest != strings.ToLower(digest) {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func validCommerceManifestState(state string) bool {
	switch state {
	case "draft", "publishing", "active", "rejected", "apply_failed", "superseded":
		return true
	default:
		return false
	}
}
