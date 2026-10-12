package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"boardly.dev/boardly/server/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/pkg/protocol"
)

const (
	googleOAuthWebBindingCookieName = "boardly_oauth_google_web"
	googleOAuthWebCallbackPath      = "/api/v1/auth/google/web/callback"
)

type GoogleOAuthService interface {
	ProviderEnabled() bool
	WebEnabled() bool
	DesktopEnabled() bool
	StartWeb(context.Context, auth.WebOAuthStartInput) (auth.WebOAuthStartResult, error)
	CompleteWeb(context.Context, auth.WebOAuthCompleteInput) (auth.WebOAuthCompleteResult, error)
	StartDesktop(context.Context, auth.DesktopOAuthStartInput) (auth.DesktopOAuthStartResult, error)
	CompleteDesktop(context.Context, auth.DesktopOAuthCompleteInput) (auth.Result, error)
}

// authProviderCapability is one provider row of the deployment capability
// probe. Enabled is a documented denormalization that must stay: it is
// exactly webEnabled || desktopEnabled, and installed desktop builds validate
// the field when probing a deployment — removing it, or letting it drift from
// the pair it summarizes, breaks clients that already shipped.
type authProviderCapability struct {
	Provider       string `json:"provider"`
	Enabled        bool   `json:"enabled"`
	WebEnabled     bool   `json:"webEnabled"`
	DesktopEnabled bool   `json:"desktopEnabled"`
}

type authProvidersResponse struct {
	Items []authProviderCapability `json:"items"`
}

type webOAuthStartRequest struct {
	ReturnPath     string                   `json:"returnPath"`
	ClientMetadata auth.OAuthClientMetadata `json:"clientMetadata"`
}

type webOAuthStartResponse struct {
	AuthorizationURL string `json:"authorizationUrl"`
	ExpiresAt        string `json:"expiresAt"`
}

type desktopOAuthStartRequest struct {
	RedirectURI    string                   `json:"redirectUri"`
	CodeChallenge  string                   `json:"codeChallenge"`
	ClientMetadata auth.OAuthClientMetadata `json:"clientMetadata"`
}

type desktopOAuthStartResponse struct {
	FlowID           uuid.UUID `json:"flowId"`
	State            string    `json:"state"`
	AuthorizationURL string    `json:"authorizationUrl"`
	ExpiresAt        string    `json:"expiresAt"`
}

type desktopOAuthCompleteRequest struct {
	FlowID        string `json:"flowId"      binding:"required,uuid"`
	RedirectURI   string `json:"redirectUri" binding:"required"`
	State         string `json:"state"       binding:"required"`
	Code          string `json:"code"        binding:"required_without=ProviderError,excluded_with=ProviderError"`
	ProviderError string `json:"error"       binding:"omitempty,oneof=access_denied,required_without=Code,excluded_with=Code"`
	Verifier      string `json:"verifier"    binding:"required"`
}

func (h *Handler) registerGoogleOAuthRoutes(group *gin.RouterGroup) {
	authRoutes := group.Group("/auth")
	authRoutes.GET("/providers", h.listAuthProviders)
	authRoutes.POST("/google/web/start", h.startGoogleOAuthWeb)
	authRoutes.GET("/google/web/callback", h.completeGoogleOAuthWeb)
	authRoutes.POST("/google/desktop/start", h.startGoogleOAuthDesktop)
	authRoutes.POST("/google/desktop/complete", h.completeGoogleOAuthDesktop)
}

func (h *Handler) listAuthProviders(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	service := h.GoogleOAuthService
	webEnabled := service != nil && service.WebEnabled()
	desktopEnabled := service != nil && service.DesktopEnabled()
	c.JSON(http.StatusOK, authProvidersResponse{
		Items: []authProviderCapability{{
			Provider:       "google",
			Enabled:        webEnabled || desktopEnabled,
			WebEnabled:     webEnabled,
			DesktopEnabled: desktopEnabled,
		}},
	})
}

func (h *Handler) startGoogleOAuthWeb(c *gin.Context) {
	request, err := decodeAuthJSON[webOAuthStartRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	service := h.GoogleOAuthService
	if service == nil {
		writeAuthError(c, auth.ErrOAuthNotConfigured)
		return
	}
	result, err := service.StartWeb(c.Request.Context(), auth.WebOAuthStartInput{
		ReturnPath:     request.ReturnPath,
		ClientMetadata: request.ClientMetadata,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	setGoogleOAuthWebBindingCookie(c, result.BrowserBinding, result.ExpiresAt, h.AuthCookies.Secure)
	c.JSON(http.StatusOK, webOAuthStartResponse{
		AuthorizationURL: result.AuthorizationURL,
		ExpiresAt:        protocol.FormatWireTime(result.ExpiresAt),
	})
}

func (h *Handler) startGoogleOAuthDesktop(c *gin.Context) {
	request, err := decodeAuthJSON[desktopOAuthStartRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	service := h.GoogleOAuthService
	if service == nil {
		writeAuthError(c, auth.ErrOAuthNotConfigured)
		return
	}
	result, err := service.StartDesktop(c.Request.Context(), auth.DesktopOAuthStartInput{
		RedirectURI:    request.RedirectURI,
		CodeChallenge:  request.CodeChallenge,
		ClientMetadata: request.ClientMetadata,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	c.JSON(http.StatusOK, desktopOAuthStartResponse{
		FlowID:           result.FlowID,
		State:            result.State,
		AuthorizationURL: result.AuthorizationURL,
		ExpiresAt:        protocol.FormatWireTime(result.ExpiresAt),
	})
}

func (h *Handler) completeGoogleOAuthWeb(c *gin.Context) {
	completion, ok := webOAuthCallbackCompletion(c.Request)
	if !ok {
		h.writeGoogleOAuthWebError(c, auth.ErrOAuthFlowInvalid)
		return
	}
	binding, ok := exactGoogleOAuthWebBinding(c.Request)
	if !ok {
		h.writeGoogleOAuthWebError(c, auth.ErrOAuthFlowInvalid)
		return
	}
	service := h.GoogleOAuthService
	if service == nil || !service.WebEnabled() {
		h.writeGoogleOAuthWebError(c, auth.ErrOAuthNotConfigured)
		return
	}
	if h.OAuthWebAppOrigin == "" || h.CSRF == nil {
		writeBoundaryError(c, boundaryErrorInternal)
		return
	}
	completion.BrowserBinding = binding
	result, err := service.CompleteWeb(c.Request.Context(), completion)
	if err != nil {
		h.writeGoogleOAuthWebError(c, err)
		return
	}
	if !safeStoredOAuthReturnPath(result.ReturnPath) {
		writeBoundaryError(c, boundaryErrorInternal)
		return
	}
	if !writeWebAuthResultCookies(c, result.Result, h.CSRF, h.AuthCookies) {
		return
	}
	c.Header("Location", h.OAuthWebAppOrigin+result.ReturnPath)
	c.Status(http.StatusSeeOther)
}

func (h *Handler) writeGoogleOAuthWebError(c *gin.Context, err error) {
	if h.OAuthWebAppOrigin != "" {
		for _, mapping := range authErrorMappings {
			if errors.Is(err, mapping.target) {
				c.Header(
					"Location",
					h.OAuthWebAppOrigin+"/login?auth_error="+url.QueryEscape(mapping.code),
				)
				c.Status(http.StatusSeeOther)
				return
			}
		}
	}
	writeBoundaryError(c, boundaryErrorInternal)
}

func (h *Handler) completeGoogleOAuthDesktop(c *gin.Context) {
	request, err := decodeAuthJSON[desktopOAuthCompleteRequest](c)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	flowID, err := util.ParseGoUUID(request.FlowID)
	if err != nil {
		writeAuthError(c, auth.ErrInvalidInput)
		return
	}
	service := h.GoogleOAuthService
	if service == nil {
		writeAuthError(c, auth.ErrOAuthNotConfigured)
		return
	}
	result, err := service.CompleteDesktop(c.Request.Context(), auth.DesktopOAuthCompleteInput{
		FlowID:        flowID,
		RedirectURI:   request.RedirectURI,
		State:         request.State,
		Code:          request.Code,
		ProviderError: request.ProviderError,
		Verifier:      request.Verifier,
	})
	if err != nil {
		writeAuthError(c, err)
		return
	}
	h.writeAuthSuccess(c, http.StatusOK, auth.ClientDesktop, result)
}

func webOAuthCallbackCompletion(request *http.Request) (auth.WebOAuthCompleteInput, bool) {
	if request == nil || len(request.URL.RawQuery) == 0 || len(request.URL.RawQuery) > 8<<10 {
		return auth.WebOAuthCompleteInput{}, false
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return auth.WebOAuthCompleteInput{}, false
	}
	// Google also returns metadata such as scope and authuser. Ignore response
	// extensions, while keeping the authorization evidence single-valued.
	for _, key := range []string{"state", "code", "error"} {
		if values, present := query[key]; present && (len(values) != 1 || values[0] == "") {
			return auth.WebOAuthCompleteInput{}, false
		}
	}
	state, stateOK := exactQueryValue(query, "state")
	code, codeOK := exactQueryValue(query, "code")
	providerError, errorOK := exactQueryValue(query, "error")
	if !stateOK || codeOK == errorOK || (errorOK && providerError != "access_denied") {
		return auth.WebOAuthCompleteInput{}, false
	}
	return auth.WebOAuthCompleteInput{State: state, Code: code, ProviderError: providerError}, true
}

func exactQueryValue(query url.Values, key string) (string, bool) {
	values, ok := query[key]
	return firstString(values), ok && len(values) == 1 && values[0] != ""
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func exactGoogleOAuthWebBinding(request *http.Request) (string, bool) {
	if request == nil {
		return "", false
	}
	value := ""
	count := 0
	for _, cookie := range request.Cookies() {
		if cookie.Name == googleOAuthWebBindingCookieName {
			count++
			value = cookie.Value
		}
	}
	return value, count == 1 && value != ""
}

func safeStoredOAuthReturnPath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") && !strings.ContainsAny(path, "\r\n")
}

func setGoogleOAuthWebBindingCookie(c *gin.Context, value string, expiresAt time.Time, secure bool) {
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     googleOAuthWebBindingCookieName,
		Value:    value,
		Path:     googleOAuthWebCallbackPath,
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearGoogleOAuthWebBindingCookie(c *gin.Context, secure bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     googleOAuthWebBindingCookieName,
		Value:    "",
		Path:     googleOAuthWebCallbackPath,
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
