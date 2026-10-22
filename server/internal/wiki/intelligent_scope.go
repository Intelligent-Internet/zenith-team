package wiki

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

type uiScope struct {
	parent   *uiScope
	initial  map[string]any
	computed map[string]any
	sources  map[string]any
}

func (scope *uiScope) state(name string) (any, bool, bool) {
	for current := scope; current != nil; current = current.parent {
		if value, ok := current.initial[name]; ok {
			return value, true, true
		}
		if _, ok := current.computed[name]; ok {
			return nil, true, false
		}
	}
	return nil, false, false
}

func (scope *uiScope) data(name string) bool {
	for current := scope; current != nil; current = current.parent {
		if _, ok := current.sources[name]; ok {
			return true
		}
	}
	return false
}

// uiValidationLocation carries native JSON coordinates without changing the
// public diagnostic code or exposing authored values in errors.
type uiValidationLocation struct {
	path  []string
	cause error
}

func (e *uiValidationLocation) Error() string { return e.cause.Error() }
func (e *uiValidationLocation) Unwrap() error { return e.cause }

func uiErrorAt(err error, path ...string) error {
	var located *uiValidationLocation
	if errors.As(err, &located) {
		return &uiValidationLocation{path: append(slices.Clone(path), located.path...), cause: located.cause}
	}
	return &uiValidationLocation{path: slices.Clone(path), cause: err}
}

// ValidateUIDocument resolves names through structural scope ancestors. Local
// input values are never written here or mixed into the canonical definition.
func ValidateUIDocument(root BlockNode) error {
	count := 0
	var formDepth int
	var walk func(BlockNode, *uiScope, bool, int) error
	walk = func(node BlockNode, scope *uiScope, item bool, depth int) error {
		count++
		if node.Type == "uiForm" {
			if formDepth > 0 {
				return uiError("form.nested")
			}
			formDepth++
			defer func() { formDepth-- }()
		}
		if count > 10000 || depth > 32 {
			return uiError("document.limit")
		}
		if node.Type == "taskCollection" {
			if err := validateUITaskFilter(node); err != nil {
				return uiErrorAt(err, "attrs", "filter")
			}
			if expression, ok := node.Attrs["filter"].(map[string]any); ok {
				if err := validateUIReferences(expression, scope, item); err != nil {
					return uiErrorAt(err, "attrs", "filter")
				}
			}
		}
		component, reactive := uiComponent(node.Type)
		if reactive {
			if err := ValidateUINode(node); err != nil {
				path, _ := validateUINode(node, true)
				return uiErrorAt(err, path...)
			}
			if node.Type == "uiScope" {
				initial, _ := node.Attrs["initial"].(map[string]any)
				computed, _ := node.Attrs["computed"].(map[string]any)
				sources, _ := node.Attrs["sources"].(map[string]any)
				scope = &uiScope{parent: scope, initial: initial, computed: computed, sources: sources}
				for _, name := range slices.Sorted(maps.Keys(computed)) {
					if err := validateUIReferences(computed[name], scope, item); err != nil {
						return uiErrorAt(err, "attrs", "computed", name)
					}
				}
				if err := validateUICycles(computed); err != nil {
					return uiErrorAt(err, "attrs", "computed")
				}
			}
			for _, key := range component.Expressions {
				if err := validateUIReferences(node.Attrs[key], scope, item); err != nil {
					return uiErrorAt(err, "attrs", key)
				}
			}
			if raw, exists := node.Attrs["bind"]; exists {
				name, _ := raw.(string)
				value, found, mutable := scope.state(name)
				if !found || !mutable {
					return uiErrorAt(uiError("binding.undeclared"), "attrs", "bind")
				}
				if err := validateUIBoundValue(node, value); err != nil {
					return uiErrorAt(err, "attrs", "bind")
				}
			}
			if node.Type == "uiButton" || node.Type == "uiForm" || node.Type == "uiPressable" {
				action, _ := node.Attrs["action"].(map[string]any)
				if action["type"] == "setState" {
					name, _ := action["name"].(string)
					_, found, mutable := scope.state(name)
					if !found || !mutable {
						return uiErrorAt(
							uiError("binding.undeclared"),
							"attrs",
							"action",
							"name",
						)
					}
					if err := validateUIReferences(action["value"], scope, item); err != nil {
						return uiErrorAt(err, "attrs", "action", "value")
					}
				}
			}
		}
		for index, child := range node.Content {
			if err := walk(child, scope, item || node.Type == "uiRepeat", depth+1); err != nil {
				return uiErrorAt(err, "content", strconv.Itoa(index))
			}
		}
		return nil
	}
	return walk(root, nil, false, 0)
}

func validateUIReferences(raw any, scope *uiScope, item bool) error {
	node, ok := raw.(map[string]any)
	if !ok {
		return uiError("expression.object")
	}
	op, _ := node["op"].(string)
	switch op {
	case "literal":
		return nil
	case "state":
		name, _ := node["name"].(string)
		if _, found, _ := scope.state(name); !found {
			return uiErrorAt(uiError("state.undeclared"), "name")
		}
	case "data":
		name, _ := node["name"].(string)
		if !scope.data(name) {
			return uiErrorAt(uiError("data.undeclared"), "name")
		}
	case "item":
		if !item {
			return uiErrorAt(uiError("item.undeclared"), "op")
		}
	}
	if args, ok := node["args"].([]any); ok {
		for index, arg := range args {
			if err := validateUIReferences(arg, scope, item); err != nil {
				return uiErrorAt(err, "args", strconv.Itoa(index))
			}
		}
	}
	if input, exists := node["input"]; exists {
		if err := validateUIReferences(input, scope, item); err != nil {
			return uiErrorAt(err, "input")
		}
	}
	if body, exists := node["body"]; exists {
		if err := validateUIReferences(body, scope, true); err != nil {
			return uiErrorAt(err, "body")
		}
	}
	return nil
}

func validateUICycles(computed map[string]any) error {
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return uiError("state.cycle")
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		dependencies, err := UIExpressionDependencies(computed[name])
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			target, found := strings.CutPrefix(dependency, "state.")
			if !found {
				continue
			}
			if _, local := computed[target]; local {
				if err := visit(target); err != nil {
					return err
				}
			}
		}
		visiting[name] = false
		visited[name] = true
		return nil
	}
	for name := range computed {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func validateUIBoundValue(node BlockNode, value any) error {
	switch node.Type {
	case "uiTextInput", "uiTextArea", "uiWritingBlock":
		if !uiString(value, 0, 4096) {
			return uiError("binding.type")
		}
	case "uiDatePicker":
		text, ok := value.(string)
		if !ok {
			return uiError("binding.type")
		}
		minDate, _ := node.Attrs["min"].(string)
		maxDate, _ := node.Attrs["max"].(string)
		for _, date := range []string{text, minDate, maxDate} {
			if date != "" {
				if _, err := time.Parse("2006-01-02", date); err != nil {
					return uiError("date.format")
				}
			}
		}
		if minDate != "" && maxDate != "" && minDate > maxDate {
			return uiError("date.range")
		}
		if text != "" && (minDate != "" && text < minDate || maxDate != "" && text > maxDate) {
			return uiError("binding.range")
		}
	case "uiCheckbox":
		if _, ok := value.(bool); !ok {
			return uiError("binding.type")
		}
	case "uiSelect", "uiRadioGroup", "uiSegmentedControl":
		text, ok := value.(string)
		if !ok {
			return uiError("binding.type")
		}
		options, _ := node.Attrs["options"].([]any)
		if !slices.ContainsFunc(options, func(option any) bool { return option == text }) {
			return uiError("binding.option")
		}
	case "uiNumberInput", "uiSlider":
		number, ok := uiNumber(value)
		if !ok {
			return uiError("binding.type")
		}
		if minValue, ok := uiNumber(node.Attrs["min"]); ok && number < minValue {
			return uiError("binding.range")
		}
		if maxValue, ok := uiNumber(node.Attrs["max"]); ok && number > maxValue {
			return uiError("binding.range")
		}
		if node.Type == "uiSlider" &&
			(node.Attrs["min"] == nil && number < 0 || node.Attrs["max"] == nil && number > 100) {
			return uiError("binding.range")
		}
	}
	return nil
}

// UIInitialEnvironment computes defaults in dependency order for a single scope.
// Inputs are already validated document definitions and authorized data snapshots.
func UIInitialEnvironment(initial, computed, data map[string]any) (UIEnvironment, error) {
	state := make(map[string]any, len(initial)+len(computed))
	for name, value := range initial {
		state[name] = value
	}
	environment := UIEnvironment{State: state, Data: data}
	if err := validateUICycles(computed); err != nil {
		return environment, err
	}
	var resolve func(string) error
	resolve = func(name string) error {
		if _, ok := state[name]; ok {
			return nil
		}
		expression, ok := computed[name]
		if !ok {
			return uiErrorAt(uiError("state.undeclared"), "name")
		}
		dependencies, err := UIExpressionDependencies(expression)
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if target, ok := strings.CutPrefix(dependency, "state."); ok {
				if err := resolve(target); err != nil {
					return err
				}
			}
		}
		value, err := EvaluateUIExpression(expression, environment)
		if err != nil {
			return fmt.Errorf("compute state: %w", err)
		}
		state[name] = value
		return nil
	}
	for name := range computed {
		if err := resolve(name); err != nil {
			return environment, err
		}
	}
	return environment, nil
}
