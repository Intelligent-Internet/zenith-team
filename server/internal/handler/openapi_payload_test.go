package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docsRelativeDir is the API contract directory, from this package.
const docsRelativeDir = "../../../docs/api"

// TestDocumentedPayloadsExistInSpec is the third link in the contract, after
// the route table and the error vocabulary: it checks that the prose and the
// schemas describe the same payloads.
//
// It exists because they once did not. The markdown was rewritten to the
// redesigned shapes — actor refs embedded so a row renders without a second
// read, write receipts on mutations, runtime and manager references on an
// agent — while the schemas were carried over unchanged. Both halves linted,
// both gates passed, and 47 documented fields had no schema property. A
// generated client would have compiled against shapes the prose promised and
// the spec never declared.
//
// Every JSON example in the resource docs is complete by house rule, which is
// what makes this checkable: each field named in an example must exist as a
// property somewhere in the spec.
func TestDocumentedPayloadsExistInSpec(t *testing.T) {
	t.Parallel()

	declared := propertiesInSpec(t)

	for _, doc := range resourceDocuments(t) {
		fields := fieldsInJSONExamples(t, doc)
		var undeclared []string
		for _, field := range fields {
			if !declared[field] && !freeFormPayloadKeys[field] {
				undeclared = append(undeclared, field)
			}
		}
		if len(undeclared) > 0 {
			sort.Strings(undeclared)
			t.Errorf(
				"%s documents %d field(s) with no property anywhere in the spec:\n  %s",
				filepath.Base(doc), len(undeclared), strings.Join(undeclared, "\n  "),
			)
		}
	}
}

// freeFormPayloadKeys are keys inside deliberately unstructured objects — a
// runtime's device report, an agent's opaque settings, an activity row's
// details blob. The spec types these as free-form on purpose, so their inner
// keys have no property to match and are not drift.
var freeFormPayloadKeys = map[string]bool{
	"os": true, "arch": true, "path": true, "hash": true,
	"command": true, "payload": true, "files": true,
	"input_tokens": true, "output_tokens": true,
	"from": true, "to": true,
}

// jsonFieldPattern matches a JSON object key in a fenced example.
var jsonFieldPattern = regexp.MustCompile(`"(\w+)"\s*:`)

// jsonBlockPattern matches a ```json fenced block.
var jsonBlockPattern = regexp.MustCompile("(?s)```json\n(.*?)```")

// specPropertyPattern matches a property name declared in a schema, in either
// the block form (`name:` on its own line) or the inline form
// (`name: { … }`). The inline arm is deliberately loose: a property whose
// value is the empty schema — `from: {}` on the write receipt, which accepts
// any type — is still a declared property.
var specPropertyPattern = regexp.MustCompile(`(?m)^\s+(\w+):\s*(?:$|\{)`)

// resourceDocuments returns the prose files that carry payload examples.
// conventions/errors/overview/realtime/client-migration describe rules and
// migrations rather than one resource's wire shape.
func resourceDocuments(t *testing.T) []string {
	t.Helper()

	resources := []string{
		"activity-and-usage", "agents", "approvals", "authentication",
		"companies", "conversations", "daemon", "tasks", "inbox", "runs",
	}
	paths := make([]string, 0, len(resources))
	for _, name := range resources {
		path := filepath.Join(docsRelativeDir, name+".md")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("resource document %s is missing: %v", name, err)
		}
		paths = append(paths, path)
	}
	return paths
}

// fieldsInJSONExamples returns every object key named in the document's JSON
// examples.
func fieldsInJSONExamples(t *testing.T, path string) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	seen := make(map[string]bool)
	for _, block := range jsonBlockPattern.FindAllStringSubmatch(string(raw), -1) {
		for _, field := range jsonFieldPattern.FindAllStringSubmatch(block[1], -1) {
			seen[field[1]] = true
		}
	}
	// A resource document with no example fields is not a document this gate
	// approves — it is one this gate can no longer read. Every resource doc
	// carries JSON examples by house rule, so an empty result means the fence
	// pattern stopped matching, and the document would drop out silently while
	// the gate stayed green.
	if len(seen) == 0 {
		t.Fatalf("%s yielded no example fields; the ```json fences no longer match",
			filepath.Base(path))
	}
	return keys(seen)
}

// propertiesInSpec returns every property name declared across the split spec.
// Names rather than paths: this test asks whether a documented field exists in
// the contract at all, which is the failure that actually happened. Checking
// each field against its own schema would be stricter, and is the next thing
// to tighten once the shapes have settled.
func propertiesInSpec(t *testing.T) map[string]bool {
	t.Helper()

	specDir := filepath.Join(docsRelativeDir, "spec")
	entries, err := os.ReadDir(specDir)
	if err != nil {
		t.Fatalf("read the spec directory: %v", err)
	}

	declared := make(map[string]bool)
	files := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(filepath.Join(specDir, entry.Name())))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for _, match := range specPropertyPattern.FindAllStringSubmatch(string(raw), -1) {
			declared[match[1]] = true
		}
		files++
	}
	if files == 0 {
		t.Fatal("the spec directory holds no domain files")
	}
	return declared
}
