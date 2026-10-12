package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestResponsesAreNotSingleEntityWrappers enforces the rule in
// conventions.md: a response about one entity returns that entity at the top
// level, never wrapped in a key named after it.
//
// The wrapper was not merely noise. Returning a Company under a `company`
// property on some endpoints but directly on others made `response.company`
// vs `response` a per-endpoint guessing game for every generated client.
//
// A 2xx whose entire schema is an inline object with exactly one property, and
// whose property is a $ref to an *entity*, is that shape and nothing else — a
// genuine composite has more than one part, and a page has `items` plus
// `nextCursor`. A lone property referencing a scalar schema (an enum such as
// RunStatus) is a projection of one field, not a wrapped entity: there is no
// entity to hoist, and the alternative is a bare JSON scalar body.
func TestResponsesAreNotSingleEntityWrappers(t *testing.T) {
	t.Parallel()

	specDir := filepath.Join(docsRelativeDir, "spec")
	entries, err := os.ReadDir(specDir)
	if err != nil {
		t.Fatalf("read the spec directory: %v", err)
	}

	var offenders []string
	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(specDir, entry.Name())
		raw, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		scanned++
		offenders = append(offenders, wrapperResponses(specDir, entry.Name(), string(raw))...)
	}
	// A scan that reads nothing reports no offenders, which is indistinguishable
	// from a clean spec until someone checks.
	if scanned == 0 {
		t.Fatal("scanned no spec files; the spec directory has moved")
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf(
			"response(s) wrapping a single entity in a key named after it (%d).\n"+
				"Return the entity at the top level — see the \"A response is the "+
				"thing itself\" rule in docs/api/conventions.md:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "),
		)
	}
}

var (
	operationIDLine    = regexp.MustCompile(`^\s+operationId: (\S+)`)
	successStatusLine  = regexp.MustCompile(`^(\s+)'(2\d\d)':\s*$`)
	inlineObjectLine   = regexp.MustCompile(`^\s+type: object\s*$`)
	propertiesLine     = regexp.MustCompile(`^(\s+)properties:\s*$`)
	anyPropertyLine    = regexp.MustCompile(`^\s+(\w+):`)
	singleRefPropLine  = regexp.MustCompile(`^\s+(\w+):\s*\{\s*\$ref:`)
	nestedRefPropStart = regexp.MustCompile(`^\s+(\w+):\s*$`)
)

// wrapperResponses returns "file operationId {key}" for each 2xx response in
// the document whose schema is an inline object holding exactly one $ref'd
// property.
func wrapperResponses(specDir, file, source string) []string {
	lines := strings.Split(source, "\n")
	operation := "(unnamed)"
	var found []string

	for i, line := range lines {
		if match := operationIDLine.FindStringSubmatch(line); match != nil {
			operation = match[1]
			continue
		}
		match := successStatusLine.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		statusIndent := len(match[1])

		// Walk the response body looking for an inline `type: object` schema.
		for j := i + 1; j < len(lines) && indentOf(lines[j]) > statusIndent; j++ {
			if !inlineObjectLine.MatchString(lines[j]) {
				continue
			}
			if key, ref, single := lonePropertyRef(lines, j, statusIndent); single &&
				refersToEntity(specDir, file, ref) {
				found = append(found, file+" "+operation+" -> {"+key+"}")
			}
			break
		}
	}
	return found
}

// lonePropertyRef reports whether the object starting at objectLine declares
// exactly one property, and that property is a reference to a named schema. It
// returns the property name and the reference it carries.
func lonePropertyRef(lines []string, objectLine, statusIndent int) (string, string, bool) {
	propertyIndent := -1
	var names []string
	referenced := map[string]string{}

	for k := objectLine + 1; k < len(lines) && indentOf(lines[k]) > statusIndent; k++ {
		if strings.TrimSpace(lines[k]) == "" {
			continue
		}
		// Bind to the object's own property block, not a nested one: a later
		// `properties:` belongs to a child schema and its keys are not the
		// response's top-level shape.
		if match := propertiesLine.FindStringSubmatch(lines[k]); match != nil {
			if propertyIndent < 0 {
				propertyIndent = len(match[1]) + 2
			}
			continue
		}
		if propertyIndent < 0 || indentOf(lines[k]) != propertyIndent {
			continue
		}
		// Every property counts toward the total, however it is written — a
		// sibling declared inline (`code: { type: string }`) is still a second
		// property, and missing it would read a two-field response as a wrapper.
		match := anyPropertyLine.FindStringSubmatch(lines[k])
		if match == nil {
			continue
		}
		names = append(names, match[1])

		if singleRefPropLine.MatchString(lines[k]) {
			referenced[match[1]] = lines[k]
			continue
		}
		// A property whose body is a bare $ref on the next line is a wrapper too.
		if nestedRefPropStart.MatchString(lines[k]) &&
			k+1 < len(lines) && strings.Contains(lines[k+1], "$ref:") {
			referenced[match[1]] = lines[k+1]
		}
	}

	if len(names) != 1 || referenced[names[0]] == "" {
		return "", "", false
	}
	return names[0], referenced[names[0]], true
}

var refTargetPattern = regexp.MustCompile(`\$ref:\s*'?([^'\s}]+)`)

// refersToEntity reports whether a $ref line points at a schema that is an
// object. Enums and other scalars are not entities, so a response carrying one
// under a single key is a field projection rather than a wrapper.
func refersToEntity(specDir, file, refLine string) bool {
	match := refTargetPattern.FindStringSubmatch(refLine)
	if match == nil {
		return true
	}
	target := match[1]
	documentPath, anchor, found := strings.Cut(target, "#")
	if !found {
		return true
	}
	name := anchor[strings.LastIndex(anchor, "/")+1:]
	if documentPath == "" {
		documentPath = file
	}
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(specDir, filepath.Base(documentPath))))
	if err != nil {
		// An unreadable target is reported as an entity so a broken $ref
		// surfaces here rather than silently exempting a real wrapper.
		return true
	}
	return schemaIsObject(string(raw), name)
}

// schemaIsObject reports whether the named schema block declares an object.
func schemaIsObject(source, name string) bool {
	lines := strings.Split(source, "\n")
	header := regexp.MustCompile(`^(\s+)` + regexp.QuoteMeta(name) + `:\s*$`)
	for i, line := range lines {
		match := header.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		blockIndent := len(match[1])
		for j := i + 1; j < len(lines) && indentOf(lines[j]) > blockIndent; j++ {
			trimmed := strings.TrimSpace(lines[j])
			if trimmed == "type: object" || trimmed == "properties:" ||
				strings.HasPrefix(trimmed, "allOf:") {
				return true
			}
			if strings.HasPrefix(trimmed, "type:") || strings.HasPrefix(trimmed, "enum:") {
				return false
			}
		}
		return false
	}
	return true
}

func indentOf(line string) int {
	if strings.TrimSpace(line) == "" {
		return 1 << 30
	}
	return len(line) - len(strings.TrimLeft(line, " "))
}
