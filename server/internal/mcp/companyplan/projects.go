package companyplan

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
)

var projectKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// readOnly reports a Project its reader may read but whose Tasks they may
// not change: project.tasks.write is what lets a plan change them (ADR
// 0017).
func (p projectResponse) readOnly() bool {
	return !slices.Contains(p.Access.Permissions, "project.tasks.write")
}

// baseProjects is the base's Project entries. A read-only one says so, and
// the apply then leaves that Project alone even if its applier may change it
// by the time the plan is applied: the plan was made without changing it.
func baseProjects(values []projectResponse) []BaseEntity {
	result := make([]BaseEntity, 0, len(values))
	for _, value := range values {
		result = append(result, BaseEntity{
			ID: value.ID, Key: value.Key, Revision: value.Revision, ReadOnly: value.readOnly(),
		})
	}
	return result
}

func validateProjectAction(action Action) error {
	if !projectKeyPattern.MatchString(action.Key) {
		return errors.New("invalid Project key")
	}
	if action.Operation == "archive" {
		if action.Values != nil || action.Set != nil {
			return errors.New("archive forbids values and set")
		}
		return nil
	}
	fields := action.Values
	switch action.Operation {
	case "create":
		if fields == nil || action.Set != nil || fields.Title == nil {
			return errors.New("create Project requires title in values")
		}
	case "update":
		fields = action.Set
		if fields == nil || action.Values != nil || fields.empty() {
			return errors.New("update Project requires set")
		}
	default:
		return errors.New("invalid Project operation")
	}
	if fields.ProjectKey != nil || fields.AgentID != nil || fields.MilestoneType != nil || fields.Priority != nil || fields.DependsOn != nil {
		return errors.New("Project accepts only title and description")
	}
	return validateProvided(*fields)
}

func applyProjectAction(plan *FlatPlan, action Action) error {
	current, exists := plan.Projects[action.Key]
	if action.Operation == "create" {
		if exists {
			return fmt.Errorf("Project %q already exists", action.Key)
		}
		current = Project{Key: action.Key, Title: *action.Values.Title}
		if action.Values.Description != nil {
			current.Description = *action.Values.Description
		}
	} else {
		if !exists || current.Archived {
			return fmt.Errorf("Project %q is missing or archived", action.Key)
		}
		if action.Operation == "archive" {
			current.Archived = true
		} else {
			if action.Set.Title != nil {
				current.Title = *action.Set.Title
			}
			if action.Set.Description != nil {
				current.Description = *action.Set.Description
			}
		}
	}
	plan.Projects[action.Key] = current
	return nil
}

func diffProjects(current, desired map[string]Project) []Change {
	var changes []Change
	for _, key := range sortedKeys(desired) {
		after := desired[key]
		before, exists := current[key]
		// Whether the reader may change a Project is not part of it.
		before.ReadOnly, after.ReadOnly = false, false
		change := Change{EntityType: "project", Key: key}
		switch {
		case !exists:
			change.Operation = "create"
			if after.Archived {
				change.Operation = "blocked"
				change.Reason = "new Project cannot be archived"
			}
		case before == after:
			continue
		case before.Archived:
			change.Operation = "blocked"
			change.Reason = "archived Project is immutable"
		case after.Archived:
			change.Operation = "archive"
		default:
			change.Operation = "update"
		}
		for _, field := range []FieldChange{{Field: "title", Before: before.Title, After: after.Title}, {Field: "description", Before: before.Description, After: after.Description}} {
			if field.Before != field.After {
				change.Fields = append(change.Fields, field)
			}
		}
		changes = append(changes, change)
	}
	return changes
}

// refuseReadOnlyChanges refuses a plan that changes a Project its author may
// only read, or a Task in one (ADR 0017). The server leaves such a Project
// as it is, so the plan would not do what it says.
func refuseReadOnlyChanges(changes []Change, current, desired FlatPlan) error {
	for _, change := range changes {
		projectKey := change.Key
		if change.EntityType == "task" {
			projectKey = desired.Tasks[change.Key].ProjectKey
			if task, ok := current.Tasks[change.Key]; ok {
				projectKey = task.ProjectKey
			}
		}
		if current.Projects[projectKey].ReadOnly {
			return fmt.Errorf(
				"%s %q cannot be changed: you may read Project %q but not change its Tasks",
				change.EntityType, change.Key, projectKey,
			)
		}
	}
	return nil
}
