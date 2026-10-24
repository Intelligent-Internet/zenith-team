package wiki

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
)

// UIEnvironment contains only host-authorized snapshots and local input values.
// The interpreter has no clock, network, credentials or host action capability.
type UIEnvironment struct {
	Item    any            `json:"item,omitempty"`
	HasItem bool           `json:"hasItem,omitempty"`
	State   map[string]any `json:"state"`
	Data    map[string]any `json:"data"`
}

// UIEvaluationError exposes a stable diagnostic code without private values.
type UIEvaluationError struct {
	Code string `json:"code"`
}

func (e *UIEvaluationError) Error() string { return "intelligent ui: " + e.Code }
func (e *UIEvaluationError) Unwrap() error { return ErrPageInvalidInput }

// Limits are part of language version 1 and shared with the browser evaluator.
const (
	UIExpressionMaxDepth = 32
	UIExpressionMaxNodes = 4096
	UIEvaluationMaxSteps = 20000
	UICollectionMaxItems = 1000
	UIValueMaxBytes      = 1 << 20
)

type (
	uiEvaluation struct{ steps int }
	uiItem       struct {
		value any
		bound bool
	}
)

// EvaluateUIExpression validates and evaluates an untrusted expression. JSON
// normalization gives Go callers the same number/array semantics as the browser.
func EvaluateUIExpression(expression any, environment UIEnvironment) (any, error) {
	normalized, err := decodeUIJSON(expression)
	if err != nil {
		return nil, err
	}
	if err = ValidateUIExpression(normalized); err != nil {
		return nil, err
	}
	inputs, err := normalizeUIJSON(environment)
	if err != nil {
		return nil, err
	}
	object, ok := inputs.(map[string]any)
	if !ok {
		return nil, uiError("input.type")
	}
	state, _ := object["state"].(map[string]any)
	data, _ := object["data"].(map[string]any)
	evaluator := uiEvaluation{}
	hasItem, _ := object["hasItem"].(bool)
	result, err := evaluator.evaluate(
		normalized,
		UIEnvironment{State: state, Data: data},
		uiItem{value: object["item"], bound: hasItem},
		0,
	)
	if err != nil {
		return nil, err
	}
	if err := checkUIJSONValue(result); err != nil {
		return nil, err
	}
	return normalizeUIJSON(result)
}

func uiError(code string) error { return &UIEvaluationError{Code: code} }

func decodeUIJSON(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > UIValueMaxBytes {
		return nil, uiError("value.size")
	}
	var normalized any
	if json.Unmarshal(raw, &normalized) != nil {
		return nil, uiError("value.json")
	}
	return normalized, nil
}

func normalizeUIJSON(value any) (any, error) {
	normalized, err := decodeUIJSON(value)
	if err != nil {
		return nil, err
	}
	return normalized, checkUIJSONValue(normalized)
}

func checkUIJSONValue(normalized any) error {
	count := 0
	bytes := 0
	var visit func(any, int) error
	visit = func(v any, depth int) error {
		count++
		bytes += 8
		if bytes > UIValueMaxBytes {
			return uiError("value.size")
		}
		if depth > UIExpressionMaxDepth || count > UIEvaluationMaxSteps {
			return uiError("value.depth")
		}
		switch v := v.(type) {
		case map[string]any:
			for key, child := range v {
				bytes += len(key)
				if !safeUIField(key) {
					return uiError("field.unsafe")
				}
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
		case []any:
			if len(v) > UICollectionMaxItems {
				return uiError("collection.size")
			}
			for _, child := range v {
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return uiError("number.finite")
			}
		case string:
			bytes += len(v)
			if bytes > UIValueMaxBytes {
				return uiError("value.size")
			}
			if len(v) > 65536 {
				return uiError("string.size")
			}
		}
		return nil
	}
	return visit(normalized, 0)
}

func safeUIField(name string) bool {
	return name != "__proto__" && name != "prototype" && name != "constructor"
}

// ValidateUIExpression checks the complete AST, including unselected branches.
// Expressions use closed objects; arbitrary source code is never interpreted.
func ValidateUIExpression(expression any) error {
	_, err := validateUIExpression(expression, false)
	return err
}

func validateUIExpression(expression any, locate bool) ([]string, error) {
	count := 0
	var path, invalid []string
	fail := func(code string, field ...string) error {
		if locate {
			invalid = append(slices.Clone(path), field...)
		}
		return uiError(code)
	}
	var visit func(any, int) error
	child := func(raw any, depth int, field ...string) error {
		previous := len(path)
		if locate {
			path = append(path, field...)
		}
		err := visit(raw, depth)
		path = path[:previous]
		return err
	}
	visit = func(raw any, depth int) error {
		count++
		if depth > UIExpressionMaxDepth || count > UIExpressionMaxNodes {
			return fail("expression.limit")
		}
		node, ok := raw.(map[string]any)
		if !ok {
			return fail("expression.object")
		}
		op, ok := node["op"].(string)
		if !ok {
			return fail("expression.op", "op")
		}
		allowed := []string{"op"}
		argsCount := -1
		switch op {
		case "literal":
			allowed = append(allowed, "value")
			if _, ok = node["value"]; !ok {
				return fail("literal.value", "value")
			}
			if _, err := normalizeUIJSON(node["value"]); err != nil {
				if locate {
					invalid = append(slices.Clone(path), "value")
				}
				return err
			}
		case "state", "data":
			allowed = append(allowed, "name")
			name, ok := node["name"].(string)
			if !ok || !validUIName(name) {
				return fail("reference.name", "name")
			}
		case "item":
		case "field":
			allowed = append(allowed, "input", "field")
			field, ok := node["field"].(string)
			if !ok || field == "" || len(field) > 128 || !safeUIField(field) {
				return fail("field.unsafe", "field")
			}
			if err := child(node["input"], depth+1, "input"); err != nil {
				return err
			}
		case "count", "sum", "min", "max", "avg":
			allowed = append(allowed, "input")
			if err := child(node["input"], depth+1, "input"); err != nil {
				return err
			}
		case "map", "filter", "sort":
			allowed = append(allowed, "input", "body")
			if op == "sort" {
				allowed = append(allowed, "descending")
				if value, exists := node["descending"]; exists {
					if _, ok := value.(bool); !ok {
						return fail("sort.direction", "descending")
					}
				}
			}
			if err := child(node["input"], depth+1, "input"); err != nil {
				return err
			}
			if err := child(node["body"], depth+1, "body"); err != nil {
				return err
			}
		case "format":
			allowed = append(allowed, "input", "style", "digits")
			style, ok := node["style"].(string)
			if !ok || !slices.Contains([]string{"decimal", "percent", "text"}, style) {
				return fail("format.style", "style")
			}
			if digits, exists := node["digits"]; exists {
				n, ok := digits.(float64)
				if !ok || n < 0 || n > 6 || math.Trunc(n) != n {
					return fail("format.digits", "digits")
				}
			}
			if err := child(node["input"], depth+1, "input"); err != nil {
				return err
			}
		case "not":
			argsCount = 1
		case "if":
			argsCount = 3
		case "add",
			"sub",
			"mul",
			"div",
			"mod",
			"eq",
			"ne",
			"lt",
			"lte",
			"gt",
			"gte",
			"and",
			"or",
			"coalesce",
			"take",
			"dateDiff":
			argsCount = 2
		default:
			return fail("expression.unknown", "op")
		}
		if argsCount >= 0 {
			allowed = append(allowed, "args")
			args, ok := node["args"].([]any)
			if !ok || len(args) != argsCount {
				return fail("expression.arity", "args")
			}
			for index, arg := range args {
				if err := child(arg, depth+1, "args", strconv.Itoa(index)); err != nil {
					return err
				}
			}
		}
		for _, key := range slices.Sorted(maps.Keys(node)) {
			if !slices.Contains(allowed, key) {
				return fail("expression.property", key)
			}
		}
		return nil
	}
	err := visit(expression, 0)
	return invalid, err
}

func validUIName(name string) bool {
	if name == "" || len(name) > 64 || !safeUIField(name) {
		return false
	}
	for i, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && char != '_' &&
			(i <= 0 || char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func (e *uiEvaluation) evaluate(raw any, env UIEnvironment, item uiItem, depth int) (any, error) {
	e.steps++
	if e.steps > UIEvaluationMaxSteps || depth > UIExpressionMaxDepth {
		return nil, uiError("evaluation.budget")
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return nil, uiError("expression.object")
	}
	op, _ := node["op"].(string)
	eval := func(value any) (any, error) { return e.evaluate(value, env, item, depth+1) }
	switch op {
	case "literal":
		return node["value"], nil
	case "state", "data":
		name, _ := node["name"].(string)
		values := env.State
		if op == "data" {
			values = env.Data
		}
		value, ok := values[name]
		if !ok {
			return nil, uiError("reference.missing")
		}
		return value, nil
	case "item":
		if !item.bound {
			return nil, uiError("reference.item")
		}
		return item.value, nil
	case "field":
		value, err := eval(node["input"])
		if err != nil {
			return nil, err
		}
		if value == nil {
			return nil, nil
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, uiError("field.type")
		}
		field, _ := node["field"].(string)
		return object[field], nil
	case "map", "filter", "sort", "count", "sum", "min", "max", "avg":
		return e.collection(node, env, item, depth)
	case "format":
		value, err := eval(node["input"])
		if err != nil {
			return nil, err
		}
		style, _ := node["style"].(string)
		if style == "text" {
			if err := checkUIJSONValue(value); err != nil {
				return nil, err
			}
			if value == nil {
				return "Unavailable", nil
			}
			if text, ok := value.(string); ok {
				return text, nil
			}
			raw, err := json.Marshal(value)
			return string(raw), err
		}
		number, ok := value.(float64)
		if !ok {
			return nil, uiError("number.type")
		}
		digits, _ := node["digits"].(float64)
		suffix := ""
		if style == "percent" {
			number *= 100
			suffix = "%"
		}
		if math.IsInf(number, 0) {
			return nil, uiError("number.finite")
		}
		if number == 0 {
			number = 0
		}
		return strconv.FormatFloat(number, 'f', int(digits), 64) + suffix, nil
	}
	args, _ := node["args"].([]any)
	if len(args) == 0 {
		return nil, uiError("expression.arity")
	}
	left, err := eval(args[0])
	if err != nil {
		return nil, err
	}
	if op == "if" {
		condition, ok := left.(bool)
		if !ok {
			return nil, uiError("boolean.type")
		}
		if condition {
			return eval(args[1])
		}
		return eval(args[2])
	}
	if op == "coalesce" {
		if left != nil {
			return left, nil
		}
		return eval(args[1])
	}
	if op == "not" {
		value, ok := left.(bool)
		if !ok {
			return nil, uiError("boolean.type")
		}
		return !value, nil
	}
	if op == "and" || op == "or" {
		value, ok := left.(bool)
		if !ok {
			return nil, uiError("boolean.type")
		}
		if op == "and" && !value || op == "or" && value {
			return value, nil
		}
		right, err := eval(args[1])
		if err != nil {
			return nil, err
		}
		if _, ok := right.(bool); !ok {
			return nil, uiError("boolean.type")
		}
		return right, nil
	}
	right, err := eval(args[1])
	if err != nil {
		return nil, err
	}
	switch op {
	case "eq":
		return reflect.DeepEqual(left, right), nil
	case "ne":
		return !reflect.DeepEqual(left, right), nil
	case "lt", "lte", "gt", "gte":
		order, err := compareUIValues(left, right)
		if err != nil {
			return nil, err
		}
		return op == "lt" && order < 0 || op == "lte" && order <= 0 || op == "gt" && order > 0 ||
			op == "gte" && order >= 0, nil
	case "take":
		values, ok := left.([]any)
		n, number := right.(float64)
		if !ok || !number || n < 0 || n > UICollectionMaxItems || math.Trunc(n) != n {
			return nil, uiError("take.type")
		}
		return values[:min(len(values), int(n))], nil
	case "dateDiff":
		a, aok := left.(string)
		b, bok := right.(string)
		if !aok || !bok {
			return nil, uiError("date.type")
		}
		first, err := time.Parse(time.RFC3339Nano, a)
		if err != nil {
			return nil, uiError("date.format")
		}
		second, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			return nil, uiError("date.format")
		}
		// Seconds rather than time.Duration avoids saturation beyond 290 years.
		return float64(first.Unix()-second.Unix()) + float64(first.Nanosecond()-second.Nanosecond())/1e9, nil
	}
	a, aok := left.(float64)
	b, bok := right.(float64)
	if !aok || !bok {
		return nil, uiError("number.type")
	}
	var result float64
	switch op {
	case "add":
		result = a + b
	case "sub":
		result = a - b
	case "mul":
		result = a * b
	case "div":
		if b == 0 {
			return nil, uiError("number.zeroDivision")
		}
		result = a / b
	case "mod":
		if b == 0 {
			return nil, uiError("number.zeroDivision")
		}
		result = math.Mod(a, b)
	default:
		return nil, uiError("expression.unknown")
	}
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return nil, uiError("number.finite")
	}
	if result == 0 {
		result = 0
	}
	return result, nil
}

func compareUIValues(left, right any) (int, error) {
	if left == nil && right == nil {
		return 0, nil
	}
	if left == nil {
		return 1, nil
	}
	if right == nil {
		return -1, nil
	}
	switch left := left.(type) {
	case float64:
		right, ok := right.(float64)
		if !ok {
			return 0, uiError("comparison.type")
		}
		if left < right {
			return -1, nil
		}
		if left > right {
			return 1, nil
		}
		return 0, nil
	case string:
		right, ok := right.(string)
		if !ok {
			return 0, uiError("comparison.type")
		}
		return strings.Compare(left, right), nil
	default:
		return 0, uiError("comparison.type")
	}
}

func (e *uiEvaluation) collection(node map[string]any, env UIEnvironment, item uiItem, depth int) (any, error) {
	value, err := e.evaluate(node["input"], env, item, depth+1)
	if err != nil {
		return nil, err
	}
	values, ok := value.([]any)
	if !ok {
		return nil, uiError("collection.type")
	}
	if len(values) > UICollectionMaxItems {
		return nil, uiError("collection.size")
	}
	op, _ := node["op"].(string)
	if op == "count" {
		return float64(len(values)), nil
	}
	if slices.Contains([]string{"sum", "min", "max", "avg"}, op) {
		if len(values) == 0 {
			if op == "sum" {
				return float64(0), nil
			}
			return nil, nil
		}
		var total float64
		for i, value := range values {
			e.steps++
			if e.steps > UIEvaluationMaxSteps {
				return nil, uiError("evaluation.budget")
			}
			number, ok := value.(float64)
			if !ok {
				return nil, uiError("number.type")
			}
			switch op {
			case "min":
				if i == 0 || number < total {
					total = number
				}
			case "max":
				if i == 0 || number > total {
					total = number
				}
			default:
				total += number
			}
			if math.IsInf(total, 0) {
				return nil, uiError("number.finite")
			}
		}
		if op == "avg" {
			total /= float64(len(values))
		}
		return total, nil
	}
	result := make([]any, 0, len(values))
	type row struct{ value, key any }
	var rows []row
	for _, value := range values {
		evaluated, err := e.evaluate(node["body"], env, uiItem{value: value, bound: true}, depth+1)
		if err != nil {
			return nil, err
		}
		switch op {
		case "map":
			result = append(result, evaluated)
		case "filter":
			keep, ok := evaluated.(bool)
			if !ok {
				return nil, uiError("boolean.type")
			}
			if keep {
				result = append(result, value)
			}
		case "sort":
			rows = append(rows, row{value: value, key: evaluated})
		}
	}
	if op == "sort" {
		// Validate one homogeneous key type before sorting: comparators cannot
		// safely report errors after partially rearranging a caller's snapshot.
		var sample any
		for _, row := range rows {
			if row.key != nil {
				if _, err := compareUIValues(row.key, row.key); err != nil {
					return nil, err
				}
				if sample != nil {
					if _, err := compareUIValues(sample, row.key); err != nil {
						return nil, err
					}
				}
				sample = row.key
			}
		}
		descending, _ := node["descending"].(bool)
		// Match the browser's merge order so comparison budgets are portable.
		scratch := make([]row, len(rows))
		for width := 1; width < len(rows); width *= 2 {
			for start := 0; start < len(rows); start += 2 * width {
				middle := min(start+width, len(rows))
				end := min(start+2*width, len(rows))
				left, right := start, middle
				for at := start; at < end; at++ {
					before := false
					if left < middle && right < end {
						e.steps++
						if e.steps > UIEvaluationMaxSteps {
							return nil, uiError("evaluation.budget")
						}
						a, b := rows[left].key, rows[right].key
						order, _ := compareUIValues(a, b)
						if descending && a != nil && b != nil {
							order = -order
						}
						before = order <= 0
					}
					if right >= end || left < middle && before {
						scratch[at] = rows[left]
						left++
					} else {
						scratch[at] = rows[right]
						right++
					}
				}
			}
			copy(rows, scratch)
		}
		for _, row := range rows {
			result = append(result, row.value)
		}
	}
	return result, nil
}

// UIExpressionDependencies is the minimal set of state/data names a compiled
// expression consumes. The runtime uses it to invalidate affected values only.
func UIExpressionDependencies(expression any) ([]string, error) {
	if err := ValidateUIExpression(expression); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(raw any) {
		node, ok := raw.(map[string]any)
		if !ok {
			return
		}
		op, _ := node["op"].(string)
		if op == "literal" {
			return
		}
		if op == "state" || op == "data" {
			seen[fmt.Sprint(op, ".", node["name"])] = true
		}
		if args, ok := node["args"].([]any); ok {
			for _, arg := range args {
				walk(arg)
			}
		}
		walk(node["input"])
		walk(node["body"])
	}
	walk(expression)
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	slices.Sort(result)
	return result, nil
}
