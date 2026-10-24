package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

// maxWikiPageRequestBytes admits the largest valid page write however it is
// escaped. The limits count characters (title 240, summary 2048, body
// 262144), and the costliest valid encoding of one character is an astral
// rune sent as a \u surrogate pair: 12 bytes. 4 KiB covers the keys, the
// revision and the collection id. Nothing larger can be a valid page, so it
// stays a 413 before the service is reached.
const maxWikiPageRequestBytes = 12*(240+2048+262144) + 4<<10

type wikiPageRequest struct {
	Title        string     `json:"title"        binding:"required"`
	Body         string     `json:"body"`
	Summary      string     `json:"summary"`
	CollectionID *uuid.UUID `json:"collectionId"`
}

type wikiDraftMetadataRequest struct {
	ExpectedSequence optional[int64]  `json:"expectedSequence" binding:"omitnil,required,gte=0,max=9007199254740991"`
	Title            string           `json:"title"            binding:"required"`
	Summary          optional[string] `json:"summary"          binding:"omitnil,required"`
	CollectionID     json.RawMessage  `json:"collectionId"`
}

func (request wikiDraftMetadataRequest) domainInput() (wiki.DraftMetadataInput, error) {
	if len(request.CollectionID) == 0 {
		return wiki.DraftMetadataInput{}, errors.New("collectionId is required")
	}
	var collectionID *uuid.UUID
	if err := json.Unmarshal(request.CollectionID, &collectionID); err != nil {
		return wiki.DraftMetadataInput{}, errors.New("collectionId must be a UUID or null")
	}
	return wiki.DraftMetadataInput{
		ExpectedSequence: *request.ExpectedSequence.ptr(),
		Title:            request.Title,
		Summary:          *request.Summary.ptr(),
		CollectionID:     collectionID,
	}, nil
}

type wikiPublishRequest struct {
	ExpectedRevision optional[int64] `json:"expectedRevision" binding:"omitnil,required,min=0,max=9007199254740991"`
	ExpectedSequence optional[int64] `json:"expectedSequence" binding:"omitnil,required,gte=0,max=9007199254740991"`
}

type wikiPageRevisionRequest struct {
	ExpectedRevision optional[int64] `json:"expectedRevision" binding:"omitnil,required,min=0,max=9007199254740991"`
}

type wikiRevisionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

type wikiRestoreRevisionRequest struct {
	SchemaVersion    int             `json:"schemaVersion,omitempty" binding:"omitempty,oneof=1 2"`
	ExpectedRevision optional[int64] `json:"expectedRevision"        binding:"omitnil,required,min=0,max=9007199254740991"`
	RevisionID       uuid.UUID       `json:"revisionId"              binding:"required"`
	ExpectedSequence optional[int64] `json:"expectedSequence"        binding:"omitnil,required,gte=0,max=9007199254740991"`
}

type wikiCollectionRequest struct {
	Name string `json:"name" binding:"required"`
}

type wikiRenameCollectionRequest struct {
	Name             string `json:"name"             binding:"required"`
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
}

type wikiFavoriteRequest struct {
	Favorite optional[bool] `json:"favorite" binding:"omitnil,required"`
}

type wikiCollectionListResponse struct {
	Items []wiki.CollectionResponse `json:"items"`
}

func (h *Handler) registerWikiRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.WikiService == nil {
		return
	}
	routes := group.Group("/companies/:companyId/wiki",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
		middleware.RequireHumanActor(),
	)
	publish := middleware.RequireCompanyPermission(h.CompanyService, auth.PermissionWikiPublish)
	routes.GET("/collections", h.listWikiCollections)
	routes.POST("/collections", publish, h.createWikiCollection)
	routes.PATCH("/collections/:collectionId", publish, h.renameWikiCollection)
	routes.DELETE("/collections/:collectionId", publish, h.deleteWikiCollection)
	routes.GET("/pages", h.listWikiPages)
	routes.POST("/pages", publish, h.createWikiPage)
	routes.GET("/pages/:pageId", h.getWikiPage)
	routes.GET("/pages/:pageId/links", h.getWikiPageLinks)
	routes.GET("/pages/:pageId/document", publish, h.wikiDocument)
	routes.POST("/pages/:pageId/document", publish, h.syncWikiDocument)
	routes.POST("/pages/:pageId/document/ids", publish, h.reserveWikiBlockIDs)
	routes.PATCH("/pages/:pageId/document/metadata", publish, h.updateWikiDraftMetadata)
	routes.POST("/pages/:pageId/publish", publish, h.publishWikiPage)
	routes.POST("/pages/:pageId/archive", publish, h.archiveWikiPage)
	routes.POST("/pages/:pageId/restore", publish, h.restoreWikiPage)
	routes.GET("/pages/:pageId/revisions", h.listWikiRevisions)
	routes.POST("/pages/:pageId/restore-revision", publish, h.restoreWikiRevision)
	routes.PUT("/pages/:pageId/favorite", h.favoriteWikiPage)
	routes.GET("/graph", h.getWikiGraph)
}

func wikiID(c *gin.Context, param string) (uuid.UUID, bool) {
	id, err := util.ParseGoUUID(c.Param(param))
	if err != nil {
		writeWikiError(c, wiki.ErrPageInvalidInput)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) listWikiCollections(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	items, err := h.WikiService.ListCollections(c.Request.Context(), principal)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, wikiCollectionListResponse{Items: items})
}

func (h *Handler) createWikiCollection(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	request, err := bindRequest[wikiCollectionRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.WikiService.CreateCollection(c.Request.Context(), principal, request.Name)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (h *Handler) renameWikiCollection(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "collectionId")
	if !ok {
		return
	}
	request, err := bindRequest[wikiRenameCollectionRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.WikiService.RenameCollection(
		c.Request.Context(),
		principal,
		id,
		request.ExpectedRevision,
		request.Name,
	)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) deleteWikiCollection(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "collectionId")
	if !ok {
		return
	}
	request, err := bindRequest[wikiRevisionRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.WikiService.DeleteCollection(
		c.Request.Context(),
		principal,
		id,
		request.ExpectedRevision,
	); err != nil {
		writeWikiError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) listWikiPages(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	input := wiki.PageListInput{Search: c.Query("search"), Status: c.DefaultQuery("status", "active"), Limit: 50}
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			writeWikiError(c, wiki.ErrPageInvalidInput)
			return
		}
		input.Limit = int32(parsed)
	}
	if value := c.Query("offset"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			writeWikiError(c, wiki.ErrPageInvalidInput)
			return
		}
		input.Offset = int32(parsed)
	}
	if value := c.Query("favorites_only"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeWikiError(c, wiki.ErrPageInvalidInput)
			return
		}
		input.FavoritesOnly = parsed
	}
	if value := c.Query("collection_id"); value != "" {
		id, err := util.ParseGoUUID(value)
		if err != nil {
			writeWikiError(c, wiki.ErrPageInvalidInput)
			return
		}
		input.CollectionID = &id
	}
	result, err := h.WikiService.ListPages(c.Request.Context(), principal, input)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getWikiGraph(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	result, err := h.WikiService.GetGraph(c.Request.Context(), principal)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getWikiPage(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	result, err := h.WikiService.GetPage(c.Request.Context(), principal, id)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) getWikiPageLinks(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	result, err := h.WikiService.GetPageLinks(c.Request.Context(), principal, id)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) createWikiPage(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	request, err := bindRequest[wikiPageRequest](c, maxWikiPageRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.WikiService.CreatePage(c.Request.Context(), principal, wiki.CreatePageInput{
		Title: request.Title, Body: request.Body, Summary: request.Summary, CollectionID: request.CollectionID,
	})
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (h *Handler) updateWikiDraftMetadata(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	request, err := bindRequest[wikiDraftMetadataRequest](c, maxWikiPageRequestBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input, err := request.domainInput()
	if err != nil {
		writeBoundaryErrorDetail(c, boundaryErrorValidationFailed, err.Error())
		return
	}
	result, err := h.WikiService.UpdateDraftMetadata(c.Request.Context(), principal, id, input)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) publishWikiPage(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	request, err := bindRequest[wikiPublishRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.WikiService.PublishPage(
		c.Request.Context(),
		principal,
		id,
		wiki.PublishPageInput{
			ExpectedRevision: *request.ExpectedRevision.ptr(),
			ExpectedSequence: *request.ExpectedSequence.ptr(),
		},
	)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) wikiDocument(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	input := wiki.DocumentReadInput{Since: -1, Projection: c.Query("projection") == "true"}
	if raw := c.Query("after_seq"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			writeWikiError(c, wiki.ErrPageInvalidInput)
			return
		}
		input.Since = value
	}
	if raw := c.Query("state_vector"); raw != "" {
		vector, err := base64.StdEncoding.DecodeString(raw)
		if err != nil || len(vector) > 65536 || !wiki.ValidBlockStateVector(vector) {
			writeWikiError(c, wiki.ErrPageInvalidInput)
			return
		}
		input.StateVector = vector
	}
	result, err := h.WikiService.ReadDocument(c.Request.Context(), principal, id, input)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) syncWikiDocument(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	input, err := bindRequest[blockDocumentRequest](c, 12<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if !wiki.ValidBlockStateVector(input.StateVector) {
		writeWikiError(c, wiki.ErrPageInvalidInput)
		return
	}
	result, err := h.WikiService.SyncDocument(
		c.Request.Context(),
		principal,
		id,
		wiki.SyncDocumentInput{
			Update:        input.Update,
			BaseSequence:  input.BaseSequence,
			SchemaVersion: input.SchemaVersion,
		},
	)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	result.State, result.StateVector, err = wiki.BlockStateDelta(result.State, input.StateVector)
	if err != nil {
		writeWikiError(c, wiki.ErrPageInvalidInput)
		return
	}
	if len(input.StateVector) > 0 {
		result.Content = nil
		result.Markdown = ""
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) reserveWikiBlockIDs(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
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
	ids, err := h.WikiService.ReserveBlockIDs(c.Request.Context(), principal, id, input.Count)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		IDs []uuid.UUID `json:"ids"`
	}{ids})
}

func (h *Handler) archiveWikiPage(c *gin.Context) { h.setWikiArchive(c, true) }
func (h *Handler) restoreWikiPage(c *gin.Context) { h.setWikiArchive(c, false) }

func (h *Handler) setWikiArchive(c *gin.Context, archive bool) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	request, err := bindRequest[wikiPageRevisionRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	var result wiki.PageResponse
	if archive {
		result, err = h.WikiService.ArchivePage(
			c.Request.Context(),
			principal,
			id,
			*request.ExpectedRevision.ptr(),
		)
	} else {
		result, err = h.WikiService.RestorePage(
			c.Request.Context(),
			principal,
			id,
			*request.ExpectedRevision.ptr(),
		)
	}
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listWikiRevisions(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	var offset int64
	var err error
	if value := c.Query("offset"); value != "" {
		offset, err = strconv.ParseInt(value, 10, 32)
		if err != nil {
			writeWikiError(c, wiki.ErrPageInvalidInput)
			return
		}
	}
	items, err := h.WikiService.ListRevisions(c.Request.Context(), principal, id, int32(offset), 50)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}

func (h *Handler) restoreWikiRevision(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	request, err := bindRequest[wikiRestoreRevisionRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.WikiService.RestoreRevision(
		c.Request.Context(),
		principal,
		id,
		request.RevisionID,
		wiki.RestoreRevisionInput{
			ExpectedRevision: *request.ExpectedRevision.ptr(),
			ExpectedSequence: *request.ExpectedSequence.ptr(),
			SchemaVersion:    request.SchemaVersion,
		},
	)
	if err != nil {
		writeWikiError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) favoriteWikiPage(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	id, ok := wikiID(c, "pageId")
	if !ok {
		return
	}
	request, err := bindRequest[wikiFavoriteRequest](c, 4096)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.WikiService.SetFavorite(c.Request.Context(), principal, id, *request.Favorite.ptr()); err != nil {
		writeWikiError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

var wikiErrorMappings = []errorMapping{
	{
		wiki.ErrUIAuthoringDisabled,
		http.StatusServiceUnavailable,
		"Intelligent UI authoring is temporarily disabled",
		"intelligent_ui_authoring_disabled",
	},
	{wiki.ErrPageInvalidInput, http.StatusUnprocessableEntity, "Invalid Wiki input", "validation_failed"},
	{wiki.ErrPageBodyTooLarge, http.StatusRequestEntityTooLarge, "Wiki page is too large", "request_too_large"},
	{
		wiki.ErrPageForbidden,
		http.StatusForbidden,
		"Your Company role does not allow Wiki publishing",
		"company_permission_required",
	},
	{wiki.ErrPageNotFound, http.StatusNotFound, "Wiki page not found", "wiki_page_not_found"},
	{wiki.ErrCollectionNotFound, http.StatusNotFound, "Wiki collection not found", "wiki_collection_not_found"},
	{wiki.ErrPageConflict, http.StatusConflict, "Wiki page changed since you opened it", "wiki_page_conflict"},
	{wiki.ErrPageArchived, http.StatusConflict, "Wiki page is archived", "wiki_page_archived"},
	{
		wiki.ErrCollectionConflict,
		http.StatusConflict,
		"Wiki collection changed or name is already used",
		"wiki_collection_conflict",
	},
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
	{
		wiki.ErrAgentAccessDisabled,
		http.StatusForbidden,
		"Company Wiki reading is switched off for this Agent",
		"wiki_agent_access_disabled",
	},
	{
		wiki.ErrAgentWriteForbidden,
		http.StatusForbidden,
		"This Run may not write the Company Wiki",
		"wiki_agent_write_forbidden",
	},
	{
		wiki.ErrAgentWriteLimit,
		http.StatusUnprocessableEntity,
		"A Run can write at most 40 Wiki pages",
		"wiki_agent_write_limit",
	},
	{wiki.ErrRunNotFound, http.StatusNotFound, "Run not found", "run_not_found"},
	{execution.ErrRunProofInvalid, http.StatusUnauthorized, "Run token is invalid", "run_token_invalid"},
}

func writeWikiError(c *gin.Context, err error) {
	writeMappedError(c, err, wikiErrorMappings, internalErrorFallback)
}
