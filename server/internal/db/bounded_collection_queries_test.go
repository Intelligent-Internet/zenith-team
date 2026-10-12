package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundedCollectionQueriesHaveNoPaginationPredicates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file      string
		queryName string
		orderBy   string
	}{
		{"companies.sql", "ListCompaniesForUser", "ORDER BY companies.created_at, companies.id"},
		{"companies.sql", "ListActiveCompanyMembers", "ORDER BY members.created_at, members.user_id"},
		{
			"company_resources.sql",
			"ListCompanyResourcesForOwner",
			"ORDER BY company_resources.position, company_resources.created_at, company_resources.id",
		},
		{"agent_projections.sql", "ListAgentProjectionsForActor", "ORDER BY agents.created_at, agents.id"},
	}

	for _, test := range tests {
		t.Run(test.queryName, func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", test.file))
			if err != nil {
				t.Fatalf("read %s: %v", test.file, err)
			}
			query := namedQuerySource(t, string(raw), test.queryName)
			for _, retired := range []string{"has_cursor", "after_", "page_size", "LIMIT"} {
				if strings.Contains(query, retired) {
					t.Errorf(
						"%s still contains retired pagination token %q",
						test.queryName,
						retired,
					)
				}
			}
			if !strings.Contains(query, test.orderBy) {
				t.Errorf("%s does not preserve deterministic ordering %q", test.queryName, test.orderBy)
			}
		})
	}
}

func TestBoundedCollectionQueriesApplyDocumentedFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file      string
		queryName string
		want      []string
	}{
		{
			file:      "agent_projections.sql",
			queryName: "ListAgentProjectionsForActor",
			want: []string{
				"sqlc.narg(reports_to_agent_id)",
				"agents.reports_to_agent_id = sqlc.narg(reports_to_agent_id)",
			},
		},
		{
			file:      "company_resources.sql",
			queryName: "ListCompanyResourcesForOwner",
			want: []string{
				"sqlc.narg(daemon_id)",
				"company_resources.daemon_id = sqlc.narg(daemon_id)",
			},
		},
		{
			file:      "runtimes.sql",
			queryName: "ListRuntimesForCompany",
			want: []string{
				"sqlc.narg(status)",
				"status = sqlc.narg(status)",
				"sqlc.narg(provider)",
				"provider = sqlc.narg(provider)",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.queryName, func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "queries", test.file))
			if err != nil {
				t.Fatalf("read %s: %v", test.file, err)
			}
			query := namedQuerySource(t, string(raw), test.queryName)
			for _, want := range test.want {
				if !strings.Contains(query, want) {
					t.Errorf("%s does not contain %q", test.queryName, want)
				}
			}
		})
	}
}

func namedQuerySource(t *testing.T, source, name string) string {
	t.Helper()
	marker := "-- name: " + name + " "
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("query %s not found", name)
	}
	rest := source[start+len(marker):]
	if end := strings.Index(rest, "\n-- name: "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}
