package handler

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
)

type handlerSource struct {
	pkg       *packages.Package
	functions map[string]*ast.FuncDecl
}

type successWrite struct {
	status   int
	body     types.Type
	position token.Position
}

type statusBindings map[*types.Var][]int

type locatedYAML struct {
	file string
	node *yaml.Node
}

type responseContract struct {
	description string
	content     map[string]*contractSchema
}

type contractSchema struct {
	types                 map[string]bool
	format                string
	properties            map[string]*contractSchema
	required              map[string]bool
	items                 *contractSchema
	additionalProperties  *contractSchema
	allowsAdditional      bool
	hasAdditionalContract bool
	enum                  []string
	constant              *string
	alternatives          []*contractSchema
}

type openAPIResponseContract struct {
	t           *testing.T
	documents   map[string]*yaml.Node
	root        locatedYAML
	resolving   map[string]*contractSchema
	maxRefDepth int
}

func TestOpenAPISuccessResponsesUseTypedBodies(t *testing.T) {
	t.Parallel()

	source := loadHandlerSource(t)
	routes := New(fullyWiredHandler()).router.Routes()
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Method+routes[i].Path < routes[j].Method+routes[j].Path
	})

	for _, route := range routes {
		name := route.Method + " " + route.Path
		if responseContractExemptRoute(name) || name == "GET /metrics" {
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

		writes := source.collectSuccessWrites(function, nil, nil)
		if len(writes) == 0 {
			t.Errorf("%s (%s) has no statically discoverable success response", name, method)
			continue
		}
		for _, write := range writes {
			if write.body == nil || !isUntypedJSONRoot(write.body) {
				continue
			}
			t.Errorf(
				"%s returns an untyped success body at %s; use a named response DTO, got %s",
				name,
				write.position,
				write.body,
			)
		}
	}
}

func TestOpenAPISuccessResponsesMatchHandlerWireTypes(t *testing.T) {
	t.Parallel()

	source := loadHandlerSource(t)
	contract := loadOpenAPIResponseContract(t)
	routes := New(fullyWiredHandler()).router.Routes()
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].Method+routes[i].Path < routes[j].Method+routes[j].Path
	})

	for _, route := range routes {
		name := route.Method + " " + route.Path
		if responseContractExemptRoute(name) {
			continue
		}
		if name == "GET /metrics" {
			contract.checkMetricsResponse(name)
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
		writes := source.collectSuccessWrites(function, nil, nil)
		if len(writes) == 0 {
			t.Errorf("%s (%s) has no statically discoverable success response", name, method)
			continue
		}

		responses := contract.successResponses(name)
		compareSuccessStatuses(t, name, writes, responses)
		for _, write := range writes {
			response, exists := responses[write.status]
			if !exists {
				continue
			}
			if write.status == 204 {
				continue
			}
			if write.body == nil {
				if len(response.content) > 0 {
					t.Errorf("%s declares a body for bodyless status %d", name, write.status)
				}
				continue
			}
			schema := response.content["application/json"]
			if schema == nil {
				t.Errorf(
					"%s status %d writes JSON but the spec declares no application/json schema",
					name,
					write.status,
				)
				continue
			}
			for _, mismatch := range compareGoWireType(source.pkg, write.body, schema, "$", false, nil) {
				t.Errorf("%s status %d at %s: %s", name, write.status, write.position, mismatch)
			}
		}
	}
}

func loadOpenAPIResponseContract(t *testing.T) *openAPIResponseContract {
	t.Helper()

	rootPath, err := filepath.Abs(specRelativePath)
	if err != nil {
		t.Fatalf("resolve OpenAPI root: %v", err)
	}
	contract := &openAPIResponseContract{
		t:           t,
		documents:   make(map[string]*yaml.Node),
		resolving:   make(map[string]*contractSchema),
		maxRefDepth: 100,
	}
	contract.root = locatedYAML{file: rootPath, node: contract.loadDocument(rootPath)}
	return contract
}

func (c *openAPIResponseContract) loadDocument(path string) *yaml.Node {
	c.t.Helper()

	path = filepath.Clean(path)
	if document := c.documents[path]; document != nil {
		return document
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		c.t.Fatalf("read OpenAPI document %s: %v", path, err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		c.t.Fatalf("parse OpenAPI document %s: %v", path, err)
	}
	if len(document.Content) != 1 {
		c.t.Fatalf("parse OpenAPI document %s: got %d roots, want 1", path, len(document.Content))
	}
	c.documents[path] = document.Content[0]
	return document.Content[0]
}

// operation resolves a registered route to its OpenAPI operation mapping,
// carrying the file it was found in so relative $refs keep resolving.
func (c *openAPIResponseContract) operation(route string) locatedYAML {
	c.t.Helper()

	method, path, found := strings.Cut(routeToOpenAPIPath(route), " ")
	if !found {
		c.t.Fatalf("parse registered route %q", route)
	}
	paths := mappingValue(c.t, c.root.node, "paths")
	pathItem := mappingValue(c.t, paths, path)
	if pathItem == nil {
		c.t.Fatalf("%s has no OpenAPI path item", route)
	}
	resolvedPath := c.resolve(locatedYAML{file: c.root.file, node: pathItem}, 0)
	operation := mappingValue(c.t, resolvedPath.node, strings.ToLower(method))
	if operation == nil {
		c.t.Fatalf("%s has no OpenAPI operation", route)
	}
	return locatedYAML{file: resolvedPath.file, node: operation}
}

func (c *openAPIResponseContract) successResponses(route string) map[int]responseContract {
	c.t.Helper()

	operation := c.operation(route)
	responsesNode := mappingValue(c.t, operation.node, "responses")
	if responsesNode == nil || responsesNode.Kind != yaml.MappingNode {
		c.t.Fatalf("%s has no OpenAPI responses", route)
	}

	responses := make(map[int]responseContract)
	for index := 0; index < len(responsesNode.Content); index += 2 {
		statusNode := responsesNode.Content[index]
		status, err := strconv.Atoi(statusNode.Value)
		if err != nil || status < 200 || status >= 400 {
			continue
		}
		locatedResponse := c.resolve(locatedYAML{
			file: operation.file,
			node: responsesNode.Content[index+1],
		}, 0)
		response := responseContract{content: make(map[string]*contractSchema)}
		if description := mappingValue(c.t, locatedResponse.node, "description"); description != nil {
			response.description = description.Value
		}
		content := mappingValue(c.t, locatedResponse.node, "content")
		if content != nil {
			if content.Kind != yaml.MappingNode {
				c.t.Fatalf("%s status %d content is not a mapping", route, status)
			}
			for contentIndex := 0; contentIndex < len(content.Content); contentIndex += 2 {
				mediaType := content.Content[contentIndex].Value
				media := content.Content[contentIndex+1]
				schemaNode := mappingValue(c.t, media, "schema")
				if schemaNode == nil {
					c.t.Fatalf("%s status %d %s has no schema", route, status, mediaType)
				}
				response.content[mediaType] = c.normalizeSchema(locatedYAML{
					file: locatedResponse.file,
					node: schemaNode,
				}, 0)
			}
		}
		responses[status] = response
	}
	return responses
}

func (c *openAPIResponseContract) resolve(value locatedYAML, depth int) locatedYAML {
	c.t.Helper()

	if depth > c.maxRefDepth {
		c.t.Fatalf("OpenAPI reference depth exceeded %d at %s", c.maxRefDepth, value.file)
	}
	reference := mappingValue(c.t, value.node, "$ref")
	if reference == nil {
		return value
	}
	return c.referenceTarget(value.file, reference.Value, depth+1)
}

func (c *openAPIResponseContract) referenceTarget(file, reference string, depth int) locatedYAML {
	c.t.Helper()

	filePart, fragment, _ := strings.Cut(reference, "#")
	targetFile := file
	if filePart != "" {
		targetFile = filepath.Join(filepath.Dir(file), filepath.FromSlash(filePart))
	}
	targetFile = filepath.Clean(targetFile)
	target := c.loadDocument(targetFile)
	if fragment != "" {
		for _, rawPart := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
			part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
			target = mappingValue(c.t, target, part)
			if target == nil {
				c.t.Fatalf("OpenAPI reference %q from %s has no %q segment", reference, file, part)
			}
		}
	}
	resolved := locatedYAML{file: targetFile, node: target}
	if mappingValue(c.t, target, "$ref") != nil {
		return c.resolve(resolved, depth)
	}
	return resolved
}

func (c *openAPIResponseContract) normalizeSchema(value locatedYAML, depth int) *contractSchema {
	c.t.Helper()

	if depth > c.maxRefDepth {
		c.t.Fatalf("OpenAPI schema depth exceeded %d at %s", c.maxRefDepth, value.file)
	}
	if value.node == nil || value.node.Kind != yaml.MappingNode {
		c.t.Fatalf("OpenAPI schema in %s is not a mapping", value.file)
	}

	result := newContractSchema()
	if reference := mappingValue(c.t, value.node, "$ref"); reference != nil {
		key := value.file + "#" + reference.Value
		if resolving := c.resolving[key]; resolving != nil {
			return resolving
		}
		// Keep the same schema identity at recursive references. The Go/schema
		// pair guard can then stop a cycle without discarding nested fields.
		c.resolving[key] = result
		target := c.referenceTarget(value.file, reference.Value, depth+1)
		*result = *mergeContractSchemas(result, c.normalizeSchema(target, depth+1))
		delete(c.resolving, key)
	}

	if allOf := mappingValue(c.t, value.node, "allOf"); allOf != nil {
		if allOf.Kind != yaml.SequenceNode {
			c.t.Fatalf("OpenAPI allOf in %s is not a sequence", value.file)
		}
		for _, part := range allOf.Content {
			*result = *mergeContractSchemas(result, c.normalizeSchema(locatedYAML{
				file: value.file,
				node: part,
			}, depth+1))
		}
	}
	*result = *mergeContractSchemas(result, c.normalizeDirectSchema(value, depth))
	return result
}

func (c *openAPIResponseContract) normalizeDirectSchema(
	value locatedYAML,
	depth int,
) *contractSchema {
	c.t.Helper()

	result := newContractSchema()
	if typeNode := mappingValue(c.t, value.node, "type"); typeNode != nil {
		switch typeNode.Kind {
		case yaml.ScalarNode:
			result.types[typeNode.Value] = true
		case yaml.SequenceNode:
			for _, item := range typeNode.Content {
				result.types[item.Value] = true
			}
		default:
			c.t.Fatalf("OpenAPI schema type in %s is neither a scalar nor sequence", value.file)
		}
	}
	if format := mappingValue(c.t, value.node, "format"); format != nil {
		result.format = format.Value
	}
	if required := mappingValue(c.t, value.node, "required"); required != nil {
		if required.Kind != yaml.SequenceNode {
			c.t.Fatalf("OpenAPI required in %s is not a sequence", value.file)
		}
		for _, item := range required.Content {
			result.required[item.Value] = true
		}
	}
	if properties := mappingValue(c.t, value.node, "properties"); properties != nil {
		if properties.Kind != yaml.MappingNode {
			c.t.Fatalf("OpenAPI properties in %s is not a mapping", value.file)
		}
		for index := 0; index < len(properties.Content); index += 2 {
			name := properties.Content[index].Value
			result.properties[name] = c.normalizeSchema(locatedYAML{
				file: value.file,
				node: properties.Content[index+1],
			}, depth+1)
		}
	}
	if items := mappingValue(c.t, value.node, "items"); items != nil {
		result.items = c.normalizeSchema(locatedYAML{file: value.file, node: items}, depth+1)
	}
	if additional := mappingValue(c.t, value.node, "additionalProperties"); additional != nil {
		result.hasAdditionalContract = true
		switch additional.Kind {
		case yaml.ScalarNode:
			allowed, err := strconv.ParseBool(additional.Value)
			if err != nil {
				c.t.Fatalf("OpenAPI additionalProperties in %s is not boolean", value.file)
			}
			result.allowsAdditional = allowed
		case yaml.MappingNode:
			result.allowsAdditional = true
			result.additionalProperties = c.normalizeSchema(
				locatedYAML{file: value.file, node: additional},
				depth+1,
			)
		default:
			c.t.Fatalf("OpenAPI additionalProperties in %s is invalid", value.file)
		}
	}
	if enum := mappingValue(c.t, value.node, "enum"); enum != nil {
		if enum.Kind != yaml.SequenceNode {
			c.t.Fatalf("OpenAPI enum in %s is not a sequence", value.file)
		}
		for _, item := range enum.Content {
			result.enum = append(result.enum, item.Value)
		}
		sort.Strings(result.enum)
	}
	if constantNode := mappingValue(c.t, value.node, "const"); constantNode != nil {
		constantValue := constantNode.Value
		result.constant = &constantValue
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		alternatives := mappingValue(c.t, value.node, keyword)
		if alternatives == nil {
			continue
		}
		if alternatives.Kind != yaml.SequenceNode {
			c.t.Fatalf("OpenAPI %s in %s is not a sequence", keyword, value.file)
		}
		for _, alternative := range alternatives.Content {
			result.alternatives = append(result.alternatives, c.normalizeSchema(locatedYAML{
				file: value.file,
				node: alternative,
			}, depth+1))
		}
	}
	return result
}

func newContractSchema() *contractSchema {
	return &contractSchema{
		types:      make(map[string]bool),
		properties: make(map[string]*contractSchema),
		required:   make(map[string]bool),
	}
}

func mergeContractSchemas(left, right *contractSchema) *contractSchema {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	if len(left.alternatives) > 0 && !schemaIsEmpty(right) {
		merged := newContractSchema()
		for _, alternative := range left.alternatives {
			merged.alternatives = append(merged.alternatives, mergeContractSchemas(alternative, right))
		}
		return merged
	}
	if len(right.alternatives) > 0 && !schemaIsEmpty(left) {
		merged := newContractSchema()
		for _, alternative := range right.alternatives {
			merged.alternatives = append(merged.alternatives, mergeContractSchemas(left, alternative))
		}
		return merged
	}

	merged := newContractSchema()
	merged.format = left.format
	if merged.format == "" {
		merged.format = right.format
	}
	for name := range left.types {
		merged.types[name] = true
	}
	if len(merged.types) == 0 {
		for name := range right.types {
			merged.types[name] = true
		}
	} else if len(right.types) > 0 {
		for name := range merged.types {
			if !right.types[name] {
				delete(merged.types, name)
			}
		}
	}
	for name, property := range left.properties {
		merged.properties[name] = property
	}
	for name, property := range right.properties {
		if existing := merged.properties[name]; existing != nil {
			merged.properties[name] = mergeContractSchemas(existing, property)
		} else {
			merged.properties[name] = property
		}
	}
	for name := range left.required {
		merged.required[name] = true
	}
	for name := range right.required {
		merged.required[name] = true
	}
	merged.items = left.items
	if merged.items == nil {
		merged.items = right.items
	} else if right.items != nil {
		merged.items = mergeContractSchemas(merged.items, right.items)
	}
	merged.hasAdditionalContract = left.hasAdditionalContract || right.hasAdditionalContract
	switch {
	case left.hasAdditionalContract && right.hasAdditionalContract:
		merged.allowsAdditional = left.allowsAdditional && right.allowsAdditional
	case left.hasAdditionalContract:
		merged.allowsAdditional = left.allowsAdditional
	case right.hasAdditionalContract:
		merged.allowsAdditional = right.allowsAdditional
	}
	switch {
	case left.additionalProperties != nil && right.additionalProperties != nil:
		merged.additionalProperties = mergeContractSchemas(
			left.additionalProperties,
			right.additionalProperties,
		)
	case left.additionalProperties != nil:
		merged.additionalProperties = left.additionalProperties
	default:
		merged.additionalProperties = right.additionalProperties
	}
	merged.enum = intersectOrTake(left.enum, right.enum)
	merged.constant = left.constant
	if merged.constant == nil {
		merged.constant = right.constant
	}
	merged.alternatives = append(merged.alternatives, left.alternatives...)
	merged.alternatives = append(merged.alternatives, right.alternatives...)
	return merged
}

func schemaIsEmpty(schema *contractSchema) bool {
	return schema == nil || len(schema.types) == 0 && schema.format == "" && len(schema.properties) == 0 &&
		len(schema.required) == 0 && schema.items == nil && !schema.hasAdditionalContract &&
		len(schema.enum) == 0 && schema.constant == nil && len(schema.alternatives) == 0
}

func intersectOrTake(left, right []string) []string {
	if len(left) == 0 {
		return append([]string(nil), right...)
	}
	if len(right) == 0 {
		return append([]string(nil), left...)
	}
	rightSet := make(map[string]bool, len(right))
	for _, value := range right {
		rightSet[value] = true
	}
	var shared []string
	for _, value := range left {
		if rightSet[value] {
			shared = append(shared, value)
		}
	}
	return shared
}

func mappingValue(t *testing.T, mapping *yaml.Node, key string) *yaml.Node {
	t.Helper()
	if mapping == nil {
		return nil
	}
	if mapping.Kind != yaml.MappingNode {
		t.Fatalf("YAML node containing %q is not a mapping", key)
	}
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func compareSuccessStatuses(
	t *testing.T,
	route string,
	writes []successWrite,
	responses map[int]responseContract,
) {
	t.Helper()

	implemented := make(map[int]bool)
	for _, write := range writes {
		implemented[write.status] = true
	}
	for status := range implemented {
		if _, exists := responses[status]; !exists {
			t.Errorf("%s implements undocumented success status %d", route, status)
		}
	}
	for status, response := range responses {
		if !implemented[status] {
			t.Errorf("%s documents unimplemented success status %d", route, status)
		}
		if status == 204 && len(response.content) > 0 {
			t.Errorf("%s status 204 must not declare response content", route)
		}
	}
}

func (c *openAPIResponseContract) checkMetricsResponse(route string) {
	c.t.Helper()

	responses := c.successResponses(route)
	if len(responses) != 1 {
		c.t.Errorf("%s declares %d success statuses, want exactly 200", route, len(responses))
		return
	}
	response, exists := responses[200]
	if !exists {
		c.t.Errorf("%s does not declare status 200", route)
		return
	}
	schema := response.content["text/plain"]
	if schema == nil || !schema.types["string"] {
		c.t.Errorf("%s status 200 must be text/plain with a string schema", route)
	}
}

func compareGoWireType(
	pkg *packages.Package,
	value types.Type,
	schema *contractSchema,
	path string,
	isOptional bool,
	seen map[string]bool,
) []string {
	if value == nil {
		return []string{path + " has no statically known Go type"}
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
	if pointer, ok := value.(*types.Pointer); ok {
		allowsNull := schemaAllowsNull(schema)
		if !isOptional && !allowsNull {
			return []string{path + " is nullable in Go but not nullable in OpenAPI"}
		}
		return compareGoWireType(
			pkg,
			pointer.Elem(),
			schemaWithoutNull(schema),
			path,
			false,
			seen,
		)
	}
	// Unlike pointer nullability, a slice, map, interface, or RawMessage's nil
	// state depends on the value flow that built this response. This source/type
	// gate checks their JSON array/object/union shape; exact handler and domain
	// tests own non-null empty-collection normalization at concrete write sites.
	if _, ok := value.Underlying().(*types.Interface); ok && len(schema.alternatives) > 0 {
		implementers := sealedInterfaceImplementers(value)
		if len(implementers) == 0 {
			return []string{fmt.Sprintf(
				"%s is %s, which has no statically discoverable concrete response variants",
				path,
				value,
			)}
		}
		var mismatches []string
		for _, implementer := range implementers {
			mismatches = append(mismatches, compareGoWireAlternatives(
				pkg,
				implementer,
				schema.alternatives,
				path,
				isOptional,
				seen,
			)...)
		}
		return mismatches
	}
	if len(schema.alternatives) > 0 {
		return compareGoWireAlternatives(pkg, value, schema.alternatives, path, isOptional, seen)
	}

	if named, ok := value.(*types.Named); ok {
		if mismatch := compareKnownWireNamedType(named, schema, path); mismatch != "" {
			return []string{mismatch}
		}
		if isKnownWireNamedType(named) {
			return nil
		}
		if isJSONRawMessage(named) {
			// RawMessage is the deliberate opaque-document seam. Its owning
			// domain validates the document before persistence; the handler does
			// not have a second, weaker representation to compare here.
			return nil
		}
		if mismatches := compareNamedEnum(pkg, named, schema, path); len(mismatches) > 0 {
			return mismatches
		}
	}

	var shapeMismatches []string
	switch underlying := value.Underlying().(type) {
	case *types.Struct:
		shapeMismatches = compareGoWireStruct(pkg, underlying, schema, path, seen)
	case *types.Slice:
		if basic, ok := types.Unalias(underlying.Elem()).Underlying().(*types.Basic); ok &&
			basic.Kind() == types.Byte {
			shapeMismatches = compareScalarSchema(schema, path, "string", "byte")
			break
		}
		shapeMismatches = compareScalarSchema(schema, path, "array", "")
		if schema.items == nil {
			shapeMismatches = append(
				shapeMismatches,
				path+" is an array in Go but OpenAPI declares no items schema",
			)
			break
		}
		shapeMismatches = append(shapeMismatches, compareGoWireType(
			pkg,
			underlying.Elem(),
			schema.items,
			path+"[]",
			false,
			seen,
		)...)
	case *types.Array:
		shapeMismatches = compareScalarSchema(schema, path, "array", "")
		if schema.items == nil {
			shapeMismatches = append(
				shapeMismatches,
				path+" is an array in Go but OpenAPI declares no items schema",
			)
			break
		}
		shapeMismatches = append(shapeMismatches, compareGoWireType(
			pkg,
			underlying.Elem(),
			schema.items,
			path+"[]",
			false,
			seen,
		)...)
	case *types.Map:
		shapeMismatches = compareGoWireMap(pkg, underlying, schema, path, seen)
	case *types.Interface:
		if schemaAcceptsAnyJSON(schema) {
			break
		}
		shapeMismatches = []string{fmt.Sprintf(
			"%s is %s, which cannot prove the structured OpenAPI shape",
			path,
			value,
		)}
	case *types.Basic:
		expectedType, expectedFormat := basicJSONSchema(underlying)
		if expectedType == "" {
			shapeMismatches = []string{fmt.Sprintf("%s uses unsupported Go wire type %s", path, value)}
			break
		}
		shapeMismatches = compareScalarSchema(schema, path, expectedType, expectedFormat)
	default:
		shapeMismatches = []string{fmt.Sprintf("%s uses unsupported Go wire type %s", path, value)}
	}
	return shapeMismatches
}

func sealedInterfaceImplementers(value types.Type) []types.Type {
	named, ok := value.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return nil
	}
	contract, ok := named.Underlying().(*types.Interface)
	if !ok || contract.NumMethods() == 0 {
		return nil
	}
	contract.Complete()

	owner := named.Obj().Pkg()
	var implementers []types.Type
	for _, name := range owner.Scope().Names() {
		typeName, ok := owner.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		candidate := types.Unalias(typeName.Type())
		if _, isInterface := candidate.Underlying().(*types.Interface); isInterface {
			continue
		}
		switch {
		case types.Implements(candidate, contract):
			implementers = append(implementers, candidate)
		case types.Implements(types.NewPointer(candidate), contract):
			implementers = append(implementers, types.NewPointer(candidate))
		}
	}
	sort.Slice(implementers, func(i, j int) bool {
		return types.TypeString(implementers[i], packagePathQualifier) <
			types.TypeString(implementers[j], packagePathQualifier)
	})
	return implementers
}

func compareGoWireAlternatives(
	pkg *packages.Package,
	value types.Type,
	alternatives []*contractSchema,
	path string,
	isOptional bool,
	seen map[string]bool,
) []string {
	var closest []string
	for _, alternative := range alternatives {
		if schemaIsPureNull(alternative) {
			continue
		}
		mismatches := compareGoWireType(pkg, value, alternative, path, isOptional, seen)
		if len(mismatches) == 0 {
			return nil
		}
		if closest == nil || len(mismatches) < len(closest) {
			closest = mismatches
		}
	}
	if len(closest) == 0 {
		return []string{path + " has no non-null OpenAPI alternative"}
	}
	return []string{fmt.Sprintf(
		"%s (%s) matches no OpenAPI oneOf/anyOf branch; closest mismatch: %s",
		path,
		types.TypeString(value, packagePathQualifier),
		strings.Join(closest, "; "),
	)}
}

type goJSONField struct {
	name       string
	value      types.Type
	isOptional bool
}

func compareGoWireStruct(
	pkg *packages.Package,
	value *types.Struct,
	schema *contractSchema,
	path string,
	seen map[string]bool,
) []string {
	mismatches := compareScalarSchema(schema, path, "object", "")
	if schemaIsFreeFormObject(schema) {
		return mismatches
	}
	fields := goJSONFields(value, false)
	fieldByName := make(map[string]goJSONField, len(fields))
	for _, field := range fields {
		fieldByName[field.name] = field
		property := schema.properties[field.name]
		fieldPath := path + "." + field.name
		if property == nil {
			if !schema.allowsAdditional {
				mismatches = append(mismatches, fieldPath+" exists in Go but not OpenAPI")
			} else if schema.additionalProperties != nil {
				mismatches = append(mismatches, compareGoWireType(
					pkg,
					field.value,
					schema.additionalProperties,
					fieldPath,
					field.isOptional,
					seen,
				)...)
			}
			continue
		}
		if schema.required[field.name] && field.isOptional {
			mismatches = append(mismatches, fieldPath+" is optional in Go but required in OpenAPI")
		}
		mismatches = append(mismatches, compareGoWireType(
			pkg,
			field.value,
			property,
			fieldPath,
			field.isOptional,
			seen,
		)...)
	}
	for name := range schema.properties {
		if _, exists := fieldByName[name]; !exists && schema.required[name] {
			mismatches = append(mismatches, path+"."+name+" is required in OpenAPI but absent from Go")
		}
	}
	return mismatches
}

func goJSONFields(value *types.Struct, inheritedOptional bool) []goJSONField {
	var fields []goJSONField
	for index := range value.NumFields() {
		field := value.Field(index)
		tag := reflect.StructTag(value.Tag(index)).Get("json")
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "-" {
			continue
		}
		isOptional := inheritedOptional || slicesContain(parts[1:], "omitempty") ||
			slicesContain(parts[1:], "omitzero")
		if field.Anonymous() && name == "" {
			embedded := types.Unalias(field.Type())
			if pointer, ok := embedded.(*types.Pointer); ok {
				embedded = types.Unalias(pointer.Elem())
				isOptional = true
			}
			if structure, ok := embedded.Underlying().(*types.Struct); ok {
				fields = append(fields, goJSONFields(structure, isOptional)...)
				continue
			}
		}
		if !field.Exported() {
			continue
		}
		if name == "" {
			name = field.Name()
		}
		fields = append(fields, goJSONField{
			name:       name,
			value:      field.Type(),
			isOptional: isOptional,
		})
	}
	return fields
}

func compareGoWireMap(
	pkg *packages.Package,
	value *types.Map,
	schema *contractSchema,
	path string,
	seen map[string]bool,
) []string {
	mismatches := compareScalarSchema(schema, path, "object", "")
	key, ok := types.Unalias(value.Key()).Underlying().(*types.Basic)
	if !ok || key.Info()&types.IsString == 0 {
		return append(mismatches, fmt.Sprintf("%s has non-string Go map key %s", path, value.Key()))
	}
	if len(schema.properties) > 0 {
		mismatches = append(mismatches, path+" is a dynamic Go map but OpenAPI declares fixed properties")
	}
	if !schema.hasAdditionalContract {
		if !schemaIsEmpty(schema) {
			mismatches = append(mismatches, path+" is a Go map but OpenAPI omits additionalProperties")
		}
		return mismatches
	}
	if !schema.allowsAdditional {
		return append(mismatches, path+" is a Go map but OpenAPI forbids additionalProperties")
	}
	if schema.additionalProperties == nil {
		return mismatches
	}
	return append(mismatches, compareGoWireType(
		pkg,
		value.Elem(),
		schema.additionalProperties,
		path+"[*]",
		false,
		seen,
	)...)
}

func compareScalarSchema(
	schema *contractSchema,
	path, expectedType, expectedFormat string,
) []string {
	if len(schema.types) > 0 && !schema.types[expectedType] {
		return []string{fmt.Sprintf(
			"%s is %s in Go but %s in OpenAPI",
			path,
			expectedType,
			strings.Join(sortedKeys(schema.types), "|"),
		)}
	}
	if expectedType == "object" && len(schema.types) == 0 &&
		len(schema.properties) == 0 && !schema.hasAdditionalContract && schemaIsEmpty(schema) {
		return nil
	}
	if !responseFormatsCompatible(expectedFormat, schema.format) {
		return []string{fmt.Sprintf(
			"%s has provable Go format %s but OpenAPI format %q",
			path,
			expectedFormat,
			schema.format,
		)}
	}
	return nil
}

func responseFormatsCompatible(goFormat, schemaFormat string) bool {
	if goFormat == "" || schemaFormat == "" || goFormat == schemaFormat {
		return true
	}
	return goFormat == "int32" && schemaFormat == "int64"
}

func basicJSONSchema(value *types.Basic) (schemaType, format string) {
	switch value.Kind() {
	case types.Bool:
		return "boolean", ""
	case types.String:
		return "string", ""
	case types.Int8, types.Int16, types.Int32:
		return "integer", "int32"
	case types.Int64:
		return "integer", "int64"
	case types.Int, types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64, types.Uintptr:
		return "integer", ""
	case types.Float32, types.Float64:
		return "number", ""
	default:
		return "", ""
	}
}

func compareKnownWireNamedType(
	value *types.Named,
	schema *contractSchema,
	path string,
) string {
	if isNamedType(value, "time", "Time") {
		return firstMismatch(compareScalarSchema(schema, path, "string", "date-time"))
	}
	if isNamedType(value, "github.com/google/uuid", "UUID") {
		return firstMismatch(compareScalarSchema(schema, path, "string", "uuid"))
	}
	if isNamedType(value, "github.com/shopspring/decimal", "Decimal") {
		return firstMismatch(compareScalarSchema(schema, path, "string", ""))
	}
	return ""
}

func isKnownWireNamedType(value *types.Named) bool {
	return isNamedType(value, "time", "Time") ||
		isNamedType(value, "github.com/google/uuid", "UUID") ||
		isNamedType(value, "github.com/shopspring/decimal", "Decimal")
}

func isJSONRawMessage(value *types.Named) bool {
	// Go 1.27 represents json.RawMessage as an alias of jsontext.Value.
	return isNamedType(value, "encoding/json", "RawMessage") ||
		isNamedType(value, "encoding/json/jsontext", "Value")
}

func isNamedType(value *types.Named, packagePath, name string) bool {
	object := value.Obj()
	return object != nil && object.Pkg() != nil &&
		object.Pkg().Path() == packagePath && object.Name() == name
}

func compareNamedEnum(
	pkg *packages.Package,
	value *types.Named,
	schema *contractSchema,
	path string,
) []string {
	if len(schema.enum) == 0 && schema.constant == nil {
		return nil
	}
	underlying, ok := value.Underlying().(*types.Basic)
	if !ok || underlying.Info()&(types.IsString|types.IsInteger) == 0 {
		return nil
	}
	constants := constantsForNamedType(pkg, value)
	if len(constants) == 0 {
		return nil
	}
	accepted := make(map[string]bool, len(schema.enum)+1)
	for _, candidate := range schema.enum {
		accepted[candidate] = true
	}
	if schema.constant != nil {
		accepted[*schema.constant] = true
	}
	var unsupported []string
	for _, candidate := range constants {
		if !accepted[candidate] {
			unsupported = append(unsupported, candidate)
		}
	}
	if len(unsupported) == 0 {
		return nil
	}
	return []string{fmt.Sprintf(
		"%s Go enum can emit unsupported values [%s]; OpenAPI accepts [%s]",
		path,
		strings.Join(unsupported, ", "),
		strings.Join(sortedKeys(accepted), ", "),
	)}
}

func constantsForNamedType(pkg *packages.Package, value *types.Named) []string {
	seen := make(map[string]bool)
	add := func(object types.Object) {
		constantObject, ok := object.(*types.Const)
		if !ok || !types.Identical(types.Unalias(constantObject.Type()), value) {
			return
		}
		seen[constantObject.Val().ExactString()] = true
	}
	if owner := value.Obj().Pkg(); owner != nil {
		for _, name := range owner.Scope().Names() {
			add(owner.Scope().Lookup(name))
		}
	}
	if value.Obj().Pkg() == pkg.Types {
		for _, object := range pkg.TypesInfo.Defs {
			add(object)
		}
	}
	values := make([]string, 0, len(seen))
	for raw := range seen {
		if unquoted, err := strconv.Unquote(raw); err == nil {
			values = append(values, unquoted)
		} else {
			values = append(values, raw)
		}
	}
	sort.Strings(values)
	return values
}

func schemaAllowsNull(schema *contractSchema) bool {
	if schema == nil {
		return true
	}
	if schema.types["null"] {
		return true
	}
	for _, alternative := range schema.alternatives {
		if schemaAllowsNull(alternative) {
			return true
		}
	}
	return false
}

func schemaWithoutNull(schema *contractSchema) *contractSchema {
	if schema == nil {
		return nil
	}
	result := cloneContractSchema(schema)
	delete(result.types, "null")
	if len(result.enum) > 0 {
		nonNullEnum := result.enum[:0]
		for _, value := range result.enum {
			if value != "null" {
				nonNullEnum = append(nonNullEnum, value)
			}
		}
		result.enum = nonNullEnum
	}
	result.alternatives = nil
	for _, alternative := range schema.alternatives {
		if schemaIsPureNull(alternative) {
			continue
		}
		result.alternatives = append(result.alternatives, schemaWithoutNull(alternative))
	}
	return result
}

func cloneContractSchema(schema *contractSchema) *contractSchema {
	result := *schema
	result.types = make(map[string]bool, len(schema.types))
	for name, exists := range schema.types {
		result.types[name] = exists
	}
	result.properties = make(map[string]*contractSchema, len(schema.properties))
	for name, property := range schema.properties {
		result.properties[name] = property
	}
	result.required = make(map[string]bool, len(schema.required))
	for name, exists := range schema.required {
		result.required[name] = exists
	}
	result.enum = append([]string(nil), schema.enum...)
	result.alternatives = append([]*contractSchema(nil), schema.alternatives...)
	return &result
}

func schemaIsPureNull(schema *contractSchema) bool {
	if schema == nil || !schema.types["null"] || len(schema.types) != 1 {
		return false
	}
	withoutTypes := cloneContractSchema(schema)
	withoutTypes.types = make(map[string]bool)
	return schemaIsEmpty(withoutTypes)
}

func schemaAcceptsAnyJSON(schema *contractSchema) bool {
	return schema != nil && schemaIsEmpty(schema)
}

func schemaIsFreeFormObject(schema *contractSchema) bool {
	return schema != nil && schema.types["object"] && len(schema.types) == 1 &&
		len(schema.properties) == 0 && len(schema.required) == 0 &&
		schema.hasAdditionalContract && schema.allowsAdditional &&
		schema.additionalProperties == nil && len(schema.alternatives) == 0
}

func firstMismatch(mismatches []string) string {
	if len(mismatches) == 0 {
		return ""
	}
	return mismatches[0]
}

func packagePathQualifier(pkg *types.Package) string {
	if pkg == nil {
		return ""
	}
	return pkg.Path()
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func loadHandlerSource(t *testing.T) handlerSource {
	t.Helper()

	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedCompiledGoFiles |
			packages.NeedSyntax |
			packages.NeedTypes |
			packages.NeedTypesInfo |
			packages.NeedImports |
			packages.NeedDeps,
		Dir:   ".",
		Tests: false,
	}, ".")
	if err != nil {
		t.Fatalf("load handler source: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("load handler source: got %d packages, want 1", len(loaded))
	}
	if len(loaded[0].Errors) > 0 {
		messages := make([]string, 0, len(loaded[0].Errors))
		for _, packageError := range loaded[0].Errors {
			messages = append(messages, packageError.Error())
		}
		t.Fatalf("type-check handler source:\n%s", strings.Join(messages, "\n"))
	}

	functions := make(map[string]*ast.FuncDecl)
	for _, file := range loaded[0].Syntax {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			functions[function.Name.Name] = function
		}
	}
	return handlerSource{pkg: loaded[0], functions: functions}
}

func handlerMethodName(runtimeName string) (string, bool) {
	name := strings.TrimSuffix(runtimeName, "-fm")
	const receiver = ".(*Handler)."
	index := strings.LastIndex(name, receiver)
	if index < 0 {
		return "", false
	}
	method := name[index+len(receiver):]
	return method, method != ""
}

func responseContractExemptRoute(route string) bool {
	switch route {
	// These routes stream authorized bytes through streamAttachmentDownload.
	case "GET /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/files/:attachmentId",
		"GET /api/v1/public/artifacts/:token/whiteboard/files/:attachmentId",
		"GET /api/v1/companies/:companyId/projects/:projectId/presentations/:presentationId/export",
		"GET /api/v1/ws",
		"GET /api/v1/daemon/ws",
		"GET /api/v1/attachments/:attachmentId/download",
		"GET /api/v1/daemon/runtimes/:runtimeId/runs/:runId/attachments/:attachmentId",
		"GET /api/v1/companies/:companyId/runs/:runId/artifacts/:artifactId/items/:ordinal/download",
		"GET /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/items/:ordinal/download":
		return true
	case "GET /api/v1/public/artifacts/:token/items/:ordinal/download",
		"GET /api/v1/companies/:companyId/artifacts/:artifactId/items/:ordinal/download":
		return true
	// A Whiteboard preview writes PNG bytes, 204 or 304.
	case "GET /api/v1/companies/:companyId/artifacts/:artifactId/whiteboard/preview":
		return true
	default:
		return false
	}
}

func (s handlerSource) collectSuccessWrites(
	function *ast.FuncDecl,
	bindings statusBindings,
	stack map[string]bool,
) []successWrite {
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

	var writes []successWrite
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if s.isGinContextMethod(selector, "JSON", "PureJSON", "Status") {
			if len(call.Args) == 0 {
				return true
			}
			statuses := s.statusValues(function, call.Args[0], bindings)
			for _, status := range statuses {
				if status < 200 || status >= 400 {
					continue
				}
				var body types.Type
				if selector.Sel.Name != "Status" && len(call.Args) >= 2 {
					body = s.pkg.TypesInfo.TypeOf(call.Args[1])
				}
				writes = append(writes, successWrite{
					status:   status,
					body:     body,
					position: s.pkg.Fset.Position(call.Pos()),
				})
			}
			return true
		}

		called := s.handlerMethod(selector)
		if called == nil {
			return true
		}
		calleeBindings := s.bindStatusParameters(function, called, call.Args, bindings)
		writes = append(writes, s.collectSuccessWrites(called, calleeBindings, nextStack)...)
		return true
	})
	return deduplicateSuccessWrites(writes)
}

func (s handlerSource) isGinContextMethod(selector *ast.SelectorExpr, names ...string) bool {
	if !slicesContain(names, selector.Sel.Name) {
		return false
	}
	selection := s.pkg.TypesInfo.Selections[selector]
	if selection == nil || selection.Obj().Pkg() == nil {
		return false
	}
	return selection.Obj().Pkg().Path() == "github.com/gin-gonic/gin"
}

func (s handlerSource) handlerMethod(selector *ast.SelectorExpr) *ast.FuncDecl {
	selection := s.pkg.TypesInfo.Selections[selector]
	if selection == nil || selection.Obj().Pkg() != s.pkg.Types {
		return nil
	}
	return s.functions[selection.Obj().Name()]
}

func (s handlerSource) bindStatusParameters(
	caller *ast.FuncDecl,
	callee *ast.FuncDecl,
	arguments []ast.Expr,
	bindings statusBindings,
) statusBindings {
	next := make(statusBindings)
	signature, ok := s.pkg.TypesInfo.Defs[callee.Name].Type().(*types.Signature)
	if !ok {
		return next
	}
	parameters := signature.Params()
	for index := 0; index < parameters.Len() && index < len(arguments); index++ {
		parameter := parameters.At(index)
		if !isIntegerType(parameter.Type()) {
			continue
		}
		values := s.statusValues(caller, arguments[index], bindings)
		if len(values) > 0 {
			next[parameter] = values
		}
	}
	return next
}

func (s handlerSource) statusValues(
	function *ast.FuncDecl,
	expression ast.Expr,
	bindings statusBindings,
) []int {
	if value, ok := integerConstant(s.pkg.TypesInfo.Types[expression].Value); ok {
		return []int{value}
	}
	identifier, ok := expression.(*ast.Ident)
	if !ok {
		return nil
	}
	object, ok := s.pkg.TypesInfo.ObjectOf(identifier).(*types.Var)
	if !ok {
		return nil
	}
	if values := bindings[object]; len(values) > 0 {
		return values
	}

	seen := make(map[int]bool)
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch declaration := node.(type) {
		case *ast.AssignStmt:
			for index, left := range declaration.Lhs {
				if index >= len(declaration.Rhs) ||
					s.pkg.TypesInfo.ObjectOf(asIdentifier(left)) != object {
					continue
				}
				if value, constantValue := integerConstant(
					s.pkg.TypesInfo.Types[declaration.Rhs[index]].Value,
				); constantValue {
					seen[value] = true
				}
			}
		case *ast.ValueSpec:
			for index, name := range declaration.Names {
				if index >= len(declaration.Values) || s.pkg.TypesInfo.Defs[name] != object {
					continue
				}
				if value, constantValue := integerConstant(
					s.pkg.TypesInfo.Types[declaration.Values[index]].Value,
				); constantValue {
					seen[value] = true
				}
			}
		}
		return true
	})
	values := make([]int, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	sort.Ints(values)
	return values
}

func integerConstant(value constant.Value) (int, bool) {
	if value == nil || value.Kind() != constant.Int {
		return 0, false
	}
	parsed, err := strconv.Atoi(value.ExactString())
	return parsed, err == nil
}

func asIdentifier(expression ast.Expr) *ast.Ident {
	identifier, _ := expression.(*ast.Ident)
	return identifier
}

func isIntegerType(value types.Type) bool {
	basic, ok := types.Unalias(value).Underlying().(*types.Basic)
	return ok && basic.Info()&types.IsInteger != 0
}

func isUntypedJSONRoot(value types.Type) bool {
	value = types.Unalias(value)
	if named, ok := value.(*types.Named); ok {
		if object := named.Obj(); object.Pkg() != nil &&
			object.Pkg().Path() == "github.com/gin-gonic/gin" && object.Name() == "H" {
			return true
		}
		value = named.Underlying()
	}
	switch underlying := value.Underlying().(type) {
	case *types.Interface:
		return true
	case *types.Map:
		_, isAny := types.Unalias(underlying.Elem()).Underlying().(*types.Interface)
		return isAny
	default:
		return false
	}
}

func deduplicateSuccessWrites(writes []successWrite) []successWrite {
	seen := make(map[string]bool, len(writes))
	result := make([]successWrite, 0, len(writes))
	for _, write := range writes {
		body := "<none>"
		if write.body != nil {
			body = types.TypeString(write.body, nil)
		}
		key := fmt.Sprintf("%d:%s:%s", write.status, body, write.position)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, write)
	}
	return result
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestRecursiveDocumentContractKeepsBlockFields(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		childType    types.Type
		wantMismatch bool
	}{
		{"matching recursive blocks", types.Typ[types.String], false},
		{"incorrect nested field", types.Typ[types.Int], true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			contract := loadOpenAPIResponseContract(t)
			path := filepath.Join(filepath.Dir(contract.root.file), "canvas.yaml")
			document := contract.loadDocument(path)
			schemas := mappingValue(t, document, "schemas")
			schema := contract.normalizeSchema(
				locatedYAML{file: path, node: mappingValue(t, schemas, "CanvasBlock")},
				0,
			)
			pkg := types.NewPackage("contract.test", "contract")
			child := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Child", nil), nil, nil)
			child.SetUnderlying(types.NewStruct([]*types.Var{
				types.NewField(token.NoPos, pkg, "Type", test.childType, false),
				types.NewField(token.NoPos, pkg, "Content", types.NewSlice(child), false),
			}, []string{`json:"type"`, `json:"content,omitempty"`}))
			root := types.NewStruct([]*types.Var{
				types.NewField(token.NoPos, pkg, "Type", types.Typ[types.String], false),
				types.NewField(token.NoPos, pkg, "Content", types.NewSlice(child), false),
			}, []string{`json:"type"`, `json:"content,omitempty"`})
			got := compareGoWireType(&packages.Package{Types: pkg}, root, schema, "$", false, nil)
			if (len(got) > 0) != test.wantMismatch {
				t.Fatalf("recursive wire mismatches = %v, want mismatch %v", got, test.wantMismatch)
			}
		})
	}
}
