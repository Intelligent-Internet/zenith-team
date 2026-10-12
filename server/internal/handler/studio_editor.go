package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"
)

type studioEditorRequest struct {
	SchemaVersion int             `json:"schemaVersion" binding:"eq=2"`
	RequestID     uuid.UUID       `json:"requestId" binding:"required"`
	RuntimeID     uuid.UUID       `json:"runtimeId" binding:"required"`
	Operation     string          `json:"operation" binding:"required,oneof=resolveContext ensureBinding"`
	Payload       json.RawMessage `json:"payload" binding:"required"`
}

type studioEnsureRequest struct {
	Title                   string          `json:"title"`
	Description             *string         `json:"description"`
	ExpectedBindingID       json.RawMessage `json:"expectedBindingId"`
	ExpectedBindingRevision *int64          `json:"expectedBindingRevision"`
	ConnectionGeneration    uuid.UUID       `json:"connectionGeneration"`
}

type studioEditorResponse[T any] struct {
	SchemaVersion int       `json:"schemaVersion"`
	RequestID     uuid.UUID `json:"requestId"`
	Operation     string    `json:"operation"`
	Data          T         `json:"data"`
}

func (h *Handler) registerStudioEditorRoutes(api *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.StudioEditorService == nil {
		return
	}
	group := api.Group(
		"/companies/:companyId",
		middleware.PrivateNoStore(),
		middleware.RequestTimeout(30*time.Second),
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireHumanActor(),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	group.POST("/conversations/:conversationId/studio/operations", h.studioEditorOperation)
}

func (h *Handler) studioEditorOperation(c *gin.Context) {
	p, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	id, err := util.ParseGoUUID(c.Param("conversationId"))
	if err != nil || id == uuid.Nil {
		writeMappedError(
			c,
			conversation.ErrStudioEditorNotFound,
			studioEditorErrorMappings,
			internalErrorFallback,
		)
		return
	}
	request, err := bindRequest[studioEditorRequest](c, 16<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	var contextResult conversation.StudioContextResponse
	var bindingResult conversation.StudioBindingResponse
	access := conversation.StudioEditorAccess{ConversationID: id, RuntimeID: request.RuntimeID}
	if request.RequestID == uuid.Nil || request.RuntimeID == uuid.Nil || !validStudioUnicodeJSON(request.Payload) {
		err = conversation.ErrStudioEditorInvalid
	} else {
		switch request.Operation {
		case "resolveContext":
			var empty struct{}
			if util.BindStrictJSON(bytes.NewReader(request.Payload), &empty) != nil {
				err = conversation.ErrStudioEditorInvalid
				break
			}
			contextResult, err = h.StudioEditorService.ResolveContext(c.Request.Context(), p, access)
		case "ensureBinding":
			var body studioEnsureRequest
			if util.BindStrictJSON(bytes.NewReader(request.Payload), &body) != nil ||
				body.Description == nil ||
				body.ExpectedBindingRevision == nil ||
				len(body.ExpectedBindingID) == 0 {
				err = conversation.ErrStudioEditorInvalid
				break
			}
			var binding *uuid.UUID
			if json.Unmarshal(body.ExpectedBindingID, &binding) != nil {
				err = conversation.ErrStudioEditorInvalid
				break
			}
			bindingResult, err = h.StudioEditorService.EnsureBinding(
				c.Request.Context(),
				p,
				conversation.StudioEnsureInput{
					Access:                  access,
					RequestID:               request.RequestID,
					Generation:              body.ConnectionGeneration,
					ExpectedBindingID:       binding,
					ExpectedBindingRevision: *body.ExpectedBindingRevision,
					Title:                   body.Title,
					Description:             *body.Description,
				},
			)
		default:
			err = conversation.ErrStudioEditorInvalid
		}
	}
	if err != nil {
		writeMappedError(c, err, studioEditorErrorMappings, internalErrorFallback)
		return
	}
	switch {
	case request.Operation == "resolveContext":
		c.JSON(
			http.StatusOK,
			studioEditorResponse[conversation.StudioContextResponse]{
				SchemaVersion: 2,
				RequestID:     request.RequestID,
				Operation:     request.Operation,
				Data:          contextResult,
			},
		)
	default:
		c.JSON(
			http.StatusOK,
			studioEditorResponse[conversation.StudioBindingResponse]{
				SchemaVersion: 2,
				RequestID:     request.RequestID,
				Operation:     request.Operation,
				Data:          bindingResult,
			},
		)
	}
}

var studioEditorErrorMappings = []errorMapping{
	{conversation.ErrStudioEditorNotFound, 404, "Studio editor not found", "studio_editor_not_found"},
	{conversation.ErrStudioEditorInvalid, 422, "Studio editor invalid", "studio_editor_invalid"},
	{
		conversation.ErrStudioEditorPermissionDenied,
		403,
		"Studio editor permission denied",
		"studio_editor_permission_denied",
	},
	{
		conversation.ErrStudioEditorGenerationConflict,
		409,
		"Studio editor generation conflict",
		"studio_editor_generation_conflict",
	},
	{
		conversation.ErrStudioEditorBindingConflict,
		409,
		"Studio editor binding conflict",
		"studio_editor_binding_conflict",
	},
	{
		conversation.ErrStudioEditorOriginConflict,
		409,
		"Studio editor origin conflict",
		"studio_editor_origin_conflict",
	},
	{
		conversation.ErrStudioEditorAccountConflict,
		409,
		"Studio editor account conflict",
		"studio_editor_account_conflict",
	},
	{
		conversation.ErrStudioEditorRequestConflict,
		409,
		"Studio editor request conflict",
		"studio_editor_request_conflict",
	},
	{
		conversation.ErrStudioEditorReauthRequired,
		409,
		"Studio editor reauth required",
		"studio_editor_reauth_required",
	},
	{
		conversation.ErrStudioEditorUpstreamDenied,
		403,
		"Studio editor upstream denied",
		"studio_editor_upstream_denied",
	},
	{
		conversation.ErrStudioEditorSourceUnavailable,
		404,
		"Studio editor source unavailable",
		"studio_editor_source_unavailable",
	},
	{
		conversation.ErrStudioEditorRuntimeUnavailable,
		503,
		"Studio editor runtime unavailable",
		"studio_editor_runtime_unavailable",
	},
	{
		conversation.ErrStudioEditorAuthorityUnavailable,
		503,
		"Studio editor authority unavailable",
		"studio_editor_authority_unavailable",
	},
	{conversation.ErrStudioEditorUnsupported, 409, "Studio editor unsupported", "studio_editor_unsupported"},
	{conversation.ErrStudioEditorRateLimited, 429, "Studio editor rate limited", "studio_editor_rate_limited"},
	{
		conversation.ErrStudioEditorOutcomeUnknown,
		504,
		"Studio editor outcome unknown",
		"studio_editor_outcome_unknown",
	},
	{
		conversation.ErrStudioEditorRecoveryRequired,
		409,
		"Studio editor recovery required",
		"studio_editor_recovery_required",
	},
	{
		conversation.ErrStudioEditorUpstreamInvalid,
		502,
		"Studio editor upstream invalid",
		"studio_editor_upstream_invalid",
	},
}

// encoding/json replaces lone UTF-16 surrogates with U+FFFD. Refuse those
// escaped inputs before decoding, while preserving intentional literal backslashes.
func validStudioUnicodeJSON(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 >= len(raw) {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
