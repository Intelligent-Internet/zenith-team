package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/middleware"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/task"
)

type CompanyPlanService interface {
	ApplyCompanyPlan(context.Context, auth.UserPrincipal, company.PlanArtifact) (company.PlanApplyResult, error)
}

func (h *Handler) registerCompanyPlanRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil ||
		h.CompanyPlanService == nil {
		return
	}
	companyPlan := group.Group(
		"/companies/:companyId/company-plan",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	companyPlan.POST("/apply", h.applyCompanyPlan)
}

func (h *Handler) applyCompanyPlan(c *gin.Context) {
	principal, ok := companyPlanPrincipal(c)
	if !ok {
		return
	}
	origin, requestKey, runTokenRequest, ok := h.resolveHITLRaise(
		c, principal, approval.ErrApprovalInvalidInput,
	)
	if !ok {
		return
	}
	// A person applies under company.plan.apply; a Run raises an Approval
	// instead. The gate answers before the body is read, like every route
	// that declares its permission as middleware.
	if !runTokenRequest && !principal.Can(auth.PermissionCompanyPlanApply) {
		writeCompanyError(c, company.ErrForbidden)
		return
	}
	artifact, err := bindRequest[company.PlanArtifact](c, 16<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if runTokenRequest {
		if h.ApprovalService == nil {
			writeApprovalError(c, approval.ErrApprovalConfiguration)
			return
		}
		payload, err := json.Marshal(artifact)
		if err != nil {
			writeApprovalError(c, approval.ErrApprovalInvalidInput)
			return
		}
		// 202 answers both first creation and exact replay with the bare
		// durable Approval receipt; the request was accepted, not applied.
		result, _, err := h.ApprovalService.RequestCompanyPlanApply(
			c.Request.Context(), principal, origin, requestKey, payload,
		)
		if err != nil {
			writeApprovalError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, result)
		return
	}
	result, err := h.CompanyPlanService.ApplyCompanyPlan(c.Request.Context(), principal, artifact)
	if err != nil {
		writeCompanyPlanError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func companyPlanPrincipal(c *gin.Context) (auth.UserPrincipal, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeCompanyPlanError(c, company.ErrUnauthenticated)
	}
	return principal, ok
}

var companyPlanErrorMappings = append([]errorMapping{
	{company.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{company.ErrPlanInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{
		company.ErrPlanConflict,
		http.StatusConflict,
		"Work Plan mutation conflicts with current state",
		"work_plan_conflict",
	},
	{company.ErrPlanNotFound, http.StatusNotFound, "Work Plan entity not found", "work_plan_not_found"},
	{
		company.ErrForbidden,
		http.StatusForbidden,
		"Your Company role does not allow this action",
		"company_permission_required",
	},
}, companyPlanTaskAuthorityMappings...)

// companyPlanTaskAuthorityMappings are the Task rules a Company Plan apply
// keeps on top of project.tasks.write, answered as a Task PATCH answers
// them (ADR 0014, ADR 0017).
var companyPlanTaskAuthorityMappings = []errorMapping{
	{
		task.ErrHandoffPermissionRequired,
		http.StatusForbidden,
		"Only the task's assignee, owners and admins can change who works on it.",
		"company_permission_required",
	},
	{
		task.ErrReviewPermissionRequired,
		http.StatusForbidden,
		"Only the task's assignee, owners and admins can move it out of review.",
		"company_permission_required",
	},
	{
		company.ErrPlanTaskDeletePermissionRequired,
		http.StatusForbidden,
		"Only the task's assignee, owners and admins can delete it.",
		"company_permission_required",
	},
}

func writeCompanyPlanError(c *gin.Context, err error) {
	writeMappedError(c, err, companyPlanErrorMappings, internalErrorFallback)
}
