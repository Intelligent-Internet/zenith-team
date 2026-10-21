package handler

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
)

const maxMilestoneBodyBytes = 16 << 10

// MilestoneService records what the founder says was finished before Zenith
// Team. There is no read surface: the only consumer is the Agent, and it
// receives attestations in its frozen Run brief.
type MilestoneService interface {
	AttestMilestones(
		context.Context,
		auth.UserPrincipal,
		[]task.MilestoneType,
		string,
	) ([]company.MilestoneAttestation, error)
}

type attestMilestonesRequest struct {
	Milestones []task.MilestoneType `json:"milestones"`
	Note       string               `json:"note"`
}

type attestMilestonesResponse struct {
	Attested []task.MilestoneType `json:"attested"`
}

func (h *Handler) registerMilestoneRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.MilestoneService == nil {
		return
	}
	// Two gates, because this records a person's statement but is written by an
	// Agent: the session must be the Company owner, and the request must prove a
	// live Run in that same Company. Neither alone is enough.
	group.POST(
		"/companies/:companyId/milestone-attestations",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionCompanyPlanApply),
		h.attestMilestones,
	)
}

func (h *Handler) attestMilestones(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	request, err := bindRequest[attestMilestonesRequest](c, maxMilestoneBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !h.attestationRunProven(c, principal) {
		return
	}
	attested, err := h.MilestoneService.AttestMilestones(
		c.Request.Context(),
		principal,
		request.Milestones,
		request.Note,
	)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	milestones := make([]task.MilestoneType, 0, len(attested))
	for _, attestation := range attested {
		milestones = append(milestones, attestation.MilestoneType)
	}
	c.JSON(http.StatusOK, attestMilestonesResponse{Attested: milestones})
}

// attestationRunProven requires the caller to hold a live Run token for this
// Company. An owner session alone would let any client assert the founder's
// history outside the conversation where the founder was actually asked.
func (h *Handler) attestationRunProven(c *gin.Context, principal auth.UserPrincipal) bool {
	if h.RunProofResolver == nil {
		writeCompanyError(c, company.ErrForbidden)
		return false
	}
	token := c.GetHeader(runTokenHeader)
	if token == "" {
		writeCompanyError(c, company.ErrForbidden)
		return false
	}
	origin, err := h.resolveRunOrigin(c, token)
	if err != nil || !runProofBearer(principal, origin) {
		writeCompanyError(c, execution.ErrRunProofInvalid)
		return false
	}
	return true
}
