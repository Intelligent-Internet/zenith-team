package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
)

type configureHuddleBudgetRequest struct {
	MonthlyLimitUSDTicks string `json:"monthlyLimitUsdTicks" binding:"required,number,max=16"`
	ExpectedRevision     *int64 `json:"expectedRevision"     binding:"required,gte=0,lte=9007199254740990"`
}

func (h *Handler) registerHuddleBudgetRoutes(api *gin.RouterGroup) {
	if h.HuddleBudgetService == nil || h.AuthService == nil || h.CompanyService == nil {
		return
	}
	group := api.Group(
		"/companies/:companyId/huddle-budget",
		middleware.PrivateNoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireHumanActor(),
	)
	group.GET(
		"",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionUsageRead),
		h.getHuddleBudget,
	)
	group.PUT(
		"",
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionCompanyProfileUpdate),
		h.configureHuddleBudget,
	)
}

func (h *Handler) getHuddleBudget(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.HuddleBudgetService.Get(c.Request.Context(), principal)
	if err != nil {
		writeHuddleBudgetError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) configureHuddleBudget(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	body, err := bindRequest[configureHuddleBudgetRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	ticks, err := strconv.ParseInt(body.MonthlyLimitUSDTicks, 10, 64)
	if err != nil || ticks < 0 || ticks > 9007199254740991 {
		writeHuddleBudgetError(c, usage.ErrHuddleBudgetInvalid)
		return
	}
	result, err := h.HuddleBudgetService.Configure(
		c.Request.Context(),
		principal,
		usage.ConfigureHuddleBudgetInput{MonthlyLimitUSDTicks: ticks, ExpectedRevision: *body.ExpectedRevision},
	)
	if err != nil {
		writeHuddleBudgetError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

var huddleBudgetErrorMappings = []errorMapping{
	{
		usage.ErrHuddleBudgetInvalid,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{
		usage.ErrHuddleBudgetForbidden,
		http.StatusForbidden,
		"Huddle budget access is not allowed.",
		"huddle_budget_forbidden",
	},
	{
		usage.ErrHuddleBudgetConflict,
		http.StatusConflict,
		"Huddle budget changed. Reload and retry.",
		"huddle_budget_conflict",
	},
	{
		usage.ErrHuddleBudgetExhausted,
		http.StatusConflict,
		"Huddle AI budget is unavailable or exhausted.",
		"huddle_budget_exhausted",
	},
}

func writeHuddleBudgetError(c *gin.Context, err error) {
	writeMappedError(c, err, huddleBudgetErrorMappings, internalErrorFallback)
}
