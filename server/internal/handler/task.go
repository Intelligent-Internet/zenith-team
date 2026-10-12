package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/task"
)

const maxTaskBodyBytes = 512 << 10

type TaskService interface {
	FinalizeResults(
		context.Context,
		auth.UserPrincipal,
		execution.RunOrigin,
		task.FinalizeResultsInput,
	) (task.Response, error)
	ResultSelection(context.Context, auth.UserPrincipal, uuid.UUID) (task.ResultSelection, error)
	SubmitHuddleProposal(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		task.SubmitHuddleProposalInput,
	) (conversation.HuddleProposal, error)
	Create(context.Context, auth.UserPrincipal, task.CreateInput) (task.MutationResult, error)
	List(context.Context, auth.UserPrincipal, task.ListOptions, *task.RunScope) (task.Page, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID, *task.RunScope) (task.Response, error)
	ResolveReference(context.Context, auth.UserPrincipal, string) (uuid.UUID, error)
	Workspace(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		task.WorkspaceOptions,
		*task.RunScope,
	) (task.Workspace, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, task.UpdateInput) (task.MutationResult, error)
	StartExecution(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		task.StartExecutionInput,
	) (task.MutationResult, error)
	MarkFailed(
		context.Context,
		auth.UserPrincipal,
		execution.RunOrigin,
		task.FailureInput,
	) (task.Response, error)
	ListDependencies(context.Context, auth.UserPrincipal, uuid.UUID, *task.RunScope) (task.DependencySet, error)
	ListDependencyGraph(
		context.Context,
		auth.UserPrincipal,
		task.DependencyGraphOptions,
	) (task.DependencyGraph, error)
}

// taskActorRequest is the write-side actor reference: AssigneeRef (always
// `kind: user`) and ExecutorRef (`user` or `agent`) on the contract. The
// kinds each field admits are checked where the field is parsed.
type taskActorRequest struct {
	Kind string `json:"kind" binding:"required,oneof=user agent"`
	ID   string `json:"id"   binding:"required,uuid"`
}

type taskResultPolicyRequest struct {
	ProposalOnly   *bool `json:"proposalOnly"   binding:"required"`
	ReviewRequired *bool `json:"reviewRequired" binding:"required"`
}

func (r *taskResultPolicyRequest) input() *task.ResultPolicy {
	if r == nil {
		return nil
	}
	return &task.ResultPolicy{ProposalOnly: *r.ProposalOnly, ReviewRequired: *r.ReviewRequired}
}

// createTaskRequest: assignee and executedBy are both optional (ADR 0014).
// An omitted assignee is the caller; an omitted executedBy is the assignee.
type createTaskRequest struct {
	ResultPolicy  *taskResultPolicyRequest `json:"resultPolicy"`
	ProjectID     string                   `json:"projectId"     binding:"required,uuid"`
	Labels        json.RawMessage          `json:"labels"`
	StartDate     json.RawMessage          `json:"startDate"`
	DueDate       json.RawMessage          `json:"dueDate"`
	Key           string                   `json:"key"`
	MilestoneType task.MilestoneType       `json:"milestoneType" binding:"required"`
	Title         string                   `json:"title"`
	Description   json.RawMessage          `json:"description"`
	Status        json.RawMessage          `json:"status"`
	Priority      json.RawMessage          `json:"priority"`
	Assignee      json.RawMessage          `json:"assignee"`
	ExecutedBy    json.RawMessage          `json:"executedBy"`
	AttachmentIDs json.RawMessage          `json:"attachmentIds"`
}

type updateTaskRequest struct {
	ResultSelection  *task.ResultSelectionInput `json:"resultSelection"`
	ResultPolicy     *taskResultPolicyRequest   `json:"resultPolicy"`
	Labels           json.RawMessage            `json:"labels"`
	StartDate        json.RawMessage            `json:"startDate"`
	DueDate          json.RawMessage            `json:"dueDate"`
	ExpectedRevision int64                      `json:"expectedRevision" binding:"revision"`
	MilestoneType    json.RawMessage            `json:"milestoneType"`
	Title            json.RawMessage            `json:"title"`
	Description      json.RawMessage            `json:"description"`
	Status           json.RawMessage            `json:"status"`
	Priority         json.RawMessage            `json:"priority"`
	Assignee         json.RawMessage            `json:"assignee"`
	ExecutedBy       json.RawMessage            `json:"executedBy"`
	ConfirmHandoff   bool                       `json:"confirmHandoff"`
}

type startTaskExecutionRequest struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"revision"`
}

type taskPageResponse struct {
	Items []task.Response `json:"items"`
}

type taskWorkspaceResponse struct {
	Task                 task.Response                    `json:"task"`
	Conversation         directConversationResponse       `json:"conversation"`
	Messages             []messageWithAttachmentsResponse `json:"messages"`
	EarliestAvailableSeq int64                            `json:"earliestAvailableSeq"`
	ThroughSeq           int64                            `json:"throughSeq"`
	RunTimelines         []task.RunTimeline               `json:"runTimelines"`
	ActiveRuns           []execution.RunView              `json:"activeRuns"`
	RunQueue             conversation.RunQueueProjection  `json:"runQueue"`
	Dependencies         task.DependencySet               `json:"dependencies"`
}

func newTaskWorkspaceResponse(workspace task.Workspace) taskWorkspaceResponse {
	conversationResponse := newDirectConversationResponse(workspace.Conversation.Response)
	conversationResponse.TaskID = &workspace.Conversation.TaskID
	return taskWorkspaceResponse{
		Task:                 workspace.Task,
		Conversation:         conversationResponse,
		Messages:             messageWithAttachmentsResponses(workspace.Messages),
		EarliestAvailableSeq: workspace.EarliestAvailableSeq,
		ThroughSeq:           workspace.ThroughSeq,
		RunTimelines:         workspace.RunTimelines,
		ActiveRuns:           workspace.ActiveRuns,
		RunQueue:             workspace.RunQueue,
		Dependencies:         workspace.Dependencies,
	}
}

func (h *Handler) registerTaskRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.CompanyService == nil ||
		h.TaskService == nil {
		return
	}
	companyRoutes := group.Group(
		"/companies/:companyId",
		middleware.RequireUserAuth(h.AuthService),
		middleware.RequireCompanyMember(h.CompanyService),
	)
	companyRoutes.GET("/task-dependencies", h.listTaskDependencyGraph)
	routes := companyRoutes.Group("/tasks")
	routes.GET("", h.listTasks)
	routes.POST("", h.createTask)
	routes.GET("/:taskId", h.getTask)
	routes.GET("/:taskId/workspace", h.getTaskWorkspace)
	routes.PATCH("/:taskId", h.updateTask)
	routes.GET("/:taskId/dependencies", h.listTaskDependencies)
	routes.POST("/:taskId/runs", h.startTaskExecution)
}

func (h *Handler) getTaskWorkspace(c *gin.Context) {
	options, err := parseTaskWorkspaceOptions(c.Request.URL.RawQuery)
	if err != nil {
		writeTaskError(c, task.ErrInvalidInput)
		return
	}
	principal, taskID, ok := taskRequestIdentity(c)
	if !ok {
		return
	}
	run, ok := h.taskRunScope(c, principal)
	if !ok {
		return
	}
	result, err := h.TaskService.Workspace(c.Request.Context(), principal, taskID, options, run)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	if result.RunTimelines == nil {
		result.RunTimelines = []task.RunTimeline{}
	}
	if result.ActiveRuns == nil {
		result.ActiveRuns = []execution.RunView{}
	}
	if result.RunQueue.Items == nil {
		result.RunQueue.Items = []conversation.RunQueueItem{}
	}
	if result.Dependencies.BlockedBy == nil {
		result.Dependencies.BlockedBy = []task.DependencyResponse{}
	}
	if result.Dependencies.Blocks == nil {
		result.Dependencies.Blocks = []task.DependencyResponse{}
	}
	c.JSON(http.StatusOK, newTaskWorkspaceResponse(result))
}

func (h *Handler) listTaskDependencyGraph(c *gin.Context) {
	options, err := parseTaskDependencyGraphOptions(c.Request.URL.RawQuery)
	if err != nil {
		writeTaskError(c, task.ErrInvalidInput)
		return
	}
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	result, err := h.TaskService.ListDependencyGraph(
		c.Request.Context(),
		principal,
		options,
	)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	if result.Dependencies == nil {
		result.Dependencies = []task.DependencyGraphEdge{}
	}
	if result.Tasks == nil {
		result.Tasks = []task.DependencyGraphTask{}
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) listTaskDependencies(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	run, ok := h.taskRunScope(c, principal)
	if !ok {
		return
	}
	taskID, ok := h.taskReadTarget(c, principal)
	if !ok {
		return
	}
	result, err := h.TaskService.ListDependencies(c.Request.Context(), principal, taskID, run)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	if result.BlockedBy == nil {
		result.BlockedBy = []task.DependencyResponse{}
	}
	if result.Blocks == nil {
		result.Blocks = []task.DependencyResponse{}
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) createTask(c *gin.Context) {
	request, err := bindRequest[createTaskRequest](c, maxTaskBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	labels, labelsErr := parseTaskLabels(request.Labels)
	startDate, startDateErr := parseTaskDate(request.StartDate)
	dueDate, dueDateErr := parseTaskDate(request.DueDate)
	description, _, descriptionErr := parseOptionalTaskString(request.Description)
	statusValue, _, statusErr := parseOptionalTaskString(request.Status)
	priorityValue, _, priorityErr := parseOptionalTaskString(request.Priority)
	assignee, _, assigneeErr := parseOptionalTaskAssignee(request.Assignee)
	executedBy, executedByPresent, executedByErr := parseOptionalTaskActor(request.ExecutedBy)
	attachmentIDs, attachmentsErr := parseTaskAttachmentIDs(request.AttachmentIDs)
	if errors.Join(
		labelsErr, startDateErr, dueDateErr,
		descriptionErr,
		statusErr,
		priorityErr,
		assigneeErr,
		executedByErr,
		attachmentsErr,
	) != nil {
		writeTaskError(c, task.ErrInvalidInput)
		return
	}
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	run, ok := h.taskRunScope(c, principal)
	if !ok {
		return
	}
	input := task.CreateInput{
		ResultPolicy:  request.ResultPolicy.input(),
		ProjectID:     uuid.MustParse(request.ProjectID),
		Key:           request.Key,
		MilestoneType: request.MilestoneType, Title: request.Title,
		Description: description, Status: task.Status(statusValue), Priority: task.Priority(priorityValue),
		Assignee: assignee, Labels: labels, StartDate: startDate.Value, DueDate: dueDate.Value,
		Run: run, AttachmentIDs: attachmentIDs,
	}
	if executedByPresent {
		input.ExecutedBy = &executedBy
	}
	result, err := h.TaskService.Create(c.Request.Context(), principal, input)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusCreated, task.NewMutationResponse(result))
}

// taskRunScope is the scope a Task read or create is made under (runReadScope).
// A Run reads only its own Tasks (owner decision, 2026-09-26): the ones its
// Agent does for the member it works for, its authorizer (ADR 0014), and
// only in Projects everyone reading its answer may read (ADR 0017).
func (h *Handler) taskRunScope(c *gin.Context, principal auth.UserPrincipal) (*task.RunScope, bool) {
	return h.runReadScope(c, principal, writeTaskError)
}

// admitTaskRunRead fences a read of a Task's other surfaces that the task
// service does not serve — its results — when the request carries a Run's
// proof: the Task must be the Run's own, read under the same scope as
// getTask, or the refusal is written instead. A member's own read carries
// no proof and is admitted without a Task read.
func (h *Handler) admitTaskRunRead(c *gin.Context, principal auth.UserPrincipal, taskID uuid.UUID) bool {
	run, ok := h.taskRunScope(c, principal)
	if !ok || run == nil {
		return ok
	}
	if h.TaskService == nil {
		writeTaskError(c, errors.New("task service is not configured"))
		return false
	}
	if _, err := h.TaskService.Get(c.Request.Context(), principal, taskID, run); err != nil {
		writeTaskError(c, err)
		return false
	}
	return true
}

// taskReadTarget is the Task a read names. The two Task reads take the id,
// or the Task's key or {taskPrefix}-{number} label as people give it: a Run
// told to "run BSC-8" cannot list a Task that is not its own to find the id,
// and must still get the same refusal as by id. A name that is no Task is
// 404.
func (h *Handler) taskReadTarget(c *gin.Context, principal auth.UserPrincipal) (uuid.UUID, bool) {
	reference := c.Param("taskId")
	if id, err := util.ParseGoUUID(reference); err == nil {
		return id, true
	}
	id, err := h.TaskService.ResolveReference(c.Request.Context(), principal, reference)
	if err != nil {
		writeTaskError(c, err)
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) listTasks(c *gin.Context) {
	options, err := parseTaskListOptions(c)
	if err != nil {
		writeTaskError(c, task.ErrInvalidInput)
		return
	}
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	run, ok := h.taskRunScope(c, principal)
	if !ok {
		return
	}
	page, err := h.TaskService.List(c.Request.Context(), principal, options, run)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	if page.Tasks == nil {
		page.Tasks = []task.Response{}
	}
	c.JSON(http.StatusOK, taskPageResponse{Items: page.Tasks})
}

func (h *Handler) getTask(c *gin.Context) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return
	}
	run, ok := h.taskRunScope(c, principal)
	if !ok {
		return
	}
	taskID, ok := h.taskReadTarget(c, principal)
	if !ok {
		return
	}
	value, err := h.TaskService.Get(c.Request.Context(), principal, taskID, run)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, value)
}

func (h *Handler) updateTask(c *gin.Context) {
	principal, taskID, ok := taskRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[updateTaskRequest](c, maxTaskBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	if (request.ResultSelection != nil || request.ResultPolicy != nil) && middleware.RefuseRunProof(c) {
		return
	}
	labels, labelsErr := parseTaskLabels(request.Labels)
	startDate, startDateErr := parseTaskDate(request.StartDate)
	dueDate, dueDateErr := parseTaskDate(request.DueDate)
	milestoneValue, milestonePresent, milestoneErr := parseOptionalTaskString(request.MilestoneType)
	title, titlePresent, titleErr := parseOptionalTaskString(request.Title)
	description, descriptionPresent, descriptionErr := parseOptionalTaskString(request.Description)
	statusValue, statusPresent, statusErr := parseOptionalTaskString(request.Status)
	priorityValue, priorityPresent, priorityErr := parseOptionalTaskString(request.Priority)
	assignee, assigneePresent, assigneeErr := parseOptionalTaskAssignee(request.Assignee)
	executedBy, executedByPresent, executedByErr := parseOptionalTaskActor(request.ExecutedBy)
	if errors.Join(
		labelsErr, startDateErr, dueDateErr,
		milestoneErr,
		titleErr, descriptionErr, statusErr, priorityErr, assigneeErr, executedByErr,
	) != nil {
		writeTaskError(c, task.ErrInvalidInput)
		return
	}
	input := task.UpdateInput{
		ResultSelection: request.ResultSelection,
		ResultPolicy:    request.ResultPolicy.input(),
		Labels:          labels, StartDate: startDate, DueDate: dueDate,
		ExpectedRevision: request.ExpectedRevision,
		ConfirmHandoff:   request.ConfirmHandoff,
		MilestoneType: task.MilestoneTypeChange{
			Present: milestonePresent,
			Value:   task.MilestoneType(milestoneValue),
		},
	}
	if titlePresent {
		input.Title = &title
	}
	if descriptionPresent {
		input.Description = &description
	}
	if statusPresent {
		status := task.Status(statusValue)
		input.Status = &status
	}
	if priorityPresent {
		priority := task.Priority(priorityValue)
		input.Priority = &priority
	}
	if assigneePresent {
		input.Assignee = &assignee
	}
	if executedByPresent {
		input.ExecutedBy = &executedBy
	}
	result, err := h.TaskService.Update(c.Request.Context(), principal, taskID, input)
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusOK, task.NewMutationResponse(result))
}

func (h *Handler) startTaskExecution(c *gin.Context) {
	principal, taskID, ok := taskRequestIdentity(c)
	if !ok {
		return
	}
	request, err := bindRequest[startTaskExecutionRequest](c, maxTaskBodyBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	result, err := h.TaskService.StartExecution(c.Request.Context(), principal, taskID, task.StartExecutionInput{
		ExpectedRevision: request.ExpectedRevision,
	})
	if err != nil {
		writeTaskError(c, err)
		return
	}
	c.JSON(http.StatusCreated, task.NewMutationResponse(result))
}

func taskPrincipal(c *gin.Context) (auth.UserPrincipal, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
	}
	return principal, ok
}

func taskRequestIdentity(c *gin.Context) (auth.UserPrincipal, uuid.UUID, bool) {
	principal, ok := taskPrincipal(c)
	if !ok {
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	taskID, err := util.ParseGoUUID(c.Param("taskId"))
	if err != nil {
		writeTaskError(c, task.ErrNotFound)
		return auth.UserPrincipal{}, uuid.Nil, false
	}
	return principal, taskID, true
}

func parseTaskActor(request taskActorRequest) (task.ActorRef, error) {
	id, err := util.ParseGoUUID(request.ID)
	actor := task.ActorRef{Kind: task.ActorKind(request.Kind), ID: id}
	if err != nil || actor.Kind != task.ActorUser && actor.Kind != task.ActorAgent {
		return task.ActorRef{}, task.ErrInvalidInput
	}
	return actor, nil
}

func parseOptionalTaskActor(raw json.RawMessage) (task.ActorRef, bool, error) {
	if len(raw) == 0 {
		return task.ActorRef{}, false, nil
	}
	if string(raw) == "null" {
		return task.ActorRef{}, false, task.ErrInvalidInput
	}
	var request taskActorRequest
	if err := util.BindStrictJSON(bytes.NewReader(raw), &request); err != nil {
		return task.ActorRef{}, false, task.ErrInvalidInput
	}
	actor, err := parseTaskActor(request)
	return actor, true, err
}

// parseOptionalTaskAssignee is parseOptionalTaskActor narrowed to the
// assignee rule: always a member (ADR 0014).
func parseOptionalTaskAssignee(raw json.RawMessage) (task.ActorRef, bool, error) {
	actor, present, err := parseOptionalTaskActor(raw)
	if err != nil || !present {
		return actor, present, err
	}
	if actor.Kind != task.ActorUser {
		return task.ActorRef{}, true, task.ErrInvalidInput
	}
	return actor, true, nil
}

func parseOptionalTaskUUID(raw json.RawMessage) (uuid.UUID, bool, error) {
	if len(raw) == 0 {
		return uuid.Nil, false, nil
	}
	if string(raw) == "null" {
		return uuid.Nil, false, task.ErrInvalidInput
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return uuid.Nil, false, task.ErrInvalidInput
	}
	id, err := util.ParseGoUUID(value)
	if err != nil {
		return uuid.Nil, false, task.ErrInvalidInput
	}
	return id, true, nil
}

func parseOptionalTaskString(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	if string(raw) == "null" {
		return "", false, task.ErrInvalidInput
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, task.ErrInvalidInput
	}
	return value, true, nil
}

func parseTaskListOptions(c *gin.Context) (task.ListOptions, error) {
	query, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return task.ListOptions{}, task.ErrInvalidInput
	}
	allowed := map[string]bool{
		"status": true, "priority": true,
		"assignee_id": true, "executed_by_agent_id": true,
		"dependencies_resolved": true, "project_id": true,
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			return task.ListOptions{}, task.ErrInvalidInput
		}
	}
	var options task.ListOptions
	if values, exists := query["project_id"]; exists {
		id, err := util.ParseGoUUID(values[0])
		if err != nil {
			return task.ListOptions{}, task.ErrInvalidInput
		}
		options.ProjectID = &id
	}
	if values, exists := query["status"]; exists {
		parts := strings.Split(values[0], ",")
		options.Statuses = make([]task.Status, len(parts))
		for index, part := range parts {
			if part == "" {
				return task.ListOptions{}, task.ErrInvalidInput
			}
			options.Statuses[index] = task.Status(part)
		}
	}
	if values, exists := query["priority"]; exists {
		parts := strings.Split(values[0], ",")
		options.Priorities = make([]task.Priority, len(parts))
		for index, part := range parts {
			if part == "" {
				return task.ListOptions{}, task.ErrInvalidInput
			}
			options.Priorities[index] = task.Priority(part)
		}
	}
	if values, exists := query["assignee_id"]; exists {
		id, err := util.ParseGoUUID(values[0])
		if err != nil {
			return task.ListOptions{}, task.ErrInvalidInput
		}
		options.AssigneeUserID = &id
	}
	if values, exists := query["executed_by_agent_id"]; exists {
		id, err := util.ParseGoUUID(values[0])
		if err != nil {
			return task.ListOptions{}, task.ErrInvalidInput
		}
		options.ExecutedByAgentID = &id
	}
	if values, exists := query["dependencies_resolved"]; exists {
		var resolved bool
		switch values[0] {
		case "true":
			resolved = true
		case "false":
		default:
			return task.ListOptions{}, task.ErrInvalidInput
		}
		options.DependenciesResolved = &resolved
	}
	return options, nil
}

func parseTaskDependencyGraphOptions(rawQuery string) (task.DependencyGraphOptions, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return task.DependencyGraphOptions{}, task.ErrInvalidInput
	}
	if len(query) != 0 {
		return task.DependencyGraphOptions{}, task.ErrInvalidInput
	}
	return task.DependencyGraphOptions{}, nil
}

func parseTaskWorkspaceOptions(rawQuery string) (task.WorkspaceOptions, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return task.WorkspaceOptions{}, task.ErrInvalidInput
	}
	for key, values := range query {
		if key != "message_limit" || len(values) != 1 || values[0] == "" {
			return task.WorkspaceOptions{}, task.ErrInvalidInput
		}
	}
	options := task.WorkspaceOptions{MessageLimit: 50}
	if values, exists := query["message_limit"]; exists {
		limit, err := strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 100 {
			return task.WorkspaceOptions{}, task.ErrInvalidInput
		}
		options.MessageLimit = limit
	}
	return options, nil
}

var taskErrorMappings = []errorMapping{
	{
		task.ErrResultSelectionRequired,
		http.StatusConflict,
		"Select the delivered results before completing this Task",
		"task_result_selection_required",
	},
	{
		task.ErrResultApplicationRequired,
		http.StatusConflict,
		"Accept the required suggestions before approving these results",
		"task_result_application_required",
	},
	{task.ErrProjectArchived, http.StatusConflict, "Project is archived", "project_archived"},
	{task.ErrUnauthenticated, http.StatusUnauthorized, "authentication is required", "authentication_required"},
	// A create sent through a Run's channel whose Run is no longer live.
	runTokenInvalidMapping,
	{task.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{task.ErrConflict, http.StatusConflict, "Task mutation conflicts with current state", "task_conflict"},
	{task.ErrNotFound, http.StatusNotFound, "Task not found", "task_not_found"},
	// A Task in a Project the caller cannot read answers exactly like a missing
	// one (ADR 0017).
	{auth.ErrTaskNotVisible, http.StatusNotFound, "Task not found", "task_not_found"},
	// A list filtered by a Project the caller cannot read answers exactly like
	// one filtered by a Project that does not exist.
	{auth.ErrProjectNotVisible, http.StatusNotFound, "Project not found", "project_not_found"},
	// A Task assigned or handed to a member who cannot read its Project. The
	// sentence names nobody.
	{
		task.ErrAssigneeOutsideProject,
		http.StatusConflict,
		"the assignee cannot see this Project; give them access to it first",
		"project_member_required",
	},
	projectPermissionRequiredMapping,
	// The Task's own rules (ADR 0014): who may start it, who may assign it,
	// who may move it out of review, and the confirmation a handoff of a
	// Task that has run needs.
	{
		task.ErrAssigneeRequired,
		http.StatusForbidden,
		"only the Task's assignee can start it; it runs on their computer",
		"task_assignee_required",
	},
	{
		task.ErrAssignPermissionRequired,
		http.StatusForbidden,
		"Only owners and admins can assign a task to someone else. You can assign it to yourself.",
		"company_permission_required",
	},
	{
		task.ErrHandoffPermissionRequired,
		http.StatusForbidden,
		"Only the task's assignee, owners and admins can change who works on it.",
		"company_permission_required",
	},
	{
		task.ErrReviewPermissionRequired,
		http.StatusForbidden,
		"Only the task's assignee, owners and admins can move it out of review.",
		"company_permission_required",
	},
	{
		task.ErrAssigneeInactive,
		http.StatusConflict,
		"the assignee is not an active member of this Company",
		"task_conflict",
	},
	// The handoff refusal carries runLive and has a second detail for a Run
	// in flight: writeTaskHandoffRefusal.
	taskHandoffMapping,
	// The kernel's refusals surface with their own codes: the caller has not
	// set the Agent up on a runtime of theirs, or is not the assignee the
	// Run must execute for.
	{
		execution.ErrRunAgentNotBound,
		http.StatusConflict,
		"set this Agent up on one of your computers first",
		"agent_binding_required",
	},
	{
		execution.ErrRunTaskAssigneeRequired,
		http.StatusForbidden,
		"only the Task's assignee can start it; it runs on their computer",
		"task_assignee_required",
	},
	taskOutsideRunScopeMapping,
	{
		task.ErrDependencyBlocked,
		http.StatusConflict,
		"Task execution is blocked by an incomplete dependency",
		"task_dependency_blocked",
	},
	{task.ErrAgentBusy, http.StatusConflict, "Task report recipient Agent is busy", "task_report_agent_busy"},
	{
		task.ErrAttachmentUnavailable,
		http.StatusConflict,
		"Task attachment is unavailable",
		"task_attachment_unavailable",
	},
	{task.ErrNoPendingReports, http.StatusConflict, "No pending Task reports", "task_report_no_pending"},
}

// taskHandoffMapping is the handoff refusal of a Task that ran before and
// has nothing live. It is named because the boundary serves it with a body
// the table cannot express; it stays inside taskErrorMappings so the
// error-code registry still sees it.
var taskHandoffMapping = errorMapping{
	task.ErrHandoffConfirmationRequired,
	http.StatusConflict,
	"this Task has run before; confirm the handoff to move it and start the work over",
	"task_handoff_confirmation_required",
}

// taskHandoffLiveRunMapping is the handoff refusal when confirming would
// cancel a Run in flight right now. Same status and code as the table row:
// the two details only tell the person what confirming costs.
var taskHandoffLiveRunMapping = errorMapping{
	task.ErrHandoffConfirmationRequired,
	http.StatusConflict,
	"this Task has a run in flight; confirm the handoff to cancel it and move the Task",
	"task_handoff_confirmation_required",
}

// parseTaskAttachmentIDs reads the optional file ids a create attaches. The
// count and uniqueness are the Task domain's rules; only the shape is read
// here.
func parseTaskAttachmentIDs(raw json.RawMessage) ([]uuid.UUID, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, task.ErrInvalidInput
	}
	result := make([]uuid.UUID, len(values))
	for index, value := range values {
		id, err := util.ParseGoUUID(value)
		if err != nil {
			return nil, task.ErrInvalidInput
		}
		result[index] = id
	}
	return result, nil
}

func writeTaskError(c *gin.Context, err error) {
	if errors.Is(err, task.ErrHandoffConfirmationRequired) {
		var handoff *task.HandoffConfirmationError
		writeTaskHandoffRefusal(c, errors.As(err, &handoff) && handoff.RunLive)
		return
	}
	if writeTaskExecuteRefusal(c, err) {
		return
	}
	writeMappedError(c, err, taskErrorMappings, internalErrorFallback)
}

// writeTaskExecuteRefusal writes an execution.ExecutePermissionError, from a Task
// write or a Task chat message, and reports whether err was one. It has the
// same status and code as the plain project_permission_required row; only
// the detail is composed, naming the Agent.
func writeTaskExecuteRefusal(c *gin.Context, err error) bool {
	var execute *execution.ExecutePermissionError
	if !errors.As(err, &execute) {
		return false
	}
	var callerID uuid.UUID
	if c.Request != nil {
		if principal, ok := auth.UserPrincipalFromContext(c.Request.Context()); ok {
			callerID = principal.UserID
		}
	}
	writeMappedError(c, err, []errorMapping{{
		execute, http.StatusForbidden,
		taskExecutePermissionDetail(execute, callerID), "project_permission_required",
	}}, internalErrorFallback)
	return true
}

// writeTaskHandoffRefusal renders the handoff 409: the standard error
// envelope with `runLive` flat beside it, so a client words its confirmation
// from the same answer the server gave — confirming cancels a Run in flight,
// or only moves a Task that ran before. A Task in progress may have nothing
// live, so the client cannot read this off the Task.
//
// It composes the envelope here, as writeTerminalHeadMismatch does, because
// util.WriteError writes a closed three-key body.
func writeTaskHandoffRefusal(c *gin.Context, runLive bool) {
	mapping := taskHandoffMapping
	if runLive {
		mapping = taskHandoffLiveRunMapping
	}
	var requestID any
	if id := util.RequestIDFromContext(c); id != "" {
		requestID = id
	}
	c.JSON(mapping.status, gin.H{
		"detail":    mapping.detail,
		"code":      mapping.code,
		"requestId": requestID,
		"runLive":   runLive,
	})
}

// taskExecutePermissionDetail is the 403 a Task Run start gets when the
// assignee lacks project.tasks.execute (ADR 0017), in a few plain words: the
// Agent that did not start and that the person may not run Tasks here, so
// nobody goes to fix a provider sign-in. It never names the Project or tells
// anyone to ask someone, and a colleague whose message would have waited for
// the assignee is told the assignee cannot run it, without naming them.
func taskExecutePermissionDetail(refusal *execution.ExecutePermissionError, callerID uuid.UUID) string {
	reason := "you can't run tasks here."
	if refusal.AssigneeID != uuid.Nil && refusal.AssigneeID != callerID {
		reason = "the assignee can't run tasks here."
	}
	if refusal.AgentName == "" {
		return strings.ToUpper(reason[:1]) + reason[1:]
	}
	return refusal.AgentName + " didn't start: " + reason
}
