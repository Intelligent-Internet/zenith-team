package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

const maxAgentBodyBytes = 64 << 10

var errInvalidAgentJSONShape = errors.New("invalid Agent JSON shape")

type AgentService interface {
	List(context.Context, auth.UserPrincipal, agent.ListOptions) ([]agent.Response, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (agent.Response, error)
	Create(context.Context, auth.UserPrincipal, agent.CreateInput) (agent.Response, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, agent.UpdateInput) (agent.Response, error)
	Archive(context.Context, auth.UserPrincipal, uuid.UUID, agent.RevisionInput) (agent.Response, error)
	Restore(context.Context, auth.UserPrincipal, uuid.UUID, agent.RevisionInput) (agent.Response, error)
}

type agentCollectionResponse struct {
	Items []agent.Response `json:"items"`
}

type createAgentRequest struct {
	Name         string          `json:"name"         binding:"required"`
	Title        string          `json:"title"        binding:"required"`
	Instructions json.RawMessage `json:"instructions"`
	// ReportsToAgentID is empty for a root agent: the org starts empty and
	// its first agent has no parent.
	ReportsToAgentID string `json:"reportsToAgentId" binding:"omitempty,uuid"`
	// AvatarKey and AccentColor are the agent's own appearance; both optional.
	AvatarKey   *string `json:"avatarKey"`
	AccentColor *string `json:"accentColor"`
	// Provider is the template's, fixed for its life (ADR 0014). RuntimeID
	// is optional: "also set it up on this computer of mine now", one of the
	// caller's runtimes of that provider; the root agent requires it.
	Provider      string          `json:"provider"      binding:"required,oneof=claude codex"`
	RuntimeID     string          `json:"runtimeId"     binding:"omitempty,uuid"`
	Model         json.RawMessage `json:"model"         binding:"required"`
	ThinkingLevel json.RawMessage `json:"thinkingLevel" binding:"required"`
	// MaxConcurrentRuns is optional at creation and defaults to 3; the same
	// named 1..10 rule as on update, and an explicit null is a 400 like
	// every other optional[T] — the cap always has a value.
	MaxConcurrentRuns optional[int32] `json:"maxConcurrentRuns" binding:"omitnil,maxConcurrentRuns"`
}

type updateAgentRequest struct {
	Voice            json.RawMessage `json:"voice"`
	Capabilities     json.RawMessage `json:"capabilities"`
	ExpectedRevision int64           `json:"expectedRevision" binding:"revision"`
	Name             json.RawMessage `json:"name"             binding:"required_without_all=Title Instructions ReportsToAgentID Model ThinkingLevel MaxConcurrentRuns Capabilities AvatarKey AccentColor Provider Voice"`
	Title            json.RawMessage `json:"title"`
	Instructions     json.RawMessage `json:"instructions"`
	ReportsToAgentID json.RawMessage `json:"reportsToAgentId"`
	Model            json.RawMessage `json:"model"`
	ThinkingLevel    json.RawMessage `json:"thinkingLevel"`
	// AvatarKey and AccentColor are nullable like model: an explicit null
	// clears the appearance.
	AvatarKey   json.RawMessage `json:"avatarKey"`
	AccentColor json.RawMessage `json:"accentColor"`
	// The 1..10 bound is the named maxConcurrentRuns rule; an explicit null is
	// a 400 like every other optional[T] — the cap always has a value, so
	// there is no "unset" to say.
	MaxConcurrentRuns optional[int32] `json:"maxConcurrentRuns" binding:"omitnil,maxConcurrentRuns"`
	// Provider is declared only to be refused with a reason: an Agent's
	// provider is fixed (ADR 0014), so any value is ErrProviderImmutable
	// rather than the unknown-field 400 that says nothing. It counts as a
	// field for Name's rule, so a body carrying only it gets that answer.
	Provider json.RawMessage `json:"provider"`
}

type agentVersionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

func (h *Handler) registerAgentRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil ||
		h.AgentService == nil {
		return
	}
	routes := group.Group(
		"/companies/:companyId/agents",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	routes.GET("", h.listAgents)
	routes.POST("", h.createAgent)
	routes.GET("/:agentId", h.getAgent)
	routes.PATCH("/:agentId", h.updateAgent)
	routes.POST("/:agentId/archive", h.archiveAgent)
	routes.POST("/:agentId/restore", h.restoreAgent)
}

func (h *Handler) listAgents(c *gin.Context) {
	options, err := parseAgentListOptions(c)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := h.AgentService.List(c.Request.Context(), principal, options)
	if err != nil {
		writeAgentError(c, err)
		return
	}
	if items == nil {
		items = []agent.Response{}
	}
	c.JSON(http.StatusOK, agentCollectionResponse{Items: items})
}

func (h *Handler) getAgent(c *gin.Context) {
	agentID, err := util.ParseGoUUID(c.Param("agentId"))
	if err != nil {
		writeBoundaryError(c, boundaryErrorAgentNotFound)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	view, err := h.AgentService.Get(c.Request.Context(), principal, agentID)
	if err != nil {
		writeAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (h *Handler) createAgent(c *gin.Context) {
	request, err := bindRequest[createAgentRequest](c, maxAgentBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input, err := request.domainInput()
	if err != nil {
		if errors.Is(err, errInvalidAgentJSONShape) {
			writeBoundaryError(c, boundaryErrorInvalidRequestBody)
		} else {
			writeBoundaryError(c, boundaryErrorValidationFailed)
		}
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	result, err := h.AgentService.Create(c.Request.Context(), principal, input)
	if err != nil {
		writeAgentError(c, err)
		return
	}
	c.JSON(http.StatusCreated, result)
}

func (h *Handler) updateAgent(c *gin.Context) {
	principal, agentID, ok := agentMutationIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[updateAgentRequest](c, maxAgentBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input, err := request.domainInput()
	if err != nil {
		writeAgentRequestError(c, err)
		return
	}
	result, err := h.AgentService.Update(c.Request.Context(), principal, agentID, input)
	if err != nil {
		writeAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) archiveAgent(c *gin.Context) {
	h.mutateAgentArchiveState(c, true)
}

func (h *Handler) restoreAgent(c *gin.Context) {
	h.mutateAgentArchiveState(c, false)
}

func (h *Handler) mutateAgentArchiveState(c *gin.Context, archive bool) {
	principal, agentID, ok := agentMutationIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[agentVersionRequest](c, maxAgentBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	input := agent.RevisionInput{ExpectedRevision: request.ExpectedRevision}
	var result agent.Response
	if archive {
		result, err = h.AgentService.Archive(c.Request.Context(), principal, agentID, input)
	} else {
		result, err = h.AgentService.Restore(c.Request.Context(), principal, agentID, input)
	}
	if err != nil {
		writeAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (r createAgentRequest) domainInput() (agent.CreateInput, error) {
	var parentID *uuid.UUID
	if r.ReportsToAgentID != "" {
		parent, err := util.ParseGoUUID(r.ReportsToAgentID)
		if err != nil {
			return agent.CreateInput{}, agent.ErrInvalidInput
		}
		parentID = &parent
	}
	var runtimeID *uuid.UUID
	if r.RuntimeID != "" {
		runtime, err := util.ParseGoUUID(r.RuntimeID)
		if err != nil {
			return agent.CreateInput{}, agent.ErrInvalidInput
		}
		runtimeID = &runtime
	}
	model, err := parseRequiredNullableAgentString(r.Model)
	if err != nil {
		return agent.CreateInput{}, err
	}
	thinking, err := parseRequiredNullableAgentString(r.ThinkingLevel)
	if err != nil {
		return agent.CreateInput{}, err
	}
	instructions := ""
	if len(r.Instructions) != 0 {
		if bytes.Equal(bytes.TrimSpace(r.Instructions), []byte("null")) ||
			json.Unmarshal(r.Instructions, &instructions) != nil {
			return agent.CreateInput{}, errInvalidAgentJSONShape
		}
	}
	return agent.CreateInput{
		Name:              r.Name,
		Title:             r.Title,
		Instructions:      instructions,
		ReportsToAgentID:  parentID,
		Provider:          r.Provider,
		RuntimeID:         runtimeID,
		Model:             model,
		ThinkingLevel:     thinking,
		AvatarKey:         r.AvatarKey,
		AccentColor:       r.AccentColor,
		MaxConcurrentRuns: r.MaxConcurrentRuns.ptr(),
	}, nil
}

func (r updateAgentRequest) domainInput() (agent.UpdateInput, error) {
	if len(r.Provider) != 0 {
		return agent.UpdateInput{}, agent.ErrProviderImmutable
	}
	voiceSettings, err := parseAgentVoice(r.Voice)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	capabilities, err := parseAgentCapabilities(r.Capabilities)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	name, err := parseOptionalAgentString(r.Name)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	title, err := parseOptionalAgentString(r.Title)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	instructions, err := parseOptionalAgentString(r.Instructions)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	parentID, err := parseOptionalAgentUUID(r.ReportsToAgentID)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	model, err := parseOptionalNullableAgentString(r.Model)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	thinking, err := parseOptionalNullableAgentString(r.ThinkingLevel)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	avatarKey, err := parseOptionalNullableAgentString(r.AvatarKey)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	accentColor, err := parseOptionalNullableAgentString(r.AccentColor)
	if err != nil {
		return agent.UpdateInput{}, err
	}
	return agent.UpdateInput{
		Voice:             voiceSettings,
		Capabilities:      capabilities,
		ExpectedRevision:  r.ExpectedRevision,
		Name:              name,
		Title:             title,
		Instructions:      instructions,
		ReportsToAgentID:  parentID,
		Model:             model,
		ThinkingLevel:     thinking,
		AvatarKey:         avatarKey,
		AccentColor:       accentColor,
		MaxConcurrentRuns: r.MaxConcurrentRuns.ptr(),
	}, nil
}

func agentMutationIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	agentID, err := util.ParseGoUUID(c.Param("agentId"))
	if err != nil {
		writeBoundaryError(c, boundaryErrorAgentNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, agentID, true
}

func parseAgentListOptions(c *gin.Context) (agent.ListOptions, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return agent.ListOptions{}, agent.ErrInvalidInput
	}
	for key, values := range query {
		if (key != "include_archived" && key != "reports_to_agent_id") ||
			len(values) != 1 || values[0] == "" {
			return agent.ListOptions{}, agent.ErrInvalidInput
		}
	}
	options := agent.ListOptions{}
	if rawArchived, exists := query["include_archived"]; exists {
		switch rawArchived[0] {
		case "true":
			options.IncludeArchived = true
		case "false":
		default:
			return agent.ListOptions{}, agent.ErrInvalidInput
		}
	}
	if values, exists := query["reports_to_agent_id"]; exists {
		reportsToAgentID, err := util.ParseGoUUID(values[0])
		if err != nil {
			return agent.ListOptions{}, agent.ErrInvalidInput
		}
		options.ReportsToAgentID = &reportsToAgentID
	}
	return options, nil
}

func parseRequiredNullableAgentString(raw json.RawMessage) (*string, error) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var value string
	if len(trimmed) == 0 || json.Unmarshal(trimmed, &value) != nil {
		return nil, errInvalidAgentJSONShape
	}
	return &value, nil
}

func parseOptionalAgentString(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, errInvalidAgentJSONShape
	}
	var value string
	if json.Unmarshal(trimmed, &value) != nil {
		return nil, errInvalidAgentJSONShape
	}
	return &value, nil
}

func parseOptionalAgentUUID(raw json.RawMessage) (*uuid.UUID, error) {
	value, err := parseOptionalAgentString(raw)
	if err != nil || value == nil {
		return nil, err
	}
	id, err := util.ParseGoUUID(*value)
	if err != nil {
		return nil, agent.ErrInvalidInput
	}
	return &id, nil
}

func parseOptionalNullableAgentString(raw json.RawMessage) (agent.NullableTextChange, error) {
	if len(raw) == 0 {
		return agent.NullableTextChange{}, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return agent.NullableTextChange{Present: true}, nil
	}
	var value string
	if json.Unmarshal(trimmed, &value) != nil {
		return agent.NullableTextChange{}, errInvalidAgentJSONShape
	}
	return agent.NullableTextChange{Present: true, Value: &value}, nil
}

func writeAgentRequestError(c *gin.Context, err error) {
	if errors.Is(err, errInvalidAgentJSONShape) {
		writeBoundaryError(c, boundaryErrorInvalidRequestBody)
		return
	}
	writeAgentError(c, err)
}

var agentErrorMappings = []errorMapping{
	{agent.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	{agent.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{
		agent.ErrThinkingLevelUnsupported,
		http.StatusUnprocessableEntity,
		"thinkingLevel is not a level the template's provider accepts" +
			" (Claude Code: low, medium, high, xhigh, max; Codex: a level from the runtime's model catalog)",
		"validation_failed",
	},
	{
		agent.ErrProviderImmutable,
		http.StatusUnprocessableEntity,
		"An agent's provider is fixed; duplicate the agent to use the other provider",
		"validation_failed",
	},
	{
		agent.ErrConflict,
		http.StatusConflict,
		"Agent creation conflicts with an existing resource",
		"agent_create_conflict",
	},
	{
		agent.ErrRevisionConflict,
		http.StatusConflict,
		"Agent mutation conflicts with the current revision",
		"agent_revision_conflict",
	},
	{
		agent.ErrCycle,
		http.StatusConflict,
		"Agent reports-to relationship would create a cycle",
		"agent_reports_to_cycle",
	},
	{
		agent.ErrRootImmutable,
		http.StatusConflict,
		"root Agent topology is immutable",
		"root_agent_topology_immutable",
	},
	{agent.ErrArchiveBlocked, http.StatusConflict, "Agent has active dependents", "agent_archive_blocked"},
	{agent.ErrNotFound, http.StatusNotFound, "Agent not found", "agent_not_found"},
	{
		agent.ErrForbidden,
		http.StatusForbidden,
		"only the Agent's creator, or a Company owner or admin, may do this; the root Agent needs agents.manage",
		"agent_creator_required",
	},
	// A runtime the caller cannot bind onto — unknown, removed, foreign or
	// another member's — is one answer (ADR 0014): the runtime surface's own
	// not-found, so a bind attempt is not an oracle.
	{agent.ErrRuntimeNotFound, http.StatusNotFound, "Runtime not found", "runtime_not_found"},
	{
		agent.ErrBindingProviderMismatch,
		http.StatusUnprocessableEntity,
		"the runtime runs a different provider than this agent",
		"agent_binding_provider_mismatch",
	},
	{
		agent.ErrBindingAgentArchived,
		http.StatusConflict,
		"the agent is archived and cannot be set up on a runtime",
		"agent_archived",
	},
}

func writeAgentError(c *gin.Context, err error) {
	writeMappedError(c, err, agentErrorMappings, internalErrorFallback)
}
