package handler

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/studiocanvas"
)

const maxDeliverableBodyBytes = 128 << 10

type DeliverableService interface {
	ReadArtifactForRun(
		context.Context, auth.UserPrincipal, execution.RunOrigin, uuid.UUID,
	) (execution.DeliverableOutput, error)
	AttachStudioCanvas(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		studiocanvas.Canvas,
	) (execution.SavedStudioCanvas, error)
	Present(
		context.Context,
		auth.UserPrincipal,
		execution.RunOrigin,
		execution.DeliverablePresentRequest,
	) (execution.DeliverablePresentResult, error)
	FetchRunResult(context.Context, auth.UserPrincipal, uuid.UUID) (execution.DeliverableRunResult, error)
	FetchRunArtifactResult(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
	) (execution.DeliverableRunResult, error)
	ListTaskResults(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		execution.TaskResultListOptions,
	) (execution.TaskResultPage, error)
	ListConversationArtifacts(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		execution.ArtifactListOptions,
	) (execution.ConversationArtifactPage, error)
	ListArtifacts(
		context.Context,
		auth.UserPrincipal,
		execution.CompanyArtifactListOptions,
	) (execution.ArtifactPage, error)
	ResolveArtifacts(context.Context, auth.UserPrincipal, []uuid.UUID) ([]execution.ArtifactRunResult, error)
	ResolveArtifactFileAttachment(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		int,
	) (uuid.UUID, error)
	SetArtifactVisibility(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		execution.ArtifactVisibilityInput,
	) (execution.ArtifactAccess, error)
	RevokeArtifactConversation(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		execution.ArtifactConversationInput,
	) (execution.ArtifactAccess, error)
	GetPublicArtifact(context.Context, uuid.UUID) (execution.PublicArtifact, error)
	ResolvePublicArtifactFile(context.Context, uuid.UUID, int) (uuid.UUID, uuid.UUID, error)
	GetArtifact(context.Context, auth.UserPrincipal, uuid.UUID) (execution.ArtifactDetail, error)
	ListUnownedArtifacts(context.Context, auth.UserPrincipal) ([]execution.UnownedArtifact, error)
	ClaimUnownedArtifact(context.Context, auth.UserPrincipal, uuid.UUID, int64) (execution.ArtifactDetail, error)
	ChangeArtifactGrant(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		*execution.ArtifactPermission,
		int64,
	) (execution.ArtifactAccess, error)
}

type deliverablePresentItemRequest struct {
	Role           execution.DeliverableItemRole `json:"role"           binding:"required,oneof=primary final supporting"`
	Label          string                        `json:"label"          binding:"required,max=120"`
	AssetVersionID *uuid.UUID                    `json:"assetVersionId" binding:"required_without=AttachmentID,excluded_with=AttachmentID"`
	AttachmentID   *uuid.UUID                    `json:"attachmentId"   binding:"required_without=AssetVersionID,excluded_with=AssetVersionID"`
}

type deliverablePresentAppCommandRequest struct {
	Command          string   `json:"command"                    binding:"required,max=4096"`
	Args             []string `json:"args,omitempty"             binding:"max=64,dive,max=4096"`
	WorkingDirectory string   `json:"workingDirectory,omitempty" binding:"omitempty,max=4096"`
	Port             int      `json:"port"                       binding:"required,min=1024,max=65535"`
}

type deliverablePresentAppRequest struct {
	Commands []deliverablePresentAppCommandRequest `json:"commands" binding:"required,min=1,max=8,dive"`
}

type deliverablePresentRequest struct {
	ArtifactID      *uuid.UUID                      `json:"artifactId,omitempty"      binding:"omitempty,excluded_with=PresentationID"`
	PresentationID  *uuid.UUID                      `json:"presentationId,omitempty"  binding:"omitempty"`
	Type            execution.ArtifactType          `json:"type,omitempty"            binding:"omitempty,oneof=app media text file"`
	App             *deliverablePresentAppRequest   `json:"app,omitempty"             binding:"required_if=Type app,excluded_unless=Type app"`
	Title           string                          `json:"title"                     binding:"required,max=160"`
	Summary         string                          `json:"summary"                   binding:"required,max=4000"`
	SubmissionNotes *string                         `json:"submissionNotes,omitempty" binding:"omitempty,max=4000"`
	Items           []deliverablePresentItemRequest `json:"items,omitempty"           binding:"excluded_if=Type app,required_unless=Type app,omitempty,min=1,max=32,dive"`
}

type deliverablePresentResponse struct {
	ArtifactID *uuid.UUID `json:"artifactId,omitempty"`
	SnapshotID *uuid.UUID `json:"snapshotId,omitempty"`
	OutputID   uuid.UUID  `json:"outputId"`
}

type artifactShareResponse struct {
	URL string `json:"url"`
}

func (h *Handler) registerDeliverableRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.DeliverableService == nil || h.RunProofResolver == nil {
		return
	}
	group.GET("/public/artifacts/:token", h.getPublicArtifact)
	if h.AttachmentService != nil {
		group.GET("/public/artifacts/:token/items/:ordinal/download", h.downloadPublicArtifact)
	}
	if h.CompanyService == nil {
		return
	}
	runs := group.Group("/runs", middleware.RequireUserAuth(h.AuthService))
	runs.POST("/:runId/output", h.presentRunOutput)
	company := group.Group(
		"/companies/:companyId",
		middleware.NoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	company.PUT("/artifacts/:artifactId/visibility", middleware.RequireHumanActor(), h.setArtifactVisibility)
	company.DELETE(
		"/artifacts/:artifactId/conversations/:conversationId",
		middleware.RequireHumanActor(),
		h.revokeArtifactConversation,
	)
	company.GET("/runs/:runId/result", h.fetchRunResult)
	company.POST("/runs/:runId/result/studio-canvas", h.attachStudioCanvas)
	company.GET("/tasks/:taskId/results", h.listTaskResults)
	company.POST("/tasks/:taskId/results", h.finalizeTaskResults)
	company.GET("/conversations/:conversationId/artifacts", h.listConversationArtifacts)
	company.GET("/artifacts/unowned", middleware.RequireCompanyPermission(h.CompanyService,
		auth.PermissionResultsReview), h.listUnownedArtifacts)
	company.GET("/artifacts", h.listArtifacts)
	company.GET("/artifacts/resolve", h.resolveArtifacts)
	company.GET("/artifacts/:artifactId", h.fetchArtifact)
	company.GET("/runs/:runId/agent-artifacts/:artifactId", h.inspectArtifactForRun)
	company.POST(
		"/artifacts/:artifactId/claim",
		middleware.RequireHumanActor(),
		middleware.RequireCompanyPermission(h.CompanyService,
			auth.PermissionResultsReview),
		h.claimUnownedArtifact,
	)
	company.PUT("/artifacts/:artifactId/grants/:userId", middleware.RequireHumanActor(), h.putArtifactGrant)
	company.DELETE("/artifacts/:artifactId/grants/:userId", middleware.RequireHumanActor(), h.deleteArtifactGrant)
	if h.AttachmentService != nil {
		company.GET(
			"/runs/:runId/agent-artifacts/:artifactId/items/:ordinal/download",
			h.downloadArtifactForRun,
		)
		company.GET("/artifacts/:artifactId/items/:ordinal/download", h.downloadArtifactFile)
		company.GET(
			"/runs/:runId/artifacts/:artifactId/items/:ordinal/download",
			h.downloadArtifactItem,
		)
		company.POST(
			"/runs/:runId/artifacts/:artifactId/items/:ordinal/share",
			h.shareArtifactItem,
		)
	}
}

func (h *Handler) presentRunOutput(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	runID, ok := parseRunPathID(c.Param("runId"))
	if !ok {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	if _, err := util.ParseGoUUID(c.GetHeader(idempotencyHeader)); err != nil {
		writeDeliverableError(c, execution.ErrDeliverableInvalidInput)
		return
	}
	token := c.GetHeader(runTokenHeader)
	if token == "" {
		writeDeliverableError(c, execution.ErrDeliverableForbidden)
		return
	}
	origin, err := h.resolveRunOrigin(c, token)
	if err != nil {
		if errors.Is(err, execution.ErrRunProofInvalid) {
			writeDeliverableError(c, execution.ErrDeliverableForbidden)
			return
		}
		writeDeliverableError(c, err)
		return
	}
	if origin.RunID != runID || origin.AuthorizedByUserID == nil || *origin.AuthorizedByUserID != principal.UserID {
		writeDeliverableError(c, execution.ErrDeliverableForbidden)
		return
	}
	// This Run-scoped URL has no Company middleware. Resolve membership from
	// the verified proof before passing the canonical principal to the service.
	principal, err = h.CompanyService.Authorize(c.Request.Context(), principal, origin.CompanyID)
	if err != nil {
		writeCompanyError(c, err)
		return
	}
	if !runProofBearer(principal, origin) {
		writeDeliverableError(c, execution.ErrDeliverableForbidden)
		return
	}
	request, err := bindRequest[deliverablePresentRequest](c, maxDeliverableBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	items := make([]execution.DeliverablePresentItemRequest, len(request.Items))
	for index, item := range request.Items {
		items[index] = execution.DeliverablePresentItemRequest{
			Role: item.Role, Label: item.Label,
			AssetVersionID: item.AssetVersionID, AttachmentID: item.AttachmentID,
		}
	}
	var app *execution.DeliverablePresentAppRequest
	if request.App != nil {
		commands := make([]execution.AppPreviewCommand, len(request.App.Commands))
		for index, command := range request.App.Commands {
			commands[index] = execution.AppPreviewCommand{
				Command: command.Command, Args: command.Args,
				WorkingDirectory: command.WorkingDirectory, Port: command.Port,
			}
		}
		app = &execution.DeliverablePresentAppRequest{Commands: commands}
	}
	result, err := h.DeliverableService.Present(
		c.Request.Context(),
		principal,
		origin,
		execution.DeliverablePresentRequest{
			ArtifactID:     request.ArtifactID,
			PresentationID: request.PresentationID,
			Type:           request.Type, App: app,
			Title: request.Title, Summary: request.Summary,
			SubmissionNotes: request.SubmissionNotes, Items: items,
		},
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(
		status,
		deliverablePresentResponse{
			OutputID:   result.OutputID,
			ArtifactID: result.ArtifactID,
			SnapshotID: result.SnapshotID,
		},
	)
}

func (h *Handler) fetchRunResult(c *gin.Context) {
	principal, runID, ok := deliverableRunRequest(c)
	if !ok {
		return
	}
	var (
		result     execution.DeliverableRunResult
		artifactID *uuid.UUID
		err        error
	)
	if value := c.Query("artifactId"); value != "" {
		parsed, parseErr := util.ParseGoUUID(value)
		if parseErr != nil {
			writeDeliverableError(c, execution.ErrDeliverableNotFound)
			return
		}
		artifactID = &parsed
		result, err = h.DeliverableService.FetchRunArtifactResult(
			c.Request.Context(), principal, runID, parsed,
		)
	} else {
		result, err = h.DeliverableService.FetchRunResult(c.Request.Context(), principal, runID)
	}
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	if artifactID == nil && result.Output != nil && result.Output.URL == nil &&
		h.PublicationService != nil {
		publication, publicationErr := h.PublicationService.Fetch(
			c.Request.Context(), principal, runID,
		)
		if publicationErr != nil {
			writePublicationError(c, publicationErr)
			return
		}
		result.Publication = publication.Publication
		result.CanPublish = publication.CanPublish
		result.CanUnpublish = publication.CanUnpublish
		result.PublicationSubdomain = publication.Subdomain
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) downloadArtifactItem(c *gin.Context) {
	principal, runID, ok := deliverableRunRequest(c)
	if !ok {
		return
	}
	artifactID, err := util.ParseGoUUID(c.Param("artifactId"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	ordinal, err := strconv.Atoi(c.Param("ordinal"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	attachmentID, err := h.DeliverableService.ResolveArtifactFileAttachment(
		c.Request.Context(), principal, runID, artifactID, ordinal,
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	h.downloadResolvedArtifact(c, principal.CompanyID, attachmentID)
}

func (h *Handler) shareArtifactItem(c *gin.Context) {
	principal, runID, ok := deliverableRunRequest(c)
	if !ok {
		return
	}
	artifactID, err := util.ParseGoUUID(c.Param("artifactId"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	ordinal, err := strconv.Atoi(c.Param("ordinal"))
	if err != nil {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	_, err = h.DeliverableService.ResolveArtifactFileAttachment(
		c.Request.Context(), principal, runID, artifactID, ordinal,
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	if h.OAuthWebAppOrigin == "" {
		writeDeliverableError(c, execution.ErrDeliverableInvalidConfiguration)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, artifactShareResponse{
		URL: strings.TrimRight(h.OAuthWebAppOrigin, "/") +
			"/companies/" + principal.CompanyID.String() + "/artifacts/" +
			artifactID.String() + "?item=" + strconv.Itoa(ordinal),
	})
}

func deliverableRunRequest(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	runID, ok := parseRunPathID(c.Param("runId"))
	if !ok {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, runID, true
}

var deliverableErrorMappings = []errorMapping{
	runTokenInvalidMapping,
	{
		execution.ErrDeliverableInvalidInput,
		http.StatusUnprocessableEntity,
		"request validation failed",
		"validation_failed",
	},
	{execution.ErrDeliverableForbidden, http.StatusForbidden, "Artifact action is not permitted", "forbidden"},
	{execution.ErrDeliverableNotFound, http.StatusNotFound, "Run result not found", "not_found"},
	{execution.ErrArtifactNotFound, http.StatusNotFound, "Conversation Artifacts not found", "not_found"},
	{
		execution.ErrDeliverableConflict,
		http.StatusConflict,
		"the Run result changed; refresh and try again",
		"conflict",
	},
	{
		execution.ErrDeliverableTargetUnavailable,
		http.StatusUnprocessableEntity,
		"a submitted result item is unavailable",
		"target_unavailable",
	},
	projectPermissionRequiredMapping,
	noEligibleResolverMapping,
}

func writeDeliverableError(c *gin.Context, err error) {
	writeMappedError(
		c,
		err,
		slices.Concat(deliverableErrorMappings, publicationErrorMappings),
		errorFallback{detail: "Run result is unavailable", code: "deliverable_unavailable"},
	)
}

func (h *Handler) attachStudioCanvas(c *gin.Context) {
	principal, runID, ok := deliverableRunRequest(c)
	if !ok {
		return
	}
	request, err := bindRequest[studioCanvasRequest](c, (1<<20)+4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.DeliverableService.AttachStudioCanvas(c.Request.Context(), principal, runID, request.Canvas)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

type studioCanvasRequest struct {
	Canvas studiocanvas.Canvas `json:"canvas" binding:"required"`
}
