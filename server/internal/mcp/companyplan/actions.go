package companyplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const MaxActionBytes = 4 << 20

var actionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,119}$`)

type ActionDocument struct {
	Kind          string   `yaml:"kind"                  json:"kind"`
	FormatVersion int      `yaml:"format_version"        json:"formatVersion"`
	Company       string   `yaml:"company"               json:"company"`
	Description   string   `yaml:"description,omitempty" json:"description,omitempty"`
	Actions       []Action `yaml:"actions"               json:"actions"`
}

type Action struct {
	ID        string        `yaml:"id"               json:"id"`
	Operation string        `yaml:"operation"        json:"operation"`
	Entity    string        `yaml:"entity"           json:"entity"`
	Key       string        `yaml:"key"              json:"key"`
	Reason    string        `yaml:"reason,omitempty" json:"reason,omitempty"`
	Values    *ActionFields `yaml:"values,omitempty" json:"values,omitempty"`
	Set       *ActionFields `yaml:"set,omitempty"    json:"set,omitempty"`
}

type ActionFields struct {
	ProjectKey    *string           `yaml:"project_key,omitempty"    json:"projectKey,omitempty"`
	Title         *string           `yaml:"title,omitempty"          json:"title,omitempty"`
	Description   *string           `yaml:"description,omitempty"    json:"description,omitempty"`
	Priority      *string           `yaml:"priority,omitempty"       json:"priority,omitempty"`
	AgentID       *string           `yaml:"agent_id,omitempty"       json:"agentId,omitempty"`
	MilestoneType *string           `yaml:"milestone_type,omitempty" json:"milestoneType,omitempty"`
	DependsOn     *[]TaskDependency `yaml:"depends_on,omitempty"     json:"dependsOn,omitempty"`
}

func ParseActions(contents []byte) (ActionDocument, string, error) {
	if len(contents) > MaxActionBytes {
		return ActionDocument{}, "", errors.New("Company Plan transaction exceeds 4 MiB")
	}
	if !utf8.Valid(contents) {
		return ActionDocument{}, "", errors.New("Company Plan transaction YAML is not valid UTF-8")
	}
	var syntax yaml.Node
	if err := yaml.Unmarshal(contents, &syntax); err != nil {
		return ActionDocument{}, "", fmt.Errorf("invalid Company Plan transaction YAML: %w", err)
	}
	if containsAlias(&syntax) {
		return ActionDocument{}, "", errors.New("Company Plan transaction YAML aliases are not supported")
	}
	if containsUnsupportedYAML(&syntax) {
		return ActionDocument{}, "", errors.New(
			"Company Plan transaction YAML merge keys and custom tags are not supported",
		)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var document ActionDocument
	if err := decoder.Decode(&document); err != nil {
		return ActionDocument{}, "", fmt.Errorf("invalid Company Plan transaction YAML: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ActionDocument{}, "", errors.New(
			"invalid Company Plan transaction YAML: expected exactly one document",
		)
	}
	normalizeDescriptions(&document)
	if err := validateActions(document); err != nil {
		return ActionDocument{}, "", err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return ActionDocument{}, "", err
	}
	digest := sha256.Sum256(encoded)
	return document, hex.EncodeToString(digest[:]), nil
}

func validateActions(document ActionDocument) error {
	if document.Kind != "company_plan_transaction" || document.FormatVersion != FormatVersion {
		return fmt.Errorf("kind must be company_plan_transaction and format_version must be %d", FormatVersion)
	}
	if !validActionText(document.Company, 240, false) || !validActionText(document.Description, 1000, true) ||
		len(document.Description) > 64*1024 {
		return errors.New("Company Plan transaction company or description is invalid")
	}
	if document.Actions == nil {
		return errors.New("Company Plan transaction actions must be a list")
	}
	if len(document.Actions) > 500 {
		return errors.New("Company Plan transaction exceeds 500 actions")
	}
	ids, targets := map[string]bool{}, map[string]bool{}
	for index, action := range document.Actions {
		label := fmt.Sprintf("action %d", index+1)
		if !actionIDPattern.MatchString(action.ID) {
			return fmt.Errorf("%s id is invalid", label)
		}
		if ids[action.ID] {
			return fmt.Errorf("duplicate action id %q", action.ID)
		}
		ids[action.ID] = true
		if !validActionText(action.Key, 0, false) || !validActionText(action.Reason, 1000, true) {
			return fmt.Errorf("action %q key or reason is invalid", action.ID)
		}
		if action.Entity != "task" && action.Entity != "project" {
			return fmt.Errorf("action %q entity is invalid", action.ID)
		}
		target := action.Entity + "\x00" + action.Key
		if targets[target] {
			return fmt.Errorf("duplicate target %s %q", action.Entity, action.Key)
		}
		targets[target] = true
		if action.Entity == "project" {
			if err := validateProjectAction(action); err != nil {
				return fmt.Errorf("action %q: %w", action.ID, err)
			}
			continue
		}
		switch action.Operation {
		case "create":
			if action.Values == nil || action.Set != nil {
				return fmt.Errorf("action %q create requires values and forbids set", action.ID)
			}
			if err := validateCreateFields(*action.Values); err != nil {
				return fmt.Errorf("action %q: %w", action.ID, err)
			}
		case "update":
			if action.Set == nil || action.Values != nil || action.Set.empty() {
				return fmt.Errorf(
					"action %q update requires a non-empty set and forbids values",
					action.ID,
				)
			}
			if err := validateUpdateFields(*action.Set); err != nil {
				return fmt.Errorf("action %q: %w", action.ID, err)
			}
		case "cancel":
			if action.Values != nil || action.Set != nil {
				return fmt.Errorf("action %q cancel forbids values and set", action.ID)
			}
		default:
			return fmt.Errorf("action %q operation is invalid", action.ID)
		}
	}
	return nil
}

func validateCreateFields(fields ActionFields) error {
	for _, required := range []struct {
		name     string
		value    *string
		maxRunes int
	}{
		{name: "project_key", value: fields.ProjectKey},
		{name: "milestone_type", value: fields.MilestoneType},
		{name: "title", value: fields.Title, maxRunes: 240},
		{name: "description", value: fields.Description},
	} {
		if err := validateCreateText(
			required.name,
			required.value,
			required.maxRunes,
		); err != nil {
			return err
		}
	}
	if fields.Priority == nil {
		return errors.New("create Task priority is invalid or missing")
	}
	if !validPriority(*fields.Priority) {
		return fmt.Errorf("create Task priority has unsupported value %q", *fields.Priority)
	}
	if !validMilestoneType(*fields.MilestoneType) {
		return fmt.Errorf(
			"create Task milestone_type has unsupported value %q",
			*fields.MilestoneType,
		)
	}
	if fields.AgentID != nil && *fields.AgentID != "" && !validAgentID(*fields.AgentID) {
		return fmt.Errorf("create Task agent_id has unsupported value %q", *fields.AgentID)
	}
	if err := validateTaskDependencies(fields.DependsOn); err != nil {
		return err
	}
	if fields.Description != nil && len(*fields.Description) > 64*1024 {
		return errors.New("create description is too long")
	}
	return nil
}

func validateCreateText(name string, value *string, maxRunes int) error {
	if value == nil || !validActionText(*value, maxRunes, false) {
		return fmt.Errorf("create Task %s is invalid or missing", name)
	}
	return nil
}

func validateUpdateFields(fields ActionFields) error {
	if fields.ProjectKey != nil {
		return errors.New("Task project_key is immutable")
	}
	if err := validateProvided(fields); err != nil {
		return err
	}
	if fields.Description != nil && strings.TrimSpace(*fields.Description) == "" {
		return errors.New("Task description must not be empty")
	}
	return validateTaskDependencies(fields.DependsOn)
}

func validateProvided(fields ActionFields) error {
	for name, value := range map[string]*string{
		"title": fields.Title, "description": fields.Description,
		"agent_id":       fields.AgentID,
		"milestone_type": fields.MilestoneType,
	} {
		if value != nil && !validActionText(
			*value,
			map[string]int{"title": 240}[name],
			name == "description" || name == "agent_id",
		) {
			return fmt.Errorf("set %s is invalid", name)
		}
		if name == "description" && value != nil && len(*value) > 64*1024 {
			return errors.New("set description is too long")
		}
	}
	if fields.Priority != nil && !validPriority(*fields.Priority) {
		return errors.New("set priority is invalid")
	}
	if fields.AgentID != nil && *fields.AgentID != "" && !validAgentID(*fields.AgentID) {
		return errors.New("set agent_id is invalid")
	}
	if fields.MilestoneType != nil && !validMilestoneType(*fields.MilestoneType) {
		return errors.New("set milestone_type is invalid")
	}
	return nil
}

func validActionText(value string, maxRunes int, allowEmpty bool) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && (allowEmpty || value != "") &&
		(maxRunes == 0 || utf8.RuneCountInString(value) <= maxRunes)
}

func normalizeDescriptions(document *ActionDocument) {
	document.Description = strings.TrimSuffix(document.Description, "\n")
	for index := range document.Actions {
		normalizeFieldDescription(document.Actions[index].Values)
		normalizeFieldDescription(document.Actions[index].Set)
	}
}

func normalizeFieldDescription(fields *ActionFields) {
	if fields != nil && fields.Description != nil {
		*fields.Description = strings.TrimSuffix(*fields.Description, "\n")
	}
}

func validateTaskDependencies(value *[]TaskDependency) error {
	if value == nil {
		return nil
	}
	seen := make(map[string]bool, len(*value))
	for _, dependency := range *value {
		if !validActionText(dependency.TaskKey, 0, false) {
			return errors.New("dependency task_key is invalid")
		}
		if seen[dependency.TaskKey] {
			return fmt.Errorf("duplicate dependency task_key %q", dependency.TaskKey)
		}
		seen[dependency.TaskKey] = true
	}
	return nil
}

func (f ActionFields) empty() bool {
	return f.ProjectKey == nil && f.Title == nil && f.Description == nil && f.Priority == nil &&
		f.AgentID == nil && f.MilestoneType == nil && f.DependsOn == nil
}

func containsUnsupportedYAML(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if strings.HasPrefix(node.Tag, "!") && !strings.HasPrefix(node.Tag, "!!") ||
		node.Kind == yaml.ScalarNode && node.Value == "<<" {
		return true
	}
	for _, child := range node.Content {
		if containsUnsupportedYAML(child) {
			return true
		}
	}
	return false
}

func ApplyActions(current Document, transaction ActionDocument) (Document, error) {
	if err := validateActions(transaction); err != nil {
		return Document{}, err
	}
	if current.Company != transaction.Company {
		return Document{}, fmt.Errorf(
			"transaction Company %q does not match exported Company %q",
			transaction.Company,
			current.Company,
		)
	}
	plan := flatten(current)
	for _, operation := range []string{"create", "update", "cancel", "archive"} {
		for _, action := range transaction.Actions {
			if action.Operation != operation {
				continue
			}
			var err error
			switch {
			case action.Entity == "project":
				err = applyProjectAction(&plan, action)
			case operation == "cancel":
				err = cancelAction(&plan, action)
			default:
				err = applyAction(&plan, action)
			}
			if err != nil {
				return Document{}, err
			}
		}
	}
	return unflatten(transaction.Company, plan), nil
}

func validateAddedDependencies(current, desired Document) error {
	currentPlan := flatten(current)
	desiredPlan := flatten(desired)
	for _, taskKey := range sortedKeys(desiredPlan.Tasks) {
		existing := map[string]bool{}
		if task, ok := currentPlan.Tasks[taskKey]; ok {
			for _, dependency := range task.DependsOn {
				existing[dependency.TaskKey] = true
			}
		}
		for _, dependency := range desiredPlan.Tasks[taskKey].DependsOn {
			if existing[dependency.TaskKey] {
				continue
			}
			prerequisite, exists := desiredPlan.Tasks[dependency.TaskKey]
			if exists && prerequisite.Status == "cancelled" {
				return fmt.Errorf(
					"dependency_prerequisite_cancelled: %s -> %s",
					taskKey,
					dependency.TaskKey,
				)
			}
		}
	}
	return nil
}

func applyAction(plan *FlatPlan, action Action) error {
	if action.Operation == "create" {
		f := *action.Values
		if _, ok := plan.Tasks[action.Key]; ok {
			return fmt.Errorf("action %q target Task already exists", action.ID)
		}
		dependsOn := []TaskDependency{}
		if f.DependsOn != nil {
			dependsOn = append(dependsOn, (*f.DependsOn)...)
		}
		agentID := ""
		if f.AgentID != nil {
			agentID = *f.AgentID
		}
		plan.Tasks[action.Key] = Task{
			Key:           action.Key,
			ProjectKey:    *f.ProjectKey,
			AgentID:       agentID,
			MilestoneType: *f.MilestoneType,
			Title:         *f.Title,
			Description:   *f.Description,
			Status:        "backlog",
			Priority:      *f.Priority,
			DependsOn:     dependsOn,
		}
		return nil
	}
	f := *action.Set
	v, ok := plan.Tasks[action.Key]
	if !ok {
		return fmt.Errorf("action %q target Task was not found", action.ID)
	}
	switch v.Status {
	case "backlog", "todo":
	case "failed", "error":
		updated, err := applyTaskDependencyRemoval(v, f, action.ID)
		if err != nil {
			return err
		}
		plan.Tasks[action.Key] = updated
		return nil
	default:
		return fmt.Errorf(
			"action %q can update Task only while status is backlog or todo; current status is %q",
			action.ID,
			v.Status,
		)
	}
	if f.Title != nil {
		v.Title = *f.Title
	}
	if f.Description != nil {
		v.Description = *f.Description
	}
	if f.Priority != nil {
		v.Priority = *f.Priority
	}
	if f.AgentID != nil {
		v.AgentID = *f.AgentID
	}
	if f.MilestoneType != nil {
		v.MilestoneType = *f.MilestoneType
	}
	if f.DependsOn != nil {
		v.DependsOn = append([]TaskDependency{}, (*f.DependsOn)...)
	}
	plan.Tasks[action.Key] = v
	return nil
}

func applyTaskDependencyRemoval(task Task, fields ActionFields, actionID string) (Task, error) {
	changesMetadata := fields.Title != nil || fields.Description != nil || fields.Priority != nil ||
		fields.AgentID != nil || fields.MilestoneType != nil
	if fields.DependsOn == nil || changesMetadata {
		return Task{}, fmt.Errorf(
			"action %q can only remove dependencies from Task while status is %q",
			actionID,
			task.Status,
		)
	}
	if !taskDependenciesAreSubset(*fields.DependsOn, task.DependsOn) {
		return Task{}, fmt.Errorf(
			"action %q cannot add dependencies to Task while status is %q",
			actionID,
			task.Status,
		)
	}
	task.DependsOn = append([]TaskDependency{}, (*fields.DependsOn)...)
	return task, nil
}

func taskDependenciesAreSubset(desired, current []TaskDependency) bool {
	existing := make(map[string]struct{}, len(current))
	for _, dependency := range current {
		existing[dependency.TaskKey] = struct{}{}
	}
	for _, dependency := range desired {
		if _, ok := existing[dependency.TaskKey]; !ok {
			return false
		}
	}
	return true
}

func cancelAction(plan *FlatPlan, action Action) error {
	target, ok := plan.Tasks[action.Key]
	if !ok {
		return fmt.Errorf("action %q target Task was not found", action.ID)
	}
	if terminalStatus(target.Status) {
		return fmt.Errorf("action %q target Task is already terminal and cannot be cancelled", action.ID)
	}
	target.Status = "cancelled"
	plan.Tasks[action.Key] = target
	return nil
}

func unflatten(company string, plan FlatPlan) Document {
	document := Document{
		Projects:      []Project{},
		FormatVersion: FormatVersion,
		Company:       company,
		Tasks:         make([]Task, 0, len(plan.Tasks)),
	}
	for _, key := range sortedKeys(plan.Projects) {
		document.Projects = append(document.Projects, plan.Projects[key])
	}
	for _, taskKey := range sortedKeys(plan.Tasks) {
		document.Tasks = append(document.Tasks, plan.Tasks[taskKey])
	}
	return document
}
