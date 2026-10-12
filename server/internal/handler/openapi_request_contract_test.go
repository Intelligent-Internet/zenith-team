package handler

import (
	"fmt"
	"go/ast"
	"go/types"
	"reflect"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
)

// The request direction of the OpenAPI contract. The response direction is
// pinned by openapi_response_contract_test.go; this file walks the same
// handler source for every bindRequest[T] a mutating route reaches and holds
// T's JSON shape against the operation's requestBody schema. The stakes are
// higher than for responses: the binder rejects unknown fields, so a property
// the spec declares but T does not is not ignored — the generated desktop
// client sends it and every call answers 400.
//
// What is compared, per body: the JSON key set at every object level in both
// directions, the Go binding's bare `required` rules against the schema's
// required list, and the JSON type class (object/array/string/number/boolean)
// of each leaf. Formats, enums, and value bounds stay with the validator and
// the response-direction machinery.
//
// A field the spec marks required that the Go binding does not is accepted:
// requiredness beyond the bare tag (min=1 on a revision, cross-field rules)
// is not statically decidable, and a client forced to send a field the server
// tolerates is not a runtime failure.

func TestOpenAPIRequestBodiesMatchHandlerWireTypes(t *testing.T) {
	t.Parallel()

	source := loadHandlerSource(t)
	contract := loadOpenAPIResponseContract(t)
	routes := New(fullyWiredHandler()).router.Routes()
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Method+routes[i].Path < routes[j].Method+routes[j].Path
	})

	compared := 0
	for _, route := range routes {
		if !requestBodyMethod(route.Method) {
			continue
		}
		name := route.Method + " " + route.Path
		if responseContractExemptRoute(name) {
			continue
		}

		method, ok := handlerMethodName(route.Handler)
		if !ok {
			t.Errorf("%s is not registered with a Handler method: %s", name, route.Handler)
			continue
		}
		function := source.functions[method]
		if function == nil {
			t.Errorf("%s uses %s, whose source declaration was not found", name, method)
			continue
		}

		bindings := source.collectRequestBindings(function, nil, nil)
		schema, hasJSONBody := contract.jsonRequestBody(name)

		if len(bindings) == 0 {
			if hasJSONBody && rawSignedJSONRequestRoute(name) {
				// Signature verification must see the exact provider bytes, so
				// this one route intentionally cannot pass through bindRequest.
				continue
			}
			if hasJSONBody {
				t.Errorf(
					"%s declares an application/json request body but the handler binds none",
					name,
				)
			}
			continue
		}
		if !hasJSONBody {
			// A conditional empty-object bind pins "no body may carry state";
			// an operation that declares no body agrees with it.
			if allBindingsEmpty(bindings) {
				continue
			}
			t.Errorf(
				"%s binds a request body (%s) but the spec declares no application/json request body",
				name,
				requestBindingNames(bindings),
			)
			continue
		}
		compared++
		for _, binding := range bindings {
			for _, mismatch := range compareGoRequestShape(source.pkg, binding, schema, "$", nil) {
				t.Errorf("%s request body: %s", name, mismatch)
			}
		}
	}
	// The two discovery halves cover each other's total failure, but not a
	// shared one. Dozens of routes bind bodies today; far fewer compared
	// pairs means the discovery machinery has rotted, not the API.
	if compared < 25 {
		t.Errorf(
			"compared only %d request bodies against the spec; the discovery machinery has lost the routes",
			compared,
		)
	}
}

func rawSignedJSONRequestRoute(route string) bool {
	return route == "POST /api/v1/webhooks/resend/mailbox" ||
		route == "POST /api/v1/webhooks/stripe/commerce"
}

func requestBodyMethod(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

func requestBindingNames(bindings []types.Type) string {
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, types.TypeString(binding, packagePathQualifier))
	}
	return strings.Join(names, ", ")
}

func allBindingsEmpty(bindings []types.Type) bool {
	for _, binding := range bindings {
		structure, ok := types.Unalias(binding).Underlying().(*types.Struct)
		if !ok || len(goRequestJSONFields(structure)) != 0 {
			return false
		}
	}
	return true
}

// jsonRequestBody resolves a route's requestBody down to its normalized
// application/json schema. hasJSON is false when the operation declares no
// request body at all, or only non-JSON media (multipart uploads).
func (c *openAPIResponseContract) jsonRequestBody(route string) (schema *contractSchema, hasJSON bool) {
	c.t.Helper()

	operation := c.operation(route)
	bodyNode := mappingValue(c.t, operation.node, "requestBody")
	if bodyNode == nil {
		return nil, false
	}
	located := c.resolve(locatedYAML{file: operation.file, node: bodyNode}, 0)
	content := mappingValue(c.t, located.node, "content")
	if content == nil {
		return nil, false
	}
	if content.Kind != yaml.MappingNode {
		c.t.Fatalf("%s request body content is not a mapping", route)
	}
	for index := 0; index < len(content.Content); index += 2 {
		if content.Content[index].Value != "application/json" {
			continue
		}
		schemaNode := mappingValue(c.t, content.Content[index+1], "schema")
		if schemaNode == nil {
			c.t.Fatalf("%s request body application/json has no schema", route)
		}
		return c.normalizeSchema(locatedYAML{file: located.file, node: schemaNode}, 0), true
	}
	return nil, false
}

// collectRequestBindings walks a handler function and everything it calls in
// this package, returning the type argument of every reachable bindRequest[T].
// typeArgs substitutes the enclosing function's type parameters, so a generic
// middleman like decodeAuthJSON[T] reports its caller's concrete type.
func (s handlerSource) collectRequestBindings(
	function *ast.FuncDecl,
	typeArgs map[*types.TypeParam]types.Type,
	stack map[string]bool,
) []types.Type {
	if stack == nil {
		stack = make(map[string]bool)
	}
	if stack[function.Name.Name] {
		return nil
	}
	nextStack := make(map[string]bool, len(stack)+1)
	for name, active := range stack {
		nextStack[name] = active
	}
	nextStack[function.Name.Name] = true

	var bindings []types.Type
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee := call.Fun
		switch instantiated := callee.(type) {
		case *ast.IndexExpr:
			callee = instantiated.X
		case *ast.IndexListExpr:
			callee = instantiated.X
		}

		switch callee := callee.(type) {
		case *ast.Ident:
			object := s.pkg.TypesInfo.ObjectOf(callee)
			if object == nil || object.Pkg() != s.pkg.Types {
				return true
			}
			if callee.Name == "bindRequest" {
				instance, ok := s.pkg.TypesInfo.Instances[callee]
				if !ok || instance.TypeArgs.Len() != 1 {
					return true
				}
				bindings = append(
					bindings,
					substituteTypeParameter(instance.TypeArgs.At(0), typeArgs),
				)
				return true
			}
			called := s.functions[callee.Name]
			if called == nil {
				return true
			}
			bindings = append(bindings, s.collectRequestBindings(
				called,
				s.calleeTypeArguments(callee, object, typeArgs),
				nextStack,
			)...)
		case *ast.SelectorExpr:
			called := s.handlerMethod(callee)
			if called == nil {
				return true
			}
			bindings = append(bindings, s.collectRequestBindings(called, nil, nextStack)...)
		}
		return true
	})
	return deduplicateTypes(bindings)
}

// calleeTypeArguments maps a generic callee's type parameters to the concrete
// types this call site instantiates them with.
func (s handlerSource) calleeTypeArguments(
	callee *ast.Ident,
	object types.Object,
	callerArgs map[*types.TypeParam]types.Type,
) map[*types.TypeParam]types.Type {
	instance, ok := s.pkg.TypesInfo.Instances[callee]
	if !ok {
		return nil
	}
	signature, ok := object.Type().(*types.Signature)
	if !ok {
		return nil
	}
	parameters := signature.TypeParams()
	if parameters == nil || parameters.Len() != instance.TypeArgs.Len() {
		return nil
	}
	arguments := make(map[*types.TypeParam]types.Type, parameters.Len())
	for index := range parameters.Len() {
		arguments[parameters.At(index)] = substituteTypeParameter(
			instance.TypeArgs.At(index),
			callerArgs,
		)
	}
	return arguments
}

func substituteTypeParameter(
	value types.Type,
	typeArgs map[*types.TypeParam]types.Type,
) types.Type {
	if parameter, ok := types.Unalias(value).(*types.TypeParam); ok {
		if concrete, exists := typeArgs[parameter]; exists {
			return concrete
		}
	}
	return value
}

func deduplicateTypes(values []types.Type) []types.Type {
	seen := make(map[string]bool, len(values))
	result := make([]types.Type, 0, len(values))
	for _, value := range values {
		key := types.TypeString(value, packagePathQualifier)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

type goRequestField struct {
	name     string
	value    types.Type
	required bool
}

// goRequestJSONFields lists a request DTO's JSON fields with the requiredness
// its binding tag declares. Only the bare `required` rule counts, mirroring
// what bindRequest itself enforces unconditionally.
func goRequestJSONFields(value *types.Struct) []goRequestField {
	var fields []goRequestField
	for index := range value.NumFields() {
		field := value.Field(index)
		tags := reflect.StructTag(value.Tag(index))
		name, _, _ := strings.Cut(tags.Get("json"), ",")
		if name == "-" {
			continue
		}
		if field.Anonymous() && name == "" {
			embedded := types.Unalias(field.Type())
			if pointer, ok := embedded.(*types.Pointer); ok {
				embedded = types.Unalias(pointer.Elem())
			}
			if structure, ok := embedded.Underlying().(*types.Struct); ok {
				fields = append(fields, goRequestJSONFields(structure)...)
				continue
			}
		}
		if !field.Exported() {
			continue
		}
		if name == "" {
			name = field.Name()
		}
		fields = append(fields, goRequestField{
			name:     name,
			value:    field.Type(),
			required: declaresRequired(tags.Get("binding")),
		})
	}
	return fields
}

// compareGoRequestShape holds a bound Go request type against the normalized
// requestBody schema: key sets in both directions, bare-required rules, and
// the JSON type class of each leaf.
func compareGoRequestShape(
	pkg *packages.Package,
	value types.Type,
	schema *contractSchema,
	path string,
	seen map[string]bool,
) []string {
	if value == nil {
		return []string{path + " has no statically known Go type"}
	}
	if schema == nil || schemaAcceptsAnyJSON(schema) {
		return nil
	}
	if seen == nil {
		seen = make(map[string]bool)
	}
	key := types.TypeString(value, packagePathQualifier) + ":" + fmt.Sprintf("%p", schema)
	if seen[key] {
		return nil
	}
	seen[key] = true
	defer delete(seen, key)

	value = types.Unalias(value)
	// A pointer or an optional[T] wrapper is the binder's "may be absent"
	// shape; requiredness is judged at the field, so both unwrap here. The
	// schema drops its null branch alongside: a client that omits the field
	// and one that may not send null agree.
	if pointer, ok := value.(*types.Pointer); ok {
		return compareGoRequestShape(pkg, pointer.Elem(), schemaWithoutNull(schema), path, seen)
	}
	if element, ok := optionalTypeArgument(pkg, value); ok {
		return compareGoRequestShape(pkg, element, schemaWithoutNull(schema), path, seen)
	}

	if len(schema.alternatives) > 0 {
		return compareGoRequestAlternatives(pkg, value, schema, path, seen)
	}

	if named, ok := value.(*types.Named); ok {
		if isNamedType(named, "time", "Time") ||
			isNamedType(named, "github.com/google/uuid", "UUID") ||
			isNamedType(named, "github.com/shopspring/decimal", "Decimal") {
			return compareRequestTypeClass(schema, path, "string")
		}
		if isJSONRawMessage(named) {
			// The opaque-document seam: its owning domain validates the
			// document, and the schema stays free-form or domain-shaped.
			return nil
		}
	}

	switch underlying := value.Underlying().(type) {
	case *types.Struct:
		return compareGoRequestStruct(pkg, underlying, schema, path, seen)
	case *types.Slice:
		if basic, ok := types.Unalias(underlying.Elem()).Underlying().(*types.Basic); ok &&
			basic.Kind() == types.Byte {
			return compareRequestTypeClass(schema, path, "string")
		}
		return compareGoRequestArray(pkg, underlying.Elem(), schema, path, seen)
	case *types.Array:
		return compareGoRequestArray(pkg, underlying.Elem(), schema, path, seen)
	case *types.Map:
		return compareGoRequestMap(pkg, underlying, schema, path, seen)
	case *types.Interface:
		return nil
	case *types.Basic:
		expectedType, _ := basicJSONSchema(underlying)
		if expectedType == "" {
			return []string{fmt.Sprintf("%s uses unsupported Go request type %s", path, value)}
		}
		return compareRequestTypeClass(schema, path, expectedType)
	default:
		return []string{fmt.Sprintf("%s uses unsupported Go request type %s", path, value)}
	}
}

func compareGoRequestAlternatives(
	pkg *packages.Package,
	value types.Type,
	schema *contractSchema,
	path string,
	seen map[string]bool,
) []string {
	base := cloneContractSchema(schema)
	base.alternatives = nil
	var closest []string
	for _, alternative := range schema.alternatives {
		if schemaIsPureNull(alternative) {
			continue
		}
		merged := mergeContractSchemas(base, alternative)
		mismatches := compareGoRequestShape(pkg, value, merged, path, seen)
		if len(mismatches) == 0 {
			return nil
		}
		if closest == nil || len(mismatches) < len(closest) {
			closest = mismatches
		}
	}
	// A strict request struct can represent the allowed key union while a
	// downstream discriminator check enforces which branch-specific fields
	// travel together. This is the schedule-trigger shape: the binder owns the
	// complete key/type surface and parseScheduleTrigger owns the cross-field
	// once-vs-cron invariant. First trying each branch above preserves the
	// ordinary case where a concrete binding represents exactly one variant.
	if _, ok := types.Unalias(value).Underlying().(*types.Struct); ok {
		union := mergeRequestAlternativeShapes(base, schema.alternatives)
		if mismatches := compareGoRequestShape(pkg, value, union, path, seen); len(mismatches) == 0 {
			return nil
		}
	}
	if closest == nil {
		return []string{path + " has no non-null OpenAPI request alternative"}
	}
	return []string{fmt.Sprintf(
		"%s (%s) matches no OpenAPI oneOf/anyOf branch; closest mismatch: %s",
		path,
		types.TypeString(value, packagePathQualifier),
		strings.Join(closest, "; "),
	)}
}

func mergeRequestAlternativeShapes(
	base *contractSchema,
	alternatives []*contractSchema,
) *contractSchema {
	result := newContractSchema()
	first := true
	for _, alternative := range alternatives {
		if schemaIsPureNull(alternative) {
			continue
		}
		branch := mergeContractSchemas(cloneContractSchema(base), alternative)
		for schemaType := range branch.types {
			result.types[schemaType] = true
		}
		for name, property := range branch.properties {
			if existing := result.properties[name]; existing != nil {
				combined := newContractSchema()
				combined.alternatives = []*contractSchema{existing, property}
				result.properties[name] = combined
			} else {
				result.properties[name] = property
			}
		}
		if first {
			for name := range branch.required {
				result.required[name] = true
			}
			first = false
		} else {
			for name := range result.required {
				if !branch.required[name] {
					delete(result.required, name)
				}
			}
		}
		result.hasAdditionalContract = result.hasAdditionalContract || branch.hasAdditionalContract
		result.allowsAdditional = result.allowsAdditional || branch.allowsAdditional
	}
	return result
}

func compareGoRequestStruct(
	pkg *packages.Package,
	value *types.Struct,
	schema *contractSchema,
	path string,
	seen map[string]bool,
) []string {
	mismatches := compareRequestTypeClass(schema, path, "object")
	if schemaIsFreeFormObject(schema) {
		return mismatches
	}
	fields := goRequestJSONFields(value)
	fieldByName := make(map[string]goRequestField, len(fields))
	for _, field := range fields {
		fieldByName[field.name] = field
		fieldPath := path + "." + field.name
		property := schema.properties[field.name]
		if property == nil {
			if !schema.allowsAdditional {
				mismatches = append(
					mismatches,
					fieldPath+" exists in the Go binding but not in the OpenAPI requestBody",
				)
			} else if schema.additionalProperties != nil {
				mismatches = append(mismatches, compareGoRequestShape(
					pkg,
					field.value,
					schema.additionalProperties,
					fieldPath,
					seen,
				)...)
			}
			continue
		}
		if field.required && !schema.required[field.name] {
			mismatches = append(
				mismatches,
				fieldPath+" is required by the server binding but optional in OpenAPI",
			)
		}
		mismatches = append(
			mismatches,
			compareGoRequestShape(pkg, field.value, property, fieldPath, seen)...,
		)
	}
	for _, name := range sortedKeys(propertyNameSet(schema)) {
		if _, exists := fieldByName[name]; !exists {
			mismatches = append(mismatches, fmt.Sprintf(
				"%s.%s exists in the OpenAPI requestBody but the strict binding rejects it as an unknown field",
				path,
				name,
			))
		}
	}
	return mismatches
}

func propertyNameSet(schema *contractSchema) map[string]bool {
	names := make(map[string]bool, len(schema.properties))
	for name := range schema.properties {
		names[name] = true
	}
	return names
}

func compareGoRequestArray(
	pkg *packages.Package,
	element types.Type,
	schema *contractSchema,
	path string,
	seen map[string]bool,
) []string {
	mismatches := compareRequestTypeClass(schema, path, "array")
	if schema.items == nil {
		if !schemaIsEmpty(schema) && len(schema.types) > 0 {
			mismatches = append(
				mismatches,
				path+" is an array in Go but OpenAPI declares no items schema",
			)
		}
		return mismatches
	}
	return append(
		mismatches,
		compareGoRequestShape(pkg, element, schema.items, path+"[]", seen)...,
	)
}

func compareGoRequestMap(
	pkg *packages.Package,
	value *types.Map,
	schema *contractSchema,
	path string,
	seen map[string]bool,
) []string {
	mismatches := compareRequestTypeClass(schema, path, "object")
	key, ok := types.Unalias(value.Key()).Underlying().(*types.Basic)
	if !ok || key.Info()&types.IsString == 0 {
		return append(mismatches, fmt.Sprintf("%s has non-string Go map key %s", path, value.Key()))
	}
	if len(schema.properties) > 0 {
		mismatches = append(
			mismatches,
			path+" is a dynamic Go map but OpenAPI declares fixed properties",
		)
	}
	if schema.hasAdditionalContract && !schema.allowsAdditional {
		return append(mismatches, path+" is a Go map but OpenAPI forbids additionalProperties")
	}
	if schema.additionalProperties == nil {
		return mismatches
	}
	return append(mismatches, compareGoRequestShape(
		pkg,
		value.Elem(),
		schema.additionalProperties,
		path+"[*]",
		seen,
	)...)
}

// compareRequestTypeClass checks only the JSON type class; formats and value
// bounds belong to the validator and the response-direction machinery.
func compareRequestTypeClass(schema *contractSchema, path, expectedType string) []string {
	if len(schema.types) == 0 || schema.types[expectedType] {
		return nil
	}
	if expectedType == "number" && schema.types["integer"] {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s is %s in Go but %s in OpenAPI",
		path,
		expectedType,
		strings.Join(sortedKeys(schema.types), "|"),
	)}
}

// optionalTypeArgument unwraps this package's optional[T] presence wrapper.
func optionalTypeArgument(pkg *packages.Package, value types.Type) (types.Type, bool) {
	named, ok := value.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() != pkg.Types ||
		named.Obj().Name() != "optional" {
		return nil, false
	}
	if named.TypeArgs().Len() != 1 {
		return nil, false
	}
	return named.TypeArgs().At(0), true
}
