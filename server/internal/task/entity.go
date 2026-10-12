package task

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/protocol"
)

type ActorKind = protocol.ActorKind

const (
	ActorUser  ActorKind = "user"
	ActorAgent ActorKind = "agent"
)

// CreateInput is a create as the service reads it. Assignee is the member
// responsible for running the Task on their own computer (ADR 0014); a zero
// Assignee means the caller. ExecutedBy is who does the work: an Agent, or
// the assignee themself; nil means the assignee.
type CreateInput struct {
	ResultPolicy  *ResultPolicy
	ProjectID     uuid.UUID
	Key           string
	MilestoneType MilestoneType
	Title         string
	Description   string
	Status        Status
	Priority      Priority
	Assignee      ActorRef
	ExecutedBy    *ActorRef
	Labels        []string
	StartDate     *string
	DueDate       *string
	Schedule      *schedule.Trigger
	// Run is the scope of the Run a create comes from, nil for a member's
	// own create. A Run files work only for the member it works for.
	Run *RunScope
	// AttachmentIDs are ready uploads of the creator's that the Task carries
	// as its input files. They need the assignment Run a create starts when
	// the caller is the assignee and an Agent executes (ADR 0014), because
	// they travel on a message that Run owns.
	AttachmentIDs []uuid.UUID
}

// maxCreateAttachments is the most files one message may carry, and so the
// most one create may attach.
const maxCreateAttachments = 20

// UUIDChange distinguishes an omitted update field from a replacement value.
type UUIDChange struct {
	Present bool
	Value   uuid.UUID
}

// UpdateInput is a partial update. Changing Assignee or ExecutedBy is a
// handoff (ADR 0014): once the Task has run, it needs ConfirmHandoff, and
// a confirmed handoff cancels the live work and moves the Task to the new
// assignee's computer.
type UpdateInput struct {
	ResultSelection  *ResultSelectionInput
	ResultPolicy     *ResultPolicy
	ExpectedRevision int64
	MilestoneType    MilestoneTypeChange
	Title            *string
	Description      *string
	Status           *Status
	Priority         *Priority
	Assignee         *ActorRef
	ExecutedBy       *ActorRef
	ConfirmHandoff   bool
	Labels           []string
	StartDate        DateChange
	DueDate          DateChange
}

type StartExecutionInput struct {
	ExpectedRevision int64
}

type ListOptions struct {
	assistedTaskID       *uuid.UUID
	ProjectID            *uuid.UUID
	Statuses             []Status
	Priorities           []Priority
	AssigneeUserID       *uuid.UUID
	ExecutedByAgentID    *uuid.UUID
	DependenciesResolved *bool
}

type Page struct {
	Tasks []Response
}

type MutationResult struct {
	Task  Response
	RunID *uuid.UUID
	// AssigneeOutsideProject lists the written Tasks whose assignee cannot
	// read the Task's Project (ADR 0017). An assignee who loses access keeps
	// the Task until someone hands it over; the receipt says so without
	// naming anyone.
	AssigneeOutsideProject []uuid.UUID
}

// MutationResponse is the one Task-write wire receipt: the Task flat at the
// top level with the run the transition enqueued beside it. Whether a run was
// created is exactly runId != null — there is no separate flag. PATCH and the
// run-start action share this shape. AssigneeOutsideProjectTaskIDs is
// never null.
type MutationResponse struct {
	Response
	RunID                         *uuid.UUID  `json:"runId"`
	AssigneeOutsideProjectTaskIDs []uuid.UUID `json:"assigneeOutsideProjectTaskIds"`
}

// NewMutationResponse is the wire receipt of one Task write.
func NewMutationResponse(result MutationResult) MutationResponse {
	outside := result.AssigneeOutsideProject
	if outside == nil {
		outside = []uuid.UUID{}
	}
	return MutationResponse{Response: result.Task, RunID: result.RunID, AssigneeOutsideProjectTaskIDs: outside}
}

// normalizeCreateInput settles the assignment: the caller is the assignee
// unless the create names another member, and the assignee does the work
// unless the create names an Agent. A user executedBy that is not the
// assignee is invalid — nobody runs a Task on somebody else's computer.
func normalizeCreateInput(input CreateInput, callerID uuid.UUID) (CreateInput, error) {
	if input.Run != nil && input.ResultPolicy != nil {
		return CreateInput{}, ErrInvalidInput
	}
	if input.Assignee == (ActorRef{}) {
		input.Assignee = ActorRef{Kind: ActorUser, ID: callerID}
	}
	if input.ProjectID == uuid.Nil || !ValidMilestoneType(input.MilestoneType) ||
		!validAssignee(input.Assignee) {
		return CreateInput{}, ErrInvalidInput
	}
	if input.ExecutedBy == nil {
		executedBy := ActorRef{Kind: ActorUser, ID: input.Assignee.ID}
		input.ExecutedBy = &executedBy
	}
	if !validExecutedBy(*input.ExecutedBy, input.Assignee) {
		return CreateInput{}, ErrInvalidInput
	}
	input.Title = protocol.TrimContractWhitespace(input.Title)
	// No key means the server mints one at create; a key that is only
	// whitespace is a malformed key, not an omitted one.
	key := protocol.TrimContractWhitespace(input.Key)
	if input.Key != "" && !validKey(key) {
		return CreateInput{}, ErrInvalidInput
	}
	input.Key = key
	input.Description = protocol.TrimContractWhitespace(input.Description)
	if input.Status == "" {
		input.Status = StatusBacklog
	}
	if input.Priority == "" {
		input.Priority = PriorityMedium
	}
	if !validTitle(input.Title) || !validDescription(input.Description) ||
		!validMutationStatus(input.Status) || !validPriority(input.Priority) {
		return CreateInput{}, ErrInvalidInput
	}
	var err error
	input.Labels, err = normalizeLabels(input.Labels)
	if err != nil || !validTaskDates(input.StartDate, input.DueDate) {
		return CreateInput{}, ErrInvalidInput
	}
	if len(input.AttachmentIDs) > maxCreateAttachments {
		return CreateInput{}, ErrInvalidInput
	}
	seen := make(map[uuid.UUID]bool, len(input.AttachmentIDs))
	for _, attachmentID := range input.AttachmentIDs {
		if !util.ValidUUID(attachmentID) || seen[attachmentID] {
			return CreateInput{}, ErrInvalidInput
		}
		seen[attachmentID] = true
	}
	if input.AttachmentIDs == nil {
		input.AttachmentIDs = []uuid.UUID{}
	}
	return input, nil
}

func normalizeUpdateInput(input UpdateInput) (UpdateInput, error) {
	if input.ResultSelection != nil &&
		(input.Title != nil || input.Description != nil || input.Priority != nil || input.Assignee != nil || input.ExecutedBy != nil || input.Labels != nil || input.StartDate.Present || input.DueDate.Present || input.MilestoneType.Present || input.ConfirmHandoff) {
		return UpdateInput{}, ErrInvalidInput
	}

	if !input.MilestoneType.Present && input.Title == nil && input.Description == nil &&
		input.Status == nil && input.Priority == nil && input.Assignee == nil && input.ExecutedBy == nil &&
		input.Labels == nil && !input.StartDate.Present && !input.DueDate.Present &&
		input.ResultSelection == nil && input.ResultPolicy == nil {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.MilestoneType.Present && !ValidMilestoneType(input.MilestoneType.Value) {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.Title != nil {
		title := protocol.TrimContractWhitespace(*input.Title)
		if !validTitle(title) {
			return UpdateInput{}, ErrInvalidInput
		}
		input.Title = &title
	}
	if input.Description != nil {
		description := protocol.TrimContractWhitespace(*input.Description)
		if !validDescription(description) {
			return UpdateInput{}, ErrInvalidInput
		}
		input.Description = &description
	}
	if input.Status != nil && !validMutationStatus(*input.Status) ||
		input.Priority != nil && !validPriority(*input.Priority) ||
		input.Assignee != nil && !validAssignee(*input.Assignee) ||
		input.ExecutedBy != nil && !validActor(*input.ExecutedBy) {
		return UpdateInput{}, ErrInvalidInput
	}
	// A user executedBy must be the assignee the update lands on; when the
	// update also moves the assignee, the two must agree here. Against the
	// current assignee the service checks after it has locked the row.
	if input.ExecutedBy != nil && input.ExecutedBy.Kind == ActorUser && input.Assignee != nil &&
		input.ExecutedBy.ID != input.Assignee.ID {
		return UpdateInput{}, ErrInvalidInput
	}
	if input.Labels != nil {
		labels, err := normalizeLabels(input.Labels)
		if err != nil {
			return UpdateInput{}, err
		}
		input.Labels = labels
	}
	if !validTaskDates(input.StartDate.Value, input.DueDate.Value) {
		return UpdateInput{}, ErrInvalidInput
	}

	return input, nil
}

func normalizeListOptions(options ListOptions) (ListOptions, error) {
	if options.ProjectID != nil && *options.ProjectID == uuid.Nil {
		return ListOptions{}, ErrInvalidInput
	}
	for _, status := range options.Statuses {
		if !validStatus(status) {
			return ListOptions{}, ErrInvalidInput
		}
	}
	for _, priority := range options.Priorities {
		if !validPriority(priority) {
			return ListOptions{}, ErrInvalidInput
		}
	}
	if options.AssigneeUserID != nil && !util.ValidUUID(*options.AssigneeUserID) ||
		options.ExecutedByAgentID != nil && !util.ValidUUID(*options.ExecutedByAgentID) {
		return ListOptions{}, ErrInvalidInput
	}
	return options, nil
}

func validActor(actor ActorRef) bool {
	return (actor.Kind == ActorUser || actor.Kind == ActorAgent) && util.ValidUUID(actor.ID)
}

// validAssignee is the assignee rule (ADR 0014): always a member.
func validAssignee(actor ActorRef) bool {
	return actor.Kind == ActorUser && util.ValidUUID(actor.ID)
}

// validExecutedBy is the executedBy rule: an Agent, or the assignee
// themself. Another member cannot be named — a Run executes on the
// assignee's own computer, and nobody else's.
func validExecutedBy(executedBy, assignee ActorRef) bool {
	switch executedBy.Kind {
	case ActorAgent:
		return util.ValidUUID(executedBy.ID)
	case ActorUser:
		return executedBy.ID == assignee.ID && util.ValidUUID(executedBy.ID)
	default:
		return false
	}
}

// executedByAgentID is the executing Agent's id, or nil when the assignee
// does the work themself.
func executedByAgentID(executedBy ActorRef) *uuid.UUID {
	if executedBy.Kind != ActorAgent {
		return nil
	}
	id := executedBy.ID
	return &id
}

func sameActor(left, right ActorRef) bool {
	return left.Kind == right.Kind && left.ID == right.ID
}

func validStatus(value Status) bool {
	switch value {
	case StatusBacklog, StatusTodo, StatusInProgress, StatusInReview, StatusDone, StatusBlocked,
		StatusFailed, StatusError, StatusCancelled:
		return true
	default:
		return false
	}
}

// validMutationStatus excludes the two server-owned failure outcomes from
// ordinary create/update requests. Error is a technical Run failure; failed
// is an Agent-reported business outcome.
func validMutationStatus(value Status) bool {
	return validStatus(value) && value != StatusFailed && value != StatusError
}

func validPriority(value Priority) bool {
	switch value {
	case PriorityLow, PriorityMedium, PriorityHigh, PriorityUrgent:
		return true
	default:
		return false
	}
}

// MintKey is the key a Task gets when its creator chose none: the company's
// task prefix and the Task's number, the exact string every surface displays
// for it. A Company Plan chooses its own stable slugs instead. The format is
// protocol.TaskLabel, the one formatter, so a key minted here and the label
// a notification quotes for an older plan-slug Task cannot drift apart.
func MintKey(taskPrefix string, number int64) string {
	return protocol.TaskLabel(taskPrefix, number)
}

// KeyInMintedNamespace reports whether key has the shape the server mints,
// {taskPrefix}-{digits}, in any letter case. That namespace belongs to the
// server: a chosen key shaped like a label would either lie about the
// Task's number or take the label of a later Task, and the unique key fence
// would then refuse every keyless create once the counter reached it.
func KeyInMintedNamespace(taskPrefix, key string) bool {
	if len(key) <= len(taskPrefix)+1 || key[len(taskPrefix)] != '-' ||
		!strings.EqualFold(key[:len(taskPrefix)], taskPrefix) {
		return false
	}
	for _, r := range key[len(taskPrefix)+1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validKey(value string) bool {
	return value != "" && utf8.ValidString(value) && protocol.TrimContractWhitespace(value) == value
}

func validTitle(value string) bool {
	return value != "" && utf8.ValidString(value) && protocol.TrimContractWhitespace(value) == value &&
		utf8.RuneCountInString(value) <= 240
}

func validDescription(value string) bool {
	return utf8.ValidString(value) && protocol.TrimContractWhitespace(value) == value &&
		utf8.RuneCountInString(value) <= 64*1024 && len(value) <= 64*1024
}

func sameUUID(left, right *uuid.UUID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

// ActorRef is the shared actor reference shape.
type ActorRef = protocol.ActorRef

// Status is a Task's lifecycle state.
type Status string

const (
	StatusBacklog    Status = "backlog"
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in_progress"
	StatusInReview   Status = "in_review"
	StatusDone       Status = "done"
	StatusBlocked    Status = "blocked"
	StatusFailed     Status = "failed"
	StatusError      Status = "error"
	StatusCancelled  Status = "cancelled"
)

// Priority is a Task's urgency.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityMedium Priority = "medium"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

// NamedRef is an id with the display name a reader would otherwise join for.
type NamedRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Execution is whether the Task can start on its assignee's computer,
// without naming that computer (ADR 0014). UnreadyReason is nil when ready.
type Execution struct {
	Agent         *ActorRef `json:"agent"`
	AssigneeReady bool      `json:"assigneeReady"`
	UnreadyReason *string   `json:"unreadyReason"`
}

// Access is what the reader may do with a Task: their effective permissions on
// its Project, sorted. The server decides it; a client never re-derives it.
type Access struct {
	Permissions []auth.ProjectPermission `json:"permissions"`
}

// Response is the Task's public projection, shared by REST reads and frames.
// Assignee is always a member; ExecutedBy is the executing Agent, or the
// assignee again when they do the work themself.
type Response struct {
	ScheduleGeneration   int64         `json:"-"`
	AssignmentStatus     string        `json:"assignmentStatus"`
	ScheduledAt          *time.Time    `json:"scheduledAt"`
	ScheduleFiredAt      *time.Time    `json:"scheduleFiredAt"`
	ScheduleID           *uuid.UUID    `json:"scheduleId"`
	ScheduleOccurrenceAt *time.Time    `json:"scheduleOccurrenceAt"`
	ScheduleDisposition  string        `json:"scheduleDisposition"`
	ID                   uuid.UUID     `json:"id"`
	Key                  string        `json:"key"`
	CompanyID            uuid.UUID     `json:"companyId"`
	ProjectID            uuid.UUID     `json:"projectId"`
	MilestoneType        MilestoneType `json:"milestoneType"`
	ConversationID       uuid.UUID     `json:"conversationId"`
	Number               int64         `json:"number"`
	Title                string        `json:"title"`
	Description          string        `json:"description"`
	Status               Status        `json:"status"`
	Priority             Priority      `json:"priority"`
	Labels               []string      `json:"labels"`
	StartDate            *string       `json:"startDate"`
	DueDate              *string       `json:"dueDate"`
	Assignee             ActorRef      `json:"assignee"`
	ExecutedBy           ActorRef      `json:"executedBy"`
	Execution            Execution     `json:"execution"`
	Creator              ActorRef      `json:"creator"`
	BlockedByCount       int64         `json:"blockedByCount"`
	DependenciesResolved bool          `json:"dependenciesResolved"`
	// AssigneeOutsideProject is true when the Task sits in a restricted
	// Project and its assignee cannot read that Project (ADR 0017): they
	// cannot open or run it until someone gives them access.
	AssigneeOutsideProject bool       `json:"assigneeOutsideProject"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
	CompletedAt            *time.Time `json:"completedAt"`
	Revision               int64      `json:"revision"`
	// Access is the reader's permissions on the Task's Project (ADR 0017),
	// merged exactly like a Project's access. It is set on every read made for
	// a person and left out of realtime frames, which reach other people and
	// only tell a client to refetch.
	Access *Access `json:"access,omitempty"`
	// ExecutionGeneration is not served: it is the fact a handoff of a Task
	// that has run needs confirmation for (ADR 0014).
	ExecutionGeneration int64 `json:"-"`
	// ProjectFrame carries a frame's audience, the readers of the Task's
	// Project (ADR 0017); REST reads leave it empty.
	auth.ProjectFrame
}

// Field names a mutable Task attribute.
type Field string

const (
	FieldMilestoneType Field = "milestoneType"
	FieldTitle         Field = "title"
	FieldDescription   Field = "description"
	FieldStatus        Field = "status"
	FieldPriority      Field = "priority"
	FieldAssignee      Field = "assignee"
	FieldExecutedBy    Field = "executedBy"
	FieldLabels        Field = "labels"
	FieldStartDate     Field = "startDate"
	FieldDueDate       Field = "dueDate"
)
