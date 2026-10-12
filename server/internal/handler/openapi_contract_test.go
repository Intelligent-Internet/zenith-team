package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// specRelativePath is the contract, from this package's directory.
const specRelativePath = "../../../docs/api/spec/openapi.yaml"

// plannedStatus is the x-boardly-status value marking an operation that the
// contract specifies but the server does not serve yet.
const plannedStatus = "planned"

// TestOpenAPISpecMatchesRouteTable is the link that keeps docs/api/spec/openapi.yaml
// honest. wantRouteTable is what the router actually serves; the spec is what
// the TypeScript client is generated from. If the two drift, a client is
// generated against endpoints that do not exist, or an endpoint ships with no
// contract — and nothing else in the build notices.
//
// The two sides use different parameter syntax: the router writes
// /companies/:companyId, OpenAPI writes /companies/{companyId}. The conversion
// is mechanical, so the comparison is exact rather than fuzzy.
func TestOpenAPISpecMatchesRouteTable(t *testing.T) {
	t.Parallel()

	served := make(map[string]bool, len(wantRouteTable))
	for _, route := range wantRouteTable {
		served[routeToOpenAPIPath(route)] = true
	}

	specified, planned, mislabeled := operationsInSpec(t)

	missing := difference(served, specified)
	servedAsPlanned := intersection(served, planned)
	// A planned operation is contracted deliberately ahead of the server: the
	// spec leads the implementation, which is the point of writing it first.
	// It is still surfaced, because "planned" that nobody builds is just drift
	// with a nicer name.
	extra := difference(specified, served, planned)

	if len(missing) > 0 {
		t.Errorf(
			"served but absent from docs/api/spec/openapi.yaml (%d):\n  %s",
			len(missing), strings.Join(missing, "\n  "),
		)
	}
	if len(servedAsPlanned) > 0 {
		t.Errorf(
			"served but still marked x-boardly-status: planned (%d):\n  %s",
			len(servedAsPlanned), strings.Join(servedAsPlanned, "\n  "),
		)
	}
	if len(extra) > 0 {
		t.Errorf(
			"in docs/api/spec/openapi.yaml but not served, and not marked "+
				"x-boardly-status: planned (%d):\n  %s",
			len(extra), strings.Join(extra, "\n  "),
		)
	}
	// The mechanism defines exactly one value. Any other — a typo, or an
	// invented state like "served" — would sit in the spec meaning nothing,
	// looking like it means something.
	if len(mislabeled) > 0 {
		t.Errorf(
			"unknown x-boardly-status value; only %q is defined (%d):\n  %s",
			plannedStatus, len(mislabeled), strings.Join(mislabeled, "\n  "),
		)
	}

	if awaiting := keys(planned); len(awaiting) > 0 {
		t.Logf(
			"contracted and awaiting implementation (%d):\n  %s",
			len(awaiting), strings.Join(awaiting, "\n  "),
		)
	}
}

// routeToOpenAPIPath rewrites "GET /api/v1/companies/:companyId" into
// "GET /api/v1/companies/{companyId}".
func routeToOpenAPIPath(route string) string {
	method, path, found := strings.Cut(route, " ")
	if !found {
		return route
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			segments[i] = "{" + segment[1:] + "}"
		}
	}
	return method + " " + strings.Join(segments, "/")
}

// pathItemRef matches the one form of $ref the root index uses, so this test
// resolves exactly the structure the contract is written in and nothing more:
//
//	$ref: './companies.yaml#/paths/companies_companyId'
var pathItemRef = regexp.MustCompile(`^\./([\w.-]+\.yaml)#/paths/([\w-]+)$`)

// operationsInSpec returns every "METHOD /path" the spec declares, the
// subset marked x-boardly-status: planned, and the operations carrying an
// x-boardly-status value the mechanism does not define.
//
// The spec is split one file per domain, with openapi.yaml as the index, so a
// path item is a $ref into a domain file rather than the item itself. Only
// path items are resolved: schemas and their cross-file references are the
// client generator's problem, not this test's.
func operationsInSpec(t *testing.T) (all, planned map[string]bool, mislabeled []string) {
	t.Helper()

	root := readPathMap(t, specRelativePath)
	if len(root) == 0 {
		t.Fatal("the API contract declares no paths")
	}
	specDir := filepath.Dir(specRelativePath)

	// Keys that live beside the operations in a path item and are not verbs.
	nonOperation := map[string]bool{
		"summary": true, "description": true, "servers": true, "parameters": true,
		"$ref": true,
	}

	all = make(map[string]bool)
	planned = make(map[string]bool)
	for path, item := range root {
		if node, isRef := item["$ref"]; isRef && len(item) == 1 {
			item = resolvePathItem(t, specDir, path, node)
		}
		for key, node := range item {
			if nonOperation[key] {
				continue
			}
			var operation struct {
				Status string `yaml:"x-boardly-status"`
			}
			if err := node.Decode(&operation); err != nil {
				t.Fatalf("parse operation %s %s: %v", key, path, err)
			}
			name := strings.ToUpper(key) + " " + path
			all[name] = true
			switch operation.Status {
			case "":
			case plannedStatus:
				planned[name] = true
			default:
				mislabeled = append(mislabeled, name+" (x-boardly-status: "+operation.Status+")")
			}
		}
	}
	sort.Strings(mislabeled)
	return all, planned, mislabeled
}

// readPathMap decodes just the `paths` map of one spec file.
func readPathMap(t *testing.T, path string) map[string]map[string]yaml.Node {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var document struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse %s as YAML: %v", path, err)
	}
	return document.Paths
}

// resolvePathItem follows one `$ref` from the index into its domain file.
func resolvePathItem(
	t *testing.T,
	specDir, path string,
	node yaml.Node,
) map[string]yaml.Node {
	t.Helper()

	var target string
	if err := node.Decode(&target); err != nil {
		t.Fatalf("parse the $ref for %s: %v", path, err)
	}
	match := pathItemRef.FindStringSubmatch(target)
	if match == nil {
		t.Fatalf(
			"%s points at %q, which is not the ./<domain>.yaml#/paths/<key> "+
				"form the index is written in",
			path, target,
		)
	}

	items := readPathMap(t, filepath.Join(specDir, match[1]))
	item, found := items[match[2]]
	if !found {
		t.Fatalf("%s points at %q, which that file does not define", path, target)
	}
	return item
}

func keys(set map[string]bool) []string {
	var names []string
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func difference(from map[string]bool, subtract ...map[string]bool) []string {
	var only []string
	for key := range from {
		if !containedIn(key, subtract) {
			only = append(only, key)
		}
	}
	sort.Strings(only)
	return only
}

func intersection(left, right map[string]bool) []string {
	var shared []string
	for key := range left {
		if right[key] {
			shared = append(shared, key)
		}
	}
	sort.Strings(shared)
	return shared
}

func containedIn(key string, sets []map[string]bool) bool {
	for _, set := range sets {
		if set[key] {
			return true
		}
	}
	return false
}
