package wiki

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SourceDiagnostic locates a rejected authoring construct using UTF-8 byte offsets.
// Source never becomes another editable authority: compilation produces native nodes.
type SourceDiagnostic struct {
	OperationIndex *int   `json:"operationIndex,omitempty"`
	BlockID        string `json:"blockId,omitempty"`
	Code           string `json:"code"`
	Severity       string `json:"severity"`
	Start          int    `json:"start"`
	End            int    `json:"end"`
	Repair         string `json:"repair"`
}

func (d *SourceDiagnostic) Error() string {
	location := ""
	if d.OperationIndex != nil {
		location = fmt.Sprintf("operations[%d] ", *d.OperationIndex)
	}
	return fmt.Sprintf("%s%s at bytes %d:%d: %s", location, d.Code, d.Start, d.End, d.Repair)
}
func (d *SourceDiagnostic) Unwrap() error { return ErrPageInvalidInput }

// CompileSource accepts inline components among Markdown using the shared parser.
// Native envelopes are retained for lossless edits of ordinary document blocks.
// Fenced code is always literal, including recognized component tags.
func CompileSource(source string, version int) (BlockNode, error) {
	return compileSource(source, version, true)
}

func compileSource(source string, version int, document bool) (BlockNode, error) {
	if strings.HasPrefix(strings.TrimSpace(source), "<Native ") {
		return compileSourceLocated(source, version, document, nil)
	}
	return compileInlineSource(source, version)
}

func compileSourceLocated(
	source string,
	version int,
	document bool,
	retained *[]sourceNodeLocation,
) (BlockNode, error) {
	return parseSource(source, version, sourceCompileOptions{document: document, retained: retained})
}

type sourceCompileOptions struct {
	document bool
	preview  bool
	retained *[]sourceNodeLocation
}

func parseSource(source string, version int, options sourceCompileOptions) (BlockNode, error) {
	root := BlockNode{Type: "doc"}
	fail := func(code string, start, end int, repair string) (BlockNode, error) {
		return BlockNode{}, &SourceDiagnostic{
			Code:     code,
			Severity: "error",
			Start:    start,
			End:      end,
			Repair:   repair,
		}
	}
	if version != 1 {
		return fail("source.version", 0, 0, "Use languageVersion 1.")
	}
	if len(source) > 262144 || !utf8.ValidString(source) {
		return fail("source.size", 0, len(source), "Use valid UTF-8 source of at most 256 KiB.")
	}
	type frame struct {
		name  string
		node  BlockNode
		start int
		path  []string
	}
	stack := []frame{{node: root}}
	var locations []sourceNodeLocation
	var prose strings.Builder
	flush := func() {
		if strings.TrimSpace(prose.String()) != "" {
			blocks := BlocksFromMarkdown(prose.String())
			stack[len(stack)-1].node.Content = append(stack[len(stack)-1].node.Content, blocks.Content...)
		}
		prose.Reset()
	}
	offset := 0
	var fence byte
	fenceLength := 0
	for _, line := range strings.SplitAfter(source, "\n") {
		trimmed := strings.TrimSpace(line)
		start, end := offset, offset+len(line)
		offset = end
		// CommonMark fences may use backticks or tildes and must close with at
		// least as many delimiters. A component inside a fence is literal text.
		run := 0
		if len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
			for run < len(trimmed) && trimmed[run] == trimmed[0] {
				run++
			}
		}
		if fence != 0 {
			prose.WriteString(line)
			if run >= fenceLength && trimmed[0] == fence && strings.TrimSpace(trimmed[run:]) == "" {
				fence = 0
			}
			continue
		}
		if run >= 3 {
			fence = trimmed[0]
			fenceLength = run
			prose.WriteString(line)
			continue
		}
		if !strings.HasPrefix(trimmed, "<") {
			prose.WriteString(line)
			continue
		}
		flush()
		if strings.HasPrefix(trimmed, "</") {
			name := strings.TrimSuffix(strings.TrimPrefix(trimmed, "</"), ">")
			if !strings.HasSuffix(trimmed, ">") || len(stack) == 1 || stack[len(stack)-1].name != name {
				return fail(
					"source.close",
					start,
					end,
					"Close the innermost open component on its own line.",
				)
			}
			node := stack[len(stack)-1].node
			stack = stack[:len(stack)-1]
			stack[len(stack)-1].node.Content = append(stack[len(stack)-1].node.Content, node)
			continue
		}
		self := strings.HasSuffix(trimmed, "/>")
		if !strings.HasSuffix(trimmed, ">") {
			return fail("source.tag", start, end, "Complete the component on one line.")
		}
		body := strings.TrimSuffix(strings.TrimSuffix(trimmed[1:], ">"), "/")
		body = strings.TrimSpace(body)
		base := start + strings.Index(line, body)
		spans := map[string][2]int{}
		name, attrs, err := parseSourceTag(body, spans)
		if err != nil {
			var diagnostic *SourceDiagnostic
			if errors.As(err, &diagnostic) {
				clone := *diagnostic
				clone.Start += base
				clone.End += base
				return BlockNode{}, &clone
			}
			code := "source.attributes"
			var expressionError *UIEvaluationError
			if errors.As(err, &expressionError) {
				code = expressionError.Code
			}
			return fail(
				code,
				start,
				end,
				"Use unique name=JSON attributes or {bounded expressions} in catalog expression slots; arbitrary JavaScript is not accepted.",
			)
		}
		var node BlockNode
		if name == "Native" {
			if !self || len(attrs) != 1 {
				return fail(
					"source.native",
					start,
					end,
					"Use <Native node={...} /> with exactly one native node.",
				)
			}
			raw, e := json.Marshal(attrs["node"])
			if e != nil {
				return fail("source.native", start, end, "Supply a valid native node object.")
			}
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if e = decoder.Decode(&node); e != nil || node.Type == "" || node.Type == "doc" {
				return fail(
					"source.native",
					start,
					end,
					"Supply one supported native subtree, not a document root.",
				)
			}
		} else {
			native, properties, container, ok := sourceComponent(name)
			if !ok {
				return fail(
					"source.component",
					start,
					end,
					"Use a supported component or the Native escape; arbitrary HTML and JSX are not accepted.",
				)
			}
			for key := range attrs {
				if !slices.Contains(properties, key) {
					return fail(
						"source.property",
						start,
						end,
						"Remove unsupported component properties; use Native to preserve existing native attributes.",
					)
				}
			}
			node = BlockNode{Type: native, Attrs: attrs}
			if !self && !container {
				return fail("source.children", start, end, "This component must be self-closing.")
			}
		}
		parent := &stack[len(stack)-1]
		path := append(slices.Clone(parent.path), "content", strconv.Itoa(len(parent.node.Content)))
		locationPath := path
		if self && (node.Type == "uiValue" || node.Type == "wikiPageLink") {
			locationPath = append(slices.Clone(path), "content", "0")
		}
		locations = append(locations, sourceNodeLocation{
			path: locationPath, start: start, end: end, base: base,
			body: body, spans: spans, native: name == "Native",
		})
		if self {
			if node.Type == "uiValue" || node.Type == "wikiPageLink" {
				node = BlockNode{Type: "paragraph", Content: []BlockNode{node}}
			}
			stack[len(stack)-1].node.Content = append(stack[len(stack)-1].node.Content, node)
		} else {
			if len(stack) >= 32 {
				return fail("source.depth", start, end, "Use at most 31 nested containers.")
			}
			stack = append(stack, frame{name: name, node: node, start: start, path: path})
		}
	}
	flush()
	if len(stack) != 1 && !options.preview {
		return fail(
			"source.unclosed",
			stack[len(stack)-1].start,
			len(source),
			"Close the open container before saving.",
		)
	}
	// Preview closes structure only. Attribute and document validation still run,
	// so no missing state, expressions or required children are manufactured.
	for len(stack) > 1 {
		node := stack[len(stack)-1].node
		stack = stack[:len(stack)-1]
		stack[len(stack)-1].node.Content = append(stack[len(stack)-1].node.Content, node)
	}
	root = stack[0].node
	if options.retained != nil {
		*options.retained = locations
	}
	if options.document {
		if err := ValidateBlockImport(root); err != nil {
			start, end := 0, len(source)
			var located *uiValidationLocation
			if errors.As(err, &located) {
				start, end = sourceValidationSpan(locations, located.path, end)
			}
			var diagnostic *UIEvaluationError
			if errors.As(err, &diagnostic) {
				return fail(
					diagnostic.Code,
					start,
					end,
					"Check component properties and declare every state or data reference in an enclosing Scope.",
				)
			}
			return fail(
				"source.structure",
				start,
				end,
				"Use supported native attributes and valid child structure; containers must not be empty.",
			)
		}
	}
	return root, nil
}

var sourceComponents = map[string]string{
	"TaskCollection": "taskCollection", "TaskReference": "taskReference", "OutputCollection": "outputCollection",
	"WorkflowReference": "workflowReference", "ArtifactReference": "artifactReference", "Image": "image",
	"Callout": "callout", "Quote": "blockquote", "Columns": "columns", "Column": "column",
	"WikiReference": "wikiPageLink",
}

var sourceProperties = map[string][]string{
	"TaskCollection":    {"filter", "display"},
	"TaskReference":     {"taskId"},
	"OutputCollection":  {"filter", "display"},
	"WorkflowReference": {"workflowKey", "display"},
	"ArtifactReference": {"artifactId", "versionId", "label", "format", "display", "blockTargetId"},
	"Image":             {"src", "alt", "title", "display", "caption"},
	"Callout":           {"background"},
	"Quote":             {},
	"Columns":           {},
	"Column":            {},
	"WikiReference":     {"id", "label"},
}

var sourceContainers = map[string]bool{"Callout": true, "Quote": true, "Columns": true, "Column": true}

// Reference names share Zenith's property contracts and canonical native nodes.
// They do not resolve another product's media or citation identifiers.
func canonicalSourceName(name string) string {
	switch name {
	case "AsyncImage":
		return "Image"
	case "AsyncImageGroup":
		return "ImageGroup"
	case "AsyncVideo":
		return "Video"
	case "Cite", "CodeCite", "FileCite":
		return "Citation"
	case "MemoryCite":
		return "WikiReference"
	case "AppBlock":
		return "ArtifactReference"
	default:
		return name
	}
}

// sourceComponent shares reactive properties with the native document catalog.
func sourceComponent(name string) (string, []string, bool, bool) {
	name = canonicalSourceName(name)
	if component, ok := uiSourceComponent(name); ok {
		return component.NodeType, component.Properties, component.Children, true
	}
	native, ok := sourceComponents[name]
	return native, sourceProperties[name], sourceContainers[name], ok
}

func parseSourceTag(body string, spans map[string][2]int) (string, map[string]any, error) {
	at := strings.IndexAny(body, " \t\r\n")
	if at < 0 {
		at = len(body)
	}
	name, rest := body[:at], strings.TrimSpace(body[at:])
	nodeType, properties, _, known := sourceComponent(name)
	component, _ := uiComponent(nodeType)
	if name == "Native" {
		properties, known = []string{"node"}, true
	}
	attrs := map[string]any{}
	for rest != "" {
		start := len(body) - len(rest)
		if strings.HasPrefix(rest, ">") || strings.HasPrefix(rest, "/>") {
			return "", nil, sourceAttributeError(
				"source.line",
				start,
				len(body),
				"Put each opening tag, child tag and closing tag on its own line in the native block source.",
			)
		}
		equal := strings.IndexByte(rest, '=')
		if equal <= 0 {
			return "", nil, sourceAttributeError(
				"source.attributes",
				start,
				len(body),
				"Use name=JSON for each attribute.",
			)
		}
		key := rest[:equal]
		for _, char := range key {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
				return "", nil, sourceAttributeError(
					"source.attribute.name",
					start,
					start+equal,
					"Use an alphanumeric catalog property name immediately followed by =.",
				)
			}
		}
		if _, ok := attrs[key]; ok {
			return "", nil, sourceAttributeError(
				"source.attribute.duplicate",
				start,
				start+equal,
				"Keep only one value for this property.",
			)
		}
		if known && !slices.Contains(properties, key) {
			return "", nil, sourceAttributeError(
				"source.property",
				start,
				start+equal,
				"Use a property declared by this component's catalog entry.",
			)
		}
		rest = rest[equal+1:]
		valueStart := len(body) - len(rest)
		value, consumed, err := parseSourceAttribute(name, key, rest)
		if err != nil {
			var diagnostic *SourceDiagnostic
			if errors.As(err, &diagnostic) {
				clone := *diagnostic
				clone.Start += valueStart
				clone.End += valueStart
				return "", nil, &clone
			}
			var syntax *json.SyntaxError
			if errors.As(err, &syntax) {
				position := min(len(rest), max(0, int(syntax.Offset)-1))
				_, width := utf8.DecodeRuneInString(rest[position:])
				return "", nil, sourceAttributeError(
					"source.attribute.json",
					valueStart+position,
					min(len(body), valueStart+position+width),
					"Supply a valid JSON value or a bounded expression in a declared expression slot.",
				)
			}
			return "", nil, sourceAttributeError(
				"source.attributes",
				valueStart,
				len(body),
				"Complete this attribute's JSON value or bounded expression.",
			)
		}
		if consumed < len(rest) && !strings.ContainsRune(" \t\r\n", rune(rest[consumed])) {
			if strings.HasPrefix(rest[consumed:], ">") || strings.HasPrefix(rest[consumed:], "/>") {
				return "", nil, sourceAttributeError(
					"source.line",
					valueStart+consumed,
					len(body),
					"Put each opening tag, child tag and closing tag on its own line in the native block source.",
				)
			}
			return "", nil, sourceAttributeError(
				"source.attribute.separator",
				valueStart+consumed,
				valueStart+consumed+1,
				"Separate attributes with whitespace.",
			)
		}
		if name == "Native" && key == "node" {
			if path, invalid := sourceNativeShapePath(value, false, 0); invalid {
				propertyStart, propertyEnd := sourcePathSpan(rest[:consumed], path)
				return "", nil, sourceAttributeError(
					"source.native",
					valueStart+propertyStart,
					valueStart+propertyEnd,
					"Use the native node fields type, attrs, content, text and marks with their declared JSON types; marks contain only type and attrs.",
				)
			}
			// Shape validation above makes decoding safe; retain the original
			// attribute text for exact offsets rather than locating re-encoded JSON.
			raw, marshalErr := json.Marshal(value)
			var native BlockNode
			if marshalErr == nil && json.Unmarshal(raw, &native) == nil {
				if path, err := sourceNativeCatalogPath(native); err != nil {
					propertyStart, propertyEnd := sourcePathSpan(rest[:consumed], path)
					diagnostic := sourceExpressionError(
						err,
						valueStart+propertyStart,
						valueStart+propertyEnd,
					)
					diagnostic.Repair = "Use the reactive component's declared catalog properties, supported version and child structure."
					return "", nil, diagnostic
				}
			}
		}
		attrs[key] = value
		spans[key] = [2]int{valueStart, valueStart + consumed}
		for _, rule := range component.Rules {
			if rule.Property == key {
				if err := validateUIProperty(attrs, rule); err != nil {
					code := rule.Code
					var evaluation *UIEvaluationError
					if errors.As(err, &evaluation) {
						code = evaluation.Code
					}
					propertyStart, propertyEnd := sourcePropertySpan(rest[:consumed], value, rule)
					return "", nil, sourceAttributeError(
						code,
						valueStart+propertyStart,
						valueStart+propertyEnd,
						sourcePropertyRepair(rule),
					)
				}
			}
		}
		rest = strings.TrimSpace(rest[consumed:])
	}
	for _, rule := range component.Rules {
		if _, exists := attrs[rule.Property]; !exists && rule.Required {
			return "", nil, sourceAttributeError(
				rule.Code,
				0,
				len(name),
				"Add the required "+rule.Property+" property. "+sourcePropertyRepair(rule),
			)
		}
	}
	for _, rule := range component.Constraints {
		if err := validateUIConstraint(attrs, rule); err != nil {
			span, exists := spans[rule.Right]
			if !exists {
				span, exists = spans[rule.Left]
			}
			if !exists {
				span = [2]int{0, len(name)}
			}
			return "", nil, sourceAttributeError(rule.Code, span[0], span[1], rule.Repair)
		}
	}
	return name, attrs, nil
}

func sourcePropertyRepair(rule UIPropertyRule) string {
	switch rule.Type {
	case "string":
		return fmt.Sprintf("Set %s to a JSON string of %d–%d UTF-8 bytes.", rule.Property, rule.Min, rule.Max)
	case "httpsURL":
		return fmt.Sprintf(
			"Set %s to an HTTPS URL with a host, no user credentials and at most %d UTF-8 bytes.",
			rule.Property,
			rule.Max,
		)
	case "variant":
		names := make([]string, 0, len(rule.Variants))
		for name := range rule.Variants {
			names = append(names, name)
		}
		slices.Sort(names)
		return "Set " + rule.Property + " to a closed JSON object whose " + rule.Discriminator + " is one of: " + strings.Join(
			names,
			", ",
		) + ". Use only that variant's declared fields."
	case "scalar":
		return "Set " + rule.Property + " to null, a boolean, a finite number or a string of at most 4096 UTF-8 bytes."
	case "uuid":
		return "Set " + rule.Property + " to a nonzero UUID string."
	case "expression":
		return "Set " + rule.Property + " to a bounded expression or a valid language-1 expression AST."
	case "nameMap":
		return fmt.Sprintf(
			"Set %s to a JSON object with %d–%d valid declaration names. %s",
			rule.Property,
			rule.Min,
			rule.Max,
			sourcePropertyRepair(*rule.Entry),
		)
	case "object", "objectArray":
		fields := make([]string, 0, len(rule.Fields))
		for _, field := range rule.Fields {
			fields = append(fields, sourcePropertyRepair(field))
		}
		unique := ""
		if rule.UniqueBy != "" {
			unique = " Keep " + rule.UniqueBy + " unique across items."
		}
		if rule.Type == "object" {
			return "Set " + rule.Property + " to a closed JSON object. " + strings.Join(fields, " ")
		}
		return fmt.Sprintf(
			"Set %s to a JSON array of %d–%d closed objects. %s%s",
			rule.Property,
			rule.Min,
			rule.Max,
			strings.Join(fields, " "),
			unique,
		)
	case "stringArray":
		unique := ""
		if rule.Unique {
			unique = " distinct"
		}
		return fmt.Sprintf(
			"Set %s to a JSON array of %d–%d%s strings, each at most %d UTF-8 bytes.",
			rule.Property,
			rule.Min,
			rule.Max,
			unique,
			rule.ItemMaxBytes,
		)
	case "number":
		return "Set " + rule.Property + " to a finite JSON number."
	case "positiveNumber":
		return "Set " + rule.Property + " to a finite JSON number greater than zero."
	case "integer":
		return fmt.Sprintf("Set %s to an integer from %d through %d.", rule.Property, rule.Min, rule.Max)
	case "enum":
		return "Set " + rule.Property + " to one of these JSON strings: " + strings.Join(
			rule.Values,
			", ",
		) + "."
	case "name":
		return "Set " + rule.Property + " to a valid identifier of at most 64 ASCII characters; reserved object names are forbidden."
	case "field":
		return "Set " + rule.Property + " to a nonempty field name of at most 128 UTF-8 bytes; reserved object names are forbidden."
	default:
		return "Use the catalog constraint for " + rule.Property + "."
	}
}

func sourceAttributeError(code string, start, end int, repair string) *SourceDiagnostic {
	return &SourceDiagnostic{Code: code, Severity: "error", Start: start, End: end, Repair: repair}
}

// BlocksSource preserves every attribute and descendant identity. Source edits
// must copy this envelope rather than guessing correspondence from prose.
func BlocksSource(root BlockNode) string {
	nodes := root.Content
	if root.Type != "doc" {
		nodes = []BlockNode{root}
	}
	var out strings.Builder
	for _, node := range nodes {
		if node.Type == "uiDocument" && len(nodes) == 1 {
			out.WriteString(InlineSource(node))
			out.WriteByte('\n')
			continue
		}
		raw, err := json.Marshal(node)
		if err != nil {
			continue
		}
		out.WriteString("<Native node=")
		out.Write(raw)
		out.WriteString(" />\n")
	}
	return out.String()
}

// CompileOperationSource converts authoring input before the normal identity,
// permission and proposal checks. Existing IDs are never inferred from positions.
func CompileOperationSource(op *BlockOperation, target *BlockNode) error {
	if op.Source == nil {
		if op.LanguageVersion != 0 {
			return ErrPageInvalidInput
		}
		return nil
	}
	if op.Content != nil || (op.Type != "insert" && op.Type != "update") {
		return ErrPageInvalidInput
	}
	root, err := compileSource(*op.Source, op.LanguageVersion, false)
	if err != nil {
		return err
	}
	if len(root.Content) != 1 {
		return &SourceDiagnostic{
			Code:     "source.operation",
			Severity: "error",
			End:      len(*op.Source),
			Repair:   "Supply exactly one subtree per operation; use separate operations for structural insertions and removals.",
		}
	}
	node := &root.Content[0]
	if op.Type == "insert" && sourceHasIdentity(*node) {
		return &SourceDiagnostic{
			Code:     "source.identity",
			Severity: "error",
			End:      len(*op.Source),
			Repair:   "Omit blockId throughout inserted subtrees; the server allocates identities.",
		}
	}
	if op.Type == "update" {
		if target == nil {
			return ErrPageConflict
		}
		if node.Type == "uiDocument" && target.Type == "uiDocument" {
			node.Attrs["blockId"] = op.BlockID
		}
		if node.Attrs["blockId"] != op.BlockID || !sameSourceIdentity(*target, *node) {
			return &SourceDiagnostic{
				Code:     "source.identity",
				Severity: "error",
				End:      len(*op.Source),
				Repair:   "Preserve the target and descendant block IDs, types and order from page_read. Use explicit insert, remove or move operations for structural changes.",
			}
		}
	}
	op.Content = node
	op.Source = nil
	op.LanguageVersion = 0
	return nil
}

func sourceHasIdentity(node BlockNode) bool {
	if _, ok := node.Attrs["blockId"]; ok {
		return true
	}
	for _, child := range node.Content {
		if sourceHasIdentity(child) {
			return true
		}
	}
	return false
}

// CompileSourceReplacement prepares a member's selected-subtree edit. Existing
// identities are explicit and confined to that subtree; new blocks omit IDs.
// The caller reserves missing identities and validates the whole document before
// offering the result for review. Agent update operations keep their stricter
// structural contract in CompileOperationSource.
func CompileSourceReplacement(source string, version int, target BlockNode) (*BlockNode, error) {
	root, err := compileSource(source, version, false)
	if err != nil {
		return nil, err
	}
	fail := func(repair string) (*BlockNode, error) {
		return nil, &SourceDiagnostic{
			Code:     "source.identity",
			Severity: "error",
			End:      len(source),
			Repair:   repair,
		}
	}
	if len(root.Content) != 1 {
		return fail("Keep exactly one selected root block; add or remove children inside it.")
	}
	node := &root.Content[0]
	if node.Type == "uiDocument" && target.Type == "uiDocument" {
		node.Attrs["blockId"] = target.Attrs["blockId"]
	}
	if node.Type != target.Type || node.Attrs["blockId"] != target.Attrs["blockId"] {
		return fail("Keep the selected root's type and blockId.")
	}
	existing := map[string]string{}
	var collect func(BlockNode)
	collect = func(block BlockNode) {
		if id, ok := block.Attrs["blockId"].(string); ok {
			existing[id] = block.Type
		}
		for _, child := range block.Content {
			collect(child)
		}
	}
	collect(target)
	seen := map[string]bool{}
	count := 0
	var check func(BlockNode, int) bool
	check = func(block BlockNode, depth int) bool {
		count++
		if count > 10000 || depth > 32 {
			return false
		}
		if raw, present := block.Attrs["blockId"]; present {
			id, ok := raw.(string)
			if !ok || id == "" || inlineBlockNode(block.Type) || existing[id] != block.Type || seen[id] {
				return false
			}
			seen[id] = true
		}
		for _, child := range block.Content {
			if !check(child, depth+1) {
				return false
			}
		}
		return true
	}
	if !check(*node, 0) {
		return fail(
			"Use each existing descendant ID once with its existing type. Omit blockId on new blocks. Keep the subtree within 10,000 nodes and 32 levels.",
		)
	}
	return node, nil
}

// AssignMissingBlockIDs preserves retained anchors and assigns only newly
// authored blocks. The supplied identities must be reserved by PostgreSQL.
func AssignMissingBlockIDs(node *BlockNode, next func() string) {
	if node.Type != "doc" && !inlineBlockNode(node.Type) && node.Attrs["blockId"] == nil {
		if node.Attrs == nil {
			node.Attrs = map[string]any{}
		}
		if id := next(); id != "" {
			node.Attrs["blockId"] = id
		}
	}
	for i := range node.Content {
		AssignMissingBlockIDs(&node.Content[i], next)
	}
}

func sameSourceIdentity(before, after BlockNode) bool {
	// Text and inline formatting can change. Addressed descendants cannot be
	// silently inserted, removed, moved or converted by a source replacement.
	signature := func(root BlockNode) string {
		var out strings.Builder
		var walk func(BlockNode)
		walk = func(node BlockNode) {
			if inlineBlockNode(node.Type) {
				return
			}
			out.WriteString(node.Type)
			out.WriteByte(':')
			fmt.Fprint(&out, node.Attrs["blockId"])
			out.WriteByte('{')
			for _, child := range node.Content {
				walk(child)
			}
			out.WriteByte('}')
		}
		walk(root)
		return out.String()
	}
	return signature(before) == signature(after)
}

// SourcePreview is transient display data, never an accepted document update.
// AcceptedBytes counts the original source available to the shared renderer;
// incomplete expressions remain in source but are withheld by that renderer.
// A host must compile the complete source before creating a proposal.
type SourcePreview struct {
	Content       *BlockNode         `json:"content,omitempty"`
	Complete      bool               `json:"complete"`
	AcceptedBytes int                `json:"acceptedBytes"`
	Diagnostics   []SourceDiagnostic `json:"diagnostics"`
}

// PreviewSource preserves partial source for the shared streaming renderer.
// Only strict compilation can admit the final source to a saved document.
func PreviewSource(source string, version int) SourcePreview {
	result := SourcePreview{Diagnostics: []SourceDiagnostic{}}
	if strings.HasPrefix(strings.TrimSpace(source), "<Native ") {
		root, err := compileSource(source, version, true)
		if err != nil {
			var diagnostic *SourceDiagnostic
			if errors.As(err, &diagnostic) {
				result.Diagnostics = append(result.Diagnostics, *diagnostic)
			}
			return result
		}
		result.Content = &root
		result.Complete = true
		result.AcceptedBytes = len(source)
		return result
	}
	info, err := inspectInlineSource(source, version)
	if err != nil {
		var diagnostic *SourceDiagnostic
		if errors.As(err, &diagnostic) {
			result.Diagnostics = append(result.Diagnostics, *diagnostic)
		}
		return result
	}
	root := inlineSourceDocument(source)
	if info.complete && !info.interactive {
		root = BlocksFromMarkdown(source)
	}
	result.Content = &root
	result.AcceptedBytes = len(source)
	result.Complete = info.complete
	return result
}
