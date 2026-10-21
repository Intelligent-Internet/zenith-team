package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
)

var errInvalidTaskResultCursor = errors.New("invalid Task results cursor")

type taskResultPageResponse struct {
	Selection  task.ResultSelection         `json:"selection"`
	Items      []execution.TaskRunResult    `json:"items"`
	Sources    []execution.TaskResultSource `json:"sources"`
	NextCursor *string                      `json:"nextCursor"`
}

func (h *Handler) listTaskResults(c *gin.Context) {
	principal, taskID, ok := taskRequestIdentity(c)
	if !ok || !h.admitTaskRunRead(c, principal, taskID) {
		return
	}
	options, err := parseTaskResultOptions(c.Request.URL.RawQuery)
	if errors.Is(err, errInvalidTaskResultCursor) {
		writeBoundaryError(c, boundaryErrorInvalidCursor)
		return
	}
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	page, err := h.DeliverableService.ListTaskResults(c.Request.Context(), principal, taskID, options)
	if err != nil {
		writeDeliverableError(c, err)
		return
	}
	selection, err := h.TaskService.ResultSelection(c.Request.Context(), principal, taskID)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, taskResultPageResponse{
		Selection: selection,
		Items:     page.Items, Sources: page.Sources, NextCursor: encodeTaskResultCursor(page),
	})
}

func parseTaskResultOptions(raw string) (execution.TaskResultListOptions, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return execution.TaskResultListOptions{}, execution.ErrDeliverableInvalidInput
	}
	for key, entries := range query {
		if (key != "limit" && key != "cursor") || len(entries) != 1 || entries[0] == "" {
			return execution.TaskResultListOptions{}, execution.ErrDeliverableInvalidInput
		}
	}
	options := execution.TaskResultListOptions{Limit: 20}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || strconv.Itoa(limit) != value || limit < 1 || limit > 50 {
			return options, execution.ErrDeliverableInvalidInput
		}
		options.Limit = int32(limit)
	}
	if value := query.Get("cursor"); value != "" {
		if len(value) > 128 {
			return options, errInvalidTaskResultCursor
		}
		parts := strings.Split(value, ".")
		if len(parts) != 3 || parts[0] != "1" || parts[1] == "" && parts[2] == "" {
			return options, errInvalidTaskResultCursor
		}
		options.SkipResults, options.SkipSources = parts[1] == "", parts[2] == ""
		if !options.SkipResults {
			at, id, ok := decodeRunCursor(parts[1])
			if !ok {
				return options, errInvalidTaskResultCursor
			}
			options.Before = &execution.Cursor{CreatedAt: at, ID: id}
		}
		if !options.SkipSources {
			at, id, ok := decodeFeedCursor(parts[2])
			if !ok {
				return options, errInvalidTaskResultCursor
			}
			options.SourceBefore = &execution.TaskResultSourceCursor{CreatedAt: at, ID: id}
		}
	}
	return options, nil
}

// The opaque continuation combines the standard feed cursors. An empty
// component marks exhaustion, so uneven feeds never replay their first page.
func encodeTaskResultCursor(page execution.TaskResultPage) *string {
	if page.Next == nil && page.NextSource == nil {
		return nil
	}
	parts := []string{"1", "", ""}
	if page.Next != nil {
		parts[1] = encodeRunCursor(page.Next.CreatedAt, page.Next.ID)
	}
	if page.NextSource != nil {
		parts[2] = encodeFeedCursor(page.NextSource.CreatedAt, page.NextSource.ID)
	}
	value := strings.Join(parts, ".")
	return &value
}

// Agent finalization names evidence only. Policy and acceptance stay human-owned.
type finalizeTaskResultsRequest struct {
	ExpectedRevision int64       `json:"expectedRevision" binding:"revision"`
	OutputIDs        []uuid.UUID `json:"outputIds"        binding:"required,min=1,max=32"`
}

func (h *Handler) finalizeTaskResults(c *gin.Context) {
	principal, taskID, ok := taskRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[finalizeTaskResultsRequest](c, 32<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	origin, requestKey, ok := h.resolveTaskWriteOrigin(c, principal)
	if !ok {
		return
	}
	if *origin.TaskID != taskID {
		writeTaskError(c, task.ErrOutsideRunScope)
		return
	}
	result, err := h.TaskService.FinalizeResults(
		c.Request.Context(),
		principal,
		origin,
		task.FinalizeResultsInput{
			RequestKey:       requestKey,
			ExpectedRevision: request.ExpectedRevision,
			OutputIDs:        request.OutputIDs,
		},
	)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}
