package handler

import (
	"errors"
	"fmt"
	"net/http"

	"boardly.dev/boardly/server/internal/util"

	"github.com/gin-gonic/gin"
)

// boundaryErrorKey names a transport failure whose response is decided by
// the HTTP boundary rather than by a domain sentinel.
type boundaryErrorKey string

const (
	boundaryErrorAuthenticationRequired       boundaryErrorKey = "authenticationRequired"
	boundaryErrorValidationFailed             boundaryErrorKey = "validationFailed"
	boundaryErrorInvalidRequestBody           boundaryErrorKey = "invalidRequestBody"
	boundaryErrorRequestBodyTooLarge          boundaryErrorKey = "requestBodyTooLarge"
	boundaryErrorJSONContentTypeRequired      boundaryErrorKey = "jsonContentTypeRequired"
	boundaryErrorInvalidCursor                boundaryErrorKey = "invalidCursor"
	boundaryErrorInternal                     boundaryErrorKey = "internal"
	boundaryErrorRouteNotFound                boundaryErrorKey = "routeNotFound"
	boundaryErrorAgentNotFound                boundaryErrorKey = "agentNotFound"
	boundaryErrorCompanyNotFound              boundaryErrorKey = "companyNotFound"
	boundaryErrorClientTransportMismatch      boundaryErrorKey = "clientTransportMismatch"
	boundaryErrorRefreshCredentialRequired    boundaryErrorKey = "refreshCredentialRequired"
	boundaryErrorServiceNotReady              boundaryErrorKey = "serviceNotReady"
	boundaryErrorActivityCalendarUnavailable  boundaryErrorKey = "activityCalendarUnavailable"
	boundaryErrorActivityFeedUnavailable      boundaryErrorKey = "activityFeedUnavailable"
	boundaryErrorMultipartContentTypeRequired boundaryErrorKey = "multipartContentTypeRequired"
	boundaryErrorAttachmentMetadataType       boundaryErrorKey = "attachmentMetadataType"
	boundaryErrorAttachmentMetadataTooLarge   boundaryErrorKey = "attachmentMetadataTooLarge"
	boundaryErrorMultipartPartOrder           boundaryErrorKey = "multipartPartOrder"
	boundaryErrorRuntimeNotFound              boundaryErrorKey = "runtimeNotFound"
	boundaryErrorRunNotFound                  boundaryErrorKey = "runNotFound"
	boundaryErrorSkillNotFound                boundaryErrorKey = "skillNotFound"
	boundaryErrorRuntimeLoadFailed            boundaryErrorKey = "runtimeLoadFailed"
	boundaryErrorRunLoadFailed                boundaryErrorKey = "runLoadFailed"
	boundaryErrorRuntimeRegistrationFailed    boundaryErrorKey = "runtimeRegistrationFailed"
	boundaryErrorRuntimeDeregistrationFailed  boundaryErrorKey = "runtimeDeregistrationFailed"
	boundaryErrorRuntimeIDInvalid             boundaryErrorKey = "runtimeIDInvalid"
	boundaryErrorDaemonIDRequired             boundaryErrorKey = "daemonIDRequired"
	boundaryErrorHeartbeatFailed              boundaryErrorKey = "heartbeatFailed"
	boundaryErrorCompanyResolutionFailed      boundaryErrorKey = "companyResolutionFailed"
	boundaryErrorCompanyListFailed            boundaryErrorKey = "companyListFailed"
	boundaryErrorMaxRunsNegative              boundaryErrorKey = "maxRunsNegative"
	boundaryErrorRuntimeListFailed            boundaryErrorKey = "runtimeListFailed"
	boundaryErrorRunClaimFailed               boundaryErrorKey = "runClaimFailed"
	boundaryErrorPrepareLeaseExpired          boundaryErrorKey = "prepareLeaseExpired"
	boundaryErrorRunNotPreparing              boundaryErrorKey = "runNotPreparing"
	boundaryErrorOrphanRecoveryFailed         boundaryErrorKey = "orphanRecoveryFailed"
	boundaryErrorRunNotStartable              boundaryErrorKey = "runNotStartable"
	boundaryErrorRunLocalDirectoryWait        boundaryErrorKey = "runLocalDirectoryWait"
	boundaryErrorRunStudioAuthWait            boundaryErrorKey = "runStudioAuthWait"
	boundaryErrorRunSessionPinFailed          boundaryErrorKey = "runSessionPinFailed"
	boundaryErrorRunCompletionFailed          boundaryErrorKey = "runCompletionFailed"
	boundaryErrorRunFailureFailed             boundaryErrorKey = "runFailureFailed"
	boundaryErrorRunMessageListFailed         boundaryErrorKey = "runMessageListFailed"
	boundaryErrorRuntimeListingUnavailable    boundaryErrorKey = "runtimeListingUnavailable"
	boundaryErrorMailboxWebhookInvalid        boundaryErrorKey = "mailboxWebhookInvalid"
	boundaryErrorMailboxWebhookFailed         boundaryErrorKey = "mailboxWebhookFailed"
	boundaryErrorWhiteboardInvalid            boundaryErrorKey = "whiteboardInvalid"
)

// The API serves exactly one company-not-found string. These aliases keep the
// remaining call sites compiling until they migrate to
// boundaryErrorCompanyNotFound directly; both resolve to the same response.
const (
	boundaryErrorCompanyNotFoundLowercase = boundaryErrorCompanyNotFound
	boundaryErrorCompanyWasNotFound       = boundaryErrorCompanyNotFound
)

type boundaryErrorResponse struct {
	status int
	detail string
	code   string
}

// boundaryErrorResponses is the complete translation table for failures
// decided inside handlers before or after a domain call. Handlers select a
// name; only this table owns the wire response triple.
var boundaryErrorResponses = map[boundaryErrorKey]boundaryErrorResponse{
	boundaryErrorAuthenticationRequired: {
		http.StatusUnauthorized,
		"authentication is required",
		"authentication_required",
	},
	boundaryErrorValidationFailed: {
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	boundaryErrorInvalidRequestBody: {
		http.StatusBadRequest,
		"request body is invalid",
		"invalid_request",
	},
	boundaryErrorRequestBodyTooLarge: {
		http.StatusRequestEntityTooLarge,
		"request body is too large",
		"request_too_large",
	},
	boundaryErrorJSONContentTypeRequired: {
		http.StatusUnsupportedMediaType,
		"Content-Type must be application/json",
		"unsupported_media_type",
	},
	boundaryErrorInvalidCursor: {
		http.StatusUnprocessableEntity,
		"cursor is invalid",
		"invalid_cursor",
	},
	boundaryErrorInternal: {
		http.StatusInternalServerError,
		"internal server error",
		"internal_error",
	},
	boundaryErrorRouteNotFound: {
		http.StatusNotFound,
		"no route matches this method and path",
		"route_not_found",
	},
	boundaryErrorAgentNotFound:   {http.StatusNotFound, "Agent not found", "agent_not_found"},
	boundaryErrorCompanyNotFound: {http.StatusNotFound, "Company not found", "company_not_found"},
	boundaryErrorClientTransportMismatch: {
		http.StatusUnprocessableEntity,
		"client transport does not match the request origin",
		"client_transport_mismatch",
	},
	boundaryErrorRefreshCredentialRequired: {
		http.StatusBadRequest,
		"refresh credential is required",
		"refresh_token_required",
	},
	boundaryErrorServiceNotReady: {
		http.StatusServiceUnavailable,
		"service is not ready",
		"service_not_ready",
	},
	boundaryErrorActivityCalendarUnavailable: {
		http.StatusInternalServerError,
		"activity calendar is unavailable",
		"internal_error",
	},
	boundaryErrorActivityFeedUnavailable: {
		http.StatusInternalServerError,
		"activity feed is unavailable",
		"internal_error",
	},
	boundaryErrorMultipartContentTypeRequired: {
		http.StatusUnsupportedMediaType,
		"Content-Type must be multipart/form-data",
		"unsupported_media_type",
	},
	boundaryErrorAttachmentMetadataType: {
		http.StatusUnsupportedMediaType,
		"attachment metadata must use application/json",
		"unsupported_media_type",
	},
	boundaryErrorAttachmentMetadataTooLarge: {
		http.StatusRequestEntityTooLarge,
		"attachment metadata is too large",
		"request_too_large",
	},
	boundaryErrorMultipartPartOrder: {
		http.StatusUnprocessableEntity,
		"multipart request must contain exactly metadata then file",
		"invalid_multipart",
	},
	boundaryErrorRuntimeNotFound: {http.StatusNotFound, "runtime not found", "runtime_not_found"},
	boundaryErrorRunNotFound:     {http.StatusNotFound, "run not found", "run_not_found"},
	boundaryErrorSkillNotFound:   {http.StatusNotFound, "skill not found", "skill_not_found"},
	boundaryErrorRuntimeLoadFailed: {
		http.StatusInternalServerError,
		"failed to load runtime",
		"internal_error",
	},
	boundaryErrorRunLoadFailed: {
		http.StatusInternalServerError,
		"failed to load run",
		"internal_error",
	},
	boundaryErrorRuntimeRegistrationFailed: {
		http.StatusInternalServerError,
		"failed to register runtime",
		"internal_error",
	},
	boundaryErrorRuntimeDeregistrationFailed: {
		http.StatusInternalServerError,
		"failed to deregister runtime",
		"internal_error",
	},
	boundaryErrorRuntimeIDInvalid: {
		http.StatusBadRequest,
		"runtime_id must be a valid uuid",
		"invalid_request",
	},
	boundaryErrorDaemonIDRequired: {
		http.StatusBadRequest,
		"X-Boardly-Daemon-ID header is required",
		"invalid_request",
	},
	boundaryErrorHeartbeatFailed: {
		http.StatusInternalServerError,
		"heartbeat failed",
		"internal_error",
	},
	boundaryErrorCompanyResolutionFailed: {
		http.StatusInternalServerError,
		"failed to resolve companies",
		"internal_error",
	},
	boundaryErrorCompanyListFailed: {
		http.StatusInternalServerError,
		"failed to list companies",
		"internal_error",
	},
	boundaryErrorMaxRunsNegative: {
		http.StatusBadRequest,
		"max_runs must not be negative",
		"invalid_request",
	},
	boundaryErrorRuntimeListFailed: {
		http.StatusInternalServerError,
		"failed to load runtimes",
		"internal_error",
	},
	boundaryErrorRunClaimFailed: {
		http.StatusInternalServerError,
		"failed to claim runs",
		"internal_error",
	},
	boundaryErrorPrepareLeaseExpired: {
		http.StatusConflict,
		"prepare lease is no longer extendable",
		"run_prepare_lease_expired",
	},
	boundaryErrorRunNotPreparing: {http.StatusConflict, "run is not preparing", "run_not_preparing"},
	boundaryErrorOrphanRecoveryFailed: {
		http.StatusInternalServerError,
		"recover orphans failed",
		"internal_error",
	},
	boundaryErrorRunNotStartable: {http.StatusConflict, "run is not startable", "run_not_startable"},
	boundaryErrorRunLocalDirectoryWait: {
		http.StatusConflict,
		"run cannot wait for a local directory",
		"run_wait_unavailable",
	},
	boundaryErrorRunStudioAuthWait: {http.StatusConflict, "run cannot wait for Studio sign-in", "run_wait_unavailable"},
	boundaryErrorRunSessionPinFailed: {
		http.StatusInternalServerError,
		"failed to pin run session",
		"internal_error",
	},
	boundaryErrorRunCompletionFailed: {
		http.StatusInternalServerError,
		"failed to complete run",
		"internal_error",
	},
	boundaryErrorRunFailureFailed: {
		http.StatusInternalServerError,
		"failed to fail run",
		"internal_error",
	},
	boundaryErrorRunMessageListFailed: {
		http.StatusInternalServerError,
		"failed to list run messages",
		"internal_error",
	},
	boundaryErrorRuntimeListingUnavailable: {
		http.StatusInternalServerError,
		"runtime listing is unavailable",
		"internal_error",
	},
	boundaryErrorMailboxWebhookInvalid: {
		http.StatusBadRequest,
		"Mailbox webhook is invalid",
		"mailbox_webhook_invalid",
	},
	boundaryErrorMailboxWebhookFailed: {
		http.StatusInternalServerError,
		"Mailbox webhook could not be accepted",
		"mailbox_webhook_failed",
	},
	boundaryErrorWhiteboardInvalid: {
		http.StatusUnprocessableEntity,
		"Invalid Whiteboard operation",
		"whiteboard_invalid",
	},
}

func writeBoundaryError(c *gin.Context, key boundaryErrorKey) {
	writeBoundaryErrorResponse(c, boundaryErrorResponseFor(key))
}

func boundaryErrorResponseFor(key boundaryErrorKey) boundaryErrorResponse {
	response, ok := boundaryErrorResponses[key]
	if !ok {
		panic(fmt.Sprintf("handler: boundary error %q is not registered", key))
	}
	return response
}

func writeBoundaryErrorDetail(c *gin.Context, key boundaryErrorKey, detail string) {
	response := boundaryErrorResponseFor(key)
	response.detail = detail
	writeBoundaryErrorResponse(c, response)
}

func writeBoundaryErrorResponse(c *gin.Context, response boundaryErrorResponse) {
	util.WriteError(c, response.status, response.detail, response.code)
}

// errorMapping maps one domain sentinel to a fixed HTTP error response. Rows
// are evaluated in order with errors.Is, so a table must preserve the
// matching order of the switch it replaces — wrapped errors can satisfy
// several sentinels.
type errorMapping struct {
	target error
	status int
	detail string
	code   string
}

// errorFallback is a mapper's default branch; every mapper falls back to an
// internal-server-error status with a per-domain detail and code.
type errorFallback struct {
	status int
	detail string
	code   string
}

var internalErrorFallback = errorFallback{detail: "internal server error", code: "internal_error"}

func writeMappedError(c *gin.Context, err error, mappings []errorMapping, fallback errorFallback) {
	for _, mapping := range mappings {
		if errors.Is(err, mapping.target) {
			util.WriteError(c, mapping.status, mapping.detail, mapping.code)
			return
		}
	}
	status := fallback.status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	util.WriteError(c, status, fallback.detail, fallback.code)
}
