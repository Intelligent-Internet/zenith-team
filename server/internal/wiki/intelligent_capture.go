package wiki

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// UISourceCoverage distinguishes a complete filtered result from a bounded sample.
type UISourceCoverage struct {
	RowCount int  `json:"rowCount"`
	Limit    int  `json:"limit"`
	Complete bool `json:"complete"`
}

// UISourceSnapshot is supplied by the authorized host transaction. Capture never
// fetches data itself and never substitutes a later live read for this snapshot.
type UISourceSnapshot struct {
	Coverage   *UISourceCoverage `json:"coverage,omitempty"`
	Value      any               `json:"value"`
	Version    string            `json:"version"`
	ObservedAt string            `json:"observedAt"`
}

type UICaptureInput struct {
	Selected map[string]map[string]any
	Resolve  func(scopePath, name string, descriptor map[string]any) (UISourceSnapshot, error)
}

type UICapturedSource struct {
	ScopePath  string           `json:"scopePath"`
	Name       string           `json:"name"`
	Descriptor map[string]any   `json:"descriptor"`
	Snapshot   UISourceSnapshot `json:"snapshot"`
}

type UICapturedScope struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Selected contains validated scalar JSON, preserving null, string, boolean
	// and number values without exposing an unconstrained interface on the wire.
	Selected map[string]json.RawMessage `json:"selected"`
	Computed map[string]any             `json:"computed"`
}

// UICapture preserves authored semantics separately from the frozen render tree.
// Instance paths identify rendered rows without allocating new document blocks.
type UICapture struct {
	SchemaVersion   int                `json:"schemaVersion"`
	LanguageVersion int                `json:"languageVersion"`
	CatalogVersion  int                `json:"catalogVersion"`
	DefinitionHash  string             `json:"definitionHash"`
	Definition      BlockNode          `json:"definition"`
	Content         BlockNode          `json:"content"`
	Scopes          []UICapturedScope  `json:"scopes"`
	Sources         []UICapturedSource `json:"sources"`
	Values          map[string]any     `json:"values"`
}

type uiCaptureBuilder struct {
	input      UICaptureInput
	capture    UICapture
	evaluation uiEvaluation
	nodes      int
	bytes      int
	selected   map[string]bool
}

// CaptureUI consumes a validated authored document and untrusted selected inputs.
// A source or evaluation failure rejects the complete capture, never partial evidence.
func CaptureUI(root BlockNode, input UICaptureInput) (UICapture, error) {
	if root.Type != "doc" {
		return UICapture{}, uiError("capture.definition")
	}
	selected, err := json.Marshal(input.Selected)
	if err != nil || len(selected) > 1<<20 {
		return UICapture{}, uiError("capture.selection")
	}
	raw, err := json.Marshal(root)
	if err != nil || len(raw) > 4<<20 {
		return UICapture{}, uiError("capture.definition")
	}
	var definition BlockNode
	if err := json.Unmarshal(raw, &definition); err != nil {
		return UICapture{}, uiError("capture.definition")
	}
	hash := sha256.Sum256(raw)
	builder := uiCaptureBuilder{
		input: input,
		capture: UICapture{
			SchemaVersion: UIDocumentSchema(root), LanguageVersion: 1, CatalogVersion: 1,
			DefinitionHash: hex.EncodeToString(hash[:]), Definition: definition,
			Scopes: []UICapturedScope{}, Sources: []UICapturedSource{}, Values: map[string]any{},
		},
		selected: map[string]bool{},
		bytes:    len(raw)*2 + len(selected),
	}
	nodes, err := builder.walk(
		definition,
		UIEnvironment{State: map[string]any{}, Data: map[string]any{}},
		uiCapturePosition{},
	)
	if err != nil {
		return UICapture{}, err
	}
	for path := range input.Selected {
		if !builder.selected[path] {
			return UICapture{}, uiError("capture.selection")
		}
	}
	builder.capture.Content = nodes[0]
	encoded, err := json.Marshal(builder.capture)
	if err != nil || len(encoded) > 16<<20 {
		return UICapture{}, uiError("capture.size")
	}
	return builder.capture, nil
}

func (b *uiCaptureBuilder) evaluate(expression any, env UIEnvironment, path string) (any, error) {
	value, err := b.evaluation.evaluate(expression, env, uiItem{value: env.Item, bound: env.HasItem}, 0)
	if err != nil {
		return nil, err
	}
	if err := checkUIJSONValue(value); err != nil {
		return nil, err
	}
	value, err = normalizeUIJSON(value)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(value)
	b.bytes += len(encoded)*3 + len(path)
	if b.bytes > 16<<20 {
		return nil, uiError("capture.size")
	}
	b.capture.Values[path] = value
	return value, nil
}

func uiCopyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (b *uiCaptureBuilder) scope(node BlockNode, env UIEnvironment, path string) (UIEnvironment, error) {
	env.State, env.Data = uiCopyMap(env.State), uiCopyMap(env.Data)
	initial, _ := node.Attrs["initial"].(map[string]any)
	computed, _ := node.Attrs["computed"].(map[string]any)
	sources, _ := node.Attrs["sources"].(map[string]any)
	selected := uiCopyMap(initial)
	if overrides, exists := b.input.Selected[path]; exists {
		b.selected[path] = true
		for name, raw := range overrides {
			original, exists := initial[name]
			value, err := normalizeUIJSON(raw)
			if !exists || err != nil || !uiScalar(value) ||
				fmt.Sprintf("%T", value) != fmt.Sprintf("%T", original) {
				return env, uiError("capture.selection")
			}
			selected[name] = value
		}
	}
	encodedSelected, _ := json.Marshal(selected)
	b.bytes += len(encodedSelected) + len(path)
	if b.bytes > 16<<20 {
		return env, uiError("capture.size")
	}
	for name, value := range selected {
		env.State[name] = value
	}
	for name := range computed {
		delete(env.State, name)
	}
	for _, name := range uiSortedKeys(sources) {
		if b.input.Resolve == nil {
			return env, uiError("capture.source")
		}
		descriptor, _ := sources[name].(map[string]any)
		snapshot, err := b.input.Resolve(path, name, uiCopyMap(descriptor))
		if err != nil {
			return env, err
		}
		snapshot.Value, err = normalizeUIJSON(snapshot.Value)
		if err != nil || snapshot.Version == "" || len(snapshot.Version) > 65536 {
			return env, uiError("capture.source")
		}
		if _, err := time.Parse(time.RFC3339Nano, snapshot.ObservedAt); err != nil {
			return env, uiError("capture.source")
		}
		if snapshot.Coverage != nil {
			// The resolver owns its input; retained evidence must not share it.
			coverage := *snapshot.Coverage
			snapshot.Coverage = &coverage
			rows, ok := snapshot.Value.([]any)
			if !ok || coverage.RowCount != len(rows) || coverage.Limit < 1 ||
				coverage.Limit > UICollectionMaxItems || len(rows) > coverage.Limit ||
				!coverage.Complete && len(rows) != coverage.Limit {
				return env, uiError("capture.coverage")
			}
		}
		encoded, _ := json.Marshal(snapshot)
		b.bytes += len(encoded) + len(path) + len(name)
		if b.bytes > 16<<20 {
			return env, uiError("capture.size")
		}
		env.Data[name] = snapshot.Value
		b.capture.Sources = append(
			b.capture.Sources,
			UICapturedSource{ScopePath: path, Name: name, Descriptor: descriptor, Snapshot: snapshot},
		)
	}
	resolved := map[string]any{}
	active := map[string]bool{}
	var compute func(string) error
	compute = func(name string) error {
		if _, exists := resolved[name]; exists {
			return nil
		}
		if active[name] {
			return uiError("state.cycle")
		}
		active[name] = true
		dependencies, err := UIExpressionDependencies(computed[name])
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if target, ok := strings.CutPrefix(dependency, "state."); ok {
				if _, local := computed[target]; local {
					if err := compute(target); err != nil {
						return err
					}
				}
			}
		}
		value, err := b.evaluate(computed[name], env, path+"/computed/"+name)
		if err != nil {
			return err
		}
		resolved[name], env.State[name] = value, value
		active[name] = false
		return nil
	}
	for _, name := range uiSortedKeys(computed) {
		if err := compute(name); err != nil {
			return env, err
		}
	}
	retainedSelected := make(map[string]json.RawMessage, len(selected))
	for name, value := range selected {
		encoded, err := json.Marshal(value)
		if err != nil {
			return env, uiError("capture.selection")
		}
		retainedSelected[name] = encoded
	}
	b.capture.Scopes = append(
		b.capture.Scopes,
		UICapturedScope{
			Name:     fmt.Sprint(node.Attrs["name"]),
			Path:     path,
			Selected: retainedSelected,
			Computed: resolved,
		},
	)
	return env, nil
}

func uiSortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

type uiCapturePosition struct {
	path        string
	instance    bool
	depth       int
	repeatDepth int
}

func (b *uiCaptureBuilder) walk(node BlockNode, env UIEnvironment, position uiCapturePosition) ([]BlockNode, error) {
	path, instance, depth := position.path, position.instance, position.depth
	b.bytes += len(path) + 128
	if b.bytes > 16<<20 {
		return nil, uiError("capture.size")
	}
	b.nodes++
	if b.nodes > 10000 || depth > 32 {
		return nil, uiError("capture.limit")
	}
	node.Attrs = uiCopyMap(node.Attrs)
	if id, ok := node.Attrs["blockId"].(string); ok {
		path += "/" + id
	}
	if instance {
		delete(node.Attrs, "blockId")
	}
	var err error
	switch node.Type {
	case "uiDocument":
		source := ""
		for _, child := range node.Content {
			source += child.Text
		}
		retained := inlineCaptureState{
			RendererVersion: 1,
			Identity:        fmt.Sprintf("captured:%x", sha256.Sum256([]byte(path))),
			Source:          source,
			Theme:           "light",
			Width:           1024,
			State:           map[string]any{},
			Refs:            map[string]any{},
			Memo:            map[string]any{},
		}
		if selected, exists := b.input.Selected[path]; exists {
			raw, ok := selected["inlineState"].(string)
			if !ok || len(selected) != 1 || len(raw) > 512<<10 || !json.Valid([]byte(raw)) {
				return nil, uiError("capture.inline_state")
			}
			retained = inlineCaptureState{}
			decoder := json.NewDecoder(bytes.NewBufferString(raw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&retained) != nil || retained.Source != source ||
				retained.RendererVersion != 1 ||
				retained.Identity == "" ||
				len(retained.Identity) > 200 ||
				(retained.Theme != "light" && retained.Theme != "dark") ||
				retained.Width < 0 ||
				retained.Width > 100000 ||
				!retained.validState() {
				return nil, uiError("capture.inline_state")
			}
			b.selected[path] = true
		}
		raw, err := json.Marshal(retained)
		if err != nil || len(raw) > 512<<10 {
			return nil, uiError("capture.inline_state")
		}
		var value any
		if json.Unmarshal(raw, &value) != nil || checkUIJSONValue(value) != nil {
			return nil, uiError("capture.inline_state")
		}
		node.Attrs["inlineCapture"] = retained
		b.capture.Values[path+"/inlineState"] = retained
		return []BlockNode{node}, nil
	case "taskCollection":
		if expression, ok := node.Attrs["filter"].(map[string]any); ok {
			value, err := b.evaluate(expression, env, path+"/filter")
			if err != nil {
				return nil, err
			}
			filter, ok := value.(string)
			if !ok || !slices.Contains([]string{"all", "active", "review", "done"}, filter) {
				return nil, uiError("task.filter")
			}
			node.Attrs["filter"] = filter
			if rows, ok := node.Attrs["capturedTasks"].([]any); ok {
				filtered := make([]any, 0, len(rows))
				for _, row := range rows {
					task, _ := row.(map[string]any)
					status, _ := task["status"].(string)
					if filter == "all" ||
						filter == "active" && status != "done" && status != "cancelled" ||
						filter == "review" && status == "in_review" ||
						filter == "done" && status == "done" {
						filtered = append(filtered, row)
					}
				}
				node.Attrs["capturedTasks"] = filtered
			}
		}
	case "uiScope":
		env, err = b.scope(node, env, path)
		if err != nil {
			return nil, err
		}
		node.Type = "uiStack"
		node.Attrs = uiCaptureStackAttrs(node.Attrs)
	case "uiConditional":
		value, err := b.evaluate(node.Attrs["when"], env, path+"/when")
		if err != nil {
			return nil, err
		}
		show, ok := value.(bool)
		if !ok {
			return nil, uiError("boolean.type")
		}
		if !show {
			return nil, nil
		}
		node.Type = "uiStack"
		node.Attrs = uiCaptureStackAttrs(node.Attrs)
	case "uiRepeat":
		if position.repeatDepth >= 8 {
			return nil, uiError("capture.limit")
		}
		value, err := b.evaluate(node.Attrs["items"], env, path+"/items")
		if err != nil {
			return nil, err
		}
		items, ok := value.([]any)
		if !ok || len(items) > UICollectionMaxItems {
			return nil, uiError("collection.type")
		}
		keyField, _ := node.Attrs["keyField"].(string)
		if keyField == "" {
			keyField = "id"
		}
		seen := map[string]bool{}
		var output []BlockNode
		for _, item := range items {
			key := item
			if object, ok := item.(map[string]any); ok {
				key = object[keyField]
			}
			if key == nil || !uiScalar(key) {
				return nil, uiError("repeat.key")
			}
			// Browser keys identify negative and positive zero as the same item.
			if number, ok := key.(float64); ok && number == 0 {
				key = float64(0)
			}
			raw, _ := json.Marshal(key)
			hash := sha256.Sum256(raw)
			identity := hex.EncodeToString(hash[:])
			if seen[identity] {
				return nil, uiError("repeat.key")
			}
			seen[identity] = true
			bound := env
			bound.Item, bound.HasItem = item, true
			for index, child := range node.Content {
				rows, err := b.walk(child, bound, uiCapturePosition{
					path:     fmt.Sprintf("%s/item/%s/%d", path, identity, index),
					instance: true, depth: depth + 1, repeatDepth: position.repeatDepth + 1,
				})
				if err != nil {
					return nil, err
				}
				output = append(output, rows...)
			}
		}
		if len(output) == 0 {
			output = []BlockNode{{Type: "paragraph"}}
		}
		return []BlockNode{{Type: "uiStack", Attrs: uiCaptureStackAttrs(node.Attrs), Content: output}}, nil
	case "uiTextInput",
		"uiNumberInput",
		"uiSelect",
		"uiCheckbox",
		"uiSlider",
		"uiTextArea",
		"uiWritingBlock",
		"uiDatePicker",
		"uiRadioGroup",
		"uiSegmentedControl":
		name, _ := node.Attrs["bind"].(string)
		value, exists := env.State[name]
		if !exists {
			return nil, uiError("binding.undeclared")
		}
		if err := validateUIBoundValue(node, value); err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(value)
		b.bytes += len(encoded)*2 + len(path)
		if b.bytes > 16<<20 {
			return nil, uiError("capture.size")
		}
		b.capture.Values[path+"/input"] = value
		return []BlockNode{
			uiCaptureParagraph(fmt.Sprint(node.Attrs["label"])+": "+uiCaptureText(value), node.Attrs),
		}, nil
	case "uiButton", "uiEntity", "uiFollowUp":
		return []BlockNode{
			uiCaptureParagraph(
				fmt.Sprint(node.Attrs["label"])+" (action unavailable in capture)",
				node.Attrs,
			),
		}, nil
	case "uiForm", "uiPressable", "uiCarousel", "uiCarouselItem":
		node.Type = "uiStack"
		node.Attrs = uiCaptureStackAttrs(node.Attrs)
	case "uiCodeBlock":
		attrs := uiCaptureRetainedAttrs(node.Attrs)
		if attrs == nil {
			attrs = map[string]any{}
		}
		language, _ := node.Attrs["language"].(string)
		attrs["language"] = language
		code, _ := node.Attrs["code"].(string)
		var content []BlockNode
		if code != "" {
			content = []BlockNode{{Type: "text", Text: code}}
		}
		return []BlockNode{{Type: "codeBlock", Attrs: attrs, Content: content}}, nil
	case "uiDivider":
		return []BlockNode{{Type: "horizontalRule", Attrs: uiCaptureRetainedAttrs(node.Attrs)}}, nil
	case "uiLink", "uiCitation":
		return []BlockNode{
			uiCaptureParagraph(
				fmt.Sprint(node.Attrs["label"])+": "+fmt.Sprint(node.Attrs["href"]),
				node.Attrs,
			),
		}, nil
	case "uiVideo", "uiFavicon":
		return []BlockNode{
			uiCaptureParagraph(
				fmt.Sprint(node.Attrs["label"])+": "+fmt.Sprint(node.Attrs["src"]),
				node.Attrs,
			),
		}, nil
	case "uiFileNavList":
		items, _ := node.Attrs["items"].([]any)
		var output []BlockNode
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			output = append(
				output,
				uiCaptureParagraph(fmt.Sprint(item["label"])+": "+fmt.Sprint(item["href"]), nil),
			)
		}
		return output, nil

	case "uiImageGroup":
		images, _ := node.Attrs["images"].([]any)
		var output []BlockNode
		for _, raw := range images {
			image, _ := raw.(map[string]any)
			output = append(
				output,
				uiCaptureParagraph(fmt.Sprint(image["alt"])+": "+fmt.Sprint(image["src"]), nil),
			)
		}
		return output, nil
	case "uiValue",
		"uiMetric",
		"uiChart",
		"uiDataTable",
		"uiText",
		"uiTitle",
		"uiCaption",
		"uiLabel",
		"uiBadge",
		"uiMath":
		property := "value"
		if node.Type == "uiChart" || node.Type == "uiDataTable" {
			property = "data"
		}
		value, err := b.evaluate(node.Attrs[property], env, path+"/"+property)
		if err != nil {
			return nil, err
		}
		if property == "data" {
			if err := validateUICaptureData(node, value); err != nil {
				return nil, err
			}
		}
		node.Attrs[property] = map[string]any{"op": "literal", "value": value}
	}
	var children []BlockNode
	for index, child := range node.Content {
		captured, err := b.walk(child, env, uiCapturePosition{
			path: fmt.Sprintf("%s/%d", path, index), instance: instance,
			depth: depth + 1, repeatDepth: position.repeatDepth,
		})
		if err != nil {
			return nil, err
		}
		children = append(children, captured...)
	}
	if len(node.Content) > 0 && len(children) == 0 {
		children = []BlockNode{{Type: "paragraph"}}
	}
	node.Content = children
	return []BlockNode{node}, nil
}

func uiCaptureText(value any) string {
	if value == nil {
		return "Unavailable"
	}
	if text, ok := value.(string); ok {
		return text
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func uiCaptureParagraph(text string, attrs map[string]any) BlockNode {
	return BlockNode{
		Type:    "paragraph",
		Attrs:   uiCaptureRetainedAttrs(attrs),
		Content: []BlockNode{{Type: "text", Text: text}},
	}
}

func validateUICaptureData(node BlockNode, value any) error {
	rows, ok := value.([]any)
	if !ok {
		return uiError("capture.data")
	}
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return uiError("capture.data")
		}
		if node.Type == "uiChart" {
			x, _ := node.Attrs["x"].(string)
			y, _ := node.Attrs["y"].(string)
			if number, ok := uiNumber(row[y]); !ok || node.Attrs["variant"] == "pie" && number < 0 {
				return uiError("capture.data")
			}
			_, numeric := uiNumber(row[x])
			_, text := row[x].(string)
			if !numeric && (!text || node.Attrs["variant"] == "scatter") {
				return uiError("capture.data")
			}
			continue
		}
		columns, _ := node.Attrs["columns"].([]any)
		for _, raw := range columns {
			column, _ := raw.(map[string]any)
			key, _ := column["key"].(string)
			value := row[key]
			if value == nil {
				continue
			}
			valid := false
			switch column["type"] {
			case "string":
				_, valid = value.(string)
			case "number":
				_, valid = uiNumber(value)
			case "boolean":
				_, valid = value.(bool)
			case "date":
				text, ok := value.(string)
				_, err := time.Parse(time.RFC3339Nano, text)
				valid = ok && err == nil
			}
			if !valid {
				return uiError("capture.data")
			}
		}
	}
	return nil
}

// UICaptureMarkdown derives text from the frozen tree and evidence manifest. It
// does not resolve sources or reevaluate definitions against current defaults.
func UICaptureMarkdown(capture UICapture) string {
	generatedTextBlocks := map[string]struct{}{}
	var collectGeneratedTextBlocks func(BlockNode)
	collectGeneratedTextBlocks = func(node BlockNode) {
		switch node.Type {
		case "uiTextInput",
			"uiNumberInput",
			"uiSelect",
			"uiCheckbox",
			"uiSlider",
			"uiTextArea",
			"uiWritingBlock",
			"uiDatePicker",
			"uiRadioGroup",
			"uiSegmentedControl",
			"uiButton",
			"uiEntity",
			"uiFollowUp",
			"uiLink",
			"uiCitation",
			"uiVideo",
			"uiFavicon":
			if id, _ := node.Attrs["blockId"].(string); id != "" {
				generatedTextBlocks[id] = struct{}{}
			}
		}
		for _, child := range node.Content {
			collectGeneratedTextBlocks(child)
		}
	}
	collectGeneratedTextBlocks(capture.Definition)
	var convert func(BlockNode) BlockNode
	convert = func(node BlockNode) BlockNode {
		switch node.Type {
		case "uiDocument":
			raw, _ := json.MarshalIndent(node.Attrs["inlineCapture"], "", "  ")
			return BlockNode{
				Type:    "codeBlock",
				Attrs:   map[string]any{"language": "json"},
				Content: []BlockNode{{Type: "text", Text: string(raw)}},
			}
		case "uiSvg", "uiIcon":
			return uiCaptureParagraph(uiCaptureMarkdownInline(fmt.Sprint(node.Attrs["label"])), nil)
		case "uiValue", "uiMetric", "uiText", "uiTitle", "uiCaption", "uiLabel", "uiBadge", "uiMath":
			expression, _ := node.Attrs["value"].(map[string]any)
			text := uiCaptureMarkdownInline(uiCaptureText(expression["value"]))
			if node.Type == "uiValue" {
				return BlockNode{Type: "text", Text: text}
			}
			if node.Type != "uiMetric" {
				return uiCaptureParagraph(text, nil)
			}
			label := uiCaptureMarkdownInline(fmt.Sprint(node.Attrs["label"]))
			return uiCaptureParagraph(label+": "+text, nil)
		case "uiChart", "uiDataTable":
			return uiCaptureTable(node)
		}
		children := make([]BlockNode, len(node.Content))
		for i, child := range node.Content {
			if node.Type == "paragraph" && child.Type == "text" {
				if _, generated := generatedTextBlocks[fmt.Sprint(node.Attrs["blockId"])]; generated {
					child.Text = uiCaptureMarkdownInline(child.Text)
				}
			}
			children[i] = convert(child)
		}
		node.Content = children
		return node
	}
	var out strings.Builder
	out.WriteString(BlocksMarkdown(convert(capture.Content)))
	if len(capture.Scopes) > 0 {
		out.WriteString("\n## Selected assumptions\n\n")
		for _, scope := range capture.Scopes {
			out.WriteString(BlocksMarkdown(uiCaptureParagraph(uiCaptureMarkdownInline(scope.Name), nil)))
			for _, name := range uiSortedKeys(scope.Selected) {
				var value any
				_ = json.Unmarshal(scope.Selected[name], &value)
				out.WriteString(
					BlocksMarkdown(
						uiCaptureParagraph(
							uiCaptureMarkdownInline(
								name,
							)+": "+uiCaptureMarkdownInline(
								uiCaptureText(value),
							),
							nil,
						),
					),
				)
			}
		}
	}
	if len(capture.Sources) > 0 {
		out.WriteString("\n## Source observations\n\n")
		for _, source := range capture.Sources {
			hash := sha256.Sum256([]byte(source.Snapshot.Version))
			out.WriteString(
				BlocksMarkdown(
					uiCaptureParagraph(
						source.Name+" — observed "+source.Snapshot.ObservedAt+"; version digest "+hex.EncodeToString(
							hash[:],
						),
						nil,
					),
				),
			)
			coverage := "Coverage was not recorded."
			if evidence := source.Snapshot.Coverage; evidence != nil {
				if evidence.Complete {
					coverage = fmt.Sprintf("Complete matching result: %d rows.", evidence.RowCount)
				} else {
					coverage = fmt.Sprintf(
						"Bounded sample: %d rows (limit %d); additional matching rows are not included. Derived totals describe this sample only.",
						evidence.RowCount,
						evidence.Limit,
					)
				}
			}
			out.WriteString(BlocksMarkdown(uiCaptureParagraph(coverage, nil)))
		}
	}
	return out.String()
}

func uiCaptureTable(node BlockNode) BlockNode {
	var keys, labels []string
	if node.Type == "uiChart" {
		for _, property := range []string{"x", "y"} {
			key, _ := node.Attrs[property].(string)
			keys, labels = append(keys, key), append(labels, uiCaptureMarkdownInline(key))
		}
	} else {
		columns, _ := node.Attrs["columns"].([]any)
		for _, raw := range columns {
			column, _ := raw.(map[string]any)
			key, _ := column["key"].(string)
			label, _ := column["label"].(string)
			keys, labels = append(keys, key), append(labels, uiCaptureMarkdownInline(label))
		}
	}
	table := BlockNode{Type: "table"}
	header := BlockNode{Type: "tableRow"}
	for _, label := range labels {
		header.Content = append(
			header.Content,
			BlockNode{Type: "tableHeader", Content: []BlockNode{uiCaptureParagraph(label, nil)}},
		)
	}
	table.Content = append(table.Content, header)
	expression, _ := node.Attrs["data"].(map[string]any)
	rows, _ := expression["value"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		line := BlockNode{Type: "tableRow"}
		for _, key := range keys {
			line.Content = append(
				line.Content,
				BlockNode{
					Type: "tableCell",
					Content: []BlockNode{
						uiCaptureParagraph(
							uiCaptureMarkdownInline(uiCaptureText(row[key])),
							nil,
						),
					},
				},
			)
		}
		table.Content = append(table.Content, line)
	}
	children := []BlockNode{uiCaptureParagraph(uiCaptureMarkdownInline(fmt.Sprint(node.Attrs["label"])), nil)}
	if len(rows) == 0 {
		children = append(children, uiCaptureParagraph("No matching rows.", nil))
	} else {
		if node.Type == "uiChart" {
			children = append(children, uiCaptureParagraph(uiCaptureChartSummary(node, rows), nil))
		}
		children = append(children, table)
	}
	return BlockNode{Type: "doc", Content: children}
}

func uiCaptureChartSummary(node BlockNode, rows []any) string {
	x, _ := node.Attrs["x"].(string)
	y, _ := node.Attrs["y"].(string)
	first, _ := rows[0].(map[string]any)
	xMin, _ := uiNumber(first[x])
	xMax := xMin
	yMin, _ := uiNumber(first[y])
	yMax := yMin
	lowest, highest := first, first
	for _, raw := range rows[1:] {
		row, _ := raw.(map[string]any)
		if value, ok := uiNumber(row[x]); ok {
			if value < xMin {
				xMin = value
			}
			if value > xMax {
				xMax = value
			}
		}
		if value, ok := uiNumber(row[y]); ok {
			if value < yMin {
				yMin, lowest = value, row
			}
			if value > yMax {
				yMax, highest = value, row
			}
		}
	}
	if node.Attrs["variant"] == "scatter" {
		return fmt.Sprintf(
			"%d points. %s ranges from %s to %s; %s ranges from %s to %s.",
			len(
				rows,
			),
			uiCaptureMarkdownInline(x),
			uiCaptureMarkdownInline(uiCaptureText(xMin)),
			uiCaptureMarkdownInline(uiCaptureText(xMax)),
			uiCaptureMarkdownInline(y),
			uiCaptureMarkdownInline(uiCaptureText(yMin)),
			uiCaptureMarkdownInline(uiCaptureText(yMax)),
		)
	}
	return fmt.Sprintf(
		"%d points. Lowest %s: %s at %s; highest %s: %s at %s.",
		len(
			rows,
		),
		uiCaptureMarkdownInline(y),
		uiCaptureMarkdownInline(uiCaptureText(yMin)),
		uiCaptureMarkdownInline(uiCaptureText(lowest[x])),
		uiCaptureMarkdownInline(y),
		uiCaptureMarkdownInline(uiCaptureText(yMax)),
		uiCaptureMarkdownInline(uiCaptureText(highest[x])),
	)
}

func uiCaptureMarkdownInline(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	var escaped strings.Builder
	for _, char := range value {
		if strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", char) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(char)
	}
	return escaped.String()
}

func uiCaptureStackAttrs(source map[string]any) map[string]any {
	attrs := uiCaptureRetainedAttrs(source)
	attrs["direction"], attrs["gap"] = "vertical", "normal"
	return attrs
}

// Native source lineage also applies to transformed containers and controls;
// losing it would prevent the existing capture redactor from hiding their text.
func uiCaptureRetainedAttrs(source map[string]any) map[string]any {
	attrs := map[string]any{}
	for _, key := range []string{"blockId", "sourceVersionIds", "agentProtected", "capturedNeedsRefresh"} {
		if value, exists := source[key]; exists && value != nil {
			attrs[key] = value
		}
	}
	return attrs
}

// Inline source has no ambient data access. Its local viewer state is evidence,
// never authorization, and is replayed only by the pinned read-only interpreter.
type inlineCaptureState struct {
	RendererVersion int            `json:"rendererVersion"`
	Identity        string         `json:"identity"`
	Source          string         `json:"source"`
	Theme           string         `json:"theme"`
	Width           float64        `json:"width"`
	State           map[string]any `json:"state"`
	Refs            map[string]any `json:"refs"`
	Memo            map[string]any `json:"memo"`
}

func (s inlineCaptureState) validState() bool {
	if s.State == nil || s.Refs == nil || s.Memo == nil {
		return false
	}
	for _, value := range s.Refs {
		entry, ok := value.(map[string]any)
		if !ok || len(entry) != 1 {
			return false
		}
		if _, ok := entry["current"]; !ok {
			return false
		}
	}
	for _, value := range s.Memo {
		entry, ok := value.(map[string]any)
		if !ok || len(entry) != 2 {
			return false
		}
		if _, ok := entry["value"]; !ok {
			return false
		}
		if _, ok := entry["dependencies"].([]any); !ok {
			return false
		}
	}
	return true
}
