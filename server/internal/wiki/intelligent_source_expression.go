package wiki

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// uiSourceExpression parses only the closed expression language. It emits the
// same validated AST as JSON authoring and never invokes a host language parser.
type uiSourceExpression struct {
	source         string
	at             int
	depth          int
	nodes          int
	referenceSpans *[][2]int
}

func parseUISourceExpression(source string) (any, int, error) {
	parser := uiSourceExpression{source: source}
	value, err := parser.expression(0)
	if err != nil {
		var diagnostic *SourceDiagnostic
		if errors.As(err, &diagnostic) {
			return nil, 0, diagnostic
		}
		_, width := utf8.DecodeRuneInString(source[parser.at:])
		return nil, 0, sourceExpressionError(err, parser.at, min(len(source), parser.at+width))
	}
	if !parser.consume("}") {
		return nil, 0, sourceExpressionError(uiError("source.expression.close"), parser.at, parser.at)
	}
	if err := ValidateUIExpression(value); err != nil {
		return nil, 0, sourceExpressionError(err, 0, parser.at)
	}
	return value, parser.at, nil
}

func (p *uiSourceExpression) space() {
	for p.at < len(p.source) && strings.ContainsRune(" \t\r\n", rune(p.source[p.at])) {
		p.at++
	}
}

func (p *uiSourceExpression) consume(token string) bool {
	p.space()
	if !strings.HasPrefix(p.source[p.at:], token) {
		return false
	}
	p.at += len(token)
	return true
}

func (p *uiSourceExpression) identifier() string {
	p.space()
	start := p.at
	for p.at < len(p.source) {
		char := p.source[p.at]
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && char != '_' &&
			(p.at <= start || char < '0' || char > '9') {
			break
		}
		p.at++
	}
	return p.source[start:p.at]
}

func uiSourceOperation(op string, args ...any) any {
	return map[string]any{"op": op, "args": args}
}

func (p *uiSourceExpression) expression(minimum int) (any, error) {
	p.depth++
	defer func() { p.depth-- }()
	p.nodes++
	if p.depth > UIExpressionMaxDepth || p.nodes > UIExpressionMaxNodes {
		return nil, uiError("source.expression.limit")
	}
	left, err := p.primary()
	if err != nil {
		return nil, err
	}
	for {
		p.space()
		token, op, precedence := "", "", 0
		for _, candidate := range []struct {
			token, op  string
			precedence int
		}{
			{"??", "coalesce", 1},
			{"||", "or", 2},
			{"&&", "and", 3},
			{"==", "eq", 4},
			{"!=", "ne", 4},
			{"<=", "lte", 5},
			{">=", "gte", 5},
			{"<", "lt", 5},
			{">", "gt", 5},
			{"+", "add", 6},
			{"-", "sub", 6},
			{"*", "mul", 7},
			{"/", "div", 7},
			{"%", "mod", 7},
		} {
			if strings.HasPrefix(p.source[p.at:], candidate.token) {
				token, op, precedence = candidate.token, candidate.op, candidate.precedence
				break
			}
		}
		if token == "" || precedence < minimum {
			break
		}
		p.at += len(token)
		right, err := p.expression(precedence + 1)
		if err != nil {
			return nil, err
		}
		left = uiSourceOperation(op, left, right)
	}
	if minimum == 0 && p.consume("?") {
		yes, err := p.expression(0)
		if err != nil {
			return nil, err
		}
		if !p.consume(":") {
			return nil, uiError("source.expression.conditional")
		}
		no, err := p.expression(0)
		if err != nil {
			return nil, err
		}
		left = uiSourceOperation("if", left, yes, no)
	}
	return left, nil
}

func (p *uiSourceExpression) primary() (any, error) {
	p.space()
	var value any
	switch {
	case p.consume("!"):
		arg, err := p.expression(8)
		if err != nil {
			return nil, err
		}
		value = uiSourceOperation("not", arg)
	case p.consume("-"):
		arg, err := p.expression(8)
		if err != nil {
			return nil, err
		}
		value = uiSourceOperation("sub", map[string]any{"op": "literal", "value": float64(0)}, arg)
	case p.consume("("):
		var err error
		value, err = p.expression(0)
		if err != nil {
			return nil, err
		}
		if !p.consume(")") {
			return nil, uiError("source.expression.parenthesis")
		}
	default:
		p.space()
		referenceStart := p.at
		name := p.identifier()
		switch name {
		case "state", "data":
			if !p.consume(".") {
				return nil, uiError("reference.name")
			}
			nameOfValue := p.identifier()
			if !validUIName(nameOfValue) {
				return nil, uiError("reference.name")
			}
			value = map[string]any{"op": name, "name": nameOfValue}
			if p.referenceSpans != nil {
				*p.referenceSpans = append(*p.referenceSpans, [2]int{referenceStart, p.at})
			}
		case "item":
			value = map[string]any{"op": "item"}
			if p.referenceSpans != nil {
				*p.referenceSpans = append(*p.referenceSpans, [2]int{referenceStart, p.at})
			}
		case "true", "false", "null":
			var literal any
			if err := json.Unmarshal([]byte(name), &literal); err != nil {
				return nil, err
			}
			value = map[string]any{"op": "literal", "value": literal}
		case "":
			decoder := json.NewDecoder(strings.NewReader(p.source[p.at:]))
			var literal any
			if err := decoder.Decode(&literal); err != nil {
				return nil, uiError("source.expression.literal")
			}
			p.at += int(decoder.InputOffset())
			value = map[string]any{"op": "literal", "value": literal}
		default:
			var err error
			value, err = p.call(name)
			if err != nil {
				return nil, err
			}
		}
	}
	for {
		var field string
		switch {
		case p.consume("."):
			field = p.identifier()
		case p.consume("["):
			p.space()
			decoder := json.NewDecoder(strings.NewReader(p.source[p.at:]))
			if err := decoder.Decode(&field); err != nil {
				return nil, uiError("source.expression.field")
			}
			p.at += int(decoder.InputOffset())
			if !p.consume("]") {
				return nil, uiError("source.expression.field")
			}
		default:
			return value, nil
		}
		if field == "" || !safeUIField(field) {
			return nil, uiError("field.unsafe")
		}
		value = map[string]any{"op": "field", "input": value, "field": field}
	}
}

func (p *uiSourceExpression) call(name string) (any, error) {
	if !slices.Contains(
		[]string{"count", "sum", "min", "max", "avg", "map", "filter", "sort", "take", "dateDiff", "format"},
		name,
	) ||
		!p.consume("(") {
		return nil, uiError("source.expression.function")
	}
	var args []any
	if !p.consume(")") {
		for {
			arg, err := p.expression(0)
			if err != nil {
				return nil, err
			}
			args = append(args, arg)
			if len(args) > 3 {
				return nil, uiError("expression.arity")
			}
			if p.consume(")") {
				break
			}
			if !p.consume(",") {
				return nil, uiError("source.expression.arguments")
			}
		}
	}
	node := map[string]any{"op": name}
	switch name {
	case "count", "sum", "min", "max", "avg":
		if len(args) != 1 {
			return nil, uiError("expression.arity")
		}
		node["input"] = args[0]
	case "map", "filter", "sort":
		if len(args) != 2 && (name != "sort" || len(args) != 3) {
			return nil, uiError("expression.arity")
		}
		node["input"], node["body"] = args[0], args[1]
		if len(args) == 3 {
			literal, ok := args[2].(map[string]any)
			if !ok || literal["op"] != "literal" {
				return nil, uiError("sort.direction")
			}
			node["descending"] = literal["value"]
		}
	case "format":
		if len(args) < 2 || len(args) > 3 {
			return nil, uiError("expression.arity")
		}
		node["input"] = args[0]
		for i, key := range []string{"style", "digits"} {
			if i+1 >= len(args) {
				break
			}
			literal, ok := args[i+1].(map[string]any)
			if !ok || literal["op"] != "literal" {
				return nil, uiError("source.expression.format")
			}
			node[key] = literal["value"]
		}
	default:
		node["args"] = args
	}
	return node, nil
}

func parseSourceAttribute(component, property, source string) (any, int, error) {
	component = canonicalSourceName(component)
	taskFilter := component == "TaskCollection" && property == "filter"
	expression := taskFilter
	if entry, ok := uiSourceComponent(component); ok {
		expression = slices.Contains(entry.Expressions, property)
	}
	decoder := json.NewDecoder(strings.NewReader(source))
	var value any
	err := decoder.Decode(&value)
	if err == nil {
		if taskFilter {
			if _, static := value.(string); static {
				return value, int(decoder.InputOffset()), nil
			}
		}
		if expression {
			node, ok := value.(map[string]any)
			if !ok || node["op"] == nil {
				value = map[string]any{"op": "literal", "value": value}
			}
			if err := ValidateUIExpression(value); err != nil {
				start, end := 0, int(decoder.InputOffset())
				if ok && node["op"] != nil {
					start, end = sourcePropertySpan(
						source[:end],
						value,
						UIPropertyRule{Type: "expression"},
					)
				}
				return nil, 0, sourceExpressionError(err, start, end)
			}
		}
		return value, int(decoder.InputOffset()), nil
	}
	if component == "Scope" && property == "computed" && strings.HasPrefix(source, "{") {
		return parseUISourceDeclarations(source, false)
	}
	if component == "Button" && property == "action" && strings.HasPrefix(source, "{") {
		return parseUISourceDeclarations(source, true)
	}
	if !strings.HasPrefix(source, "{") {
		return nil, 0, err
	}
	if expression {
		value, consumed, err := parseUISourceExpression(source[1:])
		var diagnostic *SourceDiagnostic
		if errors.As(err, &diagnostic) {
			clone := *diagnostic
			clone.Start++
			clone.End++
			err = &clone
		}
		return value, consumed + 1, err
	}
	// Braces around ordinary JSON are accepted for authoring convenience. This
	// branch still cannot evaluate state references or any executable syntax.
	decoder = json.NewDecoder(strings.NewReader(source[1:]))
	if err := decoder.Decode(&value); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			clone := *syntax
			clone.Offset++
			err = &clone
		}
		return nil, 0, err
	}
	at := 1 + int(decoder.InputOffset())
	for at < len(source) && strings.ContainsRune(" \t\r\n", rune(source[at])) {
		at++
	}
	if at >= len(source) || source[at] != '}' {
		return nil, 0, sourceAttributeError(
			"source.attribute.close",
			at,
			at,
			"Close the JSON attribute wrapper with }.",
		)
	}
	return value, at + 1, nil
}

func sourceExpressionError(err error, start, end int) *SourceDiagnostic {
	code := "source.expression"
	var evaluation *UIEvaluationError
	if errors.As(err, &evaluation) {
		code = evaluation.Code
	}
	return sourceAttributeError(
		code,
		start,
		end,
		"Use a bounded expression with declared state/data names and allowlisted operators or functions.",
	)
}

// parseUISourceDeclarations compiles computed maps and action objects. Only
// computed values and an action's value field accept expressions; other action
// fields stay literal JSON. The existing validators enforce the closed shapes.
func parseUISourceDeclarations(source string, action bool) (any, int, error) {
	return parseUISourceDeclarationsLocated(source, action, nil)
}

func parseUISourceDeclarationsLocated(
	source string,
	action bool,
	spans map[string][2]int,
) (value any, consumed int, err error) {
	p := uiSourceExpression{source: source}
	defer func() {
		if err == nil {
			return
		}
		var diagnostic *SourceDiagnostic
		if errors.As(err, &diagnostic) {
			return
		}
		_, width := utf8.DecodeRuneInString(source[p.at:])
		err = sourceExpressionError(err, p.at, min(len(source), p.at+width))
	}()
	if !p.consume("{") {
		return nil, 0, uiError("scope.computed")
	}
	wrapped := p.consume("{")
	declarations := map[string]any{}
	if !p.consume("}") {
		for {
			p.space()
			keyStart := p.at
			decoder := json.NewDecoder(strings.NewReader(source[p.at:]))
			var name string
			if decodeErr := decoder.Decode(&name); decodeErr != nil {
				return nil, 0, uiError("state.definition")
			}
			p.at += int(decoder.InputOffset())
			if !validUIName(name) {
				return nil, 0, sourceExpressionError(uiError("state.definition"), keyStart, p.at)
			}
			if _, exists := declarations[name]; exists {
				return nil, 0, sourceExpressionError(uiError("state.duplicate"), keyStart, p.at)
			}
			if action &&
				!slices.Contains(
					[]string{
						"type",
						"name",
						"value",
						"text",
						"href",
						"entityType",
						"entityId",
						"projectId",
					},
					name,
				) {
				return nil, 0, sourceExpressionError(uiError("action.property"), keyStart, p.at)
			}
			if len(declarations) >= 64 {
				return nil, 0, uiError("scope.computed")
			}
			if !p.consume(":") {
				return nil, 0, uiError("source.computed.separator")
			}
			p.space()
			expressionStart := p.at
			var expression any
			if action && name != "value" {
				decoder = json.NewDecoder(strings.NewReader(source[p.at:]))
				if decodeErr := decoder.Decode(&expression); decodeErr != nil {
					return nil, 0, sourceAttributeError(
						"source.action.literal",
						expressionStart,
						min(len(source), expressionStart+1),
						"Use literal JSON for action properties other than value.",
					)
				}
				p.at += int(decoder.InputOffset())
			} else {
				// An explicit AST is distinguishable from an object literal by its op.
				decoder = json.NewDecoder(strings.NewReader(source[p.at:]))
				var candidate map[string]any
				if decoder.Decode(&candidate) == nil && candidate["op"] != nil {
					expression = candidate
					p.at += int(decoder.InputOffset())
				} else {
					expression, err = p.expression(0)
					if err != nil {
						return nil, 0, err
					}
				}
				if validation := ValidateUIExpression(expression); validation != nil {
					start, end := sourcePropertySpan(
						source[expressionStart:p.at],
						expression,
						UIPropertyRule{Type: "expression"},
					)
					return nil, 0, sourceExpressionError(
						validation,
						expressionStart+start,
						expressionStart+end,
					)
				}
			}
			if spans != nil {
				spans[name] = [2]int{expressionStart, p.at}
			}
			declarations[name] = expression
			if p.consume("}") {
				break
			}
			if !p.consume(",") {
				return nil, 0, uiError("source.computed.separator")
			}
		}
	}
	if wrapped && !p.consume("}") {
		return nil, 0, uiError("source.attribute.close")
	}
	if action {
		if validation := validateUIAction(declarations); validation != nil {
			return nil, 0, sourceExpressionError(validation, 0, p.at)
		}
	}
	return declarations, p.at, nil
}
