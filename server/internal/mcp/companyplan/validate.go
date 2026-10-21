package companyplan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	taskmilestone "github.com/Intelligent-Internet/zenith-team/server/internal/task/milestone"

	"gopkg.in/yaml.v3"
)

type Summary struct {
	Tasks int
}

func ParseAndValidate(contents []byte) (Document, Summary, error) {
	if len(contents) == 0 {
		return Document{}, Summary{}, errors.New("Company Plan YAML is empty")
	}
	if len(contents) > 8<<20 {
		return Document{}, Summary{}, errors.New("Company Plan YAML exceeds 8 MiB")
	}
	if !utf8.Valid(contents) {
		return Document{}, Summary{}, errors.New("Company Plan YAML is not valid UTF-8")
	}

	var syntax yaml.Node
	if err := yaml.Unmarshal(contents, &syntax); err != nil {
		return Document{}, Summary{}, fmt.Errorf("parse Company Plan YAML: %w", err)
	}
	if containsAlias(&syntax) {
		return Document{}, Summary{}, errors.New("Company Plan YAML aliases are not supported")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, Summary{}, fmt.Errorf("decode Company Plan YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Document{}, Summary{}, errors.New("Company Plan YAML must contain exactly one document")
		}
		return Document{}, Summary{}, fmt.Errorf("decode trailing Company Plan YAML: %w", err)
	}

	summary, err := validateDocument(document)
	if err != nil {
		return Document{}, Summary{}, err
	}
	return document, summary, nil
}

func validateDocument(document Document) (Summary, error) {
	if document.FormatVersion != FormatVersion {
		return Summary{}, fmt.Errorf("unsupported format_version %d", document.FormatVersion)
	}
	if err := validateRequiredString("company", document.Company); err != nil {
		return Summary{}, err
	}
	if document.Projects == nil {
		return Summary{}, errors.New("projects must be a list")
	}
	projects := map[string]Project{}
	for _, project := range document.Projects {
		if !projectKeyPattern.MatchString(project.Key) {
			return Summary{}, errors.New("invalid Project key")
		}
		if _, exists := projects[project.Key]; exists {
			return Summary{}, errors.New("duplicate Project key")
		}
		if err := validateBoundedString("Project title", project.Title, 240, 0, false); err != nil {
			return Summary{}, err
		}
		if err := validateBoundedString(
			"Project description",
			project.Description,
			0,
			64*1024,
			true,
		); err != nil {
			return Summary{}, err
		}
		projects[project.Key] = project
	}
	if document.Tasks == nil {
		return Summary{}, errors.New("tasks must be a list")
	}
	taskKeys := make(map[string]struct{}, len(document.Tasks))
	summary := Summary{Tasks: len(document.Tasks)}
	for taskIndex, task := range document.Tasks {
		taskPath := fmt.Sprintf("tasks[%d]", taskIndex)
		if _, exists := projects[task.ProjectKey]; !exists {
			return Summary{}, fmt.Errorf("%s.project_key was not found", taskPath)
		}
		if err := validateKey(taskPath+".key", task.Key, "Task", taskKeys); err != nil {
			return Summary{}, err
		}
		if task.AgentID != "" && !validAgentID(task.AgentID) {
			return Summary{}, fmt.Errorf(
				"%s.agent_id must be an Agent UUID or empty for work the assignee does themself (got %q)",
				taskPath,
				task.AgentID,
			)
		}
		if !taskmilestone.ValidType(taskmilestone.Type(task.MilestoneType)) {
			return Summary{}, fmt.Errorf(
				"%s.milestone_type has unsupported value %q",
				taskPath,
				task.MilestoneType,
			)
		}
		if err := validateBoundedString(
			taskPath+".title",
			task.Title,
			240,
			0,
			false,
		); err != nil {
			return Summary{}, err
		}
		if err := validateBoundedString(
			taskPath+".description",
			task.Description,
			64*1024,
			64*1024,
			true,
		); err != nil {
			return Summary{}, err
		}
		if !validTaskStatus(task.Status) {
			return Summary{}, fmt.Errorf(
				"%s.status has unsupported value %q",
				taskPath,
				task.Status,
			)
		}
		if !validPriority(task.Priority) {
			return Summary{}, fmt.Errorf(
				"%s.priority has unsupported value %q",
				taskPath,
				task.Priority,
			)
		}
		if task.DependsOn == nil {
			return Summary{}, fmt.Errorf("%s.depends_on must be a list", taskPath)
		}
		seenDependencies := map[string]bool{}
		for dependencyIndex, dependency := range task.DependsOn {
			dependencyPath := fmt.Sprintf("%s.depends_on[%d]", taskPath, dependencyIndex)
			if err := validateRequiredString(
				dependencyPath+".task_key",
				dependency.TaskKey,
			); err != nil {
				return Summary{}, err
			}
			if seenDependencies[dependency.TaskKey] {
				return Summary{}, fmt.Errorf(
					"%s has duplicate dependency task_key %q",
					taskPath,
					dependency.TaskKey,
				)
			}
			seenDependencies[dependency.TaskKey] = true
		}
	}
	if err := validateDependencyGraph(document); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func validateDependencyGraph(document Document) error {
	plan := flatten(document)
	taskKeys := sortedKeys(plan.Tasks)
	for _, taskKey := range taskKeys {
		dependencies := append([]TaskDependency(nil), plan.Tasks[taskKey].DependsOn...)
		sort.Slice(
			dependencies,
			func(i, j int) bool { return dependencies[i].TaskKey < dependencies[j].TaskKey },
		)
		for _, dependency := range dependencies {
			if target, exists := plan.Tasks[dependency.TaskKey]; exists &&
				target.ProjectKey != plan.Tasks[taskKey].ProjectKey {
				return fmt.Errorf("dependency_cross_project: %s -> %s", taskKey, dependency.TaskKey)
			}
			if dependency.TaskKey == taskKey {
				return fmt.Errorf("dependency_self_reference: %s -> %s", taskKey, dependency.TaskKey)
			}
			if _, exists := plan.Tasks[dependency.TaskKey]; !exists {
				return fmt.Errorf(
					"dependency_reference_not_found: %s -> %s",
					taskKey,
					dependency.TaskKey,
				)
			}
		}
	}

	state := map[string]uint8{}
	stack := make([]string, 0, len(taskKeys))
	stackIndexes := map[string]int{}
	var visit func(string) error
	visit = func(taskKey string) error {
		state[taskKey] = 1
		stackIndexes[taskKey] = len(stack)
		stack = append(stack, taskKey)
		dependencies := append([]TaskDependency(nil), plan.Tasks[taskKey].DependsOn...)
		sort.Slice(
			dependencies,
			func(i, j int) bool { return dependencies[i].TaskKey < dependencies[j].TaskKey },
		)
		for _, dependency := range dependencies {
			switch state[dependency.TaskKey] {
			case 0:
				if err := visit(dependency.TaskKey); err != nil {
					return err
				}
			case 1:
				cycle := append([]string(nil), stack[stackIndexes[dependency.TaskKey]:]...)
				cycle = append(cycle, dependency.TaskKey)
				return fmt.Errorf("dependency_cycle: %s", strings.Join(cycle, " -> "))
			}
		}
		stack = stack[:len(stack)-1]
		delete(stackIndexes, taskKey)
		state[taskKey] = 2
		return nil
	}
	for _, taskKey := range taskKeys {
		if state[taskKey] == 0 {
			if err := visit(taskKey); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validateKey(path, value, entity string, seen map[string]struct{}) error {
	if err := validateRequiredString(path, value); err != nil {
		return err
	}
	if _, exists := seen[value]; exists {
		return fmt.Errorf("duplicate %s key %q", entity, value)
	}
	seen[value] = struct{}{}
	return nil
}

func validateRequiredString(path, value string) error {
	if err := validateString(path, value); err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("%s is required", path)
	}
	return nil
}

func validateString(path, value string) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be valid UTF-8 without surrounding whitespace", path)
	}
	return nil
}

func validateBoundedString(path, value string, maxRunes, maxBytes int, allowEmpty bool) error {
	if err := validateString(path, value); err != nil {
		return err
	}
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s is required", path)
	}
	if maxRunes > 0 && utf8.RuneCountInString(value) > maxRunes || maxBytes > 0 && len(value) > maxBytes {
		return fmt.Errorf("%s is too long", path)
	}
	return nil
}

func validTaskStatus(value string) bool {
	switch value {
	case "backlog", "todo", "in_progress", "in_review", "done", "blocked", "failed", "error", "cancelled":
		return true
	default:
		return false
	}
}

func validMilestoneType(value string) bool {
	return taskmilestone.ValidType(taskmilestone.Type(value))
}

// FormatVersion is the Company Plan document contract: 5 adds Projects, and
// a Task's agent_id names the Agent that does its work (executedBy, ADR
// 0014); 3 (departments) and 4 (no Projects) are no longer accepted.
const FormatVersion = 5

// validAgentID accepts a canonical UUID: the exact id list_agents returned.
func validAgentID(value string) bool {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func validPriority(value string) bool {
	return value == "low" || value == "medium" || value == "high" || value == "urgent"
}

func containsAlias(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range node.Content {
		if containsAlias(child) {
			return true
		}
	}
	return false
}
