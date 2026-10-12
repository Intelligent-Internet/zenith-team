package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBulkRunFailureQueriesDeferRunLocksUntilConversationLock(t *testing.T) {
	t.Parallel()

	source := queryFileSource(t, "runs.sql")
	tests := []struct {
		name       string
		candidate  string
		transition string
		fences     []string
	}{
		{
			name:       "offline runtime",
			candidate:  "ListOfflineRuntimeFailureCandidates",
			transition: "FailRunsForOfflineRuntimes",
			fences:     []string{"'offline'", "'waiting_local_directory'"},
		},
		{
			name:       "stale run",
			candidate:  "ListStaleRunFailureCandidates",
			transition: "FailStaleRuns",
			fences: []string{
				"dispatch_timeout_secs", "running_timeout_secs", "runtime_stale_secs",
				"prepare_lease_expires_at",
			},
		},
		{
			name:       "queued run expiry",
			candidate:  "ListQueuedRunExpiryCandidates",
			transition: "ExpireStaleQueuedRuns",
			fences:     []string{"ttl_secs", "'queued'"},
		},
		{
			name:       "runtime recovery",
			candidate:  "ListRuntimeRecoveryFailureCandidates",
			transition: "RecoverOrphanedRunsForRuntime",
			fences:     []string{"runtime_id", "'waiting_local_directory'"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := normalizedQuery(t, source, test.candidate)
			if strings.Contains(candidate, "for update") {
				t.Fatalf("%s locks Run rows before Conversation locks: %s", test.candidate, candidate)
			}
			transition := normalizedQuery(t, source, test.transition)
			if !strings.Contains(transition, "id = any(sqlc.arg(run_ids)::uuid[])") {
				t.Fatalf("%s is not restricted to selected Run ids: %s", test.transition, transition)
			}
			for _, fence := range test.fences {
				if !strings.Contains(candidate, fence) || !strings.Contains(transition, fence) {
					t.Errorf(
						"candidate/transition pair %s/%s does not recheck %q",
						test.candidate,
						test.transition,
						fence,
					)
				}
			}
		})
	}
}

// The lock order inside a transaction is company -> member -> project ->
// conversation -> run (pkg/db/queries/README.md). A Project lock taken after a
// Conversation or Run lock can deadlock against a grant or visibility change,
// which locks the Project and then cancels Runs. This scan reads every
// production Go function (closures included, so a WithinTx callback counts as
// its enclosing helper) and fails when a Project lock call follows a
// Conversation or Run lock call in source order, or a Group lock call follows
// a Project, Conversation or Run lock call (company -> member -> group ->
// project -> conversation -> run, Access TECH section 13).
func TestProjectLocksPrecedeConversationAndRunLocks(t *testing.T) {
	t.Parallel()

	isLock := func(query string) bool {
		return strings.Contains(query, "for update") || strings.Contains(query, "for share")
	}
	projectLocks := map[string]bool{"LockProjectAccess": true}
	for file, names := range map[string][]string{
		"project_access.sql": {"LockProjectForAccessRead", "LockProjectForAccessChange"},
		"projects.sql":       {"GetProjectForUpdate", "GetTaskProjectForShare", "LockCompanyPlanProjects"},
	} {
		source := queryFileSource(t, file)
		for _, name := range names {
			if !isLock(normalizedQuery(t, source, name)) {
				t.Fatalf("%s is listed as a Project lock but takes no row lock", name)
			}
			projectLocks[name] = true
		}
	}
	laterLocks := map[string]bool{}
	queryName := regexp.MustCompile(`(?m)^-- name: (\w+)`)
	for _, file := range []string{"conversations.sql", "runs.sql"} {
		source := queryFileSource(t, file)
		for _, match := range queryName.FindAllStringSubmatch(source, -1) {
			if isLock(normalizedQuery(t, source, match[1])) {
				laterLocks[match[1]] = true
			}
		}
	}
	if len(laterLocks) == 0 {
		t.Fatal("found no Conversation or Run lock queries")
	}
	// A Group lock (Access TECH D3, D4) comes before every Project,
	// Conversation and Run lock: company -> member -> group -> project.
	groupLocks := map[string]bool{}
	groupSource := queryFileSource(t, "groups.sql")
	for _, name := range []string{"LockCompanyGroupForChange", "LockCompanyGroupForShare"} {
		if !isLock(normalizedQuery(t, groupSource, name)) {
			t.Fatalf("%s is listed as a Group lock but takes no row lock", name)
		}
		groupLocks[name] = true
	}

	fset := token.NewFileSet()
	for _, root := range []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				function, ok := decl.(*ast.FuncDecl)
				if !ok || function.Body == nil {
					continue
				}
				var firstLater, firstProject string
				ast.Inspect(function.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					var name string
					switch fun := call.Fun.(type) {
					case *ast.SelectorExpr:
						name = fun.Sel.Name
					case *ast.Ident:
						name = fun.Name
					}
					switch {
					case groupLocks[name] && (firstProject != "" || firstLater != ""):
						t.Errorf(
							"%s: %s takes %s after %s%s",
							fset.Position(call.Pos()),
							function.Name.Name,
							name,
							firstProject,
							firstLater,
						)
					case laterLocks[name] && firstLater == "":
						firstLater = name
					case projectLocks[name] && firstLater != "":
						t.Errorf("%s: %s takes %s after %s", fset.Position(call.Pos()), function.Name.Name, name, firstLater)
					case projectLocks[name] && firstProject == "":
						firstProject = name
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
}
