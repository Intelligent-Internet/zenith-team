package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestSpecSchemasAreReachable fails when a named schema is defined but nothing
// references it.
//
// An orphaned schema is worse than dead weight: it reads as part of the
// contract, it renders nowhere, and no generated client has a type for it. The
// `Changes` write receipt was exactly that — `conventions.md` made receipts a
// rule on every mutation, eight prose files showed one, and the schema sat
// unreferenced by any operation. The payload gate could not see it, because
// that test asks whether a documented *field* exists somewhere, and `Changes`
// is a schema name.
//
// A schema is reachable if any file references it by `$ref`, whether from an
// operation or from another schema.
func TestSpecSchemasAreReachable(t *testing.T) {
	t.Parallel()

	defined, referenced := schemaGraph(t)

	var orphans []string
	for name, file := range defined {
		if !referenced[name] {
			orphans = append(orphans, file+" "+name)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Errorf(
			"schema(s) defined but referenced by nothing (%d). Wire each into the "+
				"operation that returns it, or delete it — an unreachable schema "+
				"reads as contract and generates no client type:\n  %s",
			len(orphans), strings.Join(orphans, "\n  "),
		)
	}
}

var (
	schemaDefinitionLine = regexp.MustCompile(`^  ([A-Z]\w*):\s*$`)
	schemaReferenceLine  = regexp.MustCompile(`\$ref:\s*'[^']*#/schemas/(\w+)'`)
)

// schemaGraph returns every schema the split spec defines, mapped to the file
// defining it, and the set referenced from anywhere.
func schemaGraph(t *testing.T) (defined map[string]string, referenced map[string]bool) {
	t.Helper()

	specDir := filepath.Join(docsRelativeDir, "spec")
	entries, err := os.ReadDir(specDir)
	if err != nil {
		t.Fatalf("read the spec directory: %v", err)
	}

	defined = make(map[string]string)
	referenced = make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(filepath.Join(specDir, entry.Name())))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}

		inSchemas := false
		for _, line := range strings.Split(string(raw), "\n") {
			// `schemas:` at column zero opens the block in a domain file; any
			// other top-level key closes it.
			if !strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "" {
				inSchemas = strings.HasPrefix(line, "schemas:")
			}
			if inSchemas {
				if match := schemaDefinitionLine.FindStringSubmatch(line); match != nil {
					if previous, exists := defined[match[1]]; exists {
						t.Errorf(
							"schema %s is defined in both %s and %s",
							match[1], previous, entry.Name(),
						)
					}
					defined[match[1]] = entry.Name()
				}
			}
			for _, match := range schemaReferenceLine.FindAllStringSubmatch(line, -1) {
				referenced[match[1]] = true
			}
		}
	}

	if len(defined) == 0 {
		t.Fatal("the spec defines no schemas")
	}
	return defined, referenced
}
