package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/realtime"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/internal/wiki"
	"boardly.dev/boardly/server/pkg/protocol"
)

func (h *Handler) registerCanvasRoutes(api *gin.RouterGroup) {
	if h.CanvasService == nil || h.PageService == nil || h.AuthService == nil || h.CompanyService == nil {
		return
	}
	outputs := api.Group(
		"/companies/:companyId/artifacts/:artifactId/review",
		middleware.NoStore(),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		middleware.RequireHumanActor(),
	)
	outputs.GET("", h.outputReview)
	outputs.PUT("/items/:outputItemId/document/presence", h.outputDocumentPresence)
	outputs.POST("/items/:outputItemId/document/checkpoints", h.checkpointOutputDocument)
	outputs.GET("/versions/:versionId/body", h.outputDocumentVersion)
	outputs.POST("/operations", h.mutateOutputReview)
	outputs.POST("/items/:outputItemId/document/open", h.openOutputDocument)
	outputs.GET("/items/:outputItemId/document/history", h.documentHistory)
	outputs.GET("/items/:outputItemId/document/history/:sequence", h.documentHistoryState)
	outputs.POST("/items/:outputItemId/document/history/:sequence/restore", h.restoreDocumentHistory)
	outputs.PUT("/items/:outputItemId/document/history/:sequence/name", h.nameDocumentHistory)
	outputs.GET("/items/:outputItemId/document", h.readOutputDocument)
	outputs.POST("/items/:outputItemId/document", h.syncOutputDocument)
	outputs.POST("/items/:outputItemId/document/ids", h.reserveOutputBlockIDs)
	routes := api.Group(
		"/companies/:companyId/projects/:projectId/canvas",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		middleware.RequireHumanActor(),
	)
	edit := middleware.RequireProjectPermission(h.ProjectService, auth.ProjectPermissionTasksWrite)
	routes.GET("", h.getCanvas)
	routes.GET("/timeline", h.canvasTimeline)
	routes.GET("/library", h.canvasLibrary)
	routes.GET("/search", h.searchCanvas)
	routes.POST("/operations", h.mutateCanvas)
	routes.GET("/artifacts/:artifactId/history", h.canvasHistory)
	routes.GET("/versions/:versionId/threads", h.canvasThreads)
	routes.GET("/versions/:versionId/body", h.canvasBody)
	routes.GET("/versions/:versionId/document/history", h.documentHistory)
	routes.GET("/versions/:versionId/document/history/:sequence", h.documentHistoryState)
	routes.POST("/versions/:versionId/document/history/:sequence/restore", edit, h.restoreDocumentHistory)
	routes.PUT("/versions/:versionId/document/history/:sequence/name", edit, h.nameDocumentHistory)
	routes.GET("/versions/:versionId/document", h.canvasDocument)
	routes.POST("/versions/:versionId/document", edit, h.syncCanvasDocument)
	routes.POST("/versions/:versionId/document/ids", edit, h.reserveCanvasBlockIDs)
	routes.PUT("/presence", h.canvasPresence)
}

func canvasPath(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	p, ok := taskPrincipal(c)
	if !ok {
		return p, uuid.Nil, false
	}
	id, err := util.ParseGoUUID(c.Param("projectId"))
	if err != nil {
		writeCanvasError(c, canvas.ErrInvalid)
		return p, uuid.Nil, false
	}
	c.Header("Cache-Control", "private, no-store")
	return p, id, true
}

func (h *Handler) canvasLibrary(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	result, err := h.CanvasService.Library(c.Request.Context(), p, project)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) searchCanvas(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	result, err := h.CanvasService.Search(c.Request.Context(), p, project, c.Query("query"))
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Items []canvas.SearchMatch `json:"items"`
	}{result})
}

func canvasID(c *gin.Context, param string) (uuid.UUID, bool) {
	id, err := util.ParseGoUUID(c.Param(param))
	if err != nil {
		writeCanvasError(c, canvas.ErrInvalid)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) getCanvas(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	since := int64(0)
	if raw := c.Query("after_seq"); raw != "" {
		var err error
		since, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || since < 0 {
			writeCanvasError(c, canvas.ErrInvalid)
			return
		}
	}
	result, err := h.CanvasService.Snapshot(c.Request.Context(), p, project, since)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) mutateCanvas(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.Command](c, 3<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.CanvasService.Mutate(c.Request.Context(), p, project, input)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) canvasHistory(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "artifactId")
	if !ok {
		return
	}
	result, err := h.CanvasService.History(c.Request.Context(), p, project, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) canvasThreads(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "versionId")
	if !ok {
		return
	}
	result, err := h.CanvasService.Threads(c.Request.Context(), p, project, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Items []canvas.Thread `json:"items"`
	}{result})
}

func (h *Handler) canvasBody(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "versionId")
	if !ok {
		return
	}
	result, err := h.CanvasService.VersionBody(c.Request.Context(), p, project, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	content, err := h.PageService.VersionBlocks(c.Request.Context(), p, project, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Body    string          `json:"body"`
		Content json.RawMessage `json:"content,omitempty"`
	}{result, content})
}

func (h *Handler) canvasDocument(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "versionId")
	if !ok {
		return
	}
	input, ok := documentReadRequest(c)
	if !ok {
		return
	}
	input.VersionID = id
	projection := input.Projection
	vector := input.StateVector
	result, err := h.PageService.ReadDocument(c.Request.Context(), p, project, input)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	if c.Query("after_seq") == strconv.FormatInt(result.Sequence, 10) {
		result.State = nil
		result.Markdown = ""
	}
	if !projection && len(vector) > 0 {
		result.Content = nil
		result.Markdown = ""
	}
	c.JSON(http.StatusOK, result)
}

type blockDocumentRequest struct {
	BaseSequence  int64  `json:"baseSequence"  binding:"gte=0"`
	Update        []byte `json:"update"        binding:"required"`
	SchemaVersion int    `json:"schemaVersion" binding:"required,eq=1"`
	StateVector   []byte `json:"stateVector"   binding:"max=65536"`
}

func (h *Handler) syncCanvasDocument(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "versionId")
	if !ok {
		return
	}
	input, err := bindRequest[blockDocumentRequest](c, 12<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !wiki.ValidBlockStateVector(input.StateVector) {
		writeCanvasError(c, canvas.ErrInvalid)
		return
	}
	result, err := h.PageService.SyncDocument(
		c.Request.Context(),
		p,
		project,
		id,
		input.Update,
		input.BaseSequence,
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	result.State, result.StateVector, err = wiki.BlockStateDelta(result.State, input.StateVector)
	if err != nil {
		writeCanvasError(c, canvas.ErrInvalid)
		return
	}
	if len(input.StateVector) > 0 {
		result.Content = nil
		result.Markdown = ""
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) reserveCanvasBlockIDs(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	id, ok := canvasID(c, "versionId")
	if !ok {
		return
	}
	input, err := bindRequest[struct {
		Count int32 `json:"count" binding:"required,min=1,max=256"`
	}](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	ids, err := h.PageService.ReserveBlockIDs(c.Request.Context(), p, project, id, input.Count)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		IDs []uuid.UUID `json:"ids"`
	}{ids})
}

var canvasErrorMappings = []errorMapping{
	projectPermissionRequiredMapping,
	{
		wiki.ErrArtifactRefUnavailable,
		http.StatusUnprocessableEntity,
		"A cited Artifact is missing or you cannot open it",
		"wiki_artifact_ref_unavailable",
	},
	{
		wiki.ErrArtifactRefLimit,
		http.StatusUnprocessableEntity,
		"A Wiki page can cite at most 50 Artifacts",
		"wiki_artifact_ref_limit",
	},
	{canvas.ErrNotFound, http.StatusNotFound, "Canvas or source is unavailable", "canvas_not_found"},
	{
		canvas.ErrForbidden,
		http.StatusForbidden,
		"You do not have permission to change this canvas content",
		"canvas_forbidden",
	},
	{canvas.ErrInvalid, http.StatusUnprocessableEntity, "Invalid canvas operation", "canvas_invalid"},
	{canvas.ErrPageInvalidInsert, http.StatusUnprocessableEntity, "Invalid inserted block or destination. Use the native block shape from page_read. Headings and paragraphs cannot contain blocks: omit parentId for document-level siblings, and use afterId or beforeId to position them.", "page_invalid_insert"},
	{canvas.ErrConflict, http.StatusConflict, "The work changed. Refresh and try again.", "canvas_conflict"},
	{
		canvas.ErrLimit,
		http.StatusUnprocessableEntity,
		"This canvas operation exceeds the supported size",
		"canvas_limit",
	},
}

func writeCanvasError(c *gin.Context, err error) {
	writeMappedError(c, err, canvasErrorMappings, internalErrorFallback)
}

type canvasPresenceRequest struct {
	DocumentVersionID *uuid.UUID               `json:"documentVersionId"`
	Cursor            *realtime.DocumentCursor `json:"cursor"`
	SessionID         uuid.UUID                `json:"sessionId"         binding:"required"`
	X                 float64                  `json:"x"                 binding:"gte=-1000000,lte=1000000"`
	Y                 float64                  `json:"y"                 binding:"gte=-1000000,lte=1000000"`
	CameraX           float64                  `json:"cameraX"           binding:"gte=-1000000,lte=1000000"`
	CameraY           float64                  `json:"cameraY"           binding:"gte=-1000000,lte=1000000"`
	Zoom              float64                  `json:"zoom"              binding:"gte=0.05,lte=4"`
	Pointing          bool                     `json:"pointing"`
	Presenting        bool                     `json:"presenting"`
	Leave             bool                     `json:"leave"`
}

func (h *Handler) canvasPresence(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	request, err := bindRequest[canvasPresenceRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	name, err := h.CanvasService.Viewer(c.Request.Context(), p, project)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	room := p.CompanyID.String() + ":" + project.String()
	if request.DocumentVersionID != nil {
		doc, e := h.PageService.Document(c.Request.Context(), p, project, *request.DocumentVersionID, 0)
		if e != nil {
			writeCanvasError(c, e)
			return
		}
		room = p.CompanyID.String() + ":document:" + doc.PageID.String()
		if request.Cursor != nil &&
			(request.Cursor.ClientID > 4294967295 || len(request.Cursor.Anchor) > 512 || len(request.Cursor.Head) > 512) {
			writeCanvasError(c, canvas.ErrInvalid)
			return
		}
	} else if request.Cursor != nil {
		writeCanvasError(c, canvas.ErrInvalid)
		return
	}
	h.writeCanvasPresence(c, canvasPresenceActor{principal: p, room: room, name: name, request: request})
}

type canvasPresenceActor struct {
	principal  auth.UserPrincipal
	room, name string
	request    canvasPresenceRequest
}

func (h *Handler) writeCanvasPresence(c *gin.Context, actor canvasPresenceActor) {
	p, room, name, request := actor.principal, actor.room, actor.name, actor.request
	if h.CanvasPresence == nil {
		c.JSON(http.StatusOK, struct {
			Items []realtime.CanvasSession `json:"items"`
		}{[]realtime.CanvasSession{}})
		return
	}
	result, err := h.CanvasPresence.Update(
		c.Request.Context(),
		room,
		realtime.CanvasSession{
			Actor: protocol.ActorRef{
				ID:          p.UserID,
				Kind:        protocol.ActorKindUser,
				DisplayName: name,
			},
			Cursor:     request.Cursor,
			SessionID:  request.SessionID.String(),
			UserID:     p.UserID.String(),
			Name:       name,
			X:          request.X,
			Y:          request.Y,
			CameraX:    request.CameraX,
			CameraY:    request.CameraY,
			Zoom:       request.Zoom,
			Pointing:   request.Pointing,
			Presenting: request.Presenting,
		},
		request.Leave,
	)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Items []realtime.CanvasSession `json:"items"`
	}{result})
}

func (h *Handler) canvasTimeline(c *gin.Context) {
	p, project, ok := canvasPath(c)
	if !ok {
		return
	}
	result, err := h.CanvasService.Timeline(c.Request.Context(), p, project, c.Query("before"))
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) outputReview(c *gin.Context) {
	p, id, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	result, err := h.CanvasService.OutputReview(c.Request.Context(), p, id)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) mutateOutputReview(c *gin.Context) {
	p, id, ok := artifactPathRequest(c)
	if !ok {
		return
	}
	input, err := bindRequest[canvas.Command](c, 64<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.CanvasService.MutateOutputReview(c.Request.Context(), p, id, input)
	if err != nil {
		writeCanvasError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func documentReadRequest(c *gin.Context) (canvas.DocumentReadInput, bool) {
	since := int64(-1)
	if raw := c.Query("after_seq"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeCanvasError(c, canvas.ErrInvalid)
			return canvas.DocumentReadInput{}, false
		}
		since = value
	}
	var vector []byte
	if raw := c.Query("state_vector"); raw != "" {
		var err error
		vector, err = base64.StdEncoding.DecodeString(raw)
		if err != nil || len(vector) > 65536 {
			writeCanvasError(c, canvas.ErrInvalid)
			return canvas.DocumentReadInput{}, false
		}
	}
	projection := c.Query("projection") == "true"
	if !wiki.ValidBlockStateVector(vector) {
		writeCanvasError(c, canvas.ErrInvalid)
		return canvas.DocumentReadInput{}, false
	}
	return canvas.DocumentReadInput{
		Since:       since,
		Projection:  projection,
		StateVector: vector,
		BlockID:     c.Query("block_id"),
		Preview:     c.Query("preview") == "true",
	}, true
}
