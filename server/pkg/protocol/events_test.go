package protocol

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The registry is the single event vocabulary: every constant must be a
// unique, well-formed `<domain>.<verb>` name, because a duplicate silently
// routes two meanings onto one wire type and a malformed name cannot be
// classified consistently by producers and clients.
func TestEventRegistryNamesAreUniqueAndWellFormed(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "events.go", nil, 0)
	if err != nil {
		t.Fatalf("parse events.go: %v", err)
	}

	seen := make(map[string]string)
	count := 0
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec := spec.(*ast.ValueSpec)
			for i, name := range valueSpec.Names {
				if !strings.HasPrefix(name.Name, "Event") {
					continue
				}
				literal, ok := valueSpec.Values[i].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s is not a string literal", name.Name)
				}
				value := strings.Trim(literal.Value, `"`)
				count++
				if previous, duplicate := seen[value]; duplicate {
					t.Fatalf("event %q declared by both %s and %s", value, previous, name.Name)
				}
				seen[value] = name.Name
				domain, verb, found := strings.Cut(value, ".")
				if !found || domain == "" || verb == "" ||
					value != strings.ToLower(value) || strings.ContainsAny(value, " :-") {
					t.Fatalf("%s = %q is not a lowercase <domain>.<verb> name", name.Name, value)
				}
			}
		}
	}
	if count == 0 {
		t.Fatal("no event constants found; the registry parse is broken")
	}
}

// The approval and question event names moved to internal/approval with the
// bus refactor; what protocol still owns of that vocabulary is the entity
// registry the activity feed and inbox key on.
func TestHumanInputEntityVocabulary(t *testing.T) {
	t.Parallel()

	for _, entityType := range []EntityType{EntityApproval, EntityQuestion} {
		if !ValidEntityType(string(entityType)) {
			t.Errorf("ValidEntityType(%q) = false", entityType)
		}
	}
}
