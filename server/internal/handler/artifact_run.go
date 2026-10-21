package handler

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

func (h *Handler) artifactRunOrigin(c *gin.Context, p auth.UserPrincipal) (execution.RunOrigin, bool) {
	id, err := util.ParseGoUUID(c.Param("runId"))
	if err != nil || c.GetHeader(runTokenHeader) == "" {
		writeDeliverableError(c, execution.ErrRunProofInvalid)
		return execution.RunOrigin{}, false
	}
	origin, ok := h.runProofOrigin(c, p, c.GetHeader(runTokenHeader), writeDeliverableError)
	if !ok {
		return execution.RunOrigin{}, false
	}
	if origin.RunID != id || origin.ConversationKind == "schedule" {
		writeDeliverableError(c, execution.ErrRunProofInvalid)
		return execution.RunOrigin{}, false
	}
	return origin, true
}

func (h *Handler) inspectArtifactForRun(c *gin.Context) {
	p, id, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	origin, ok := h.artifactRunOrigin(c, p)
	if !ok {
		return
	}
	output, err := h.DeliverableService.ReadArtifactForRun(c.Request.Context(), p, origin, id)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(http.StatusOK, output)
}

func (h *Handler) downloadArtifactForRun(c *gin.Context) {
	p, id, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	origin, ok := h.artifactRunOrigin(c, p)
	if !ok {
		return
	}
	ordinal, err := strconv.Atoi(c.Param("ordinal"))
	if err != nil || ordinal < 0 {
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	output, err := h.DeliverableService.ReadArtifactForRun(c.Request.Context(), p, origin, id)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	var found bool
	for _, item := range output.Items {
		if item.Ordinal == ordinal {
			found = true
			break
		}
	}
	if !found {
		slog.InfoContext(c.Request.Context(), "run artifact read refused",
			"run_id", origin.RunID.String(),
			"artifact_id", id.String(),
			"conversation_kind", origin.ConversationKind,
			"reason", "item_not_found",
		)
		writeDeliverableError(c, execution.ErrDeliverableNotFound)
		return
	}
	// The artifact lookup resolves its source Run, which is distinct from the
	// consuming Run proven above. Never use the consumer as the source identity.
	detail, err := h.DeliverableService.GetArtifact(c.Request.Context(), p, id)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	sourceRunID := uuid.Nil
	if detail.RunID != nil {
		sourceRunID = *detail.RunID
	}
	attachmentID, err := h.DeliverableService.ResolveArtifactFileAttachment(
		c.Request.Context(),
		p,
		sourceRunID,
		id,
		ordinal,
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	h.downloadResolvedArtifact(c, p.CompanyID, attachmentID)
}
