package wiki

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// sourcePropertySpan narrows a catalog error within JSON. Shorthand expressions
// already carry parser locations and retain their existing diagnostic path.
func sourcePropertySpan(raw string, value any, rule UIPropertyRule) (int, int) {
	return sourcePathSpan(raw, invalidUIPropertyPath(value, rule))
}

func sourcePathSpan(raw string, path []string) (int, int) {
	start, end := 0, len(raw)
	// Ordinary JSON attributes may have one additional authoring wrapper.
	// Keep offsets in the original source while walking the inner JSON value.
	if !json.Valid([]byte(raw)) && len(raw) >= 2 && raw[0] == '{' && raw[len(raw)-1] == '}' {
		if json.Valid([]byte(raw[1 : len(raw)-1])) {
			start++
			end--
		}
	}
	for _, key := range path {
		childStart, childEnd, ok := sourceJSONChildSpan(raw[start:end], key)
		if !ok {
			break
		}
		end = start + childEnd
		start += childStart
	}
	return start, end
}

func invalidUIPropertyPath(value any, rule UIPropertyRule) []string {
	object, isObject := value.(map[string]any)
	switch rule.Type {
	case "expression":
		path, _ := validateUIExpression(value, true)
		return path
	case "variant":
		if !isObject {
			return nil
		}
		tag, _ := object[rule.Discriminator].(string)
		variant, ok := rule.Variants[tag]
		if !ok {
			return []string{rule.Discriminator}
		}
		return invalidUIPropertyPath(value, variant)
	case "object":
		if !isObject {
			return nil
		}
		for _, key := range slices.Sorted(maps.Keys(object)) {
			if !slices.ContainsFunc(
				rule.Fields,
				func(field UIPropertyRule) bool { return field.Property == key },
			) {
				return []string{key}
			}
		}
		for _, field := range rule.Fields {
			if validateUIProperty(object, field) != nil {
				if child, ok := object[field.Property]; ok {
					return append([]string{field.Property}, invalidUIPropertyPath(child, field)...)
				}
				return nil
			}
		}
	case "nameMap":
		if !isObject || len(object) < rule.Min || len(object) > rule.Max {
			return nil
		}
		for _, key := range slices.Sorted(maps.Keys(object)) {
			if !validUIName(key) {
				return []string{key}
			}
			if validateUIRuleValue(object[key], *rule.Entry) != nil {
				return append([]string{key}, invalidUIPropertyPath(object[key], *rule.Entry)...)
			}
		}
	case "stringArray":
		items, ok := value.([]any)
		if !ok || len(items) < rule.Min || len(items) > rule.Max {
			return nil
		}
		seen := map[string]bool{}
		for index, item := range items {
			text, ok := item.(string)
			if !ok || len(text) > rule.ItemMaxBytes || rule.Unique && seen[text] {
				return []string{strconv.Itoa(index)}
			}
			seen[text] = true
		}

	case "objectArray":
		items, ok := value.([]any)
		if !ok || len(items) < rule.Min || len(items) > rule.Max {
			return nil
		}
		seen := map[string]bool{}
		for index, child := range items {
			fieldRule := UIPropertyRule{
				Type:     "object",
				Fields:   rule.Fields,
				Code:     rule.ItemCode,
				ItemCode: rule.ItemCode,
			}
			if validateUIRuleValue(child, fieldRule) != nil {
				return append([]string{strconv.Itoa(index)}, invalidUIPropertyPath(child, fieldRule)...)
			}
			item, _ := child.(map[string]any)
			if rule.UniqueBy != "" {
				key, ok := item[rule.UniqueBy].(string)
				if !ok || seen[key] {
					return []string{strconv.Itoa(index), rule.UniqueBy}
				}
				seen[key] = true
			}
		}
	}
	return nil
}

func sourceJSONChildSpan(raw, key string) (int, int, bool) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil {
		return 0, 0, false
	}
	delimiter, ok := opening.(json.Delim)
	if !ok || delimiter != '{' && delimiter != '[' {
		return 0, 0, false
	}
	foundStart, foundEnd, found := 0, 0, false
	for index := 0; decoder.More(); index++ {
		childKey := strconv.Itoa(index)
		if delimiter == '{' {
			token, err := decoder.Token()
			if err != nil {
				return 0, 0, false
			}
			childKey, ok = token.(string)
			if !ok {
				return 0, 0, false
			}
		}
		start := int(decoder.InputOffset())
		for start < len(raw) && strings.ContainsRune(" \t\r\n,:", rune(raw[start])) {
			start++
		}
		var child json.RawMessage
		if decoder.Decode(&child) != nil {
			return 0, 0, false
		}
		if childKey == key {
			foundStart, foundEnd, found = start, int(decoder.InputOffset()), true
		}
	}
	return foundStart, foundEnd, found
}

// sourceNativeShapePath mirrors BlockNode/BlockMark JSON field shapes so strict
// decoder failures can be located before conversion discards source offsets.
// Native attribute semantics remain owned by document validation.
func sourceNativeShapePath(value any, mark bool, depth int) ([]string, bool) {
	object, ok := value.(map[string]any)
	if !ok || depth > 32 {
		return nil, true
	}
	allowed := []string{"type", "attrs", "content", "text", "marks"}
	if mark {
		allowed = []string{"type", "attrs"}
	}
	for _, key := range slices.Sorted(maps.Keys(object)) {
		if !slices.Contains(allowed, key) {
			return []string{key}, true
		}
	}
	nodeType, ok := object["type"].(string)
	if !ok || nodeType == "" || depth == 0 && nodeType == "doc" {
		return []string{"type"}, true
	}
	for _, key := range []string{"attrs", "text", "content", "marks"} {
		child, exists := object[key]
		if !exists || child == nil {
			continue
		}
		switch key {
		case "attrs":
			if _, ok := child.(map[string]any); !ok {
				return []string{key}, true
			}
		case "text":
			if _, ok := child.(string); !ok {
				return []string{key}, true
			}
		case "content", "marks":
			items, ok := child.([]any)
			if !ok {
				return []string{key}, true
			}
			childDepth := depth
			if key == "content" {
				childDepth++
			}
			for index, item := range items {
				if path, invalid := sourceNativeShapePath(item, key == "marks", childDepth); invalid {
					return append([]string{key, strconv.Itoa(index)}, path...), true
				}
			}
		}
	}
	return nil, false
}

// sourceNativeCatalogPath locates catalog failures inside a Native escape,
// including reactive descendants of an ordinary native container. Lexical
// reference validation still runs against the complete document after parsing.
func sourceNativeCatalogPath(node BlockNode) ([]string, error) {
	if _, reactive := uiComponent(node.Type); reactive {
		if path, err := validateUINode(node, true); err != nil {
			return path, err
		}
	}
	for index, child := range node.Content {
		if path, err := sourceNativeCatalogPath(child); err != nil {
			return append([]string{"content", strconv.Itoa(index)}, path...), err
		}
	}
	return nil, nil
}

// sourceNodeLocation maps the compiled node back to its authored opening tag.
// Native escapes retain the rest of their subtree as JSON within one attribute.
type sourceNodeLocation struct {
	path             []string
	start, end, base int
	body             string
	spans            map[string][2]int
	native           bool
}

func sourceValidationSpan(locations []sourceNodeLocation, path []string, fallbackEnd int) (int, int) {
	var best *sourceNodeLocation
	for index := range locations {
		candidate := &locations[index]
		if len(candidate.path) <= len(path) && slices.Equal(candidate.path, path[:len(candidate.path)]) &&
			(best == nil || len(candidate.path) > len(best.path)) {
			best = candidate
		}
	}
	if best == nil {
		return 0, fallbackEnd
	}
	remainder := path[len(best.path):]
	property := "node"
	if !best.native {
		if len(remainder) < 2 || remainder[0] != "attrs" {
			return best.start, best.end
		}
		property, remainder = remainder[1], remainder[2:]
	}
	span, exists := best.spans[property]
	if !exists {
		return best.start, best.end
	}
	raw := best.body[span[0]:span[1]]
	start, end := sourcePathSpan(raw, remainder)
	if !best.native {
		if narrowedStart, narrowedEnd, ok := sourceShorthandSpan(raw, property, remainder); ok {
			start, end = narrowedStart, narrowedEnd
		}
	}
	return best.base + span[0] + start, best.base + span[0] + end
}

// sourceShorthandSpan reparses only a rejected attribute with location collection
// enabled. Successful compilation keeps no token-level runtime metadata.
func sourceShorthandSpan(raw, property string, path []string) (int, int, bool) {
	if json.Valid([]byte(raw)) {
		return 0, 0, false
	}
	if property == "computed" || property == "action" {
		if len(path) == 0 {
			return 0, 0, false
		}
		spans := map[string][2]int{}
		if _, _, err := parseUISourceDeclarationsLocated(raw, property == "action", spans); err != nil {
			return 0, 0, false
		}
		span, exists := spans[path[0]]
		if !exists {
			return 0, 0, false
		}
		value := raw[span[0]:span[1]]
		start, end := sourcePathSpan(value, path[1:])
		if narrowedStart, narrowedEnd, ok := sourceShorthandSpan(value, "", path[1:]); ok {
			start, end = narrowedStart, narrowedEnd
		}
		return span[0] + start, span[0] + end, true
	}
	base := 0
	expression := raw
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		base = 1
		expression = raw[1 : len(raw)-1]
	}
	var spans [][2]int
	parser := uiSourceExpression{source: expression, referenceSpans: &spans}
	value, err := parser.expression(0)
	parser.space()
	if err != nil || parser.at != len(expression) {
		return 0, 0, false
	}
	index := sourceReferenceIndex(value, path)
	if index < 0 || index >= len(spans) {
		return 0, 0, false
	}
	return base + spans[index][0], base + spans[index][1], true
}

// References retain source order through the closed AST's args/input/body
// construction. Paths distinguish identical references in different bindings.
func sourceReferenceIndex(value any, target []string) int {
	index, found := 0, -1
	var visit func(any, []string)
	visit = func(raw any, path []string) {
		node, ok := raw.(map[string]any)
		if !ok || node["op"] == "literal" {
			return
		}
		if node["op"] == "state" || node["op"] == "data" || node["op"] == "item" {
			field := "name"
			if node["op"] == "item" {
				field = "op"
			}
			if slices.Equal(append(slices.Clone(path), field), target) {
				found = index
			}
			index++
		}
		if args, ok := node["args"].([]any); ok {
			for i, child := range args {
				visit(child, append(slices.Clone(path), "args", strconv.Itoa(i)))
			}
		}
		for _, key := range []string{"input", "body"} {
			if child, exists := node[key]; exists {
				visit(child, append(slices.Clone(path), key))
			}
		}
	}
	visit(value, nil)
	return found
}

// LocateSourceReplacementError maps a whole-document validation failure back to
// the source of an identity-preserving member replacement. Errors outside that
// subtree keep their original diagnostic rather than pointing at unrelated text.
func LocateSourceReplacementError(source string, root BlockNode, blockID string, err error) error {
	var located *uiValidationLocation
	if !errors.Is(err, ErrPageInvalidInput) || !errors.As(err, &located) {
		return err
	}
	path, found := sourceBlockPath(root, blockID)
	if !found || len(path) > len(located.path) || !slices.Equal(path, located.path[:len(path)]) {
		return err
	}
	var locations []sourceNodeLocation
	if _, compileErr := compileSourceLocated(source, 1, false, &locations); compileErr != nil {
		return err
	}
	replacementPath := append([]string{"content", "0"}, located.path[len(path):]...)
	start, end := sourceValidationSpan(locations, replacementPath, len(source))
	code := "source.validation"
	var expression *UIEvaluationError
	if errors.As(err, &expression) {
		code = expression.Code
	}
	return &SourceDiagnostic{
		Code:     code,
		Severity: "error",
		BlockID:  blockID,
		Start:    start,
		End:      end,
		Repair:   "Use valid native/catalog properties and declare state or data references in the enclosing Scope; preserve the selected root identity.",
	}
}

func sourceBlockPath(root BlockNode, blockID string) ([]string, bool) {
	if blockID != "" && root.Attrs["blockId"] == blockID {
		return nil, true
	}
	for index, child := range root.Content {
		if path, found := sourceBlockPath(child, blockID); found {
			return append([]string{"content", strconv.Itoa(index)}, path...), true
		}
	}
	return nil, false
}
