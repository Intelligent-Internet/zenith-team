package wiki

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"testing"
)

func TestUIScopeGeneratedRules(t *testing.T) {
	t.Parallel()
	counted := func(count int) map[string]any {
		values := map[string]any{}
		for i := range count {
			values["item"+strconv.Itoa(i)] = nil
		}
		return values
	}
	for _, tc := range []struct {
		name, property string
		value          any
		code           string
	}{
		{"initial type", "initial", nil, "scope.initial"},
		{"initial count", "initial", counted(65), "scope.initial"},
		{"initial scalar", "initial", map[string]any{"value": []any{}}, "state.definition"},
		{"initial name", "initial", map[string]any{"__proto__": nil}, "state.definition"},
		{"computed type", "computed", false, "scope.computed"},
		{"computed count", "computed", counted(65), "scope.computed"},
		{"computed name", "computed", map[string]any{"bad-name": uiLiteral(1)}, "state.definition"},
		{"source map", "sources", []any{}, "scope.sources"},
		{"source count", "sources", counted(17), "scope.sources"},
		{"source name", "sources", map[string]any{"bad-name": nil}, "source.name"},
		{"source object", "sources", map[string]any{"tasks": false}, "source.descriptor"},
		{"source fields", "sources", map[string]any{"tasks": map[string]any{"private": true}}, "source.property"},
		{"source type", "sources", map[string]any{"tasks": map[string]any{"type": "email"}}, "source.type"},
		{"source UUID", "sources", map[string]any{"tasks": map[string]any{"type": "tasks", "projectId": "00000000-0000-0000-0000-000000000000"}}, "source.project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := uiTestScope(BlockNode{Type: "paragraph"})
			node.Attrs[tc.property] = tc.value
			var diagnostic *UIEvaluationError
			if err := ValidateUINode(node); !errors.As(err, &diagnostic) || diagnostic.Code != tc.code {
				t.Fatalf("validation = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestUICatalogEditorDefaults(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("intelligent_catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		NodeType string         `json:"nodeType"`
		Children bool           `json:"children"`
		Defaults map[string]any `json:"editorDefaults"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run(entry.NodeType, func(t *testing.T) {
			node := BlockNode{Type: entry.NodeType, Attrs: entry.Defaults}
			if entry.Children && entry.NodeType != "uiDocument" {
				node.Content = []BlockNode{{Type: "paragraph"}}
			}
			if err := ValidateUINode(node); err != nil {
				t.Fatalf("invalid editor default: %v", err)
			}
		})
	}
}

func TestUIActionGeneratedRules(t *testing.T) {
	t.Parallel()
	id := "00000000-0000-0000-0000-000000000001"
	for _, tc := range []struct {
		name   string
		action any
		code   string
	}{
		{"set state", map[string]any{"type": "setState", "name": "count", "value": uiLiteral(1)}, ""},
		{"copy", map[string]any{"type": "copy", "text": "Copy this"}, ""},
		{"continue shape", map[string]any{"type": "continue", "text": "Review this"}, ""},
		{"navigate", map[string]any{"type": "navigate", "href": "https://example.com/path"}, ""},
		{"task", map[string]any{"type": "openEntity", "entityType": "task", "entityId": id}, ""},
		{"page current project", map[string]any{"type": "openEntity", "entityType": "page", "entityId": id}, ""},
		{"page explicit project", map[string]any{"type": "openEntity", "entityType": "page", "entityId": id, "projectId": id}, ""},
		{"non object", nil, "action.object"},
		{"unknown type", map[string]any{"type": "deleteTask"}, "action.type"},
		{"reserved state", map[string]any{"type": "setState", "name": "constructor", "value": uiLiteral(1)}, "action.state"},
		{"empty copy", map[string]any{"type": "copy", "text": ""}, "action.text"},
		{"credential URL", map[string]any{"type": "navigate", "href": "https://user:password@example.com"}, "action.href"},
		{"insecure URL", map[string]any{"type": "navigate", "href": "http://example.com"}, "action.href"},
		{"unknown entity", map[string]any{"type": "openEntity", "entityType": "company", "entityId": id}, "action.entity"},
		{"task extra project", map[string]any{"type": "openEntity", "entityType": "task", "entityId": id, "projectId": id}, "action.property"},
		{"page invalid project", map[string]any{"type": "openEntity", "entityType": "page", "entityId": id, "projectId": "invalid"}, "action.project"},
		{"unknown field", map[string]any{"type": "copy", "text": "Copy this", "script": "code"}, "action.property"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, err := range []error{
				validateUIAction(tc.action),
				ValidateUINode(BlockNode{Type: "uiButton", Attrs: map[string]any{"label": "Action", "action": tc.action}}),
			} {
				if tc.code == "" {
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				var diagnostic *UIEvaluationError
				if !errors.As(err, &diagnostic) || diagnostic.Code != tc.code {
					t.Fatalf("action validation = %v, want %s", err, tc.code)
				}
			}
		})
	}
}

func uiLiteral(value any) map[string]any { return map[string]any{"op": "literal", "value": value} }
func uiState(name string) map[string]any { return map[string]any{"op": "state", "name": name} }

func TestUINumericCrossPropertyRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, nodeType string
		bounds         map[string]any
		valid          bool
	}{
		{"number equal", "uiNumberInput", map[string]any{"min": 5, "max": 5}, true},
		{"number reversed", "uiNumberInput", map[string]any{"min": 10, "max": 5}, false},
		{"number only minimum", "uiNumberInput", map[string]any{"min": 101}, true},
		{"number only maximum", "uiNumberInput", map[string]any{"max": -1}, true},
		{"slider equal", "uiSlider", map[string]any{"min": 5, "max": 5}, false},
		{"slider default maximum", "uiSlider", map[string]any{"min": 101}, false},
		{"slider default minimum", "uiSlider", map[string]any{"max": 0}, false},
		{"slider defaults", "uiSlider", map[string]any{}, true},
		{"slider negative range", "uiSlider", map[string]any{"min": -5, "max": -1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := map[string]any{"label": "Count", "bind": "count"}
			for name, value := range tc.bounds {
				attrs[name] = value
			}
			if err := ValidateUINode(BlockNode{Type: tc.nodeType, Attrs: attrs}); (err == nil) != tc.valid {
				t.Fatalf("validation = %v; valid = %v", err, tc.valid)
			}
		})
	}
}

func TestUITableColumnSchema(t *testing.T) {
	t.Parallel()
	column := map[string]any{"key": "name", "label": "Name", "type": "string"}
	for _, tc := range []struct {
		name    string
		columns any
		valid   bool
	}{
		{"valid", []any{column}, true},
		{"empty", []any{}, false},
		{"non-array", column, false},
		{"non-object", []any{"name"}, false},
		{"duplicate key", []any{column, column}, false},
		{"missing type", []any{map[string]any{"key": "name", "label": "Name"}}, false},
		{"extra field", []any{map[string]any{"key": "name", "label": "Name", "type": "string", "formula": "code"}}, false},
		{"unknown type", []any{map[string]any{"key": "name", "label": "Name", "type": "object"}}, false},
		{"reserved key", []any{map[string]any{"key": "__proto__", "label": "Name", "type": "string"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := BlockNode{Type: "uiDataTable", Attrs: map[string]any{
				"label": "Rows", "data": uiLiteral([]any{}), "columns": tc.columns,
			}}
			if err := ValidateUINode(node); (err == nil) != tc.valid {
				t.Fatalf("column validation = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

func uiTestScope(children ...BlockNode) BlockNode {
	return BlockNode{
		Type: "uiScope",
		Attrs: map[string]any{
			"name":    "launch",
			"initial": map[string]any{"status": "all", "number": float64(5), "enabled": true},
		},
		Content: children,
	}
}

func TestUICatalogOffersInlineDocuments(t *testing.T) {
	t.Parallel()
	nodes := inlineSourceDocument("<box><text>Hello</text></box>").Content
	catalog := UICatalog()
	if len(catalog) != len(nodes) {
		t.Fatal("authoring catalog must only offer the inline document")
	}
	for i, node := range nodes {
		t.Run(node.Type, func(t *testing.T) {
			t.Parallel()
			if catalog[i].NodeType != node.Type {
				t.Fatal("catalog order/identity drift")
			}
			contract := catalog[i].Authoring
			if contract.Fallback == "" || contract.Capture == "" || len(contract.Accessibility) == 0 ||
				len(contract.Limits) == 0 {
				t.Fatal("catalog omits its presentation or capture contract")
			}
			for _, property := range catalog[i].Properties {
				described := slices.Contains(catalog[i].Expressions, property)
				for _, rule := range catalog[i].Rules {
					described = described || rule.Property == property
				}
				if !described {
					t.Errorf(
						"property %s has neither an executable rule nor an expression slot",
						property,
					)
				}
			}
			if err := ValidateUINode(node); err != nil {
				t.Fatal(err)
			}
			if err := ValidateUIDocument(uiTestScope(node)); err != nil {
				t.Fatal(err)
			}
			if UIDocumentSchema(node) != 2 {
				t.Fatal("reactive node failed schema detection")
			}
		})
	}
	mutable := UICatalog()
	mutable[0].Properties = append(mutable[0].Properties, "mutated")
	if len(UICatalog()[0].Properties) != 0 {
		t.Fatal("source must live in collaborative text, not catalog attributes")
	}
	mutable[0].Authoring.Constraints["initial"] = "mutated"
	mutable[0].Authoring.Accessibility[0] = "mutated"
	fresh := UICatalog()[0].Authoring
	if fresh.Constraints["initial"] == "mutated" || fresh.Accessibility[0] == "mutated" {
		t.Fatal("authoring metadata shares mutable catalog state")
	}
	if UIDocumentSchema(BlocksFromMarkdown("Plain prose")) != 1 {
		t.Fatal("ordinary document upgraded unnecessarily")
	}
}

func TestUINodeRejectsUnregisteredPropertiesAndActions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		node BlockNode
		code string
	}{
		{
			"unknown property",
			BlockNode{
				Type:  "uiMetric",
				Attrs: map[string]any{"label": "Count", "value": uiLiteral(1), "onclick": "script"},
			},
			"component.property",
		},
		{
			"missing label",
			BlockNode{Type: "uiCheckbox", Attrs: map[string]any{"bind": "enabled"}},
			"component.label",
		},
		{
			"mutation",
			BlockNode{
				Type: "uiButton",
				Attrs: map[string]any{
					"label":  "Delete",
					"action": map[string]any{"type": "deleteTask"},
				},
			},
			"action.type",
		},
		{
			"unsafe navigation",
			BlockNode{
				Type: "uiButton",
				Attrs: map[string]any{
					"label":  "Open",
					"action": map[string]any{"type": "navigate", "href": "javascript:alert(1)"},
				},
			},
			"action.href",
		},
		{
			"duplicate options",
			BlockNode{
				Type: "uiSelect",
				Attrs: map[string]any{
					"label":   "Status",
					"bind":    "status",
					"options": []any{"all", "all"},
				},
			},
			"select.options",
		},
		{
			"zero slider range",
			BlockNode{
				Type: "uiSlider",
				Attrs: map[string]any{
					"label": "Count",
					"bind":  "number",
					"min":   float64(1),
					"max":   float64(1),
				},
			},
			"input.range",
		},
		{
			"unknown version",
			BlockNode{
				Type:  "uiValue",
				Attrs: map[string]any{"value": uiLiteral(1), "catalogVersion": float64(2)},
			},
			"catalog.version",
		},
		{
			"leaf children",
			BlockNode{
				Type:    "uiValue",
				Attrs:   map[string]any{"value": uiLiteral(1)},
				Content: []BlockNode{{Type: "paragraph"}},
			},
			"component.children",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateUINode(tt.node)
			var diagnostic *UIEvaluationError
			if !errors.As(err, &diagnostic) || diagnostic.Code != tt.code {
				t.Fatalf("got = %v, want %s", err, tt.code)
			}
		})
	}
}
