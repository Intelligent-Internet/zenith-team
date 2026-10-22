package wiki

import (
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// UIComponent describes the release-owned native catalog. This metadata is also
// used to generate the authoring reference and the browser's registered schema.
type UIComponent struct {
	Name        string           `json:"name"`
	NodeType    string           `json:"nodeType"`
	Placement   string           `json:"placement"`
	Children    bool             `json:"children"`
	Properties  []string         `json:"properties"`
	Expressions []string         `json:"expressions"`
	Version     int              `json:"version"`
	Rules       []UIPropertyRule `json:"rules"`
	Authoring   UIAuthoring      `json:"authoring"`
	Constraints []UIConstraint   `json:"constraints"`
}

// UIConstraint describes a local relationship between already validated properties.
// Lexical references, dependency cycles and authority are separate document checks.
type UIConstraint struct {
	Type     string             `json:"type"`
	Left     string             `json:"left"`
	Right    string             `json:"right"`
	Code     string             `json:"code"`
	Repair   string             `json:"repair"`
	Strict   bool               `json:"strict"`
	Defaults map[string]float64 `json:"defaults"`
}

func validateUIConstraint(attrs map[string]any, rule UIConstraint) error {
	switch rule.Type {
	case "disjointKeys":
		left, _ := attrs[rule.Left].(map[string]any)
		right, _ := attrs[rule.Right].(map[string]any)
		for name := range right {
			if _, exists := left[name]; exists {
				return uiError(rule.Code)
			}
		}
	case "numberOrder":
		left, hasLeft := uiNumber(attrs[rule.Left])
		right, hasRight := uiNumber(attrs[rule.Right])
		if !hasLeft {
			left, hasLeft = rule.Defaults[rule.Left]
		}
		if !hasRight {
			right, hasRight = rule.Defaults[rule.Right]
		}
		if hasLeft && hasRight && (left > right || rule.Strict && left == right) {
			return uiError(rule.Code)
		}
	}
	return nil
}

// UIAuthoring carries composition and presentation contracts alongside the
// generated property rules. Runtime, authority and presentation semantics
// complement these executable validation contracts.
type UIAuthoring struct {
	Constraints   map[string]string `json:"constraints"`
	Events        []string          `json:"events"`
	Accessibility []string          `json:"accessibility"`
	Fallback      string            `json:"fallback"`
	Capture       string            `json:"capture"`
	Limits        []string          `json:"limits"`
}

// UIPropertyRule carries scalar, bounded map/array and closed-object constraints.
// Expression values delegate to the language validator; local cross-property
// relationships are described by UIConstraint.
type UIPropertyRule struct {
	Property      string                    `json:"property"`
	Type          string                    `json:"type"`
	Code          string                    `json:"code"`
	Required      bool                      `json:"required"`
	Min           int                       `json:"min"`
	Max           int                       `json:"max"`
	Values        []string                  `json:"values,omitempty"`
	ItemMaxBytes  int                       `json:"itemMaxBytes,omitempty"`
	Unique        bool                      `json:"unique,omitempty"`
	UniqueBy      string                    `json:"uniqueBy,omitempty"`
	ItemCode      string                    `json:"itemCode,omitempty"`
	Fields        []UIPropertyRule          `json:"fields,omitempty"`
	KeyCode       string                    `json:"keyCode,omitempty"`
	Entry         *UIPropertyRule           `json:"entry,omitempty"`
	Discriminator string                    `json:"discriminator,omitempty"`
	VariantCode   string                    `json:"variantCode,omitempty"`
	Variants      map[string]UIPropertyRule `json:"variants,omitempty"`
}

func validateUIProperty(attrs map[string]any, rule UIPropertyRule) error {
	value, exists := attrs[rule.Property]
	if !exists && !rule.Required {
		return nil
	}
	return validateUIRuleValue(value, rule)
}

func validateUIRuleValue(value any, rule UIPropertyRule) error {
	valid := false
	text, isString := value.(string)
	switch rule.Type {
	case "string":
		valid = uiString(value, rule.Min, rule.Max)
	case "name":
		valid = isString && validUIName(text)
	case "field":
		valid = isString && len(text) > 0 && len(text) <= 128 && safeUIField(text)
	case "enum":
		valid = isString && slices.Contains(rule.Values, text)
	case "httpsURL":
		valid = isString && len(text) <= rule.Max && strings.HasPrefix(text, "https://") &&
			safeBlockURL(text, false)
	case "variant":
		object, ok := value.(map[string]any)
		if !ok {
			return uiError(rule.Code)
		}
		tag, ok := object[rule.Discriminator].(string)
		variant, exists := rule.Variants[tag]
		if !ok || !exists {
			return uiError(rule.VariantCode)
		}
		return validateUIRuleValue(value, variant)
	case "scalar":
		valid = uiScalar(value)
	case "uuid":
		id, err := uuid.Parse(text)
		valid = isString && err == nil && id != uuid.Nil
	case "expression":
		return ValidateUIExpression(value)
	case "nameMap":
		entries, ok := value.(map[string]any)
		if !ok || len(entries) < rule.Min || len(entries) > rule.Max {
			return uiError(rule.Code)
		}
		for _, name := range slices.Sorted(maps.Keys(entries)) {
			item := entries[name]
			if !validUIName(name) {
				return uiError(rule.KeyCode)
			}
			if err := validateUIRuleValue(item, *rule.Entry); err != nil {
				return err
			}
		}
		valid = true
	case "object":
		return validateUIObject(value, rule.Fields, rule.Code, rule.ItemCode)
	case "objectArray":
		items, ok := value.([]any)
		if !ok || len(items) < rule.Min || len(items) > rule.Max {
			return uiError(rule.Code)
		}
		seen := map[string]bool{}
		for _, raw := range items {
			if err := validateUIObject(raw, rule.Fields, rule.ItemCode, rule.ItemCode); err != nil {
				return err
			}
			item, _ := raw.(map[string]any)
			if rule.UniqueBy != "" {
				key, ok := item[rule.UniqueBy].(string)
				if !ok || seen[key] {
					return uiError(rule.ItemCode)
				}
				seen[key] = true
			}
		}
		valid = true
	case "stringArray":
		items, ok := value.([]any)
		valid = ok && len(items) >= rule.Min && len(items) <= rule.Max
		if !valid {
			return uiError(rule.Code)
		}
		seen := map[string]bool{}
		for _, item := range items {
			text, ok := item.(string)
			if !ok || len(text) > rule.ItemMaxBytes || rule.Unique && seen[text] {
				valid = false
				break
			}
			seen[text] = true
		}
	case "number", "positiveNumber":
		number, ok := uiNumber(value)
		valid = ok && (rule.Type != "positiveNumber" || number > 0)
	case "integer":
		valid = boundedBlockNumber(value, rule.Min, rule.Max)
	}
	if !valid {
		return uiError(rule.Code)
	}
	return nil
}

func validateUIObject(value any, fields []UIPropertyRule, typeCode, propertyCode string) error {
	object, ok := value.(map[string]any)
	if !ok {
		return uiError(typeCode)
	}
	for _, key := range slices.Sorted(maps.Keys(object)) {
		if !slices.ContainsFunc(fields, func(field UIPropertyRule) bool { return field.Property == key }) {
			return uiError(propertyCode)
		}
	}
	for _, field := range fields {
		if err := validateUIProperty(object, field); err != nil {
			return err
		}
	}
	return nil
}

// UIDocumentSchema selects the minimum reader capable of retaining all nodes.
// Schema 2 stays sticky in storage after an upgrade even if nodes are removed.
func UIDocumentSchema(root BlockNode) int {
	if root.Type == "taskCollection" {
		if _, expression := root.Attrs["filter"].(map[string]any); expression {
			return 2
		}
	}
	if _, ok := uiComponent(root.Type); ok {
		return 2
	}
	for _, child := range root.Content {
		if UIDocumentSchema(child) == 2 {
			return 2
		}
	}
	return 1
}

// ValidateUINode validates local properties independently of placement and scope.
// The document validator additionally resolves lexical references and bindings.
func ValidateUINode(node BlockNode) error {
	_, err := validateUINode(node, false)
	return err
}

// validateUINode retains the native JSON path on failure so source authoring
// and document writes use the same catalog checks and error ordering.
func validateUINode(node BlockNode, locate bool) ([]string, error) {
	component, ok := uiComponent(node.Type)
	if !ok {
		return []string{"type"}, uiError("component.unknown")
	}
	if len(node.Marks) > 0 {
		return []string{"marks"}, uiError("component.content")
	}
	if node.Text != "" {
		return []string{"text"}, uiError("component.content")
	}
	if !component.Children && len(node.Content) > 0 ||
		component.Children && len(node.Content) == 0 && node.Type != "uiDocument" {
		return []string{"content"}, uiError("component.children")
	}
	for _, key := range slices.Sorted(maps.Keys(node.Attrs)) {
		if key == "capturedNeedsRefresh" && node.Attrs[key] == nil {
			continue
		}
		if key == "blockId" || key == "agentProtected" || key == "sourceVersionIds" {
			continue
		}
		if key == "catalogVersion" {
			if !boundedBlockNumber(node.Attrs[key], 1, 1) {
				return []string{"attrs", key}, uiError("catalog.version")
			}
			continue
		}
		if !slices.Contains(component.Properties, key) {
			return []string{"attrs", key}, uiError("component.property")
		}
	}
	for _, key := range component.Expressions {
		if path, err := validateUIExpression(node.Attrs[key], locate); err != nil {
			return append([]string{"attrs", key}, path...), err
		}
	}
	if node.Type == "uiDocument" {
		for _, child := range node.Content {
			if child.Type != "text" || len(child.Marks) != 0 {
				return []string{"content"}, uiError("component.content")
			}
		}
		if len(InlineSource(node)) > 262144 {
			return []string{"content"}, ErrPageBodyTooLarge
		}
	}

	attrs := node.Attrs
	for _, rule := range component.Rules {
		if err := validateUIProperty(attrs, rule); err != nil {
			return append(
				[]string{"attrs", rule.Property},
				invalidUIPropertyPath(attrs[rule.Property], rule)...), err
		}
	}
	for _, rule := range component.Constraints {
		if err := validateUIConstraint(attrs, rule); err != nil {
			key := rule.Right
			if _, exists := attrs[key]; !exists {
				key = rule.Left
			}
			return []string{"attrs", key}, err
		}
	}
	return nil, nil
}

func uiString(value any, minimum, maximum int) bool {
	text, ok := value.(string)
	return ok && len(text) >= minimum && len(text) <= maximum
}

func uiNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, !math.IsInf(n, 0) && !math.IsNaN(n)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		value := float64(n)
		return value, !math.IsInf(value, 0) && !math.IsNaN(value)
	case json.Number:
		parsed, err := n.Float64()
		return parsed, err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
	}
	return 0, false
}

func uiScalar(value any) bool {
	if value == nil {
		return true
	}
	switch value := value.(type) {
	case string:
		return len(value) <= 4096
	case bool:
		return true
	default:
		_, ok := uiNumber(value)
		return ok
	}
}

// validateUIAction is also used by the source shorthand parser. Both source and
// native document validation use the same generated action contract.
func validateUIAction(raw any) error {
	component, _ := uiComponent("uiButton")
	for _, rule := range component.Rules {
		if rule.Property == "action" {
			return validateUIRuleValue(raw, rule)
		}
	}
	return uiError("action.type")
}

// CheckUIDocumentWrite rejects older readers before their update can erase
// unknown nodes. Zero denotes an internal legacy caller, with schema-1 support.
func CheckUIDocumentWrite(clientVersion, savedVersion int, root BlockNode) error {
	if clientVersion == 0 {
		clientVersion = 1
	}
	if clientVersion < 1 || clientVersion > 2 || savedVersion > clientVersion ||
		UIDocumentSchema(root) > clientVersion {
		return uiError("document.schema")
	}
	return nil
}

// validateUITaskFilter extends the existing native collection without adding a
// second Task node. Static filters remain readable by schema-1 documents.
func validateUITaskFilter(node BlockNode) error {
	if node.Type != "taskCollection" || node.Attrs["filter"] == nil {
		return nil
	}
	if filter, ok := node.Attrs["filter"].(string); ok {
		if slices.Contains([]string{"", "all", "active", "review", "done"}, filter) {
			return nil
		}
		return uiError("task.filter")
	}
	return ValidateUIExpression(node.Attrs["filter"])
}
