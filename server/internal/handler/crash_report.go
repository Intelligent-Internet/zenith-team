package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/telemetry"
)

// maxCrashReportBodyBytes bounds the submission body well above the 16 KiB
// stored reason so oversize payloads fail fast without buffering.
const maxCrashReportBodyBytes = 64 << 10

// CrashReportService accepts one opt-in crash/freeze submission. The service
// owns bounding and redaction before commit.
type CrashReportService interface {
	Submit(context.Context, telemetry.Submission) error
}

type crashReportSubmissionRequest struct {
	Kind         string `json:"kind"`
	Platform     string `json:"platform"`
	Architecture string `json:"architecture"`
	AppVersion   string `json:"appVersion"`
	Reason       string `json:"reason"`
}

func (h *Handler) registerCrashReportRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CrashReportService == nil {
		return
	}
	routes := group.Group("/crash-reports", middleware.RequireUserAuth(h.AuthService))
	routes.POST("", h.submitCrashReport)
}

func (h *Handler) submitCrashReport(c *gin.Context) {
	if _, ok := auth.UserPrincipalFromContext(c.Request.Context()); !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	request, err := bindRequest[crashReportSubmissionRequest](c, maxCrashReportBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	err = h.CrashReportService.Submit(c.Request.Context(), telemetry.Submission{
		Type:         telemetry.ReportType(request.Kind),
		Platform:     request.Platform,
		Architecture: request.Architecture,
		AppVersion:   request.AppVersion,
		Reason:       request.Reason,
	})
	if err != nil {
		writeCrashReportError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

var crashReportErrorMappings = []errorMapping{
	{telemetry.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
}

func writeCrashReportError(c *gin.Context, err error) {
	writeMappedError(c, err, crashReportErrorMappings, internalErrorFallback)
}
