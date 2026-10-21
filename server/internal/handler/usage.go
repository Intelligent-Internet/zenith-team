package handler

import (
	"context"
	"net/http"

	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
)

type UsageService interface {
	ReportRunUsage(context.Context, uuid.UUID, []usage.RunUsageInput) error
	GetActivityInsight(context.Context, auth.UserPrincipal) (usage.ActivityInsight, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID, usage.Period) (usage.AgentUsageReport, error)
	GetCompany(context.Context, auth.UserPrincipal, usage.CompanyPeriod) (usage.CompanyUsageReport, error)
}

type AnalyticsService interface {
	GetAnalyticsSummary(context.Context, auth.UserPrincipal, usage.AnalyticsQuery) (usage.AnalyticsSummary, error)
	GetAnalyticsSeries(context.Context, auth.UserPrincipal, usage.AnalyticsQuery) (usage.AnalyticsSeries, error)
	GetAnalyticsHeatmap(context.Context, auth.UserPrincipal, usage.AnalyticsQuery) (usage.AnalyticsHeatmap, error)
	GetAnalyticsBreakdown(
		context.Context,
		auth.UserPrincipal,
		usage.AnalyticsQuery,
	) (usage.AnalyticsBreakdown, error)
	GetAnalyticsFailures(context.Context, auth.UserPrincipal, usage.AnalyticsQuery) (usage.AnalyticsFailures, error)
	GetTaskUsage(context.Context, auth.UserPrincipal, uuid.UUID) (usage.AnalyticsTaskUsage, error)
	GetAnalyticsBudgets(context.Context, auth.UserPrincipal) ([]usage.AnalyticsBudget, error)
	UpsertAnalyticsBudget(
		context.Context,
		auth.UserPrincipal,
		usage.AnalyticsBudgetInput,
	) (usage.AnalyticsBudget, error)
}

func (h *Handler) registerUsageRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.UsageService == nil {
		return
	}
	companyRoutes := group.Group(
		"/companies/:companyId/usage",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionUsageRead),
	)
	companyRoutes.GET("", h.getCompanyUsage)
	activityRoutes := group.Group(
		"/companies/:companyId/me/activity-insight",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	activityRoutes.GET("", h.getActivityInsight)
	agentRoutes := group.Group(
		"/companies/:companyId/agents/:agentId/usage",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionUsageRead),
	)
	agentRoutes.GET("", h.getAgentUsage)
}

func (h *Handler) getActivityInsight(c *gin.Context) {
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
	insight, err := h.UsageService.GetActivityInsight(c.Request.Context(), principal)
	if err != nil {
		writeActivityInsightError(c, err)
		return
	}
	c.JSON(http.StatusOK, insight)
}

func (h *Handler) getCompanyUsage(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	period, err := usage.ParseCompanyPeriodQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	report, err := h.UsageService.GetCompany(c.Request.Context(), principal, period)
	if err != nil {
		writeUsageError(c, err)
		return
	}
	if report.Agents == nil {
		report.Agents = []usage.AgentUsage{}
	}
	if report.Models == nil {
		report.Models = []usage.ModelSlice{}
	}
	c.JSON(http.StatusOK, report)
}

func (h *Handler) getAgentUsage(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	agentID, err := util.ParseGoUUID(c.Param("agentId"))
	if err != nil {
		writeBoundaryError(c, boundaryErrorAgentNotFound)
		return
	}
	period, err := usage.ParsePeriodQuery(c.Request.URL.RawQuery)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	report, err := h.UsageService.Get(c.Request.Context(), principal, agentID, period)
	if err != nil {
		writeUsageError(c, err)
		return
	}
	if report.Series == nil {
		report.Series = []usage.SeriesPoint{}
	}
	if report.Models == nil {
		report.Models = []usage.ModelSlice{}
	}
	if report.TopTasks == nil {
		report.TopTasks = []usage.TopTaskItem{}
	}
	c.JSON(http.StatusOK, report)
}

var usageErrorMappings = []errorMapping{
	{
		target: usage.ErrInvalidInput,
		status: http.StatusUnprocessableEntity,
		detail: "request validation failed",
		code:   "validation_failed",
	},
	{
		target: usage.ErrUnauthenticated,
		status: http.StatusUnauthorized,
		detail: "authentication is required",
		code:   "authentication_required",
	},
	{
		target: usage.ErrForbidden,
		status: http.StatusForbidden,
		detail: "Your Company role does not allow this action",
		code:   "company_permission_required",
	},
	{
		target: usage.ErrNotFound,
		status: http.StatusNotFound,
		detail: "Agent not found",
		code:   "agent_not_found",
	},
}

func writeUsageError(c *gin.Context, err error) {
	writeMappedError(c, err, usageErrorMappings, internalErrorFallback)
}

var activityInsightErrorMappings = []errorMapping{
	{
		target: usage.ErrUnauthenticated,
		status: http.StatusUnauthorized,
		detail: "authentication is required",
		code:   "authentication_required",
	},
	{
		target: usage.ErrActivityInsightForbidden,
		status: http.StatusForbidden,
		detail: "Company access is required",
		code:   "company_access_required",
	},
}

func writeActivityInsightError(c *gin.Context, err error) {
	writeMappedError(c, err, activityInsightErrorMappings, internalErrorFallback)
}
