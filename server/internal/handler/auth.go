package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type AuthService interface {
	Signup(context.Context, auth.SignupInput) (auth.SignupResult, error)
	VerifyEmail(context.Context, auth.VerifyEmailInput) (auth.Result, error)
	ResendEmailVerification(
		context.Context,
		auth.ResendEmailVerificationInput,
	) (auth.PendingEmailVerification, error)
	EmailVerificationStatus(
		context.Context,
		auth.EmailVerificationStatusInput,
	) (auth.EmailVerificationStatusResult, error)
	Login(context.Context, auth.LoginInput) (auth.LoginResult, error)
	RequestPasswordReset(context.Context, auth.PasswordResetRequestInput) error
	ResetPassword(context.Context, auth.PasswordResetInput) error
	Refresh(context.Context, auth.RefreshInput) (auth.RefreshResult, error)
	Logout(context.Context, string) (auth.SessionMutationResult, error)
	Authenticate(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error)
	MintDaemonSession(context.Context, auth.UserPrincipal) (auth.Result, error)
	CurrentUser(context.Context, uuid.UUID) (auth.User, error)
	UpdateProfile(context.Context, uuid.UUID, auth.UpdateProfileInput) (auth.User, error)
}

type CookieOptions struct {
	Secure bool
	Domain string
}

// Every request that names a `client` carries the same `oneof` rule, so an
// absent, empty or unrecognized value is 422 validation_failed from the binder
// and only a well-formed value reaches clientTransportMatches.
type signupRequest struct {
	Email          string          `json:"email"`
	Password       string          `json:"password"`
	DisplayName    string          `json:"displayName"`
	Client         auth.ClientKind `json:"client"         binding:"oneof=web desktop"`
	ClientMetadata map[string]any  `json:"clientMetadata"`
}

type loginRequest struct {
	Email          string          `json:"email"`
	Password       string          `json:"password"`
	Client         auth.ClientKind `json:"client"         binding:"oneof=web desktop"`
	ClientMetadata map[string]any  `json:"clientMetadata"`
}

type passwordResetRequest struct {
	Email string `json:"email"`
}

type passwordResetConfirmRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"newPassword"`
}

type refreshRequest struct {
	Client         auth.ClientKind `json:"client"               binding:"oneof=web desktop"`
	RefreshToken   string          `json:"refreshToken"`
	RotationID     string          `json:"rotationId,omitempty" binding:"omitempty,uuid"`
	ClientMetadata map[string]any  `json:"clientMetadata"`
}

type logoutRequest struct {
	Client       auth.ClientKind `json:"client"       binding:"oneof=web desktop"`
	RefreshToken string          `json:"refreshToken"`
}

// verificationId is a string validated as a UUID rather than a typed field,
// like the desktop OAuth flowId: a non-UUID is then a field rule that did not
// hold (422 validation_failed naming the field) instead of a decode failure.
type emailVerificationRequest struct {
	Email          string          `json:"email"`
	VerificationID string          `json:"verificationId" binding:"required,uuid"`
	Code           string          `json:"code"`
	Client         auth.ClientKind `json:"client"         binding:"oneof=web desktop"`
}

type emailVerificationLookupRequest struct {
	Email          string          `json:"email"`
	VerificationID string          `json:"verificationId" binding:"required,uuid"`
	Client         auth.ClientKind `json:"client"         binding:"oneof=web desktop"`
}

type userResponse = auth.UserResponse

type cookieSessionResponse struct {
	Transport        string    `json:"transport"`
	ID               uuid.UUID `json:"id"`
	IssuedAt         string    `json:"issuedAt"`
	AccessExpiresAt  string    `json:"accessExpiresAt"`
	RefreshExpiresAt string    `json:"refreshExpiresAt"`
}

type bearerSessionResponse struct {
	Transport        string    `json:"transport"`
	ID               uuid.UUID `json:"id"`
	IssuedAt         string    `json:"issuedAt"`
	AccessToken      string    `json:"accessToken"`
	RefreshToken     string    `json:"refreshToken"`
	AccessExpiresAt  string    `json:"accessExpiresAt"`
	RefreshExpiresAt string    `json:"refreshExpiresAt"`
}

// cookieAuthSuccessResponse and bearerAuthSuccessResponse carry the `result`
// discriminator ("session") against pendingEmailVerificationResponse
// ("pending_verification"): signup and login answer both bodies under one
// status code, so the field is the branch key.
type cookieAuthSuccessResponse struct {
	Result    string                    `json:"result"`
	User      userResponse              `json:"user"`
	Session   cookieSessionResponse     `json:"session"`
	Companies companyCollectionResponse `json:"companies"`
}

type bearerAuthSuccessResponse struct {
	Result    string                    `json:"result"`
	User      userResponse              `json:"user"`
	Session   bearerSessionResponse     `json:"session"`
	Companies companyCollectionResponse `json:"companies"`
}

type cookieSessionRotationResponse struct {
	User    userResponse          `json:"user"`
	Session cookieSessionResponse `json:"session"`
}

type bearerSessionRotationResponse struct {
	User    userResponse          `json:"user"`
	Session bearerSessionResponse `json:"session"`
}

// daemonSessionResponse answers POST /auth/daemon-session: the daemon's own
// token pair for the same person, with `client` naming who holds it.
type daemonSessionResponse struct {
	Client  string                `json:"client"`
	User    userResponse          `json:"user"`
	Session bearerSessionResponse `json:"session"`
}

type authStatusResponse struct {
	Status string `json:"status"`
}

type pendingEmailVerificationResponse struct {
	Result            string    `json:"result"`
	Status            string    `json:"status"`
	Email             string    `json:"email"`
	VerificationID    uuid.UUID `json:"verificationId"`
	ExpiresAt         time.Time `json:"expiresAt"`
	ResendAvailableAt time.Time `json:"resendAvailableAt"`
	AttemptsRemaining int       `json:"attemptsRemaining"`
	MaxAttempts       int       `json:"maxAttempts"`
}

func (h *Handler) registerAuthRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil {
		return
	}
	authRoutes := group.Group("/auth")
	authRoutes.POST("/signup", h.signup)
	authRoutes.POST("/login", h.login)
	authRoutes.POST("/password-reset/request", h.requestPasswordReset)
	authRoutes.POST("/password-reset/confirm", h.resetPassword)
	authRoutes.POST("/email-verification/verify", h.verifyEmail)
	authRoutes.POST("/email-verification/resend", h.resendEmailVerification)
	authRoutes.POST("/email-verification/status", h.emailVerificationStatus)
	authRoutes.POST("/refresh", h.refresh)
	authRoutes.POST("/logout", h.logout)
	authRoutes.POST("/daemon-session", middleware.RequireUserAuth(h.AuthService), h.mintDaemonSession)
	group.GET("/me", middleware.RequireUserAuth(h.AuthService), h.getMe)
	group.PATCH("/me/profile", middleware.RequireUserAuth(h.AuthService), h.updateProfile)
}

func (h *Handler) signup(c *gin.Context) {
	request, err := decodeAuthJSON[signupRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !clientTransportMatches(c, request.Client) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	result, err := h.AuthService.Signup(c.Request.Context(), auth.SignupInput{
		Email:          request.Email,
		Password:       request.Password,
		DisplayName:    request.DisplayName,
		Client:         request.Client,
		ClientMetadata: request.ClientMetadata,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	switch {
	case result.Auth != nil && result.Pending == nil:
		h.writeAuthSuccess(c, http.StatusCreated, request.Client, *result.Auth)
	case result.Auth == nil && result.Pending != nil:
		c.JSON(http.StatusCreated, pendingEmailVerificationToResponse(*result.Pending))
	default:
		writeBoundaryError(c, boundaryErrorInternal)
	}
}

func (h *Handler) verifyEmail(c *gin.Context) {
	request, err := decodeAuthJSON[emailVerificationRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !clientTransportMatches(c, request.Client) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	verificationID, err := util.ParseGoUUID(request.VerificationID)
	if err != nil {
		writeAuthError(c, auth.ErrInvalidInput)
		return
	}
	result, err := h.AuthService.VerifyEmail(c.Request.Context(), auth.VerifyEmailInput{
		Email: request.Email, VerificationID: verificationID,
		Code: request.Code, Client: request.Client,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	h.writeAuthSuccess(c, http.StatusOK, request.Client, result)
}

func (h *Handler) resendEmailVerification(c *gin.Context) {
	request, err := decodeAuthJSON[emailVerificationLookupRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !clientTransportMatches(c, request.Client) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	verificationID, err := util.ParseGoUUID(request.VerificationID)
	if err != nil {
		writeAuthError(c, auth.ErrInvalidInput)
		return
	}
	pending, err := h.AuthService.ResendEmailVerification(c.Request.Context(), auth.ResendEmailVerificationInput{
		Email: request.Email, VerificationID: verificationID, Client: request.Client,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, pendingEmailVerificationToResponse(pending))
}

func (h *Handler) emailVerificationStatus(c *gin.Context) {
	request, err := decodeAuthJSON[emailVerificationLookupRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !clientTransportMatches(c, request.Client) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	verificationID, err := util.ParseGoUUID(request.VerificationID)
	if err != nil {
		writeAuthError(c, auth.ErrInvalidInput)
		return
	}
	status, err := h.AuthService.EmailVerificationStatus(c.Request.Context(), auth.EmailVerificationStatusInput{
		Email: request.Email, VerificationID: verificationID, Client: request.Client,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	if status.Status == auth.EmailVerificationPending && status.Pending != nil {
		c.JSON(http.StatusOK, pendingEmailVerificationToResponse(*status.Pending))
		return
	}
	c.JSON(http.StatusOK, authStatusResponse{Status: string(status.Status)})
}

func (h *Handler) login(c *gin.Context) {
	request, err := decodeAuthJSON[loginRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !clientTransportMatches(c, request.Client) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	result, err := h.AuthService.Login(c.Request.Context(), auth.LoginInput{
		Email:          request.Email,
		Password:       request.Password,
		Client:         request.Client,
		ClientMetadata: request.ClientMetadata,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	switch {
	case result.Auth != nil && result.Pending == nil:
		h.writeAuthSuccess(c, http.StatusOK, request.Client, *result.Auth)
	case result.Auth == nil && result.Pending != nil:
		c.JSON(http.StatusOK, pendingEmailVerificationToResponse(*result.Pending))
	default:
		writeBoundaryError(c, boundaryErrorInternal)
	}
}

func (h *Handler) requestPasswordReset(c *gin.Context) {
	request, err := decodeAuthJSON[passwordResetRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.AuthService.RequestPasswordReset(c.Request.Context(), auth.PasswordResetRequestInput{
		Email: request.Email,
	}); err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, authStatusResponse{Status: "accepted"})
}

func (h *Handler) resetPassword(c *gin.Context) {
	request, err := decodeAuthJSON[passwordResetConfirmRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.AuthService.ResetPassword(c.Request.Context(), auth.PasswordResetInput{
		Token: request.Token, NewPassword: request.NewPassword,
	}); err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, authStatusResponse{Status: "password_reset"})
}

func (h *Handler) refresh(c *gin.Context) {
	request, err := decodeAuthJSON[refreshRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !clientTransportMatches(c, request.Client) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	refreshToken, ok := refreshCredential(c, request.Client, request.RefreshToken)
	if !ok {
		writeBoundaryError(c, boundaryErrorRefreshCredentialRequired)
		return
	}
	var rotationID *uuid.UUID
	if request.RotationID != "" {
		parsed, err := util.ParseGoUUID(request.RotationID)
		if err != nil {
			writeAuthError(c, auth.ErrInvalidInput)
			return
		}
		rotationID = &parsed
	}
	result, err := h.AuthService.Refresh(c.Request.Context(), auth.RefreshInput{
		RefreshToken:   refreshToken,
		Client:         request.Client,
		ClientMetadata: request.ClientMetadata,
		RotationID:     rotationID,
	})
	if err != nil {
		if request.Client == auth.ClientWeb &&
			(errors.Is(err, auth.ErrInvalidSession) || errors.Is(err, auth.ErrRefreshTokenReused)) {
			clearAuthCookies(c, h.AuthCookies)
		}
		writeAuthError(c, err)
		return
	}
	if result.Auth == nil {
		writeBoundaryError(c, boundaryErrorInternal)
		return
	}
	h.writeSessionRotation(c, http.StatusOK, request.Client, *result.Auth)
}

func (h *Handler) logout(c *gin.Context) {
	request, err := decodeAuthJSON[logoutRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !clientTransportMatches(c, request.Client) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	// Logout is idempotent on both transports: no refresh cookie and no
	// refreshToken alike mean there is nothing to revoke, and the caller is
	// already where they asked to be. Only /auth/refresh demands the credential.
	if refreshToken, ok := refreshCredential(c, request.Client, request.RefreshToken); ok {
		if _, err := h.AuthService.Logout(c.Request.Context(), refreshToken); err != nil {
			writeAuthError(c, err)
			return
		}
	}
	if request.Client == auth.ClientWeb {
		clearAuthCookies(c, h.AuthCookies)
	}
	c.Status(http.StatusNoContent)
}

// mintDaemonSession starts the daemon's own session for the signed-in
// person. The desktop app and `zen daemon start` call it with the
// person's bearer and keep the answer in daemon-auth.json. The answer is a
// bearer token pair, so a browser's cookie session cannot ask for one. A
// daemon session never reaches here: the route is a person's, and the
// credential guard refuses a daemon session without a Run proof.
func (h *Handler) mintDaemonSession(c *gin.Context) {
	if c.Request.ContentLength != 0 {
		if _, err := decodeAuthJSON[struct{}](c); err != nil {
			writeRequestError(c, err)
			return
		}
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	if principal.AuthKind != auth.TokenKindBearer || !clientTransportMatches(c, auth.ClientDesktop) {
		writeBoundaryError(c, boundaryErrorClientTransportMismatch)
		return
	}
	result, err := h.AuthService.MintDaemonSession(c.Request.Context(), principal)
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusCreated, daemonSessionResponse{
		Client:  string(auth.SessionClientDaemon),
		User:    userToResponse(result.User),
		Session: newBearerSessionResponse(result),
	})
}

func (h *Handler) getMe(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	user, err := h.AuthService.CurrentUser(c.Request.Context(), principal.UserID)
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, userToResponse(user))
}

func (h *Handler) writeAuthSuccess(c *gin.Context, status int, client auth.ClientKind, result auth.Result) {
	if h.CompanyService == nil {
		writeBoundaryError(c, boundaryErrorInternal)
		return
	}
	principal := auth.UserPrincipal{
		UserID: result.User.ID, SessionID: result.Tokens.SessionID,
		AuthKind: authKindForClient(client), AccessExpiresAt: result.Tokens.AccessExpiresAt,
	}
	companies, err := h.CompanyService.List(c.Request.Context(), principal)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	if companies == nil {
		companies = []company.Response{}
	}
	companyResponse := companyCollectionResponse{Items: companies}
	switch client {
	case auth.ClientWeb:
		session, ok := h.cookieSessionResponse(c, result)
		if !ok {
			return
		}
		c.JSON(status, cookieAuthSuccessResponse{
			Result: auth.AuthResultSession,
			User:   userToResponse(result.User), Session: session, Companies: companyResponse,
		})
	case auth.ClientDesktop:
		c.JSON(status, bearerAuthSuccessResponse{
			Result:    auth.AuthResultSession,
			User:      userToResponse(result.User),
			Session:   newBearerSessionResponse(result),
			Companies: companyResponse,
		})
	default:
		writeBoundaryError(c, boundaryErrorInternal)
	}
}

func (h *Handler) writeSessionRotation(
	c *gin.Context,
	status int,
	client auth.ClientKind,
	result auth.Result,
) {
	switch client {
	case auth.ClientWeb:
		session, ok := h.cookieSessionResponse(c, result)
		if !ok {
			return
		}
		c.JSON(status, cookieSessionRotationResponse{
			User: userToResponse(result.User), Session: session,
		})
	case auth.ClientDesktop:
		c.JSON(status, bearerSessionRotationResponse{
			User: userToResponse(result.User), Session: newBearerSessionResponse(result),
		})
	default:
		writeBoundaryError(c, boundaryErrorInternal)
	}
}

func (h *Handler) cookieSessionResponse(c *gin.Context, result auth.Result) (cookieSessionResponse, bool) {
	if !writeWebAuthResultCookies(c, result, h.CSRF, h.AuthCookies) {
		return cookieSessionResponse{}, false
	}
	return cookieSessionResponse{
		Transport:        "cookie",
		ID:               result.Tokens.SessionID,
		IssuedAt:         protocol.FormatWireTime(result.Tokens.IssuedAt),
		AccessExpiresAt:  protocol.FormatWireTime(result.Tokens.AccessExpiresAt),
		RefreshExpiresAt: protocol.FormatWireTime(result.Tokens.RefreshExpiresAt),
	}, true
}

func newBearerSessionResponse(result auth.Result) bearerSessionResponse {
	return bearerSessionResponse{
		Transport:        "bearer",
		ID:               result.Tokens.SessionID,
		IssuedAt:         protocol.FormatWireTime(result.Tokens.IssuedAt),
		AccessToken:      result.Tokens.AccessToken,
		RefreshToken:     result.Tokens.RefreshToken,
		AccessExpiresAt:  protocol.FormatWireTime(result.Tokens.AccessExpiresAt),
		RefreshExpiresAt: protocol.FormatWireTime(result.Tokens.RefreshExpiresAt),
	}
}

func authKindForClient(client auth.ClientKind) auth.TokenKind {
	if client == auth.ClientWeb {
		return auth.TokenKindSession
	}
	return auth.TokenKindBearer
}

func writeWebAuthResultCookies(
	c *gin.Context,
	result auth.Result,
	csrf auth.CSRFProtector,
	cookies CookieOptions,
) bool {
	if csrf == nil {
		writeBoundaryError(c, boundaryErrorInternal)
		return false
	}
	setAuthCookies(c, result.Tokens, csrf, cookies)
	return true
}

func refreshCredential(c *gin.Context, client auth.ClientKind, bodyToken string) (string, bool) {
	switch client {
	case auth.ClientWeb:
		token, err := c.Cookie(middleware.RefreshCookieName)
		return token, err == nil && token != ""
	case auth.ClientDesktop:
		return bodyToken, bodyToken != ""
	default:
		return "", false
	}
}

// decodeAuthJSON binds an auth request. Auth bodies carry credentials, so the
// cap is theirs rather than the shared one.
func decodeAuthJSON[T any](c *gin.Context) (T, error) {
	return bindRequest[T](c, 64<<10)
}

func clientTransportMatches(c *gin.Context, client auth.ClientKind) bool {
	if c.GetHeader("Origin") != "" {
		return client == auth.ClientWeb
	}
	return client == auth.ClientDesktop
}

var authErrorMappings = []errorMapping{
	{auth.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{auth.ErrEmailExists, http.StatusConflict, "email is already registered", "email_already_registered"},
	{
		auth.ErrEmailDeliveryUnavailable,
		http.StatusServiceUnavailable,
		"verification email could not be delivered",
		"email_delivery_unavailable",
	},
	{
		auth.ErrVerificationNotFound,
		http.StatusNotFound,
		"email verification was not found",
		"verification_not_found",
	},
	{
		auth.ErrVerificationCodeInvalid,
		http.StatusBadRequest,
		"verification code is invalid",
		"verification_code_invalid",
	},
	{
		auth.ErrVerificationCodeExpired,
		http.StatusGone,
		"verification code has expired",
		"verification_code_expired",
	},
	{
		auth.ErrVerificationCodeLocked,
		http.StatusTooManyRequests,
		"verification code is locked",
		"verification_code_locked",
	},
	{
		auth.ErrVerificationResendTooSoon,
		http.StatusTooManyRequests,
		"verification code cannot be resent yet",
		"verification_resend_too_soon",
	},
	{
		auth.ErrPasswordResetUnavailable,
		http.StatusServiceUnavailable,
		"password reset is unavailable",
		"password_reset_unavailable",
	},
	{
		auth.ErrPasswordResetInvalid,
		http.StatusBadRequest,
		"password reset link is invalid or expired",
		"password_reset_invalid",
	},
	{auth.ErrInvalidCredentials, http.StatusUnauthorized, "email or password is incorrect", "invalid_credentials"},
	{auth.ErrInvalidSession, http.StatusUnauthorized, "session is invalid or expired", "session_invalid"},
	{auth.ErrRefreshTokenReused, http.StatusUnauthorized, "session is invalid or expired", "session_invalid"},
	{
		auth.ErrSessionAlreadyRotated,
		http.StatusConflict,
		"session was already rotated; retry with the current credential",
		"session_already_rotated",
	},
	{auth.ErrAccountDisabled, http.StatusForbidden, "account is disabled", "account_disabled"},
	{
		auth.ErrDaemonSessionCannotMint,
		http.StatusForbidden,
		"A daemon session can act here only for a live Run of its person",
		"run_proof_required",
	},
	{
		auth.ErrOAuthNotConfigured,
		http.StatusServiceUnavailable,
		"OAuth provider is not configured",
		"oauth_not_configured",
	},
	{auth.ErrOAuthFlowInvalid, http.StatusBadRequest, "OAuth flow is invalid or expired", "oauth_flow_invalid"},
	{
		auth.ErrOAuthProviderDenied,
		http.StatusUnauthorized,
		"OAuth authorization was denied",
		"oauth_provider_denied",
	},
	{
		auth.ErrOAuthProviderUnavailable,
		http.StatusBadGateway,
		"OAuth provider is unavailable",
		"oauth_provider_unavailable",
	},
	{auth.ErrOAuthIdentityInvalid, http.StatusUnauthorized, "OAuth identity is invalid", "oauth_identity_invalid"},
	{auth.ErrOAuthEmailUnverified, http.StatusForbidden, "OAuth email is not verified", "oauth_email_unverified"},
	{
		auth.ErrOAuthAccountConflict,
		http.StatusConflict,
		"OAuth identity conflicts with an existing account",
		"oauth_account_conflict",
	},
	{
		auth.ErrDesktopHandoffCodeInvalid,
		http.StatusBadRequest,
		"desktop handoff code is invalid",
		"desktop_handoff_invalid",
	},
	{
		auth.ErrDesktopHandoffCodeExpired,
		http.StatusGone,
		"desktop handoff code has expired",
		"desktop_handoff_expired",
	},
}

func writeAuthError(c *gin.Context, err error) {
	writeMappedError(c, err, authErrorMappings, internalErrorFallback)
}

func pendingEmailVerificationToResponse(pending auth.PendingEmailVerification) pendingEmailVerificationResponse {
	return pendingEmailVerificationResponse{
		Result: auth.AuthResultPendingVerification,
		Status: auth.AuthResultPendingVerification, Email: pending.Email,
		VerificationID: pending.VerificationID,
		ExpiresAt:      pending.ExpiresAt, ResendAvailableAt: pending.ResendAvailableAt,
		AttemptsRemaining: pending.AttemptsRemaining, MaxAttempts: pending.MaxAttempts,
	}
}

func userToResponse(user auth.User) userResponse {
	return auth.ProjectUser(user)
}

func setAuthCookies(c *gin.Context, tokens auth.IssuedTokens, csrf auth.CSRFProtector, options CookieOptions) {
	setCookie(c, middleware.AuthCookieName, tokens.AccessToken, "/", true, tokens.AccessExpiresAt, options)
	setCookie(
		c,
		middleware.RefreshCookieName,
		tokens.RefreshToken,
		"/api/v1/auth",
		true,
		tokens.RefreshExpiresAt,
		options,
	)
	setCookie(
		c,
		middleware.CSRFCookieName,
		csrf.Token(tokens.AccessToken),
		"/",
		false,
		tokens.AccessExpiresAt,
		options,
	)
	setCookie(
		c,
		middleware.RefreshCSRFCookieName,
		csrf.Token(tokens.RefreshToken),
		"/",
		false,
		tokens.RefreshExpiresAt,
		options,
	)
}

func clearAuthCookies(c *gin.Context, options CookieOptions) {
	for _, cookie := range []struct {
		name     string
		path     string
		httpOnly bool
	}{
		{name: middleware.AuthCookieName, path: "/", httpOnly: true},
		{name: middleware.RefreshCookieName, path: "/api/v1/auth", httpOnly: true},
		{name: middleware.CSRFCookieName, path: "/", httpOnly: false},
		{name: middleware.RefreshCSRFCookieName, path: "/", httpOnly: false},
	} {
		setCookie(c, cookie.name, "", cookie.path, cookie.httpOnly, time.Unix(1, 0), options)
	}
}

func setCookie(c *gin.Context, name, value, path string, httpOnly bool, expiresAt time.Time, options CookieOptions) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if value == "" {
		maxAge = -1
	} else if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		Domain:   options.Domain,
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: httpOnly,
		Secure:   options.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}
