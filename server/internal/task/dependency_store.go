package task

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func ensureTaskExecutionEligible(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	taskID uuid.UUID,
) error {
	project, err := queries.GetTaskProjectForShare(
		ctx,
		generated.GetTaskProjectForShareParams{CompanyID: companyID, TaskID: taskID},
	)
	if err != nil {
		return fmt.Errorf("read Task Project: %w", err)
	}
	if project.ArchivedAt.Valid {
		return ErrProjectArchived
	}
	blocked, err := queries.TaskHasUnresolvedDependencies(ctx, generated.TaskHasUnresolvedDependenciesParams{
		CompanyID: companyID,
		TaskID:    taskID,
	})
	if err != nil {
		return fmt.Errorf("check Task execution dependencies: %w", err)
	}
	if blocked {
		return ErrDependencyBlocked
	}
	return nil
}

// listDependencies reads both directions of one Task's dependencies, each
// other endpoint through the reader's Project access (ADR 0017).
func listDependencies(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
) (DependencySet, error) {
	rows, err := queries.ListTaskDependencyProjections(ctx, generated.ListTaskDependencyProjectionsParams{
		CompanyID:   principal.CompanyID,
		TaskID:      taskID,
		ActorUserID: principal.UserID,
	})
	if err != nil {
		return DependencySet{}, fmt.Errorf("list Task dependency projections: %w", err)
	}
	result := DependencySet{
		BlockedBy: []DependencyResponse{},
		Blocks:    []DependencyResponse{},
	}
	for _, row := range rows {
		dependency := dependencyFromProjectionRow(row)
		if row.Direction == "blocked_by" {
			result.BlockedBy = append(result.BlockedBy, dependency)
			continue
		}
		result.Blocks = append(result.Blocks, dependency)
	}
	return result, nil
}

// listDependencyGraph returns the dependency graph of every Project the
// reader can read (ADR 0017).
func listDependencyGraph(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
) (DependencyGraph, error) {
	rows, err := queries.ListCompanyTaskDependencyGraph(ctx, generated.ListCompanyTaskDependencyGraphParams{
		CompanyID:   principal.CompanyID,
		ActorUserID: principal.UserID,
	})
	if err != nil {
		return DependencyGraph{}, fmt.Errorf("list Company Task dependency rows: %w", err)
	}
	result := DependencyGraph{
		Dependencies: make([]DependencyGraphEdge, 0, len(rows)),
		Tasks:        []DependencyGraphTask{},
	}
	tasksByID := make(map[uuid.UUID]DependencyGraphTask, len(rows)*2)
	for _, row := range rows {
		dependentTaskID := row.DependentTaskID
		prerequisiteTaskID := row.PrerequisiteTaskID
		dependency := DependencyGraphEdge{
			ProjectID: row.ProjectID,
			CompanyID: row.CompanyID, DependentTaskID: dependentTaskID,
			PrerequisiteTaskID: prerequisiteTaskID, CreatedAt: row.CreatedAt.Time, Revision: row.Version,
		}
		result.Dependencies = append(result.Dependencies, dependency)
		tasksByID[dependentTaskID] = DependencyGraphTask{
			ProjectID: row.ProjectID,
			ID:        dependentTaskID, Key: row.DependentKey, Number: row.DependentNumber,
			Title: row.DependentTitle, Status: Status(row.DependentStatus),
		}
		tasksByID[prerequisiteTaskID] = DependencyGraphTask{
			ProjectID: row.ProjectID,
			ID:        prerequisiteTaskID, Key: row.PrerequisiteKey, Number: row.PrerequisiteNumber,
			Title: row.PrerequisiteTitle, Status: Status(row.PrerequisiteStatus),
		}
	}
	for _, row := range rows {
		if task, ok := tasksByID[row.PrerequisiteTaskID]; ok {
			result.Tasks = append(result.Tasks, task)
			delete(tasksByID, row.PrerequisiteTaskID)
		}
		if task, ok := tasksByID[row.DependentTaskID]; ok {
			result.Tasks = append(result.Tasks, task)
			delete(tasksByID, row.DependentTaskID)
		}
	}
	return result, nil
}

// dependencyExecutedBy is who does the other Task's work: its executing
// Agent with the Agent's name, else its assignee with theirs.
func dependencyExecutedBy(row generated.ListTaskDependencyProjectionsRow) ActorRef {
	if row.TaskExecutedByAgentID.Valid {
		return ActorRef{
			Kind:        ActorAgent,
			ID:          row.TaskExecutedByAgentID.UUID,
			DisplayName: row.TaskExecutedByDisplayName,
		}
	}
	return ActorRef{
		Kind:        ActorUser,
		ID:          row.TaskAssigneeID,
		DisplayName: row.TaskAssigneeDisplayName,
		AvatarURL:   util.TextToPtr(row.TaskAssigneeAvatarUrl),
	}
}

func dependencyFromProjectionRow(row generated.ListTaskDependencyProjectionsRow) DependencyResponse {
	return DependencyResponse{
		Task: DependencyTask{
			ProjectID: row.TaskDependency.ProjectID,
			ID:        row.TaskID,
			Key:       row.TaskKey,
			Number:    row.TaskNumber,
			Title:     row.TaskTitle,
			Status:    Status(row.TaskStatus),
			Assignee: ActorRef{
				Kind:        ActorUser,
				ID:          row.TaskAssigneeID,
				DisplayName: row.TaskAssigneeDisplayName,
				AvatarURL:   util.TextToPtr(row.TaskAssigneeAvatarUrl),
			},
			ExecutedBy: dependencyExecutedBy(row),
			Revision:   row.TaskRevision,
		},
		CreatedAt: row.TaskDependency.CreatedAt.Time.UTC(),
		Revision:  row.TaskDependency.Version,
	}
}
