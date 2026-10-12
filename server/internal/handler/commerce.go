package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/commerce"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"
)

const (
	maxCommerceCommandBodyBytes       = 4 << 10
	maxCommerceManifestBodyBytes      = 96 << 10
	maxCommerceWebhookBodyBytes       = 1 << 20
	maxSiteRequestTokenBytes          = 16 << 10
	siteCommercePreAuthLimitWindow    = time.Minute
	siteCommerceProofIPLimit          = 3000
	siteCommerceProofGlobalLimit      = 50000
	siteCommerceChallengeIPLimit      = 3000
	siteCommerceChallengeGlobalLimit  = 20000
	siteCommercePreAuthMaximumClients = 4096
)

type siteCommercePreAuthLimit struct {
	windowStartedAt time.Time
	requestCount    int
}

type siteCommercePreAuthLimiter struct {
	mu sync.Mutex // protects every field below

	now                   func() time.Time
	ipLimit               int
	globalLimit           int
	maximumClients        int
	globalWindowStartedAt time.Time
	globalRequestCount    int
	clients               map[string]siteCommercePreAuthLimit
	overflow              siteCommercePreAuthLimit
}

func newSiteCommercePreAuthLimiter(
	ipLimit int,
	globalLimit int,
) *siteCommercePreAuthLimiter {
	return &siteCommercePreAuthLimiter{
		now: time.Now, ipLimit: ipLimit, globalLimit: globalLimit,
		maximumClients: siteCommercePreAuthMaximumClients,
		clients:        make(map[string]siteCommercePreAuthLimit),
	}
}

func newSiteCommerceProofLimiter() *siteCommercePreAuthLimiter {
	return newSiteCommercePreAuthLimiter(
		siteCommerceProofIPLimit,
		siteCommerceProofGlobalLimit,
	)
}

func newSiteCommerceChallengeLimiter() *siteCommercePreAuthLimiter {
	return newSiteCommercePreAuthLimiter(
		siteCommerceChallengeIPLimit,
		siteCommerceChallengeGlobalLimit,
	)
}

func (limiter *siteCommercePreAuthLimiter) allow(clientAddress string) (bool, time.Duration) {
	if limiter == nil {
		return false, siteCommercePreAuthLimitWindow
	}
	now := limiter.now().UTC()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if limiter.globalWindowStartedAt.IsZero() ||
		now.Sub(limiter.globalWindowStartedAt) >= siteCommercePreAuthLimitWindow {
		limiter.globalWindowStartedAt = now
		limiter.globalRequestCount = 0
	}
	if limiter.globalRequestCount >= limiter.globalLimit {
		return false, retryAfterCommercePreAuthWindow(now, limiter.globalWindowStartedAt)
	}
	entry, exists := limiter.clients[clientAddress]
	if exists && now.Sub(entry.windowStartedAt) >= siteCommercePreAuthLimitWindow {
		entry = siteCommercePreAuthLimit{windowStartedAt: now}
	}
	overflow := false
	if !exists {
		if len(limiter.clients) >= limiter.maximumClients {
			for address, candidate := range limiter.clients {
				if now.Sub(candidate.windowStartedAt) >= siteCommercePreAuthLimitWindow {
					delete(limiter.clients, address)
				}
			}
		}
		if len(limiter.clients) >= limiter.maximumClients {
			overflow = true
			entry = limiter.overflow
			if entry.windowStartedAt.IsZero() ||
				now.Sub(entry.windowStartedAt) >= siteCommercePreAuthLimitWindow {
				entry = siteCommercePreAuthLimit{windowStartedAt: now}
			}
		} else {
			entry = siteCommercePreAuthLimit{windowStartedAt: now}
		}
	}
	entryLimit := limiter.ipLimit
	if overflow {
		entryLimit = limiter.globalLimit
	}
	if entry.requestCount >= entryLimit {
		return false, retryAfterCommercePreAuthWindow(now, entry.windowStartedAt)
	}
	entry.requestCount++
	if overflow {
		limiter.overflow = entry
	} else {
		limiter.clients[clientAddress] = entry
	}
	limiter.globalRequestCount++
	return true, 0
}

func retryAfterCommercePreAuthWindow(now, startedAt time.Time) time.Duration {
	retryAfter := siteCommercePreAuthLimitWindow - now.Sub(startedAt)
	if retryAfter <= 0 || retryAfter > siteCommercePreAuthLimitWindow {
		return siteCommercePreAuthLimitWindow
	}
	return retryAfter
}

func commerceClientAddress(c *gin.Context, trustedProxies []netip.Prefix) string {
	remote, ok := parseCommerceClientAddress(c.Request.RemoteAddr)
	if !ok {
		return "unknown"
	}
	if !commerceProxyAddressTrusted(remote, trustedProxies) {
		return remote.String()
	}
	forwarded := strings.Split(c.GetHeader("X-Forwarded-For"), ",")
	for index := len(forwarded) - 1; index >= 0; index-- {
		address, parseOK := parseCommerceClientAddress(strings.TrimSpace(forwarded[index]))
		if !parseOK {
			return remote.String()
		}
		remote = address
		if !commerceProxyAddressTrusted(remote, trustedProxies) {
			break
		}
	}
	return remote.String()
}

func parseCommerceClientAddress(value string) (netip.Addr, bool) {
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	address, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func commerceProxyAddressTrusted(address netip.Addr, trustedProxies []netip.Prefix) bool {
	for _, prefix := range trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

type CommerceService interface {
	Summary(context.Context, auth.UserPrincipal) (commerce.Summary, error)
	IntegrationReadiness(
		context.Context,
		auth.UserPrincipal,
	) (commerce.IntegrationReadiness, error)
	AcceptTerms(
		context.Context,
		auth.UserPrincipal,
		commerce.AcceptTermsInput,
	) (commerce.AcceptanceResult, error)
	CreateAccountSession(
		context.Context,
		auth.UserPrincipal,
		commerce.Environment,
	) (commerce.AccountSession, error)
	CreateOnboardingLink(
		context.Context,
		auth.UserPrincipal,
		commerce.Environment,
	) (commerce.OnboardingLink, error)
	CreateDashboardLink(
		context.Context,
		auth.UserPrincipal,
		commerce.Environment,
	) (commerce.DashboardLink, error)
	SubmitManifest(
		context.Context,
		auth.UserPrincipal,
		commerce.TrustedManifestContext,
		commerce.SubmitManifestInput,
	) (commerce.ManifestSubmission, error)
	ManifestStatus(
		context.Context,
		auth.UserPrincipal,
		commerce.ManifestStatusInput,
	) (commerce.ManifestStatus, error)
	ListManifests(
		context.Context,
		auth.UserPrincipal,
		commerce.ManifestListOptions,
	) (commerce.ManifestPage, error)
	ApproveAndPublishManifest(
		context.Context,
		auth.UserPrincipal,
		commerce.ApproveManifestInput,
	) (commerce.ManifestStatus, error)
	CreateSiteChallenge(
		context.Context,
		commerce.SiteChallengeRequest,
	) (commerce.SiteChallenge, error)
	RegisterLocalSigningKey(
		context.Context,
		auth.UserPrincipal,
		commerce.TrustedLocalSigningKeyContext,
		commerce.LocalSigningKeyRequest,
	) (commerce.LocalSigningKeyRegistration, error)
	SyncSigningKeys(
		context.Context,
		auth.UserPrincipal,
		commerce.SyncSigningKeysRequest,
	) ([]commerce.SigningKeySummary, error)
	RevokeSigningKey(
		context.Context,
		auth.UserPrincipal,
		commerce.RevokeSigningKeyRequest,
	) (commerce.SigningKeySummary, error)
	CreateCheckout(
		context.Context,
		commerce.SiteSignedRequest,
		commerce.CheckoutRequest,
	) (commerce.HostedRedirect, error)
	CreatePortal(
		context.Context,
		commerce.SiteSignedRequest,
		commerce.PortalRequest,
	) (commerce.HostedRedirect, error)
	VerifyEntitlements(
		context.Context,
		commerce.SiteSignedRequest,
	) (commerce.EntitlementVerification, error)
	SessionJWKSet() (commerce.SessionJWKSet, error)
	ProcessWebhook(context.Context, []byte, string) (commerce.WebhookReceiptResult, error)
}

type acceptCommerceTermsRequest struct {
	SchemaVersion int    `json:"schemaVersion" binding:"required,eq=1"`
	TermsVersion  string `json:"termsVersion"  binding:"required,min=1,max=64"`
	Accepted      bool   `json:"accepted"      binding:"required"`
}

type commerceCommandRequest struct {
	SchemaVersion int `json:"schemaVersion" binding:"required,eq=1"`
}

type commerceAccountSessionResponse struct {
	SchemaVersion  int       `json:"schemaVersion"`
	PublishableKey string    `json:"publishableKey"`
	ClientSecret   string    `json:"clientSecret"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

type commerceOnboardingLinkResponse struct {
	SchemaVersion int       `json:"schemaVersion"`
	URL           string    `json:"url"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type commerceDashboardLinkResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	URL           string `json:"url"`
}

type submitCommerceManifestRequest struct {
	SchemaVersion int             `json:"schemaVersion" binding:"required,eq=1"`
	Environment   string          `json:"environment"   binding:"required,oneof=sandbox live"`
	Manifest      json.RawMessage `json:"manifest"      binding:"required"`
}

type approveCommerceManifestRequest struct {
	SchemaVersion int    `json:"schemaVersion" binding:"required,eq=1"`
	Digest        string `json:"digest"        binding:"required,len=64,lowercase,hexadecimal"`
}

type commerceManifestPageResponse struct {
	Manifests  []commerce.ManifestReview `json:"manifests"`
	NextCursor *string                   `json:"nextCursor"`
}

type siteCommerceCheckoutRequest struct {
	OfferAlias  string    `json:"offerAlias"  binding:"required"`
	OperationID uuid.UUID `json:"operationId" binding:"required"`
}

type siteCommercePortalRequest struct {
	OperationID uuid.UUID `json:"operationId" binding:"required"`
}

type siteCommerceEntitlementsRequest struct{}

type siteCommerceChallengeRequest struct {
	SchemaVersion int                          `json:"schemaVersion" binding:"required,eq=1"`
	SiteID        uuid.UUID                    `json:"siteId"        binding:"required"`
	Action        commerce.SiteChallengeAction `json:"action"        binding:"required,oneof=checkout portal"`
	OperationID   uuid.UUID                    `json:"operationId"   binding:"required"`
}

type siteCommerceChallengeResponse struct {
	SchemaVersion int       `json:"schemaVersion"`
	Nonce         string    `json:"nonce"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

type localCommerceSigningKeyRequest struct {
	SchemaVersion int                    `json:"schemaVersion" binding:"required,eq=1"`
	Origin        string                 `json:"origin"        binding:"required,max=2048"`
	PublicJWK     commerce.SitePublicKey `json:"publicJwk"     binding:"required"`
	Fingerprint   string                 `json:"fingerprint"   binding:"required,len=43"`
}

type localCommerceEnvironment string

const localCommerceEnvironmentSandbox localCommerceEnvironment = "sandbox"

type localCommerceSigningKeyResponse struct {
	SchemaVersion int                      `json:"schemaVersion"`
	SiteID        uuid.UUID                `json:"siteId"`
	Environment   localCommerceEnvironment `json:"environment"`
	KeyID         string                   `json:"keyId"`
}

type commerceSigningKeyPageResponse struct {
	SchemaVersion int                          `json:"schemaVersion"`
	SigningKeys   []commerce.SigningKeySummary `json:"signingKeys"`
}

type commerceSigningKeyResponse struct {
	SchemaVersion int                        `json:"schemaVersion"`
	SigningKey    commerce.SigningKeySummary `json:"signingKey"`
}

// registerCommerceRoutes serves the company Commerce routes whether or not
// this deployment has Commerce: without a service they answer 503
// commerce_checkout_disabled in the standard envelope (do not retry, hide
// Commerce actions) rather than a bare 404 that reads like a Stripe outage.
// The public site lane, the webhook and the daemon signing-key route exist
// only with a service, since nothing signs for a site without one.
func (h *Handler) registerCommerceRoutes(group *gin.RouterGroup) {
	if h.CommerceService != nil {
		group.POST("/webhooks/stripe/commerce", h.receiveCommerceWebhook)
		group.GET("/site-commerce/jwks", h.getSiteCommerceJWKSet)
		siteRoutes := group.Group("/site-commerce", commercePrivateNoStore())
		siteRoutes.POST("/challenges", h.createSiteCommerceChallenge)
		siteRoutes.POST("/checkout-sessions", h.createSiteCommerceCheckoutSession)
		siteRoutes.POST("/portal-sessions", h.createSiteCommercePortalSession)
		siteRoutes.POST("/entitlements/verify", h.verifySiteCommerceEntitlements)
	}

	if h.CommerceService != nil && h.AuthService != nil && h.Queries != nil && h.RunProofResolver != nil {
		daemonRoutes := group.Group(
			"/daemon",
			commercePrivateNoStore(),
			middleware.RequireDaemonAuth(h.AuthService),
		)
		daemonRoutes.POST(
			"/companies/:companyId/commerce/sites/:siteAlias/local-signing-keys",
			h.registerLocalCommerceSigningKey,
		)
	}

	if h.AuthService == nil || h.CompanyService == nil {
		return
	}
	// After the membership checks, so an outsider is still told nothing.
	// util.WriteError does not abort, and the handlers call the nil service.
	requireCommerce := func(c *gin.Context) {
		if h.CommerceService == nil {
			writeCommerceError(c, commerce.ErrCatalogDisabled)
			c.Abort()
		}
	}
	memberRoutes := group.Group(
		"/companies/:companyId/commerce",
		commercePrivateNoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		requireCommerce,
	)
	memberRoutes.POST("/manifests", h.submitCommerceManifest)
	memberRoutes.GET("/manifests/:manifestId", h.getCommerceManifestStatus)
	memberRoutes.GET("/account-status", h.getCommerceAccountStatus)

	readRoutes := group.Group(
		"/companies/:companyId/commerce",
		commercePrivateNoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionCommerceRead),
		requireCommerce,
	)
	readRoutes.GET("/summary", h.getCommerceSummary)
	readRoutes.GET("/manifests", h.listCommerceManifests)

	manageRoutes := group.Group(
		"/companies/:companyId/commerce",
		commercePrivateNoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionCommerceManage),
		requireCommerce,
	)
	manageRoutes.POST("/terms-acceptances", h.acceptCommerceTerms)
	manageRoutes.POST("/accounts/:environment/account-session", h.createCommerceAccountSession)
	manageRoutes.POST("/accounts/:environment/onboarding-link", h.createCommerceOnboardingLink)
	manageRoutes.POST("/accounts/:environment/dashboard-link", h.createCommerceDashboardLink)
	manageRoutes.POST("/manifests/:manifestId/approve-publish", h.approveCommerceManifest)
	manageRoutes.POST("/sites/:siteId/signing-keys/sync", h.syncCommerceSigningKeys)
	manageRoutes.POST(
		"/sites/:siteId/signing-keys/:keyId/revoke",
		h.revokeCommerceSigningKey,
	)
}

func (h *Handler) createSiteCommerceChallenge(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	if !h.allowSiteCommerceChallenge(c) {
		return
	}
	request, err := bindRequest[siteCommerceChallengeRequest](c, maxCommerceCommandBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	challenge, err := h.CommerceService.CreateSiteChallenge(
		c.Request.Context(),
		commerce.SiteChallengeRequest{
			SiteID: request.SiteID, Action: request.Action,
			OperationID:   request.OperationID,
			ClientAddress: commerceClientAddress(c, h.TrustedProxies),
		},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, siteCommerceChallengeResponse{
		SchemaVersion: 1,
		Nonce:         challenge.Nonce,
		ExpiresAt:     challenge.ExpiresAt,
	})
}

func (h *Handler) registerLocalCommerceSigningKey(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	companyID, err := util.ParseGoUUID(c.Param("companyId"))
	if err != nil {
		writeBoundaryError(c, boundaryErrorCompanyNotFoundLowercase)
		return
	}
	if !h.requireDaemonCompanyAccess(c, companyID) {
		return
	}
	principal, ok := daemonPrincipal(c)
	if !ok {
		return
	}
	principal.CompanyID = companyID
	trusted, ok := h.resolveLocalCommerceSigningKeyRun(c, principal)
	if !ok {
		return
	}
	request, err := bindRequest[localCommerceSigningKeyRequest](c, maxCommerceCommandBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	registration, err := h.CommerceService.RegisterLocalSigningKey(
		c.Request.Context(),
		principal,
		trusted,
		commerce.LocalSigningKeyRequest{
			SiteAlias: c.Param("siteAlias"), Origin: request.Origin,
			PublicKey: request.PublicJWK, Fingerprint: request.Fingerprint,
		},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, localCommerceSigningKeyResponse{
		SchemaVersion: 1,
		SiteID:        registration.SiteID,
		Environment:   localCommerceEnvironmentSandbox,
		KeyID:         registration.KeyID,
	})
}

func (h *Handler) resolveLocalCommerceSigningKeyRun(
	c *gin.Context,
	principal auth.UserPrincipal,
) (commerce.TrustedLocalSigningKeyContext, bool) {
	values := c.Request.Header.Values(runTokenHeader)
	if len(values) != 1 || values[0] == "" || h.RunProofResolver == nil {
		writeCommerceError(c, execution.ErrRunProofInvalid)
		return commerce.TrustedLocalSigningKeyContext{}, false
	}
	origin, err := h.resolveRunOrigin(c, values[0])
	if err != nil {
		writeCommerceError(c, err)
		return commerce.TrustedLocalSigningKeyContext{}, false
	}
	if !runProofBearer(principal, origin) || origin.ConversationKind != "task" ||
		origin.TaskID == nil || *origin.TaskID == uuid.Nil || origin.RunID == uuid.Nil ||
		origin.AgentID == uuid.Nil || origin.ConversationID == uuid.Nil {
		writeCommerceError(c, execution.ErrRunProofInvalid)
		return commerce.TrustedLocalSigningKeyContext{}, false
	}
	return commerce.TrustedLocalSigningKeyContext{
		CompanyID: origin.CompanyID,
		TaskID:    *origin.TaskID,
		RunID:     origin.RunID,
	}, true
}

func (h *Handler) syncCommerceSigningKeys(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	if _, err := bindRequest[commerceCommandRequest](c, maxCommerceCommandBodyBytes); err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := commercePrincipal(c)
	if !ok {
		return
	}
	siteID, err := util.ParseGoUUID(c.Param("siteId"))
	if err != nil {
		writeCommerceError(c, commerce.ErrSiteNotFound)
		return
	}
	keys, err := h.CommerceService.SyncSigningKeys(
		c.Request.Context(),
		principal,
		commerce.SyncSigningKeysRequest{SiteID: siteID},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	if keys == nil {
		keys = []commerce.SigningKeySummary{}
	}
	c.JSON(http.StatusOK, commerceSigningKeyPageResponse{
		SchemaVersion: 1,
		SigningKeys:   keys,
	})
}

func (h *Handler) revokeCommerceSigningKey(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	if _, err := bindRequest[commerceCommandRequest](c, maxCommerceCommandBodyBytes); err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := commercePrincipal(c)
	if !ok {
		return
	}
	siteID, err := util.ParseGoUUID(c.Param("siteId"))
	if err != nil {
		writeCommerceError(c, commerce.ErrSiteNotFound)
		return
	}
	keyID := c.Param("keyId")
	if !validCommerceSigningKeyID(keyID) {
		writeCommerceError(c, commerce.ErrSigningKeyNotFound)
		return
	}
	key, err := h.CommerceService.RevokeSigningKey(
		c.Request.Context(),
		principal,
		commerce.RevokeSigningKeyRequest{SiteID: siteID, KeyID: keyID},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, commerceSigningKeyResponse{SchemaVersion: 1, SigningKey: key})
}

func validCommerceSigningKeyID(value string) bool {
	if len(value) != 43 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func (h *Handler) getSiteCommerceJWKSet(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	keySet, err := h.CommerceService.SessionJWKSet()
	if err != nil {
		c.Header("Cache-Control", "no-store")
		writeCommerceError(c, err)
		return
	}
	if keySet.Keys == nil {
		keySet.Keys = []commerce.SessionJWK{}
	}
	c.Header("Cache-Control", "public, max-age=300, stale-if-error=86400")
	c.JSON(http.StatusOK, keySet)
}

func (h *Handler) getCommerceAccountStatus(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	principal, ok := commercePrincipal(c)
	if !ok {
		return
	}
	status, err := h.CommerceService.IntegrationReadiness(c.Request.Context(), principal)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, status)
}

func commercePrivateNoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("Pragma", "no-cache")
		c.Next()
	}
}

func (h *Handler) getCommerceSummary(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	principal, ok := commercePrincipal(c)
	if !ok {
		return
	}
	summary, err := h.CommerceService.Summary(c.Request.Context(), principal)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, summary)
}

func (h *Handler) acceptCommerceTerms(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	request, err := bindRequest[acceptCommerceTermsRequest](c, maxCommerceCommandBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := commercePrincipal(c)
	if !ok {
		return
	}
	result, err := h.CommerceService.AcceptTerms(
		c.Request.Context(),
		principal,
		commerce.AcceptTermsInput{
			TermsVersion: request.TermsVersion,
			Accepted:     request.Accepted,
		},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, result)
}

func (h *Handler) createCommerceAccountSession(c *gin.Context) {
	environment, principal, ok := h.commerceAccountCommand(c)
	if !ok {
		return
	}
	session, err := h.CommerceService.CreateAccountSession(
		c.Request.Context(), principal, environment,
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, commerceAccountSessionResponse{
		SchemaVersion:  1,
		PublishableKey: session.PublishableKey,
		ClientSecret:   session.ClientSecret,
		ExpiresAt:      session.ExpiresAt,
	})
}

func (h *Handler) createCommerceOnboardingLink(c *gin.Context) {
	environment, principal, ok := h.commerceAccountCommand(c)
	if !ok {
		return
	}
	link, err := h.CommerceService.CreateOnboardingLink(
		c.Request.Context(), principal, environment,
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, commerceOnboardingLinkResponse{
		SchemaVersion: 1,
		URL:           link.URL,
		ExpiresAt:     link.ExpiresAt,
	})
}

func (h *Handler) createCommerceDashboardLink(c *gin.Context) {
	environment, principal, ok := h.commerceAccountCommand(c)
	if !ok {
		return
	}
	link, err := h.CommerceService.CreateDashboardLink(
		c.Request.Context(), principal, environment,
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, commerceDashboardLinkResponse{
		SchemaVersion: 1,
		URL:           link.URL,
	})
}

func (h *Handler) submitCommerceManifest(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	request, err := bindRequest[submitCommerceManifestRequest](c, maxCommerceManifestBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := commercePrincipal(c)
	if !ok {
		return
	}
	origin, requestKey, ok := h.resolveCommerceManifestRun(c, principal, true)
	if !ok {
		return
	}
	result, err := h.CommerceService.SubmitManifest(
		c.Request.Context(),
		principal,
		commerce.TrustedManifestContext{
			CompanyID:      origin.CompanyID,
			ConversationID: origin.ConversationID,
			TaskID:         *origin.TaskID,
			AgentID:        origin.AgentID,
			RunID:          origin.RunID,
			RequestKey:     requestKey,
		},
		commerce.SubmitManifestInput{
			Environment: commerce.Environment(request.Environment),
			RawManifest: append([]byte(nil), request.Manifest...),
		},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (h *Handler) getCommerceManifestStatus(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	principal, ok := commercePrincipal(c)
	if !ok {
		return
	}
	manifestID, err := util.ParseGoUUID(c.Param("manifestId"))
	if err != nil {
		writeCommerceError(c, commerce.ErrManifestNotFound)
		return
	}
	origin, _, ok := h.resolveCommerceManifestRun(c, principal, false)
	if !ok {
		return
	}
	result, err := h.CommerceService.ManifestStatus(
		c.Request.Context(),
		principal,
		commerce.ManifestStatusInput{
			TaskID:         *origin.TaskID,
			ConversationID: origin.ConversationID,
			ManifestID:     manifestID,
		},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listCommerceManifests(c *gin.Context) {
	options, err := parseCommerceManifestListOptions(c)
	if errors.Is(err, errInvalidCommerceManifestCursor) {
		writeBoundaryError(c, boundaryErrorInvalidCursor)
		return
	}
	if err != nil {
		writeCommerceError(c, commerce.ErrInvalidInput)
		return
	}
	principal, ok := commercePrincipal(c)
	if !ok {
		return
	}
	page, err := h.CommerceService.ListManifests(c.Request.Context(), principal, options)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	if page.Items == nil {
		page.Items = []commerce.ManifestReview{}
	}
	response := commerceManifestPageResponse{Manifests: page.Items}
	if page.Next != nil {
		cursor := encodeFeedCursor(page.Next.CreatedAt, page.Next.ID)
		response.NextCursor = &cursor
	}
	c.JSON(http.StatusOK, response)
}

var errInvalidCommerceManifestCursor = errors.New("invalid Commerce manifest cursor")

func parseCommerceManifestListOptions(c *gin.Context) (commerce.ManifestListOptions, error) {
	query := c.Request.URL.Query()
	for key := range query {
		if key != "limit" && key != "cursor" {
			return commerce.ManifestListOptions{}, commerce.ErrInvalidInput
		}
	}
	options := commerce.ManifestListOptions{Limit: 50}
	if values, exists := query["limit"]; exists {
		if len(values) != 1 || values[0] == "" {
			return commerce.ManifestListOptions{}, commerce.ErrInvalidInput
		}
		value, err := strconv.ParseInt(values[0], 10, 32)
		if err != nil || strconv.FormatInt(value, 10) != values[0] || value < 1 || value > 100 {
			return commerce.ManifestListOptions{}, commerce.ErrInvalidInput
		}
		options.Limit = int32(value)
	}
	if values, exists := query["cursor"]; exists {
		if len(values) != 1 {
			return commerce.ManifestListOptions{}, errInvalidCommerceManifestCursor
		}
		createdAt, id, ok := decodeFeedCursor(values[0])
		if !ok {
			return commerce.ManifestListOptions{}, errInvalidCommerceManifestCursor
		}
		options.Before = &commerce.ManifestCursor{CreatedAt: createdAt, ID: id}
	}
	return options, nil
}

func (h *Handler) approveCommerceManifest(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	request, err := bindRequest[approveCommerceManifestRequest](c, maxCommerceCommandBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	principal, ok := commercePrincipal(c)
	if !ok {
		return
	}
	manifestID, err := util.ParseGoUUID(c.Param("manifestId"))
	if err != nil {
		writeCommerceError(c, commerce.ErrManifestNotFound)
		return
	}
	result, err := h.CommerceService.ApproveAndPublishManifest(
		c.Request.Context(),
		principal,
		commerce.ApproveManifestInput{ManifestID: manifestID, Digest: request.Digest},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) createSiteCommerceCheckoutSession(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	if !h.allowSiteCommerceProof(c) {
		return
	}
	request, signedRequest, err := bindSiteCommerceRequest[siteCommerceCheckoutRequest](c)
	if err != nil {
		writeSiteCommerceRequestError(c, err)
		return
	}
	result, err := h.CommerceService.CreateCheckout(
		c.Request.Context(),
		signedRequest,
		commerce.CheckoutRequest{OfferAlias: request.OfferAlias, OperationID: request.OperationID},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) createSiteCommercePortalSession(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	if !h.allowSiteCommerceProof(c) {
		return
	}
	request, signedRequest, err := bindSiteCommerceRequest[siteCommercePortalRequest](c)
	if err != nil {
		writeSiteCommerceRequestError(c, err)
		return
	}
	result, err := h.CommerceService.CreatePortal(
		c.Request.Context(),
		signedRequest,
		commerce.PortalRequest{OperationID: request.OperationID},
	)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) verifySiteCommerceEntitlements(c *gin.Context) {
	if !commerceQueryIsEmpty(c) {
		return
	}
	if !h.allowSiteCommerceProof(c) {
		return
	}
	_, signedRequest, err := bindSiteCommerceRequest[siteCommerceEntitlementsRequest](c)
	if err != nil {
		writeSiteCommerceRequestError(c, err)
		return
	}
	result, err := h.CommerceService.VerifyEntitlements(c.Request.Context(), signedRequest)
	if err != nil {
		writeCommerceError(c, err)
		return
	}
	if result.Entitlements == nil {
		result.Entitlements = []commerce.Entitlement{}
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) allowSiteCommerceProof(c *gin.Context) bool {
	return h.allowSiteCommercePreAuth(c, h.siteCommerceProofLimiter)
}

func (h *Handler) allowSiteCommerceChallenge(c *gin.Context) bool {
	return h.allowSiteCommercePreAuth(c, h.siteCommerceChallengeLimiter)
}

func (h *Handler) allowSiteCommercePreAuth(
	c *gin.Context,
	limiter *siteCommercePreAuthLimiter,
) bool {
	allowed, retryAfter := limiter.allow(
		commerceClientAddress(c, h.TrustedProxies),
	)
	if allowed {
		return true
	}
	writeCommerceError(c, &commerce.RateLimitError{RetryAfter: retryAfter})
	return false
}

func (h *Handler) receiveCommerceWebhook(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !commerceQueryIsEmpty(c) {
		return
	}
	signatures := c.Request.Header.Values("Stripe-Signature")
	if len(signatures) != 1 || signatures[0] == "" || len(signatures[0]) > 8<<10 ||
		strings.ContainsAny(signatures[0], "\r\n") {
		writeCommerceWebhookError(c, commerce.ErrInvalidWebhook)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(
		c.Writer,
		c.Request.Body,
		maxCommerceWebhookBodyBytes,
	))
	if err != nil || len(body) == 0 {
		writeCommerceWebhookError(c, commerce.ErrInvalidWebhook)
		return
	}
	result, err := h.CommerceService.ProcessWebhook(
		c.Request.Context(),
		body,
		signatures[0],
	)
	if err != nil {
		writeCommerceWebhookError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func writeCommerceWebhookError(c *gin.Context, err error) {
	writeMappedError(
		c,
		err,
		commerceWebhookErrorMappings,
		errorFallback{
			status: http.StatusServiceUnavailable,
			detail: "Commerce webhook could not be processed",
			code:   "commerce_webhook_unavailable",
		},
	)
}

func (h *Handler) resolveCommerceManifestRun(
	c *gin.Context,
	principal auth.UserPrincipal,
	requireRequestKey bool,
) (execution.RunOrigin, uuid.UUID, bool) {
	requestKey := uuid.Nil
	if requireRequestKey {
		var err error
		requestKey, err = util.ParseGoUUID(c.GetHeader(idempotencyHeader))
		if err != nil {
			writeCommerceError(c, commerce.ErrInvalidInput)
			return execution.RunOrigin{}, uuid.Nil, false
		}
	}
	if h.RunProofResolver == nil {
		writeCommerceError(c, errors.New("Commerce Run proof resolver is not configured"))
		return execution.RunOrigin{}, uuid.Nil, false
	}
	token := c.GetHeader(runTokenHeader)
	if token == "" {
		writeCommerceError(c, execution.ErrRunProofInvalid)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	origin, err := h.resolveRunOrigin(c, token)
	if err != nil {
		writeCommerceError(c, err)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	if !runProofBearer(principal, origin) || origin.ConversationKind != "task" ||
		origin.TaskID == nil || *origin.TaskID == uuid.Nil ||
		origin.ConversationID == uuid.Nil || origin.AgentID == uuid.Nil || origin.RunID == uuid.Nil {
		writeCommerceError(c, execution.ErrRunProofInvalid)
		return execution.RunOrigin{}, uuid.Nil, false
	}
	return origin, requestKey, true
}

type siteCommerceRequestBody struct {
	io.Reader
	io.Closer
}

func bindSiteCommerceRequest[T any](
	c *gin.Context,
) (T, commerce.SiteSignedRequest, error) {
	var zero T
	token, ok := siteCommerceRequestToken(c)
	if !ok {
		return zero, commerce.SiteSignedRequest{}, commerce.ErrUnauthenticated
	}
	digest := sha256.New()
	c.Request.Body = siteCommerceRequestBody{
		Reader: io.TeeReader(c.Request.Body, digest),
		Closer: c.Request.Body,
	}
	request, err := bindRequest[T](c, maxCommerceCommandBodyBytes)
	if err != nil {
		return zero, commerce.SiteSignedRequest{}, err
	}
	var bodySHA256 [sha256.Size]byte
	copy(bodySHA256[:], digest.Sum(nil))
	return request, commerce.SiteSignedRequest{
		Token: token, Method: c.Request.Method, Path: c.Request.URL.Path,
		BodySHA256: bodySHA256,
	}, nil
}

func writeSiteCommerceRequestError(c *gin.Context, err error) {
	if errors.Is(err, commerce.ErrUnauthenticated) {
		writeCommerceError(c, err)
		return
	}
	writeRequestError(c, err)
}

func siteCommerceRequestToken(c *gin.Context) (string, bool) {
	values := c.Request.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > maxSiteRequestTokenBytes+12 {
		return "", false
	}
	scheme, token, found := strings.Cut(values[0], " ")
	if !found || scheme != "BoardlySite" || token == "" ||
		len(token) > maxSiteRequestTokenBytes || strings.TrimSpace(token) != token ||
		strings.ContainsAny(token, " \t\r\n,") {
		return "", false
	}
	return token, true
}

func (h *Handler) commerceAccountCommand(
	c *gin.Context,
) (commerce.Environment, auth.UserPrincipal, bool) {
	if !commerceQueryIsEmpty(c) {
		return "", auth.UserPrincipal{}, false
	}
	_, err := bindRequest[commerceCommandRequest](c, maxCommerceCommandBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return "", auth.UserPrincipal{}, false
	}
	environment := commerce.Environment(c.Param("environment"))
	if environment != commerce.EnvironmentSandbox && environment != commerce.EnvironmentLive {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return "", auth.UserPrincipal{}, false
	}
	principal, ok := commercePrincipal(c)
	return environment, principal, ok
}

func commerceQueryIsEmpty(c *gin.Context) bool {
	if c.Request.URL.RawQuery == "" {
		return true
	}
	writeBoundaryError(c, boundaryErrorValidationFailed)
	return false
}

func commercePrincipal(c *gin.Context) (auth.UserPrincipal, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, false
	}
	return principal, true
}

var commerceErrorMappings = []errorMapping{
	{
		target: execution.ErrRunProofInvalid, status: http.StatusUnauthorized,
		detail: "Run token is invalid", code: "run_token_invalid",
	},
	{
		target: commerce.ErrUnauthenticated, status: http.StatusUnauthorized,
		detail: "authentication is required", code: "authentication_required",
	},
	{
		target: commerce.ErrForbidden, status: http.StatusForbidden,
		detail: "Your Company role does not allow this action", code: "company_permission_required",
	},
	{
		target: commerce.ErrAccessForbidden, status: http.StatusForbidden,
		detail: "Company access is required", code: "company_access_required",
	},
	{
		target: commerce.ErrInvalidInput, status: http.StatusUnprocessableEntity,
		detail: "request validation failed", code: "validation_failed",
	},
	{
		target: commerce.ErrSiteNotFound, status: http.StatusNotFound,
		detail: "Commerce Site not found.", code: "commerce_site_not_found",
	},
	{
		target: commerce.ErrSigningKeyNotFound, status: http.StatusNotFound,
		detail: "Commerce Site signing key not found.",
		code:   "commerce_signing_key_not_found",
	},
	{
		target: commerce.ErrRateLimited, status: http.StatusTooManyRequests,
		detail: "Too many Commerce requests; retry later", code: "commerce_rate_limited",
	},
	{
		target: commerce.ErrTermsUnavailable, status: http.StatusServiceUnavailable,
		detail: "Commerce terms are temporarily unavailable", code: "commerce_terms_unavailable",
	},
	{
		target: commerce.ErrTermsOutdated, status: http.StatusConflict,
		detail: "Current Commerce terms must be accepted", code: "commerce_terms_outdated",
	},
	{
		target: commerce.ErrTermsConflict, status: http.StatusConflict,
		detail: "Commerce terms configuration conflicts with cluster authority",
		code:   "commerce_terms_conflict",
	},
	{
		target: commerce.ErrAccountConflict, status: http.StatusConflict,
		detail: "Stripe account state changed; refresh and retry", code: "commerce_account_conflict",
	},
	{
		target: commerce.ErrAccountRecovery, status: http.StatusConflict,
		detail: "Stripe account setup needs reconciliation before it can continue",
		code:   "commerce_account_recovery_required",
	},
	{
		target: commerce.ErrProviderSetupRequired, status: http.StatusConflict,
		detail: "Complete the Stripe Connect platform setup, then retry",
		code:   "commerce_provider_setup_required",
	},
	{
		target: commerce.ErrProviderCredentialsInvalid, status: http.StatusConflict,
		detail: "Stripe Commerce credentials are invalid or expired",
		code:   "commerce_provider_credentials_invalid",
	},
	{
		target: commerce.ErrProviderPermissionRequired, status: http.StatusConflict,
		detail: "Stripe restricted-key permissions are incomplete",
		code:   "commerce_provider_permission_required",
	},
	{
		target: commerce.ErrProviderRateLimited, status: http.StatusTooManyRequests,
		detail: "Stripe rate limited the Commerce request; retry later",
		code:   "commerce_provider_rate_limited",
	},
	{
		target: commerce.ErrTaxSetupRequired, status: http.StatusConflict,
		detail: "Stripe Tax must be activated before automatic tax can be used",
		code:   "commerce_tax_setup_required",
	},
	{
		target: commerce.ErrCheckoutTaxLocationInvalid, status: http.StatusBadGateway,
		detail: "Stripe rejected the Checkout customer tax address",
		code:   "commerce_checkout_tax_address_invalid",
	},
	{
		target: commerce.ErrProviderRequestInvalid, status: http.StatusBadGateway,
		detail: "Stripe rejected Zenith Team's Commerce request",
		code:   "commerce_provider_request_invalid",
	},
	{
		target: commerce.ErrProviderUnavailable, status: http.StatusServiceUnavailable,
		detail: "Stripe Connect is temporarily unavailable", code: "commerce_provider_unavailable",
	},
	{
		target: commerce.ErrInvalidProviderState, status: http.StatusBadGateway,
		detail: "Stripe Connect returned an invalid response", code: "commerce_provider_invalid",
	},
	{
		target: commerce.ErrManifestNotFound, status: http.StatusNotFound,
		detail: "Commerce manifest or Offer was not found", code: "commerce_manifest_not_found",
	},
	{
		target: commerce.ErrCustomerNotFound, status: http.StatusNotFound,
		detail: "Commerce customer was not found", code: "commerce_customer_not_found",
	},
	{
		target: commerce.ErrManifestConflict, status: http.StatusConflict,
		detail: "Commerce manifest conflicts with approved state", code: "commerce_manifest_conflict",
	},
	{
		target: commerce.ErrManifestNotApprovable, status: http.StatusConflict,
		detail: "Commerce manifest cannot be approved in its current state",
		code:   "commerce_manifest_not_approvable",
	},
	{
		target: commerce.ErrApprovalExpired, status: http.StatusUnauthorized,
		detail: "Recent owner authentication is required", code: "owner_reauthentication_required",
	},
	{
		target: commerce.ErrCatalogDisabled, status: http.StatusServiceUnavailable,
		detail: "Commerce catalog and Checkout are disabled", code: "commerce_checkout_disabled",
	},
	{
		target: commerce.ErrSiteSigningKeyRequired, status: http.StatusConflict,
		detail: "Start the local Site with zen commerce dev before publishing",
		code:   "commerce_site_signing_key_required",
	},
	{
		target: commerce.ErrSiteAuthority, status: http.StatusConflict,
		detail: "Commerce Site verification failed", code: "commerce_site_verification_failed",
	},
	{
		target: commerce.ErrGatewayBusy, status: http.StatusConflict,
		detail: "Commerce operation is already in progress", code: "commerce_operation_in_progress",
	},
	{
		target: commerce.ErrGatewayRecovery, status: http.StatusConflict,
		detail: "Commerce operation requires reconciliation before retrying",
		code:   "commerce_operation_recovery_required",
	},
	{
		target: commerce.ErrInvalidWebhook, status: http.StatusBadRequest,
		detail: "Commerce webhook is invalid", code: "commerce_webhook_invalid",
	},
	{
		target: commerce.ErrWebhookConflict, status: http.StatusBadRequest,
		detail: "Commerce webhook is invalid", code: "commerce_webhook_invalid",
	},
	{
		target: commerce.ErrWebhookBusy, status: http.StatusServiceUnavailable,
		detail: "Commerce webhook could not be processed", code: "commerce_webhook_unavailable",
	},
}

var commerceWebhookErrorMappings = []errorMapping{
	{
		target: commerce.ErrInvalidWebhook, status: http.StatusBadRequest,
		detail: "Commerce webhook is invalid", code: "commerce_webhook_invalid",
	},
	{
		target: commerce.ErrWebhookConflict, status: http.StatusBadRequest,
		detail: "Commerce webhook is invalid", code: "commerce_webhook_invalid",
	},
	{
		target: commerce.ErrWebhookBusy, status: http.StatusServiceUnavailable,
		detail: "Commerce webhook could not be processed", code: "commerce_webhook_unavailable",
	},
}

func writeCommerceError(c *gin.Context, err error) {
	var rateLimit *commerce.RateLimitError
	if errors.As(err, &rateLimit) && rateLimit.RetryAfter > 0 {
		seconds := (rateLimit.RetryAfter + time.Second - 1) / time.Second
		c.Header("Retry-After", strconv.FormatInt(int64(seconds), 10))
	}
	writeMappedError(c, err, commerceErrorMappings, internalErrorFallback)
}
