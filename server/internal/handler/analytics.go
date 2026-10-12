package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/usage"
)

type analyticsBudgetRequest struct {
	OwnerKind            string     `json:"ownerKind" binding:"required,oneof=company agent"`
	AgentID              *uuid.UUID `json:"agentId"`
	MonthlyLimitUsdTicks string     `json:"monthlyLimitUsdTicks" binding:"required"`
	Enabled              bool       `json:"enabled"`
}

func (h *Handler) registerAnalyticsRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.AnalyticsService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/analytics",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.GET("/summary", h.getAnalyticsSummary)
	routes.GET("/series", h.getAnalyticsSeries)
	routes.GET("/breakdown", h.getAnalyticsBreakdown)
	routes.GET("/failures", h.getAnalyticsFailures)
	routes.GET("/heatmap", h.getAnalyticsHeatmap)
	routes.GET("/budget", h.getAnalyticsBudgets)
	routes.PUT("/budget", h.putAnalyticsBudget)
	taskRoutes := group.Group(
		"/companies/:companyId/tasks/:taskId/usage",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	taskRoutes.GET("", h.getTaskUsage)
}

func (h *Handler) getAnalyticsHeatmap(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	query, err := usage.ParseAnalyticsQuery(c.Request.URL.RawQuery, "heatmap")
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	result, err := h.AnalyticsService.GetAnalyticsHeatmap(c.Request.Context(), principal, query)
	if err != nil {
		writeAnalyticsError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getAnalyticsBudgets(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.AnalyticsService.GetAnalyticsBudgets(c.Request.Context(), principal)
	if err != nil {
		writeAnalyticsError(c, err)
		return
	}
	c.JSON(http.StatusOK, usage.AnalyticsBudgetPage{Items: result})
}

func (h *Handler) putAnalyticsBudget(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	request, err := bindRequest[analyticsBudgetRequest](c, 16<<10)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.AnalyticsService.UpsertAnalyticsBudget(c.Request.Context(), principal, usage.AnalyticsBudgetInput{
		OwnerKind: request.OwnerKind, AgentID: request.AgentID,
		MonthlyLimitUsdTicks: request.MonthlyLimitUsdTicks, Enabled: request.Enabled,
	})
	if err != nil {
		writeAnalyticsError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getAnalyticsSummary(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	query, err := usage.ParseAnalyticsQuery(c.Request.URL.RawQuery, "summary")
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	result, err := h.AnalyticsService.GetAnalyticsSummary(c.Request.Context(), principal, query)
	if err != nil {
		writeAnalyticsError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getAnalyticsSeries(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	query, err := usage.ParseAnalyticsQuery(c.Request.URL.RawQuery, "series")
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	result, err := h.AnalyticsService.GetAnalyticsSeries(c.Request.Context(), principal, query)
	if err != nil {
		writeAnalyticsError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getAnalyticsBreakdown(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	query, err := usage.ParseAnalyticsQuery(c.Request.URL.RawQuery, "breakdown")
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	result, err := h.AnalyticsService.GetAnalyticsBreakdown(c.Request.Context(), principal, query)
	if err != nil {
		writeAnalyticsError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getAnalyticsFailures(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	query, err := usage.ParseAnalyticsQuery(c.Request.URL.RawQuery, "failures")
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	result, err := h.AnalyticsService.GetAnalyticsFailures(c.Request.Context(), principal, query)
	if err != nil {
		writeAnalyticsError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getTaskUsage(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	taskID, err := uuid.Parse(c.Param("taskId"))
	if err != nil || taskID == uuid.Nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	result, err := h.AnalyticsService.GetTaskUsage(c.Request.Context(), principal, taskID)
	if err != nil {
		writeAnalyticsError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

var analyticsErrorMappings = []errorMapping{
	{target: usage.ErrInvalidInput, status: http.StatusUnprocessableEntity, detail: "request validation failed", code: "validation_failed"},
	{target: usage.ErrForbidden, status: http.StatusForbidden, detail: "Your Company role does not allow this action", code: "company_permission_required"},
	{target: usage.ErrTaskUsageNotFound, status: http.StatusNotFound, detail: "Task not found", code: "task_not_found"},
	// ADR 0017: a project_id or task_id filter naming work the caller does not
	// read answers like one naming work that does not exist.
	{target: auth.ErrProjectNotVisible, status: http.StatusNotFound, detail: "Project not found", code: "project_not_found"},
	{target: auth.ErrTaskNotVisible, status: http.StatusNotFound, detail: "Task not found", code: "task_not_found"},
	{target: usage.ErrAnalyticsBusy, status: http.StatusServiceUnavailable, detail: "Analytics is busy; try again", code: "analytics_busy"},
}

func writeAnalyticsError(c *gin.Context, err error) {
	writeMappedError(c, err, analyticsErrorMappings, internalErrorFallback)
}
