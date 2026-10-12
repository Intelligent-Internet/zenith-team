package companyplan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	ErrCompanyNotFound   = errors.New("Company was not found")
	ErrMultipleCompanies = errors.New("multiple Companies are not supported")
)

// API reads the Company Plan snapshot as the Run. boardly-mcp sends every
// read over the daemon's Run channel with the Run proof, so the server, not
// the daemon, decides what the export shows: every Task the person reads for
// the root Agent's direct-chat Run with planning on, only the Run's own
// Tasks for any other (Access TECH B6).
type API interface {
	FetchRunJSON(context.Context, string, any) error
}

type Document struct {
	FormatVersion int       `yaml:"format_version" json:"formatVersion"`
	Company       string    `yaml:"company"        json:"company"`
	Projects      []Project `yaml:"projects" json:"projects"`
	Tasks         []Task    `yaml:"tasks"          json:"tasks"`
}

type Project struct {
	Key         string `yaml:"key" json:"key"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`
	Archived    bool   `yaml:"archived" json:"archived"`
	// ReadOnly marks a Project the reader may read but whose Tasks they may
	// not change (no project.tasks.write, ADR 0017). A plan leaves it as
	// it is, and the apply leaves it and its Tasks alone.
	ReadOnly bool `yaml:"read_only,omitempty" json:"readOnly,omitempty"`
}

type projectResponse struct {
	ID          string  `json:"id"`
	Key         string  `json:"key"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Revision    int64   `json:"revision"`
	ArchivedAt  *string `json:"archivedAt"`
	Access      struct {
		Permissions []string `json:"permissions"`
	} `json:"access"`
}

type Task struct {
	Key           string           `yaml:"key"            json:"key"`
	ProjectKey    string           `yaml:"project_key" json:"projectKey"`
	AgentID       string           `yaml:"agent_id"       json:"agentId"`
	MilestoneType string           `yaml:"milestone_type" json:"milestoneType"`
	Title         string           `yaml:"title"          json:"title"`
	Description   string           `yaml:"description"    json:"description"`
	Status        string           `yaml:"status"         json:"status"`
	Priority      string           `yaml:"priority"       json:"priority"`
	DependsOn     []TaskDependency `yaml:"depends_on"     json:"dependsOn"`
}

type TaskDependency struct {
	TaskKey string `yaml:"task_key" json:"taskKey"`
}

type companyResponse struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Revision int64  `json:"revision"`
}

type companyCollection struct {
	Items []companyResponse `json:"items"`
}

// taskResponse is the part of a served Task the plan reads. The plan names
// only the Agent that does the work (executedBy); the assignee is always a
// member (ADR 0014), and a plan neither names nor moves one.
type taskResponse struct {
	ProjectID      string   `json:"projectId"`
	ID             string   `json:"id"`
	Key            string   `json:"key"`
	MilestoneType  string   `json:"milestoneType"`
	ConversationID string   `json:"conversationId"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Status         string   `json:"status"`
	Priority       string   `json:"priority"`
	ExecutedBy     actorRef `json:"executedBy"`
	Revision       int64    `json:"revision"`
}

type actorRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type dependencyResponse struct {
	Task     dependencyTaskResponse `json:"task"`
	Revision int64                  `json:"revision"`
}

type dependencyTaskResponse struct {
	ID string `json:"id"`
}

type dependencySetResponse struct {
	BlockedBy []dependencyResponse `json:"blockedBy"`
}

type taskPage struct {
	Items []taskResponse `json:"items"`
}

func Export(ctx context.Context, api API, companySlug string) ([]byte, error) {
	companySlug = strings.TrimSpace(companySlug)
	if companySlug == "" {
		return nil, errors.New("Company slug is required")
	}
	snapshot, err := fetchSnapshot(ctx, api, companySlug)
	if err != nil {
		return nil, err
	}
	document, err := buildDocument(
		companySlug,
		snapshot.projects,
		snapshot.tasks,
		snapshot.dependencies,
	)
	if err != nil {
		return nil, err
	}
	return marshal(document)
}

func ExportOnlyCompany(ctx context.Context, api API) ([]byte, error) {
	company, err := findOnlyCompany(ctx, api)
	if err != nil {
		return nil, err
	}
	snapshot, err := fetchCompanySnapshot(ctx, api, company)
	if err != nil {
		return nil, err
	}
	document, err := buildDocument(
		company.Slug,
		snapshot.projects,
		snapshot.tasks,
		snapshot.dependencies,
	)
	if err != nil {
		return nil, err
	}
	return marshal(document)
}

type currentSnapshot struct {
	projects     []projectResponse
	company      companyResponse
	tasks        []taskResponse
	dependencies map[string][]dependencyResponse
}

func fetchSnapshot(ctx context.Context, api API, companySlug string) (currentSnapshot, error) {
	company, err := findCompany(ctx, api, companySlug)
	if err != nil {
		return currentSnapshot{}, err
	}
	return fetchCompanySnapshot(ctx, api, company)
}

func fetchCompanySnapshot(ctx context.Context, api API, company companyResponse) (currentSnapshot, error) {
	var projects struct {
		Items []projectResponse `json:"items"`
	}
	if err := api.FetchRunJSON(
		ctx,
		fmt.Sprintf("/companies/%s/projects?include_archived=true", url.PathEscape(company.ID)),
		&projects,
	); err != nil {
		return currentSnapshot{}, err
	}
	tasks, err := listTasks(ctx, api, company.ID)
	if err != nil {
		return currentSnapshot{}, err
	}
	dependencies, err := listAllTaskDependencies(ctx, api, company.ID, tasks)
	if err != nil {
		return currentSnapshot{}, err
	}
	return currentSnapshot{
		company:      company,
		projects:     projects.Items,
		tasks:        tasks,
		dependencies: dependencies,
	}, nil
}

func listAllTaskDependencies(
	ctx context.Context,
	api API,
	companyID string,
	tasks []taskResponse,
) (map[string][]dependencyResponse, error) {
	result := make(map[string][]dependencyResponse, len(tasks))
	for _, task := range tasks {
		if task.ID == "" {
			return nil, fmt.Errorf("server returned Task %q without an ID", task.Key)
		}
		var set dependencySetResponse
		path := fmt.Sprintf(
			"/companies/%s/tasks/%s/dependencies",
			url.PathEscape(companyID),
			url.PathEscape(task.ID),
		)
		if err := api.FetchRunJSON(ctx, path, &set); err != nil {
			return nil, fmt.Errorf("list dependencies for Task %q: %w", task.Key, err)
		}
		result[task.ID] = set.BlockedBy
	}
	return result, nil
}

func findOnlyCompany(ctx context.Context, api API) (companyResponse, error) {
	var collection companyCollection
	if err := api.FetchRunJSON(ctx, "/companies", &collection); err != nil {
		return companyResponse{}, fmt.Errorf("list Companies: %w", err)
	}
	switch len(collection.Items) {
	case 0:
		return companyResponse{}, fmt.Errorf(
			"%w: the account has no Companies",
			ErrCompanyNotFound,
		)
	case 1:
		return collection.Items[0], nil
	default:
		return companyResponse{}, ErrMultipleCompanies
	}
}

func findCompany(ctx context.Context, api API, slug string) (companyResponse, error) {
	var collection companyCollection
	if err := api.FetchRunJSON(ctx, "/companies", &collection); err != nil {
		return companyResponse{}, fmt.Errorf("list Companies: %w", err)
	}
	available := make([]string, 0, len(collection.Items))
	for _, company := range collection.Items {
		available = append(available, company.Slug)
		if company.Slug == slug {
			return company, nil
		}
	}
	sort.Strings(available)
	if len(available) == 0 {
		return companyResponse{}, fmt.Errorf(
			"%w: %s (the account has no Companies)",
			ErrCompanyNotFound,
			slug,
		)
	}
	return companyResponse{}, fmt.Errorf(
		"%w: %s (available: %s)",
		ErrCompanyNotFound,
		slug,
		strings.Join(available, ", "),
	)
}

func listTasks(ctx context.Context, api API, companyID string) ([]taskResponse, error) {
	var response taskPage
	if err := api.FetchRunJSON(
		ctx,
		fmt.Sprintf("/companies/%s/tasks", url.PathEscape(companyID)),
		&response,
	); err != nil {
		return nil, fmt.Errorf("list Tasks: %w", err)
	}
	return response.Items, nil
}

func buildDocument(
	slug string,
	projects []projectResponse,
	tasks []taskResponse,
	dependencySets ...map[string][]dependencyResponse,
) (Document, error) {
	document := Document{Projects: []Project{}, FormatVersion: FormatVersion, Company: slug, Tasks: make([]Task, 0, len(tasks))}

	projectKeys := map[string]string{}
	for _, project := range projects {
		projectKeys[project.ID] = project.Key
		document.Projects = append(document.Projects, Project{
			Key: project.Key, Title: project.Title, Description: project.Description,
			Archived: project.ArchivedAt != nil, ReadOnly: project.readOnly(),
		})
	}
	taskKeysByID := make(map[string]string, len(tasks))
	for _, value := range tasks {
		if value.ID != "" {
			taskKeysByID[value.ID] = value.Key
		}
	}
	dependencies := map[string][]dependencyResponse{}
	if len(dependencySets) > 0 && dependencySets[0] != nil {
		dependencies = dependencySets[0]
	}
	for _, value := range tasks {
		if _, exists := projectKeys[value.ProjectID]; !exists {
			return Document{}, fmt.Errorf("Project for Task %q is absent from the snapshot; export again", value.Key)
		}
		// Work an Agent does names that Agent, even an archived one that
		// finished it: the server accepts it on a Task that keeps it. Work
		// the assignee does themself exports without an Agent id.
		agentID := ""
		if value.ExecutedBy.Kind == "agent" {
			agentID = value.ExecutedBy.ID
		}
		if agentID != "" && !validAgentID(agentID) {
			return Document{}, fmt.Errorf(
				"Task %q names its Agent by an id that is not a UUID",
				value.Key,
			)
		}
		dependsOn := make([]TaskDependency, 0, len(dependencies[value.ID]))
		for _, edge := range dependencies[value.ID] {
			prerequisiteKey, ok := taskKeysByID[edge.Task.ID]
			if !ok {
				return Document{}, fmt.Errorf(
					"Task %q depends on a Task that was not exported",
					value.Key,
				)
			}
			dependsOn = append(dependsOn, TaskDependency{TaskKey: prerequisiteKey})
		}
		sort.Slice(dependsOn, func(i, j int) bool { return dependsOn[i].TaskKey < dependsOn[j].TaskKey })
		document.Tasks = append(
			document.Tasks,
			Task{
				Key:           value.Key,
				ProjectKey:    projectKeys[value.ProjectID],
				AgentID:       agentID,
				MilestoneType: value.MilestoneType,
				Title:         value.Title,
				Description:   value.Description,
				Status:        value.Status,
				Priority:      value.Priority,
				DependsOn:     dependsOn,
			},
		)
	}
	return document, nil
}

func marshal(document Document) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("encode Company Plan YAML: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("finish Company Plan YAML: %w", err)
	}
	return output.Bytes(), nil
}
