package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestHandlerBoundaryErrorsUseCentralWriter keeps response triples out of
// handlers. A handler names a boundary error; errors.go is the only place
// allowed to translate that name into status, detail, and code.
func TestHandlerBoundaryErrorsUseCentralWriter(t *testing.T) {
	t.Parallel()

	files := token.NewFileSet()
	for path, parsedFile := range parseSourceFiles(t, files, ".") {
		if filepath.Base(path) == "errors.go" {
			continue
		}
		ast.Inspect(parsedFile, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if ok && isWriteError(call.Fun) {
				t.Errorf(
					"%s: call writeBoundaryError with a named key; only errors.go may call util.WriteError",
					files.Position(call.Pos()),
				)
			}
			return true
		})
	}
}

// parseSourceFiles parses every non-test .go file directly inside directory,
// keyed by its path. These tests read source text, not a type-checked package,
// so the stdlib parser is enough — and reading the directory here keeps them
// off parser.ParseDir, which Go 1.25 deprecated.
func parseSourceFiles(t *testing.T, files *token.FileSet, directory string) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read %s: %v", directory, err)
	}
	parsed := make(map[string]*ast.File, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(directory, name)
		file, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		parsed[path] = file
	}
	return parsed
}

// TestDirectErrorWritesAreRegistered checks the remaining centralized writer
// and middleware calls. A literal code must be in the registry with the same
// status, otherwise it can reach a client without appearing in the documented
// contract.
func TestDirectErrorWritesAreRegistered(t *testing.T) {
	t.Parallel()

	statuses := map[string]int{
		"StatusBadRequest":            http.StatusBadRequest,
		"StatusUnauthorized":          http.StatusUnauthorized,
		"StatusForbidden":             http.StatusForbidden,
		"StatusNotFound":              http.StatusNotFound,
		"StatusConflict":              http.StatusConflict,
		"StatusGone":                  http.StatusGone,
		"StatusRequestEntityTooLarge": http.StatusRequestEntityTooLarge,
		"StatusUnsupportedMediaType":  http.StatusUnsupportedMediaType,
		"StatusUnprocessableEntity":   http.StatusUnprocessableEntity,
		"StatusTooManyRequests":       http.StatusTooManyRequests,
		"StatusInternalServerError":   http.StatusInternalServerError,
		"StatusBadGateway":            http.StatusBadGateway,
		"StatusServiceUnavailable":    http.StatusServiceUnavailable,
	}

	files := token.NewFileSet()
	for _, directory := range []string{".", "../middleware"} {
		for _, parsedFile := range parseSourceFiles(t, files, directory) {
			ast.Inspect(parsedFile, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || len(call.Args) != 4 || !isUtilWriteError(call.Fun) {
					return true
				}
				literal, ok := call.Args[3].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				code, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Errorf("%s: parse error code: %v", files.Position(call.Pos()), err)
					return true
				}
				statusName, ok := httpStatusName(call.Args[1])
				if !ok {
					t.Errorf(
						"%s: literal error code %q has a non-literal status",
						files.Position(call.Pos()),
						code,
					)
					return true
				}
				status, ok := statuses[statusName]
				if !ok {
					t.Errorf(
						"%s: unsupported HTTP status constant %s",
						files.Position(call.Pos()),
						statusName,
					)
					return true
				}
				registered, ok := errorCodeRegistry[code]
				if !ok {
					t.Errorf(
						"%s: direct error code %q is not registered",
						files.Position(call.Pos()),
						code,
					)
				} else if registered != status {
					t.Errorf(
						"%s: direct error code %q uses status %d; registry says %d",
						files.Position(call.Pos()), code, status, registered,
					)
				}
				return true
			})
		}
	}
}

// TestBoundaryErrorsAreRegistered checks the handler-owned half of the error
// vocabulary by inspecting its response table as values.
func TestBoundaryErrorsAreRegistered(t *testing.T) {
	t.Parallel()

	for key, response := range boundaryErrorResponses {
		if response.detail == "" {
			t.Errorf("%s has an empty detail", key)
		}
		status, registered := errorCodeRegistry[response.code]
		if !registered {
			t.Errorf("%s emits unregistered code %q", key, response.code)
			continue
		}
		if status != response.status {
			t.Errorf(
				"%s emits %q with status %d; the registry says %d",
				key, response.code, response.status, status,
			)
		}
	}
}

func isWriteError(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "WriteError"
}

func isUtilWriteError(expression ast.Expr) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "WriteError" {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == "util"
}

func httpStatusName(expression ast.Expr) (string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return selector.Sel.Name, ok && identifier.Name == "http"
}

// errorDocRelativePath is the human-readable half of the error contract.
const errorDocRelativePath = "../../../docs/api/errors.md"

// TestErrorMappingsAreRegistered walks every per-domain mapping table as a Go
// value and checks each row against errorCodeRegistry. Because it reads the
// tables themselves rather than the source text, a renamed code or a changed
// status cannot slip past it.
func TestErrorMappingsAreRegistered(t *testing.T) {
	t.Parallel()

	for domain, mappings := range registeredErrorMappings {
		for _, mapping := range mappings {
			status, registered := errorCodeRegistry[mapping.code]
			if !registered {
				t.Errorf(
					"%s emits %q, which is not in errorCodeRegistry",
					domain, mapping.code,
				)
				continue
			}
			if status != mapping.status {
				t.Errorf(
					"%s emits %q with status %d; the registry says %d",
					domain, mapping.code, mapping.status, status,
				)
			}
		}
	}
}

// TestErrorCodesMatchDocumentation diffs errorCodeRegistry against the tables
// in docs/api/errors.md. That document claims to be complete in both
// directions, and this is what makes the claim true: a code cannot reach the
// API without being documented, and cannot be documented without existing.
//
// Codes under the "Planned codes" heading are contracted ahead of the server
// and are allowed to have no producer, the same way x-zenith-team-status lets an
// operation lead its implementation.
func TestErrorCodesMatchDocumentation(t *testing.T) {
	t.Parallel()

	documented, planned := errorCodesInDocumentation(t)
	emittedAsPlanned := intersection(setOfKeys(errorCodeRegistry), planned)

	undocumented := difference(setOfKeys(errorCodeRegistry), documented, planned)
	if len(undocumented) > 0 {
		t.Errorf(
			"emitted but absent from docs/api/errors.md (%d):\n  %s",
			len(undocumented), strings.Join(undocumented, "\n  "),
		)
	}

	unemitted := difference(documented, setOfKeys(errorCodeRegistry))
	if len(unemitted) > 0 {
		t.Errorf(
			"documented in docs/api/errors.md but not emitted (%d):\n  %s",
			len(unemitted), strings.Join(unemitted, "\n  "),
		)
	}
	if len(emittedAsPlanned) > 0 {
		t.Errorf(
			"emitted but still documented as planned (%d):\n  %s",
			len(emittedAsPlanned), strings.Join(emittedAsPlanned, "\n  "),
		)
	}

	if awaiting := keys(planned); len(awaiting) > 0 {
		t.Logf(
			"contracted and awaiting implementation (%d):\n  %s",
			len(awaiting), strings.Join(awaiting, "\n  "),
		)
	}
}

// docCodePattern matches a code cell at the start of a documentation table
// row: a backticked lower_snake token in the first column.
var docCodePattern = regexp.MustCompile("^\\|\\s*`([a-z][a-z0-9_]*)`\\s*\\|")

// docStatusCodePattern matches the rows in the mixed tables that lead with a
// status column instead: | 413 | `request_too_large` | … .
var docStatusCodePattern = regexp.MustCompile("^\\|\\s*(\\d{3})\\s*\\|\\s*`([a-z][a-z0-9_]*)`\\s*\\|")

// errorCodesInDocumentation returns every code named in a table in errors.md,
// and the subset under the "Planned codes" heading.
func errorCodesInDocumentation(t *testing.T) (all, planned map[string]bool) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(errorDocRelativePath))
	if err != nil {
		t.Fatalf("read the error contract: %v", err)
	}

	all = make(map[string]bool)
	planned = make(map[string]bool)

	// Two headings carry rows that are not part of the emitted vocabulary:
	// the planned block, and the rename table whose left column names codes
	// that are deliberately no longer served.
	const plannedHeading = "## Planned codes"
	const renameHeading = "## Codes this contract renames"

	inPlanned, inRenames := false, false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "## ") {
			inPlanned = strings.HasPrefix(line, plannedHeading)
			inRenames = strings.HasPrefix(line, renameHeading)
			continue
		}
		if inRenames {
			continue
		}

		code := ""
		if match := docCodePattern.FindStringSubmatch(line); match != nil {
			code = match[1]
		} else if match := docStatusCodePattern.FindStringSubmatch(line); match != nil {
			if _, err := strconv.Atoi(match[1]); err == nil {
				code = match[2]
			}
		}
		if code == "" {
			continue
		}

		if inPlanned {
			planned[code] = true
			continue
		}
		all[code] = true
	}

	if len(all) == 0 {
		t.Fatal("docs/api/errors.md documents no error codes")
	}
	return all, planned
}

func setOfKeys(registry map[string]int) map[string]bool {
	set := make(map[string]bool, len(registry))
	for code := range registry {
		set[code] = true
	}
	return set
}
