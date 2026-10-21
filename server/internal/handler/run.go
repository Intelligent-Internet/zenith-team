package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var (
	errInvalidRunListCursor = errors.New("invalid run list cursor")
	errInvalidRunListInput  = errors.New("invalid run list input")
)

// The user-facing run surface: read projections over the runs table plus the
// cancel action. Company scoping is the tenant middleware plus an explicit
// company_id match on every row (404, never 403).

type runListResponse struct {
	Items      []execution.RunView `json:"items"`
	NextCursor *string             `json:"nextCursor"`
}

type runListCursor = execution.Cursor

type runListQuery struct {
	before         *runListCursor
	limit          int32
	statuses       []string
	activeOnly     bool
	taskID         *uuid.UUID
	conversationID *uuid.UUID
	agentID        *uuid.UUID
	runtimeID      *uuid.UUID
	runType        string
	revision       *int64
	from           *time.Time
	to             *time.Time
}

func (h *Handler) registerRunRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.RunService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/runs",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.GET("", h.listRuns)
	routes.GET("/:runId", h.getRun)
	routes.POST("/:runId/cancel", h.cancelRun)
}

func (h *Handler) listRuns(c *gin.Context) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	query, err := parseRunListQuery(c)
	if err != nil {
		if errors.Is(err, errInvalidRunListCursor) {
			writeBoundaryError(c, boundaryErrorInvalidCursor)
		} else {
			writeBoundaryError(c, boundaryErrorValidationFailed)
		}
		return
	}

	page, err := h.RunService.List(c.Request.Context(), principal, execution.ListOptions{
		Before: query.before, Limit: query.limit, Statuses: query.statuses,
		ActiveOnly: query.activeOnly, TaskID: query.taskID,
		ConversationID: query.conversationID, AgentID: query.agentID, RuntimeID: query.runtimeID,
		RunType: query.runType, Revision: query.revision, From: query.from, To: query.to,
	})
	if err != nil {
		writeRunError(c, err)
		return
	}
	response := runListResponse{Items: page.Items}
	if page.Next != nil {
		cursor := encodeRunCursor(page.Next.CreatedAt, page.Next.ID)
		response.NextCursor = &cursor
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) getRun(c *gin.Context) {
	principal, runID, ok := runRequest(c)
	if !ok {
		return
	}
	// `zen run get` reads the Run's own record with its proof. The Run
	// channel offers it to every Run, a tagged one included.
	if !h.runOwnReadAdmitted(c, principal, func(origin execution.RunOrigin) bool {
		return origin.RunID == runID
	}) {
		return
	}
	run, err := h.RunService.Get(c.Request.Context(), principal, runID)
	if err != nil {
		writeRunError(c, err)
		return
	}
	c.JSON(http.StatusOK, run)
}

func (h *Handler) cancelRun(c *gin.Context) {
	principal, runID, ok := runRequest(c)
	if !ok {
		return
	}
	cancelled, err := h.RunService.Cancel(
		c.Request.Context(), principal, runID, "user_cancelled",
	)
	if err != nil {
		writeRunError(c, err)
		return
	}
	c.JSON(http.StatusOK, cancelled)
}

func runRequest(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	// A malformed path id names a run that cannot exist, which is
	// indistinguishable from one that names nothing: 404, never 422.
	// parseUUIDOrBadRequest answers 422 and stays for the routes that still
	// want that; run routes take the 404 lane.
	runID, ok := parseRunPathID(c.Param("runId"))
	if !ok {
		writeRunError(c, execution.ErrRunNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, runID, true
}

// parseRunPathID accepts canonical UUID identity; malformed or old numeric
// paths use the same not-found response as inaccessible Runs.
func parseRunPathID(raw string) (uuid.UUID, bool) {
	id, err := util.ParseGoUUID(raw)
	return id, err == nil
}

var runErrorMappings = []errorMapping{
	{execution.ErrRunNotFound, http.StatusNotFound, "Run not found", "run_not_found"},
	// Cancelling a Task Run needs project.tasks.execute (ADR 0017).
	projectPermissionRequiredMapping,
	// Stopping a Run acts on the computer it runs on (ADR 0014).
	{
		execution.ErrRunCancelPermissionRequired,
		http.StatusForbidden,
		"Only the member whose computer runs it, owners and admins can stop this run.",
		"company_permission_required",
	},
}

func writeRunError(c *gin.Context, err error) {
	writeMappedError(c, err, runErrorMappings, internalErrorFallback)
}

func parseRunListQuery(c *gin.Context) (runListQuery, error) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return runListQuery{}, errInvalidRunListInput
	}
	for key, entries := range values {
		known := key == "limit" || key == "cursor" || key == "status" || key == "active" ||
			key == "task_id" || key == "conversation_id" || key == "agent_id" || key == "runtime_id" ||
			key == "run_type" || key == "revision" || key == "from" || key == "to"
		if !known || len(entries) != 1 || entries[0] == "" {
			return runListQuery{}, errInvalidRunListInput
		}
	}

	query := runListQuery{limit: 50, statuses: []string{}}
	if entries, exists := values["limit"]; exists {
		limit, err := strconv.Atoi(entries[0])
		if err != nil || strconv.Itoa(limit) != entries[0] || limit < 1 || limit > 100 {
			return runListQuery{}, errInvalidRunListInput
		}
		query.limit = int32(limit)
	}
	if entries, exists := values["cursor"]; exists {
		createdAt, id, ok := decodeRunCursor(entries[0])
		if !ok {
			return runListQuery{}, errInvalidRunListCursor
		}
		query.before = &runListCursor{CreatedAt: createdAt, ID: id}
	}
	if entries, exists := values["status"]; exists {
		seen := make(map[string]struct{})
		for _, status := range strings.Split(entries[0], ",") {
			if !validRunListStatus(status) {
				return runListQuery{}, errInvalidRunListInput
			}
			if _, duplicate := seen[status]; duplicate {
				return runListQuery{}, errInvalidRunListInput
			}
			seen[status] = struct{}{}
			query.statuses = append(query.statuses, status)
		}
	}
	if entries, exists := values["active"]; exists {
		if _, hasStatuses := values["status"]; hasStatuses ||
			(entries[0] != "true" && entries[0] != "false") {
			return runListQuery{}, errInvalidRunListInput
		}
		query.activeOnly = entries[0] == "true"
	}
	if query.taskID, err = parseOptionalRunListUUID(values, "task_id"); err != nil {
		return runListQuery{}, err
	}
	if query.conversationID, err = parseOptionalRunListUUID(values, "conversation_id"); err != nil {
		return runListQuery{}, err
	}
	if query.agentID, err = parseOptionalRunListUUID(values, "agent_id"); err != nil {
		return runListQuery{}, err
	}
	if query.runtimeID, err = parseOptionalRunListUUID(values, "runtime_id"); err != nil {
		return runListQuery{}, err
	}
	if raw := values.Get("run_type"); raw != "" {
		switch raw {
		case "task", "direct_chat", "schedule":
			query.runType = raw
		default:
			return runListQuery{}, errInvalidRunListInput
		}
		if query.taskID != nil && raw != "task" {
			return runListQuery{}, errInvalidRunListInput
		}
	}
	if raw := values.Get("revision"); raw != "" {
		revision, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 1 || revision > protocol.MaxJSONSafeInteger ||
			strconv.FormatInt(revision, 10) != raw || (query.taskID == nil && query.conversationID == nil) {
			return runListQuery{}, errInvalidRunListInput
		}
		query.revision = &revision
	}
	for key, target := range map[string]**time.Time{"from": &query.from, "to": &query.to} {
		if raw := values.Get(key); raw != "" {
			instant, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return runListQuery{}, errInvalidRunListInput
			}
			instant = instant.UTC()
			*target = &instant
		}
	}
	if query.from != nil && query.to != nil && !query.from.Before(*query.to) {
		return runListQuery{}, errInvalidRunListInput
	}
	return query, nil
}

func validRunListStatus(status string) bool {
	switch status {
	case "queued", "deferred", "dispatched", "waiting_local_directory", "running",
		"completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func parseOptionalRunListUUID(values url.Values, key string) (*uuid.UUID, error) {
	entries, exists := values[key]
	if !exists {
		return nil, nil
	}
	id, err := util.ParseGoUUID(entries[0])
	if err != nil {
		return nil, errInvalidRunListInput
	}
	return &id, nil
}
