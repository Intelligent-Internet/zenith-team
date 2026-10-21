package companyplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
)

const ArtifactVersion = 1

type BaseEntity struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	Revision       int64  `json:"revision"`
	ConversationID string `json:"conversationId,omitempty"`
	// ReadOnly marks a Project the plan was read without project.tasks.write
	// on (ADR 0017).
	ReadOnly bool `json:"readOnly,omitempty"`
}

type BaseSnapshot struct {
	Projects     []BaseEntity     `json:"projects"`
	Tasks        []BaseEntity     `json:"tasks"`
	Dependencies []BaseDependency `json:"dependencies"`
}

type BaseDependency struct {
	DependentTaskID    string `json:"dependentTaskId"`
	PrerequisiteTaskID string `json:"prerequisiteTaskId"`
	Revision           int64  `json:"revision"`
}

type Change struct {
	EntityType string        `json:"entityType"`
	Operation  string        `json:"operation"`
	Key        string        `json:"key"`
	Reason     string        `json:"reason,omitempty"`
	Fields     []FieldChange `json:"fields,omitempty"`
}

type FieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Artifact is the plan-apply request document: base plus desired. The server
// recomputes the authoritative diff and treats only the desired document as
// input, so the wire artifact carries no change list and no content digest.
// Changes stays process-local: it is the material the preview tool renders
// for the human before an apply, never a request field.
type Artifact struct {
	ArtifactVersion int          `json:"artifactVersion"`
	CompanyID       string       `json:"companyId"`
	CompanySlug     string       `json:"companySlug"`
	CreatedAt       time.Time    `json:"createdAt"`
	Base            BaseSnapshot `json:"base"`
	Desired         Document     `json:"desired"`
	Changes         []Change     `json:"-"`
}

type ApplyAPI interface {
	PostRunJSON(context.Context, string, string, any, any) error
}

type ApprovalSummary struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// ApplyResult carries the durable Approval receipt the 202 answers with. The
// endpoint returns the bare Approval for both first creation and exact
// replay, so created-ness is not signaled anywhere.
type ApplyResult struct {
	Approval ApprovalSummary
}

func Apply(
	ctx context.Context,
	api ApplyAPI,
	artifact Artifact,
	requestKey string,
) (ApplyResult, error) {
	if err := ValidateArtifact(artifact); err != nil {
		return ApplyResult{}, err
	}
	var receipt ApprovalSummary
	path := fmt.Sprintf("/companies/%s/company-plan/apply", artifact.CompanyID)
	if err := api.PostRunJSON(ctx, path, requestKey, artifact, &receipt); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{Approval: receipt}, nil
}

type FlatPlan struct {
	Projects map[string]Project
	Tasks    map[string]Task
}

func Plan(ctx context.Context, api API, desired Document, now time.Time) (Artifact, error) {
	if _, err := validateDocument(desired); err != nil {
		return Artifact{}, err
	}
	snapshot, err := fetchSnapshot(ctx, api, desired.Company)
	if err != nil {
		return Artifact{}, err
	}
	current, err := buildDocument(
		desired.Company,
		snapshot.projects,
		snapshot.tasks,
		snapshot.dependencies,
	)
	if err != nil {
		return Artifact{}, err
	}
	if err := validateAddedDependencies(current, desired); err != nil {
		return Artifact{}, err
	}
	return planFromSnapshot(snapshot, desired, now)
}

// PlanActions fetches one authoritative snapshot, applies the explicit actions
// to that exact snapshot, and binds the resulting artifact to the same row
// versions. This prevents unrelated concurrent changes from being folded into
// a stale complete desired-state document between two backend reads.
func PlanActions(ctx context.Context, api API, transaction ActionDocument, now time.Time) (Artifact, error) {
	snapshot, err := fetchSnapshot(ctx, api, transaction.Company)
	if err != nil {
		return Artifact{}, err
	}
	current, err := buildDocument(
		transaction.Company,
		snapshot.projects,
		snapshot.tasks,
		snapshot.dependencies,
	)
	if err != nil {
		return Artifact{}, err
	}
	desired, err := ApplyActions(current, transaction)
	if err != nil {
		return Artifact{}, err
	}
	if _, err := validateDocument(desired); err != nil {
		return Artifact{}, err
	}
	if err := validateAddedDependencies(current, desired); err != nil {
		return Artifact{}, err
	}
	return planFromSnapshot(snapshot, desired, now)
}

func planFromSnapshot(snapshot currentSnapshot, desired Document, now time.Time) (Artifact, error) {
	if _, err := validateDocument(desired); err != nil {
		return Artifact{}, err
	}
	currentDocument, err := buildDocument(
		desired.Company,
		snapshot.projects,
		snapshot.tasks,
		snapshot.dependencies,
	)
	if err != nil {
		return Artifact{}, err
	}
	current := flatten(currentDocument)
	target := flatten(desired)
	changes, err := diffPlans(current, target)
	if err != nil {
		return Artifact{}, err
	}
	if err := refuseReadOnlyChanges(changes, current, target); err != nil {
		return Artifact{}, err
	}
	return Artifact{
		ArtifactVersion: ArtifactVersion,
		CompanyID:       snapshot.company.ID,
		CompanySlug:     snapshot.company.Slug,
		CreatedAt:       now.UTC(),
		Base: BaseSnapshot{
			Projects:     baseProjects(snapshot.projects),
			Tasks:        baseTasks(snapshot.tasks),
			Dependencies: baseDependencies(snapshot.dependencies),
		},
		Desired: desired,
		Changes: changes,
	}, nil
}

func ValidateArtifact(artifact Artifact) error {
	if artifact.ArtifactVersion != ArtifactVersion || artifact.CompanyID == "" || artifact.CompanySlug == "" ||
		artifact.CreatedAt.IsZero() {
		return errors.New("invalid Company Plan artifact metadata")
	}
	if artifact.CompanySlug != artifact.Desired.Company {
		return errors.New("artifact Company does not match desired Company")
	}
	if _, err := uuid.Parse(artifact.CompanyID); err != nil {
		return errors.New("artifact Company ID is invalid")
	}
	if _, err := validateDocument(artifact.Desired); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, entity := range artifact.Base.Tasks {
		if entity.ID == "" || entity.Key == "" || entity.Revision < 1 || seen[entity.Key] {
			return fmt.Errorf("invalid or duplicate Task base entity %q", entity.Key)
		}
		if _, err := uuid.Parse(entity.ID); err != nil {
			return errors.New("invalid Task base ID")
		}
		seen[entity.Key] = true
	}
	seenDependencies := map[string]bool{}
	for _, dependency := range artifact.Base.Dependencies {
		if _, err := uuid.Parse(dependency.DependentTaskID); err != nil {
			return errors.New("invalid dependency base dependent Task ID")
		}
		if _, err := uuid.Parse(dependency.PrerequisiteTaskID); err != nil {
			return errors.New("invalid dependency base prerequisite Task ID")
		}
		key := dependency.DependentTaskID + "\x00" + dependency.PrerequisiteTaskID
		if dependency.DependentTaskID == dependency.PrerequisiteTaskID || dependency.Revision < 1 ||
			seenDependencies[key] {
			return errors.New("invalid or duplicate dependency base edge")
		}
		seenDependencies[key] = true
	}
	return nil
}

func flatten(document Document) FlatPlan {
	result := FlatPlan{Projects: map[string]Project{}, Tasks: make(map[string]Task, len(document.Tasks))}
	for _, project := range document.Projects {
		result.Projects[project.Key] = project
	}
	for _, task := range document.Tasks {
		result.Tasks[task.Key] = task
	}
	return result
}

func diffPlans(current, desired FlatPlan) ([]Change, error) {
	changes := append(diffProjects(current.Projects, desired.Projects), diffTasks(current.Tasks, desired.Tasks)...)
	for _, change := range changes {
		if change.Operation == "blocked" {
			return nil, fmt.Errorf(
				"%s %q cannot be changed: %s",
				change.EntityType,
				change.Key,
				change.Reason,
			)
		}
	}
	sort.SliceStable(changes, func(i, j int) bool {
		order := map[string]int{"delete": 0, "create": 1, "update": 2}
		if order[changes[i].Operation] != order[changes[j].Operation] {
			return order[changes[i].Operation] < order[changes[j].Operation]
		}
		return changes[i].Key < changes[j].Key
	})
	for index := range changes {
		if changes[index].Operation == "update" && changes[index].EntityType == "task" {
			changes[index].Fields = changedFields(changes[index], current, desired)
		}
	}
	return changes, nil
}

func changedFields(change Change, current, desired FlatPlan) []FieldChange {
	fields := make([]FieldChange, 0)
	add := func(name, before, after string) {
		if before != after {
			fields = append(fields, FieldChange{Field: name, Before: before, After: after})
		}
	}
	before, after := current.Tasks[change.Key], desired.Tasks[change.Key]
	add("agent_id", before.AgentID, after.AgentID)
	add("milestone_type", before.MilestoneType, after.MilestoneType)
	add("title", before.Title, after.Title)
	add("description", before.Description, after.Description)
	add("status", before.Status, after.Status)
	add("priority", before.Priority, after.Priority)
	add("depends_on", formatDependencies(before.DependsOn), formatDependencies(after.DependsOn))
	return fields
}

func formatDependencies(values []TaskDependency) string {
	if values == nil {
		values = []TaskDependency{}
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func diffTasks(current, desired map[string]Task) []Change {
	changes := make([]Change, 0)
	keys := make([]string, 0, len(current)+len(desired))
	seen := map[string]bool{}
	for key := range current {
		keys = append(keys, key)
		seen[key] = true
	}
	for key := range desired {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		before, existed := current[key]
		after, wanted := desired[key]
		terminal := existed && terminalStatus(before.Status)
		changed := existed && wanted && !reflect.DeepEqual(before, after)
		terminalUpdateAllowed := changed && taskDependencyRemovalAllowed(before, after)
		switch {
		case existed && wanted && before.ProjectKey != after.ProjectKey:
			changes = append(
				changes,
				Change{
					EntityType: "task",
					Operation:  "blocked",
					Key:        key,
					Reason:     "Task Project is immutable",
				},
			)
		case !existed:
			changes = append(changes, Change{EntityType: "task", Operation: "create", Key: key})
		case terminal && !wanted:
			// Terminal entities omitted from YAML remain canonical in PostgreSQL.
			continue
		case terminal && changed && !terminalUpdateAllowed:
			changes = append(
				changes,
				Change{
					EntityType: "task",
					Operation:  "blocked",
					Key:        key,
					Reason:     "terminal entities are immutable",
				},
			)
		case !wanted:
			changes = append(changes, Change{EntityType: "task", Operation: "delete", Key: key})
		case changed:
			changes = append(changes, Change{EntityType: "task", Operation: "update", Key: key})
		}
	}
	return changes
}

func taskDependencyRemovalAllowed(before, after Task) bool {
	if before.Status != "failed" {
		return false
	}
	currentDependencies := before.DependsOn
	desiredDependencies := after.DependsOn
	before.DependsOn = nil
	after.DependsOn = nil
	return reflect.DeepEqual(before, after) &&
		taskDependenciesAreSubset(desiredDependencies, currentDependencies)
}

func terminalStatus(status string) bool {
	return status == "done" || status == "cancelled" || status == "failed"
}

func baseTasks(values []taskResponse) []BaseEntity {
	result := make([]BaseEntity, len(values))
	for i, value := range values {
		result[i] = BaseEntity{
			ID:             value.ID,
			Key:            value.Key,
			Revision:       value.Revision,
			ConversationID: value.ConversationID,
		}
	}
	return result
}

func baseDependencies(values map[string][]dependencyResponse) []BaseDependency {
	result := make([]BaseDependency, 0)
	for dependentTaskID, dependencies := range values {
		for _, dependency := range dependencies {
			result = append(result, BaseDependency{
				DependentTaskID: dependentTaskID, PrerequisiteTaskID: dependency.Task.ID,
				Revision: dependency.Revision,
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].DependentTaskID != result[j].DependentTaskID {
			return result[i].DependentTaskID < result[j].DependentTaskID
		}
		return result[i].PrerequisiteTaskID < result[j].PrerequisiteTaskID
	})
	return result
}
