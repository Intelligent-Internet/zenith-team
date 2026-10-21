package task

import (
	"time"

	"github.com/google/uuid"
)

type DependencyResponse struct {
	Task      DependencyTask `json:"task"`
	CreatedAt time.Time      `json:"createdAt"`
	Revision  int64          `json:"revision"`
}

// DependencyTask and DependencyGraphTask carry both identities a client
// needs: the stable key, and the number every surface displays as
// {taskPrefix}-{number}.
type DependencyTask struct {
	ProjectID uuid.UUID `json:"projectId"`
	ID        uuid.UUID `json:"id"`
	Key       string    `json:"key"`
	Number    int64     `json:"number"`
	Title     string    `json:"title"`
	Status    Status    `json:"status"`
	Assignee  ActorRef  `json:"assignee"`
	// ExecutedBy is the executing Agent, or the assignee themself (ADR 0014).
	ExecutedBy ActorRef `json:"executedBy"`
	Revision   int64    `json:"revision"`
}

type DependencySet struct {
	BlockedBy []DependencyResponse `json:"blockedBy"`
	Blocks    []DependencyResponse `json:"blocks"`
}

type DependencyGraphTask struct {
	ProjectID uuid.UUID `json:"projectId"`
	ID        uuid.UUID `json:"id"`
	Key       string    `json:"key"`
	Number    int64     `json:"number"`
	Title     string    `json:"title"`
	Status    Status    `json:"status"`
}

type DependencyGraphEdge struct {
	ProjectID          uuid.UUID `json:"projectId"`
	CompanyID          uuid.UUID `json:"companyId"`
	DependentTaskID    uuid.UUID `json:"dependentTaskId"`
	PrerequisiteTaskID uuid.UUID `json:"prerequisiteTaskId"`
	CreatedAt          time.Time `json:"createdAt"`
	Revision           int64     `json:"revision"`
}

type DependencyGraph struct {
	Dependencies []DependencyGraphEdge `json:"items"`
	Tasks        []DependencyGraphTask `json:"tasks"`
}
