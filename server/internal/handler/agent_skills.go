package handler

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"
)

type skillCollectionResponse struct {
	Items []agent.SkillView `json:"items"`
}
type skillGitHubRequest struct {
	URL            string `json:"url" binding:"required,max=2048"`
	ReplaceSkillID string `json:"replaceSkillId" binding:"omitempty,uuid"`
}
type skillInstallRequest struct {
	ImportID         string                    `json:"importId"         binding:"required,uuid"`
	VersionIDs       []string                  `json:"versionIds"       binding:"required,min=1,max=32,dive,uuid"`
	ReplaceSkillID   string                    `json:"replaceSkillId"   binding:"omitempty,uuid"`
	ExpectedRevision *int64                    `json:"expectedRevision" binding:"omitempty,revision"`
	Replacements     []skillReplacementRequest `json:"replacements" binding:"omitempty,max=32,dive"`
}
type skillReplacementRequest struct {
	VersionID        string `json:"versionId" binding:"required,uuid"`
	SkillID          string `json:"skillId" binding:"required,uuid"`
	ExpectedRevision int64  `json:"expectedRevision" binding:"revision"`
}
type skillUpdateRequest struct {
	ExpectedRevision int64                    `json:"expectedRevision" binding:"revision"`
	Enabled          *bool                    `json:"enabled"          binding:"omitempty"`
	Changes          []skillFileChangeRequest `json:"changes" binding:"omitempty,max=2000,dive"`
	DeletePaths      []string                 `json:"deletePaths" binding:"omitempty,max=2000,dive,required,max=1024"`
}
type skillFileChangeRequest struct {
	Path    string  `json:"path" binding:"required,max=1024"`
	Content *string `json:"content" binding:"required"`
}

func (h *Handler) registerAgentSkillRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil || h.SkillService == nil {
		return
	}
	group.GET("/companies/:companyId/message-skills", middleware.RequireUserAuth(h.AuthService), middleware.RequireCompanyMember(h.CompanyService), middleware.RequireHumanActor(), h.listMessageSkills)
	routes := group.Group(
		"/companies/:companyId/agents/:agentId",
		middleware.RequireUserAuth(
			h.AuthService,
		),
		middleware.RequireCompanyMember(h.CompanyService),
		middleware.RequireHumanActor(),
	)
	routes.GET("/skills", h.listAgentSkills)
	routes.POST("/skill-imports/upload", h.previewAgentSkillUpload)
	routes.POST("/skill-imports/github", h.previewAgentSkillGitHub)
	routes.POST("/skills", h.installAgentSkills)
	routes.PATCH("/skills/:skillId", h.updateAgentSkill)
	routes.DELETE("/skills/:skillId", h.removeAgentSkill)
	routes.GET("/skills/:skillId/reference", h.readAgentSkillReference)
}

func skillRequestIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return principal, uuid.Nil, false
	}
	id, err := util.ParseGoUUID(c.Param("agentId"))
	if err != nil {
		writeBoundaryError(c, boundaryErrorAgentNotFound)
		return principal, uuid.Nil, false
	}
	return principal, id, true
}

func (h *Handler) listAgentSkills(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, agentID, ok := skillRequestIdentity(c)
	if !ok {
		return
	}
	items, err := h.SkillService.List(c.Request.Context(), principal, agentID)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, skillCollectionResponse{Items: items})
}

func (h *Handler) previewAgentSkillUpload(c *gin.Context) {
	principal, agentID, ok := skillRequestIdentity(c)
	if !ok {
		return
	}
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, agent.MaxSkillUploadBytes+(64<<10))
	reader, err := c.Request.MultipartReader()
	if err != nil {
		writeBoundaryError(c, boundaryErrorMultipartContentTypeRequired)
		return
	}
	file, err := reader.NextPart()
	if err != nil || file.FormName() != "file" || file.FileName() == "" {
		writeBoundaryError(c, boundaryErrorInvalidRequestBody)
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, agent.MaxSkillUploadBytes+1))
	if err != nil || len(data) > agent.MaxSkillUploadBytes {
		writeBoundaryError(c, boundaryErrorRequestBodyTooLarge)
		return
	}
	var replaceID uuid.UUID
	part, err := reader.NextPart()
	if err != io.EOF {
		if err != nil || part.FormName() != "replaceSkillId" || part.FileName() != "" {
			writeBoundaryError(c, boundaryErrorInvalidRequestBody)
			return
		}
		target, err := io.ReadAll(io.LimitReader(part, 37))
		if err != nil {
			writeBoundaryError(c, boundaryErrorInvalidRequestBody)
			return
		}
		replaceID, err = util.ParseGoUUID(string(target))
		if err != nil {
			writeBoundaryError(c, boundaryErrorValidationFailed)
			return
		}
		if _, err := reader.NextPart(); err != io.EOF {
			writeBoundaryError(c, boundaryErrorInvalidRequestBody)
			return
		}
	}
	preview, err := h.SkillService.PreviewUpload(
		c.Request.Context(),
		principal,
		agentID,
		agent.SkillUploadInput{Filename: file.FileName(), Data: data, ReplaceSkillID: replaceID},
	)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusCreated, preview)
}

func (h *Handler) previewAgentSkillGitHub(c *gin.Context) {
	principal, agentID, ok := skillRequestIdentity(c)
	if !ok {
		return
	}
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	req, err := bindRequest[skillGitHubRequest](c, 8<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	replaceID, _ := util.ParseGoUUID(req.ReplaceSkillID)
	preview, err := h.SkillService.PreviewGitHub(
		c.Request.Context(),
		principal,
		agentID,
		agent.SkillGitHubInput{URL: req.URL, ReplaceSkillID: replaceID},
	)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusCreated, preview)
}

func (h *Handler) installAgentSkills(c *gin.Context) {
	principal, agentID, ok := skillRequestIdentity(c)
	if !ok {
		return
	}
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	req, err := bindRequest[skillInstallRequest](c, 8<<10)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	importID, _ := util.ParseGoUUID(req.ImportID)
	replaceID, _ := util.ParseGoUUID(req.ReplaceSkillID)
	var revision int64
	if req.ExpectedRevision != nil {
		revision = *req.ExpectedRevision
	}
	replacements := make([]agent.SkillReplacementInput, 0, len(req.Replacements))
	for _, item := range req.Replacements {
		versionID, _ := util.ParseGoUUID(item.VersionID)
		skillID, _ := util.ParseGoUUID(item.SkillID)
		replacements = append(replacements, agent.SkillReplacementInput{
			VersionID: versionID, SkillID: skillID, ExpectedRevision: item.ExpectedRevision,
		})
	}
	items, err := h.SkillService.Install(c.Request.Context(), principal, agentID, agent.SkillInstallInput{
		ImportID: importID, VersionIDs: req.VersionIDs, ReplaceSkillID: replaceID, ExpectedRevision: revision,
		Replacements: replacements,
	})
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, skillCollectionResponse{Items: items})
}

func (h *Handler) updateAgentSkill(c *gin.Context) {
	principal, agentID, ok := skillRequestIdentity(c)
	if !ok {
		return
	}
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	skillID, err := util.ParseGoUUID(c.Param("skillId"))
	if err != nil {
		writeSkillError(c, agent.ErrSkillNotFound)
		return
	}
	req, err := bindRequest[skillUpdateRequest](c, 64<<20)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	editing := len(req.Changes)+len(req.DeletePaths) > 0
	if (req.Enabled == nil && !editing) || (req.Enabled != nil && editing) {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	var result agent.SkillView
	if editing {
		changes := make([]agent.SkillFileChange, 0, len(req.Changes))
		for _, change := range req.Changes {
			changes = append(changes, agent.SkillFileChange{Path: change.Path, Content: *change.Content})
		}
		result, err = h.SkillService.EditFiles(c.Request.Context(), principal, agentID, skillID, agent.SkillEditInput{
			ExpectedRevision: req.ExpectedRevision, Changes: changes, DeletePaths: req.DeletePaths,
		})
	} else {
		result, err = h.SkillService.Update(c.Request.Context(), principal, agentID, skillID, agent.SkillUpdateInput{
			ExpectedRevision: req.ExpectedRevision, Enabled: *req.Enabled,
		})
	}
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) removeAgentSkill(c *gin.Context) {
	principal, agentID, ok := skillRequestIdentity(c)
	if !ok {
		return
	}
	if c.Request.URL.RawQuery != "" {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	skillID, err := util.ParseGoUUID(c.Param("skillId"))
	if err != nil {
		writeSkillError(c, agent.ErrSkillNotFound)
		return
	}
	req, err := bindRequest[agentVersionRequest](c, 1024)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if err := h.SkillService.Remove(
		c.Request.Context(),
		principal,
		agentID,
		skillID,
		req.ExpectedRevision,
	); err != nil {
		writeSkillError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) readAgentSkillReference(c *gin.Context) {
	principal, agentID, ok := skillRequestIdentity(c)
	if !ok {
		return
	}
	query := c.Request.URL.Query()
	if len(query["path"]) != 1 || query.Get("path") == "" || len(query) > 2 || (len(query) == 2 && len(query["versionId"]) != 1) {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	var versionID uuid.UUID
	if values, exists := query["versionId"]; exists {
		if len(values) != 1 {
			writeBoundaryError(c, boundaryErrorValidationFailed)
			return
		}
		var err error
		versionID, err = util.ParseGoUUID(values[0])
		if err != nil {
			writeBoundaryError(c, boundaryErrorValidationFailed)
			return
		}
	}
	skillID, err := util.ParseGoUUID(c.Param("skillId"))
	if err != nil {
		writeSkillError(c, agent.ErrSkillNotFound)
		return
	}
	result, err := h.SkillService.ReadReference(
		c.Request.Context(),
		principal,
		agent.SkillReadInput{AgentID: agentID, SkillID: skillID, VersionID: versionID, Path: query.Get("path")},
	)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

var skillErrorMappings = append([]errorMapping{
	{
		agent.ErrSkillFrontmatterInvalid,
		http.StatusUnprocessableEntity,
		"SKILL.md needs valid YAML frontmatter with supported fields; compatibility must be at most 500 characters",
		"agent_skill_frontmatter_invalid",
	},
	{
		agent.ErrSkillNameRequired,
		http.StatusUnprocessableEntity,
		"Add a non-empty name field to SKILL.md frontmatter",
		"agent_skill_name_required",
	},
	{
		agent.ErrSkillDescriptionInvalid,
		http.StatusUnprocessableEntity,
		"Add a non-empty plain-text description of at most 1024 characters to SKILL.md frontmatter",
		"agent_skill_description_invalid",
	},
	{
		agent.ErrSkillReferenceInvalid,
		http.StatusUnprocessableEntity,
		"Correct local links and keep their targets inside the skill folder",
		"agent_skill_reference_invalid",
	},
	{
		agent.ErrSkillReferenceMissing,
		http.StatusUnprocessableEntity,
		"Include every linked local file in the skill folder; upload a ZIP for a skill with supporting files",
		"agent_skill_reference_missing",
	},
	{
		agent.ErrSkillArchiveInvalid,
		http.StatusUnprocessableEntity,
		"The ZIP could not be read; export a fresh ZIP and try again",
		"agent_skill_archive_invalid",
	},
	{
		agent.ErrSkillFilesInvalid,
		http.StatusUnprocessableEntity,
		"Use unique relative file paths and remove hidden files, provider configuration, shortcuts and special files",
		"agent_skill_files_invalid",
	},
	{
		agent.ErrSkillPackageLimit,
		http.StatusUnprocessableEntity,
		"Reduce the skill package to fit the documented file-count and size limits",
		"agent_skill_package_limit",
	},
	{
		agent.ErrSkillInstructionsInvalid,
		http.StatusUnprocessableEntity,
		"Save SKILL.md as non-empty UTF-8 text without NUL bytes",
		"agent_skill_instructions_invalid",
	},
	{
		agent.ErrSkillCountInvalid,
		http.StatusUnprocessableEntity,
		"Include 1 to 32 skill folders, each with a file named SKILL.md",
		"agent_skill_count_invalid",
	},
	{
		agent.ErrSkillDaemonUpgrade,
		http.StatusConflict,
		"Update the Zenith Team daemon to load custom skills",
		"agent_skill_daemon_upgrade",
	},
	{
		agent.ErrSkillInvalid,
		http.StatusUnprocessableEntity,
		"Skill package is invalid; check portable SKILL.md frontmatter, file paths, references and size limits",
		"agent_skill_invalid",
	},
	{agent.ErrSkillNotFound, http.StatusNotFound, "Agent skill or import not found", "agent_skill_not_found"},
	{
		agent.ErrSkillConflict,
		http.StatusConflict,
		"Skill name or revision conflicts with the installed skill",
		"agent_skill_conflict",
	},
	{
		agent.ErrSkillUnavailable,
		http.StatusServiceUnavailable,
		"Skill storage or public GitHub source is unavailable",
		"agent_skill_unavailable",
	},
	{
		agent.ErrSkillLimit,
		http.StatusUnprocessableEntity,
		"An Agent supports 32 custom skills, 128 MB total, and 20 imports per day",
		"agent_skill_limit",
	},
}, agentErrorMappings...)

func writeSkillError(c *gin.Context, err error) {
	writeMappedError(c, err, skillErrorMappings, internalErrorFallback)
}

func (h *Handler) listMessageSkills(c *gin.Context) {
	query := c.Request.URL.Query()
	scope := agent.MessageSkillScope{}
	if len(query) != 1 || (len(query["conversation_id"]) != 1 && len(query["agent_id"]) != 1) {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	var parseErr error
	if values, exists := query["conversation_id"]; exists {
		scope.ConversationID, parseErr = util.ParseGoUUID(values[0])
	} else {
		scope.AgentID, parseErr = util.ParseGoUUID(query.Get("agent_id"))
	}
	if parseErr != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return
	}
	items, err := h.SkillService.ListForMessage(c.Request.Context(), principal, scope)
	if err != nil {
		writeSkillError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Items []agent.MessageSkill `json:"items"`
	}{items})
}
