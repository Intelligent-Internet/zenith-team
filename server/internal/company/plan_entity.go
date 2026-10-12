package company

import (
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	taskdomain "boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/protocol"
)

type PlanBaseEntity struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	Revision       int64  `json:"revision"`
	ConversationID string `json:"conversationId,omitempty"`
	// ReadOnly, on a Project, says the plan was read when its reader could
	// not change that Project's Tasks (ADR 0017). The apply leaves such a
	// Project alone even if the applier may change it by now: the plan never
	// meant to. It can only narrow an apply, so the server trusts it.
	ReadOnly bool `json:"readOnly,omitempty"`
}

type PlanBase struct {
	Projects     []PlanBaseEntity     `json:"projects"`
	Tasks        []PlanBaseEntity     `json:"tasks"`
	Dependencies []PlanBaseDependency `json:"dependencies"`
}

type PlanBaseDependency struct {
	DependentTaskID    string `json:"dependentTaskId"`
	PrerequisiteTaskID string `json:"prerequisiteTaskId"`
	Revision           int64  `json:"revision"`
}

// planFormatVersion is the Company Plan document contract the server applies.
// The MCP export and validation side names the same number.
const planFormatVersion = 5

// PlanDocument is the desired Company Plan: Company Tasks and their dependencies.
type PlanDocument struct {
	Projects      []PlanProject `json:"projects"`
	FormatVersion int           `json:"formatVersion"`
	Company       string        `json:"company"`
	Tasks         []PlanTask    `json:"tasks"`
}

type PlanProject struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Archived    bool   `json:"archived"`
	// ReadOnly is what the export told its reader: they may read this
	// Project but not change its Tasks. The apply leaves such a Project and
	// its Tasks alone. It can only narrow an apply, never widen one: the
	// server still decides every write from the applier's access.
	ReadOnly bool `json:"readOnly,omitempty"`
}

type PlanTask struct {
	ProjectKey string `json:"projectKey"`
	Key        string `json:"key"`
	// AgentID names the Agent the Task is executed by (ADR 0014). An empty
	// value means the assignee does it themself. The plan never names the
	// assignee: the applier is the assignee of every Task it creates.
	AgentID       string                   `json:"agentId"`
	MilestoneType taskdomain.MilestoneType `json:"milestoneType"`
	Title         string                   `json:"title"`
	Description   string                   `json:"description"`
	Status        string                   `json:"status"`
	Priority      string                   `json:"priority"`
	DependsOn     []PlanTaskDependency     `json:"dependsOn"`
}

type PlanTaskDependency struct {
	TaskKey string `json:"taskKey"`
}

// PlanArtifact is the whole-company plan document the apply route accepts:
// base (what the client believed the server held) plus the desired target.
// The server recomputes the authoritative diff itself, so the artifact
// carries no client-computed change list and no content digest.
type PlanArtifact struct {
	ArtifactVersion int          `json:"artifactVersion"`
	CompanyID       string       `json:"companyId"`
	CompanySlug     string       `json:"companySlug"`
	CreatedAt       time.Time    `json:"createdAt"`
	Base            PlanBase     `json:"base"`
	Desired         PlanDocument `json:"desired"`
}

// CompanyPlanApprovalPresentation is the safe, structured projection shown to
// the human reviewer. It deliberately excludes the frozen authoritative base;
// that private action remains available only to the Approval executor.
type CompanyPlanApprovalPresentation struct {
	Kind         string       `json:"kind"`
	CompanySlug  string       `json:"companySlug"`
	DraftDigest  string       `json:"draftDigest"`
	TotalChanges int          `json:"totalChanges"`
	Changes      []PlanChange `json:"changes"`
	Plan         PlanDocument `json:"plan"`
}

type PlanChange struct {
	EntityType string            `json:"entityType"`
	Operation  string            `json:"operation"`
	Key        string            `json:"key"`
	Reason     string            `json:"reason,omitempty"`
	Fields     []PlanFieldChange `json:"fields,omitempty"`
}

type PlanFieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type PlanApplyResult struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
	// Skipped lists the changes the apply left undone because the applier
	// lacks the Project permission they need (ADR 0017); the rest applied.
	Skipped []PlanSkip `json:"skipped"`
}

// PlanSkip is one Project change a Company Plan apply left undone (ADR
// 0017): renaming it without project.settings.update (operation "update");
// archiving it, or leaving it out of the plan while it has open Tasks,
// without project.archive (operation "archive"); or changing its Tasks after
// the applier lost project.tasks.write, or project.read, since the plan was
// read (operation "tasks"). In the last two the whole Project is left as it
// is.
type PlanSkip struct {
	EntityType string                 `json:"entityType"`
	Key        string                 `json:"key"`
	Operation  string                 `json:"operation"`
	Permission auth.ProjectPermission `json:"permission"`
}

func (r *PlanApplyResult) skip(entityType, key, operation string, permission auth.ProjectPermission) {
	r.Skipped = append(r.Skipped, PlanSkip{
		EntityType: entityType, Key: key, Operation: operation, Permission: permission,
	})
}

// persistedTask is the locked row a plan apply compares against. The plan
// names only who does the work (ExecutedByAgentID, ADR 0014); the member
// responsible (AssigneeUserID) is the applier on create and is never moved
// by a restated plan — reassigning is a Task handoff, not a plan operation.
type persistedTask struct {
	ID, ConversationID, ProjectID        uuid.UUID
	Key, MilestoneType                   string
	Title, Description, Status, Priority string
	AssigneeUserID                       uuid.UUID
	ExecutedByAgentID                    uuid.NullUUID
	ExecutionGeneration                  int64
	Version                              int64
}
type persistedDependency struct {
	DependentTaskID, PrerequisiteTaskID uuid.UUID
	Version                             int64
}
type desiredTask struct {
	PlanTask
}

// baseDependencyIsWellFormed decides whether one edge of the artifact's base
// snapshot can be believed: both ends are real ids, they are different tasks,
// the revision is one a JSON client round-trips exactly, and the edge has not
// already been stated in this artifact.
//
// None of it is a binding rule. PlanBaseDependency carries no tag on any
// field, `binding:"uuid"` would accept the nil UUID that ParseGoUUID rejects,
// and neither a tag nor a column can say that two edges of one submitted
// artifact must not be the same edge — the rows they describe do not exist yet.
//
// seen is read, never written: the caller records the key so that the loop's
// bookkeeping stays where the loop is.
func baseDependencyIsWellFormed(dependency PlanBaseDependency, seen map[string]bool) bool {
	dependentTaskID, dependentErr := util.ParseGoUUID(dependency.DependentTaskID)
	prerequisiteTaskID, prerequisiteErr := util.ParseGoUUID(dependency.PrerequisiteTaskID)
	key := dependency.DependentTaskID + "\x00" + dependency.PrerequisiteTaskID
	return dependentErr == nil && prerequisiteErr == nil &&
		dependentTaskID != prerequisiteTaskID &&
		dependency.Revision >= 1 && dependency.Revision <= protocol.MaxJSONSafeInteger &&
		!seen[key]
}

// artifactIdentifiesThisCompany decides whether an artifact is addressed to
// the Company in the URL, in the one artifact version this server understands,
// and stamped at all.
//
// PlanArtifact is bound straight from the request body and carries no binding
// tag on any of its fields, so nothing but this says any of it; and the
// Company comparison is the cross-Company fence — an artifact naming someone
// else's Company must not be applied to this one.
func artifactIdentifiesThisCompany(value PlanArtifact, companyID uuid.UUID) bool {
	parsedCompanyID, err := util.ParseGoUUID(value.CompanyID)
	return err == nil && parsedCompanyID == companyID &&
		value.ArtifactVersion == 1 &&
		value.CompanySlug != "" &&
		!value.CreatedAt.IsZero()
}

func validateCompanyPlanArtifact(value PlanArtifact, companyID uuid.UUID) error {
	if !artifactIdentifiesThisCompany(value, companyID) {
		return ErrPlanInvalidInput
	}
	now := time.Now().UTC()
	if value.CreatedAt.Before(now.Add(-24*time.Hour)) ||
		value.CreatedAt.After(now.Add(5*time.Minute)) {
		return ErrPlanConflict
	}
	if value.Desired.FormatVersion != planFormatVersion || value.Desired.Company != value.CompanySlug ||
		value.Desired.Tasks == nil || value.Desired.Projects == nil {
		return ErrPlanInvalidInput
	}
	projects := map[string]PlanProject{}
	for _, project := range value.Desired.Projects {
		if !projectKeyPattern.MatchString(project.Key) {
			return ErrPlanInvalidInput
		}
		if _, exists := projects[project.Key]; exists {
			return ErrPlanInvalidInput
		}
		if !validPlanText(project.Title, 240, 0, false) || !validPlanText(project.Description, 0, 64*1024, true) {
			return ErrPlanInvalidInput
		}
		projects[project.Key] = project
	}
	taskKeys := map[string]bool{}
	for _, task := range value.Desired.Tasks {
		if _, exists := projects[task.ProjectKey]; !exists {
			return ErrPlanInvalidInput
		}
		if !validCompanyPlanTask(task) || task.DependsOn == nil || taskKeys[task.Key] {
			return ErrPlanInvalidInput
		}
		dependencyKeys := map[string]bool{}
		for _, dependency := range task.DependsOn {
			if !validPlanText(dependency.TaskKey, 0, 0, false) || dependencyKeys[dependency.TaskKey] {
				return ErrPlanInvalidInput
			}
			dependencyKeys[dependency.TaskKey] = true
		}
		taskKeys[task.Key] = true
	}
	if !validCompanyPlanDependencyGraph(value.Desired) {
		return ErrPlanInvalidInput
	}
	dependencyKeys := map[string]bool{}
	for _, dependency := range value.Base.Dependencies {
		key := dependency.DependentTaskID + "\x00" + dependency.PrerequisiteTaskID
		if !baseDependencyIsWellFormed(dependency, dependencyKeys) {
			return ErrPlanInvalidInput
		}
		dependencyKeys[key] = true
	}
	return nil
}

func validCompanyPlanTask(task PlanTask) bool {
	return validPlanText(task.Key, 0, 0, false) &&
		validPlanTaskAgent(task.AgentID) &&
		taskdomain.ValidMilestoneType(task.MilestoneType) &&
		validPlanText(task.Title, 240, 0, false) &&
		validPlanText(task.Description, 64*1024, 64*1024, true) &&
		validPlanTaskStatus(task.Status) && validPlanPriority(task.Priority)
}

// validPlanTaskAgent accepts either a named Agent or an empty value for work
// the assignee does themself.
func validPlanTaskAgent(agentID string) bool {
	if agentID == "" {
		return true
	}
	return util.ValidUUID(uuidOrNil(agentID))
}

func validCompanyPlanDependencyGraph(document PlanDocument) bool {
	tasks := flattenCompanyPlanTasks(document)
	return validCompanyPlanDependencyReferences(tasks) && !companyPlanDependencyGraphHasCycle(tasks)
}

func flattenCompanyPlanTasks(document PlanDocument) map[string]PlanTask {
	tasks := map[string]PlanTask{}
	for _, task := range document.Tasks {
		tasks[task.Key] = task
	}
	return tasks
}

func validCompanyPlanDependencyReferences(tasks map[string]PlanTask) bool {
	for key, task := range tasks {
		for _, dependency := range task.DependsOn {
			prerequisite, prerequisiteExists := tasks[dependency.TaskKey]
			if dependency.TaskKey == key || !prerequisiteExists || prerequisite.ProjectKey != task.ProjectKey {
				return false
			}
		}
	}
	return true
}

const (
	dependencyUnvisited uint8 = iota
	dependencyVisiting
	dependencyVisited
)

func companyPlanDependencyGraphHasCycle(tasks map[string]PlanTask) bool {
	state := map[string]uint8{}
	for key := range tasks {
		if state[key] == dependencyUnvisited && companyPlanDependencyPathHasCycle(key, tasks, state) {
			return true
		}
	}
	return false
}

func companyPlanDependencyPathHasCycle(
	key string,
	tasks map[string]PlanTask,
	state map[string]uint8,
) bool {
	state[key] = dependencyVisiting
	for _, dependency := range tasks[key].DependsOn {
		if state[dependency.TaskKey] == dependencyVisiting {
			return true
		}
		if state[dependency.TaskKey] == dependencyUnvisited &&
			companyPlanDependencyPathHasCycle(dependency.TaskKey, tasks, state) {
			return true
		}
	}
	state[key] = dependencyVisited
	return false
}

func validPlanText(value string, maxRunes, maxBytes int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || !allowEmpty && value == "" {
		return false
	}
	return (maxRunes == 0 || utf8.RuneCountInString(value) <= maxRunes) && (maxBytes == 0 || len(value) <= maxBytes)
}

// taskMatchesDesired reports whether a persisted Task already equals what the
// plan asks for. A terminal Task is immutable, so an apply that would restate
// it identically is a no-op rather than a conflict — only a real difference is
// refused. The assignee is not compared: the plan does not name one.
func taskMatchesDesired(
	current persistedTask,
	target desiredTask,
	agentID uuid.UUID,
) bool {
	return current.MilestoneType == string(target.MilestoneType) &&
		current.Title == target.Title &&
		current.Description == target.Description &&
		current.Status == target.Status &&
		current.Priority == target.Priority &&
		samePlanUUID(current.ExecutedByAgentID, planTaskExecutedBy(agentID))
}

func terminalTaskStatus(v string) bool { return v == "done" || v == "failed" || v == "cancelled" }
func validPlanTaskStatus(v string) bool {
	switch v {
	case "backlog", "todo", "in_progress", "in_review", "done", "blocked", "failed", "error", "cancelled":
		return true
	}
	return false
}

func validDesiredPlanTaskStatus(target string, current persistedTask, exists bool) bool {
	if target != "failed" && target != "error" {
		return true
	}
	return exists && current.Status == target
}

func canAddPlanTaskDependency(current persistedTask, exists bool) bool {
	return !exists || current.Status != "failed" && current.Status != "error"
}

// planTaskExecutedBy is the executing-Agent column a plan Task lands with:
// the named Agent, or NULL when the assignee does the work themself.
func planTaskExecutedBy(agentID uuid.UUID) uuid.NullUUID {
	if agentID == uuid.Nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: agentID, Valid: true}
}

// planTaskAssignee is the locked row's assignee as the Task domain names it:
// always the responsible member.
func planTaskAssignee(userID uuid.UUID) taskdomain.ActorRef {
	return taskdomain.ActorRef{Kind: taskdomain.ActorUser, ID: userID}
}

func samePlanUUID(left, right uuid.NullUUID) bool {
	return left.Valid == right.Valid && (!left.Valid || left.UUID == right.UUID)
}

func validPlanPriority(v string) bool {
	return v == "low" || v == "medium" || v == "high" || v == "urgent"
}

// baseMatches decides staleness on id, key and revision alone. A Task's
// Conversation never changes and the wire contract marks conversationId
// optional on a base entity, so a client that omits it is not stale; the
// frozen approval artifact still carries it for the digest.
func baseMatches(a, b []PlanBaseEntity) bool {
	normalize := func(v []PlanBaseEntity) []PlanBaseEntity {
		r := append([]PlanBaseEntity(nil), v...)
		for i := range r {
			r[i].ConversationID = ""
			r[i].ReadOnly = false
		}
		sort.Slice(r, func(i, j int) bool { return r[i].Key < r[j].Key })
		return r
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func dependencyBaseMatches(a, b []PlanBaseDependency) bool {
	normalize := func(values []PlanBaseDependency) []PlanBaseDependency {
		result := append([]PlanBaseDependency(nil), values...)
		sort.Slice(result, func(i, j int) bool {
			if result[i].DependentTaskID != result[j].DependentTaskID {
				return result[i].DependentTaskID < result[j].DependentTaskID
			}
			return result[i].PrerequisiteTaskID < result[j].PrerequisiteTaskID
		})
		return result
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}

func taskBase(v map[string]persistedTask) []PlanBaseEntity {
	r := make([]PlanBaseEntity, 0, len(v))
	for _, x := range v {
		r = append(
			r,
			PlanBaseEntity{
				ID:             x.ID.String(),
				Key:            x.Key,
				Revision:       x.Version,
				ConversationID: x.ConversationID.String(),
			},
		)
	}
	return r
}

func dependencyBase(values map[string]persistedDependency) []PlanBaseDependency {
	result := make([]PlanBaseDependency, 0, len(values))
	for _, value := range values {
		result = append(result, PlanBaseDependency{
			DependentTaskID:    value.DependentTaskID.String(),
			PrerequisiteTaskID: value.PrerequisiteTaskID.String(),
			Revision:           value.Version,
		})
	}
	return result
}

func sortedKeys[T any](v map[string]T) []string {
	r := make([]string, 0, len(v))
	for k := range v {
		r = append(r, k)
	}
	sort.Strings(r)
	return r
}
func sortedTaskKeys(v map[string]desiredTask) []string            { return sortedKeys(v) }
func sortedPersistedTaskKeys(v map[string]persistedTask) []string { return sortedKeys(v) }

// uuidOrNil parses a plan-supplied id; anything that is not a canonical UUID
// becomes the nil UUID, which util.ValidUUID rejects.
func uuidOrNil(value string) uuid.UUID {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return uuid.Nil
	}
	return parsed
}
