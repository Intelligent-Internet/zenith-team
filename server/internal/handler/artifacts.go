package handler

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

var errInvalidArtifactCursor = errors.New("invalid Artifact cursor")

type conversationArtifactPageResponse struct {
	Items      []execution.ArtifactRunResult `json:"items"`
	NextCursor *string                       `json:"nextCursor"`
}

func (h *Handler) listConversationArtifacts(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	conversationID, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil {
		writeDeliverableError(c, execution.ErrArtifactNotFound)
		return
	}
	options, err := parseConversationArtifactListOptions(c.Request.URL.RawQuery)
	if errors.Is(err, errInvalidArtifactCursor) {
		writeBoundaryError(c, boundaryErrorInvalidCursor)
		return
	}
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	page, err := h.DeliverableService.ListConversationArtifacts(
		c.Request.Context(), principal, conversationID, options,
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	var nextCursor *string
	if page.Next != nil {
		value := encodeFeedCursor(page.Next.CreatedAt, page.Next.ID)
		nextCursor = &value
	}
	c.JSON(http.StatusOK, conversationArtifactPageResponse{
		Items: page.Items, NextCursor: nextCursor,
	})
}

func parseConversationArtifactListOptions(raw string) (execution.ArtifactListOptions, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return execution.ArtifactListOptions{}, execution.ErrDeliverableInvalidInput
	}
	var runID uuid.UUID
	if values, present := query["runId"]; present {
		if len(values) != 1 {
			return execution.ArtifactListOptions{}, execution.ErrDeliverableInvalidInput
		}
		runID, err = util.ParseGoUUID(values[0])
		if err != nil || runID == uuid.Nil {
			return execution.ArtifactListOptions{}, execution.ErrDeliverableInvalidInput
		}
		query.Del("runId")
	}
	options, err := parseArtifactListOptions(query.Encode())
	options.RunID = runID
	return options, err
}

func parseArtifactListOptions(raw string) (execution.ArtifactListOptions, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return execution.ArtifactListOptions{}, execution.ErrDeliverableInvalidInput
	}
	for key, entries := range query {
		if (key != "limit" && key != "cursor") || len(entries) != 1 || entries[0] == "" {
			return execution.ArtifactListOptions{}, execution.ErrDeliverableInvalidInput
		}
	}
	options := execution.ArtifactListOptions{Limit: 20}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || strconv.Itoa(limit) != value || limit < 1 || limit > 50 {
			return options, execution.ErrDeliverableInvalidInput
		}
		options.Limit = int32(limit)
	}
	if value := query.Get("cursor"); value != "" {
		at, id, ok := decodeFeedCursor(value)
		if !ok {
			return options, errInvalidArtifactCursor
		}
		options.Before = &execution.Cursor{CreatedAt: at, ID: id}
	}
	return options, nil
}

// listArtifacts is the Company-wide Artifact picker: everything the caller can
// open, newest first, optionally filtered by title.
func (h *Handler) listArtifacts(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil || len(query["search"]) > 1 || len(query["ownership"]) > 1 || len(query["source"]) > 1 {
		writeDeliverableError(c, execution.ErrDeliverableInvalidInput)
		return
	}
	search := query.Get("search")
	ownership, source := query.Get("ownership"), query.Get("source")
	query.Del("ownership")
	query.Del("source")
	query.Del("search")
	options, err := parseArtifactListOptions(query.Encode())
	if errors.Is(err, errInvalidArtifactCursor) {
		writeBoundaryError(c, boundaryErrorInvalidCursor)
		return
	}
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	page, err := h.DeliverableService.ListArtifacts(
		c.Request.Context(),
		principal,
		execution.CompanyArtifactListOptions{
			ArtifactListOptions: options,
			Search:              search,
			Ownership:           ownership,
			Source:              source,
		},
	)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	var nextCursor *string
	if page.Next != nil {
		value := encodeFeedCursor(page.Next.CreatedAt, page.Next.ID)
		nextCursor = &value
	}
	c.JSON(http.StatusOK, conversationArtifactPageResponse{Items: page.Items, NextCursor: nextCursor})
}

type resolvedArtifactsResponse struct {
	Items []execution.ArtifactRunResult `json:"items"`
}

// resolveArtifacts returns the named Artifacts (repeated `id`, at most 50) the
// caller can open now. Absent ids are unavailable to the caller, whatever the
// reason; Wiki readers resolve citations through this.
func (h *Handler) resolveArtifacts(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["id"]) == 0 || len(query["id"]) > 50 {
		writeDeliverableError(c, execution.ErrDeliverableInvalidInput)
		return
	}
	ids := make([]uuid.UUID, 0, len(query["id"]))
	for _, value := range query["id"] {
		id, err := util.ParseGoUUID(value)
		if err != nil {
			writeDeliverableError(c, execution.ErrDeliverableInvalidInput)
			return
		}
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	items, err := h.DeliverableService.ResolveArtifacts(c.Request.Context(), principal, ids)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	c.JSON(http.StatusOK, resolvedArtifactsResponse{Items: items})
}
