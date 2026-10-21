package migrations

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// baselineFiles are the schema files the ladder starts from, in order. Later
// migrations append after them.
var baselineFiles = []string{
	"000001_identity",
	"000002_agents",
	"000003_work",
	"000004_conversations",
	"000005_runs",
	"000006_content",
	"000007_mail_and_social",
	"000008_activity",
}

func TestEmbeddedMigrationVersionsHaveOneDirectionPair(t *testing.T) {
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatalf("Files.ReadDir() error = %v", err)
	}
	directionsByVersion := make(map[string]map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		var direction string
		switch {
		case strings.HasSuffix(name, ".up.sql"):
			direction = "up"
		case strings.HasSuffix(name, ".down.sql"):
			direction = "down"
		default:
			continue
		}
		version, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Fatalf("migration %q has no version prefix", name)
		}
		if directionsByVersion[version] == nil {
			directionsByVersion[version] = make(map[string]string, 2)
		}
		if duplicate := directionsByVersion[version][direction]; duplicate != "" {
			t.Fatalf(
				"migration version %s has duplicate %s files: %s and %s",
				version,
				direction,
				duplicate,
				name,
			)
		}
		directionsByVersion[version][direction] = name
	}
	for version, directions := range directionsByVersion {
		if directions["up"] == "" || directions["down"] == "" {
			t.Fatalf("migration version %s must have exactly one up/down pair: %+v", version, directions)
		}
	}
}

func TestEmbeddedMigrationVersionsAreContiguous(t *testing.T) {
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatalf("Files.ReadDir() error = %v", err)
	}
	versions := make([]int, 0, len(entries)/2)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			t.Fatalf("migration %q has no version prefix", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			t.Fatalf("migration %q has an invalid version prefix: %v", name, err)
		}
		versions = append(versions, version)
	}
	sort.Ints(versions)
	for index, version := range versions {
		want := index + 1
		if version != want {
			t.Fatalf(
				"migration versions are not contiguous: got version %d after %d, want %d",
				version,
				index,
				want,
			)
		}
	}
}

func TestBaselineFilesAreTheFirstVersions(t *testing.T) {
	for _, name := range baselineFiles {
		for _, direction := range []string{".up.sql", ".down.sql"} {
			if _, err := Files.ReadFile(name + direction); err != nil {
				t.Errorf("baseline file %s%s: %v", name, direction, err)
			}
		}
	}
}

// statementSQL lowercases migration content with comment lines removed, so
// notes in comments never trip statement guards.
func statementSQL(content []byte) string {
	lines := strings.Split(string(content), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.ToLower(strings.Join(kept, "\n"))
}

// withoutFunctionBodies blanks every $$-quoted function body, so a trigger's
// own UPDATE is not mistaken for a data statement.
func withoutFunctionBodies(sql string) string {
	return regexp.MustCompile(`(?s)\$\$.*?\$\$`).ReplaceAllString(sql, "$$$$")
}

func readBaseline(t *testing.T, name string) string {
	t.Helper()
	content, err := Files.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return statementSQL(content)
}

// baselineSeeds are the reference rows a fresh database needs, by file. They
// are the baseline's only data statements.
var baselineSeeds = map[string][]string{
	"000008_activity": {"model_prices"},
}

// The baseline is the schema itself: tables, constraints, indexes, views,
// functions and triggers, plus the seeds above. A fresh database has no rows
// to backfill or fix.
func TestBaselineIsSchemaOnly(t *testing.T) {
	t.Parallel()
	data := regexp.MustCompile(`(?m)^\s*(insert into \w+|update |delete from|truncate\b|copy |do \$\$)`)
	for _, name := range baselineFiles {
		sql := withoutFunctionBodies(readBaseline(t, name+".up.sql"))
		for _, found := range data.FindAllString(sql, -1) {
			found = strings.TrimSpace(found)
			table, isInsert := strings.CutPrefix(found, "insert into ")
			if isInsert && slices.Contains(baselineSeeds[name], table) {
				continue
			}
			t.Errorf("%s.up.sql contains the data statement %q", name, found)
		}
		for _, table := range baselineSeeds[name] {
			if !strings.Contains(sql, "insert into "+table+" ") {
				t.Errorf("%s.up.sql lost its %s seed", name, table)
			}
		}
	}
}

// Persistent procedures stay limited: plpgsql and trigger functions exist only
// for the discussion integrity checks and channel revision fences, plus the
// three LANGUAGE sql STABLE predicates that each state one access rule
// (ADR 0017). Services still own product writes, lifecycle transitions and
// events.
func TestMigrationsLimitProceduralObjectsToDiscussionConstraints(t *testing.T) {
	t.Parallel()
	functionFile := map[string]string{
		"project_permission_held":              "000003_work.up.sql",
		"delegate_permission_held":             "000004_conversations.up.sql",
		"check_discussion_shape":               "000004_conversations.up.sql",
		"check_discussion_item_shape":          "000004_conversations.up.sql",
		"check_discussion_collaboration_shape": "000004_conversations.up.sql",
		"preserve_discussion_identity":         "000004_conversations.up.sql",
		"advance_channel_execution_revision":   "000004_conversations.up.sql",
		"run_permission_held":                  "000005_runs.up.sql",
		"reject_discussion_run":                "000005_runs.up.sql",
	}
	triggerFile := map[string]string{
		"channel_archive_generation/conversations":                      "000004_conversations.up.sql",
		"channel_context_revision/conversation_items":                   "000004_conversations.up.sql",
		"discussion_shape/conversations":                                "000004_conversations.up.sql",
		"discussion_shape/conversation_human_directs":                   "000004_conversations.up.sql",
		"discussion_shape/conversation_channels":                        "000004_conversations.up.sql",
		"discussion_shape/conversation_members":                         "000004_conversations.up.sql",
		"discussion_shape/conversation_agents":                          "000004_conversations.up.sql",
		"discussion_identity/conversations":                             "000004_conversations.up.sql",
		"discussion_identity/conversation_human_directs":                "000004_conversations.up.sql",
		"discussion_identity/conversation_channels":                     "000004_conversations.up.sql",
		"discussion_identity/conversation_members":                      "000004_conversations.up.sql",
		"discussion_identity/conversation_agents":                       "000004_conversations.up.sql",
		"discussion_item_shape/conversation_items":                      "000004_conversations.up.sql",
		"discussion_collaboration_shape/conversation_message_pins":      "000004_conversations.up.sql",
		"discussion_collaboration_shape/conversation_message_reactions": "000004_conversations.up.sql",
		"discussion_collaboration_shape/conversation_thread_states":     "000004_conversations.up.sql",
		"discussion_run_guard/runs":                                     "000005_runs.up.sql",
	}
	predicates := []string{"project_permission_held", "delegate_permission_held", "run_permission_held"}

	triggers := regexp.MustCompile(`create (?:constraint )?trigger (\w+) [^;]*? on (\w+)`)
	functions := regexp.MustCompile(`create (?:or replace )?function ([\w.]+)\(`)
	predicate := regexp.MustCompile(
		`create (?:or replace )?function (\w+)\([^$]*?\) returns boolean\s+language sql stable\s+as \$\$`,
	)
	seenFunctions := make(map[string]bool)
	seenTriggers := make(map[string]bool)
	seenPredicates := make(map[string]bool)
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		sql := readBaseline(t, name)
		for _, match := range triggers.FindAllStringSubmatch(sql, -1) {
			key := match[1] + "/" + match[2]
			if triggerFile[key] != name || seenTriggers[key] {
				t.Errorf("%s declares unapproved trigger %s", name, key)
				continue
			}
			seenTriggers[key] = true
		}
		for _, match := range functions.FindAllStringSubmatch(sql, -1) {
			if strings.HasPrefix(match[1], "pg_temp.") {
				continue
			}
			if functionFile[match[1]] != name || seenFunctions[match[1]] {
				t.Errorf("%s declares unapproved persistent function %s", name, match[1])
				continue
			}
			seenFunctions[match[1]] = true
		}
		for _, match := range predicate.FindAllStringSubmatch(sql, -1) {
			seenPredicates[match[1]] = true
		}
	}
	for name := range functionFile {
		if !seenFunctions[name] {
			t.Errorf("missing pinned function %s", name)
		}
	}
	for name := range triggerFile {
		if !seenTriggers[name] {
			t.Errorf("missing discussion integrity trigger %s", name)
		}
	}
	for _, name := range predicates {
		if !seenPredicates[name] {
			t.Errorf("%s must stay one LANGUAGE sql STABLE predicate", name)
		}
	}
}

// Usage facts are immutable and FK-free: a Run's usage outlives the Run's
// other rows and is never rewritten.
func TestRunUsageIsImmutableAndFKFree(t *testing.T) {
	t.Parallel()
	sql := readBaseline(t, "000005_runs.up.sql")
	table := regexp.MustCompile(`(?s)create table run_usage \(.*?\n\);`).FindString(sql)
	if table == "" {
		t.Fatal("000005_runs.up.sql does not create run_usage")
	}
	for _, required := range []string{
		"primary key (run_id, provider, model)", "raw_json jsonb not null", "created_at timestamptz",
	} {
		if !strings.Contains(table, required) {
			t.Errorf("run_usage is missing %q", required)
		}
	}
	for _, removed := range []string{
		"worker_id", "foreign key", "references ", "updated_at",
		"provider_cost_state", "rollup_applied_hash",
	} {
		if strings.Contains(table, removed) {
			t.Errorf("run_usage retained removed vocabulary or constraint %q", removed)
		}
	}
	for _, retired := range []string{
		"usage_hourly", "usage_counted_runs", "usage_runs_hourly", "usage_run_model_memberships",
		"usage_tasks_hourly", "usage_backfill_state",
	} {
		if strings.Contains(sql, "create table "+retired) {
			t.Errorf("000005_runs.up.sql recreates the retired table %s", retired)
		}
	}
}

// Watches for resurrection of deleted tables.
func TestNoMigrationDeclaresDeletedTables(t *testing.T) {
	t.Parallel()
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatalf("Files.ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		sql := readBaseline(t, name)
		for _, forbidden := range []string{
			"create table realtime_outbox", "create table agent_sessions",
			"create table run_inputs", "create table run_input_messages",
			"create table conversation_reads", "create table inbox_user_state",
			"create table executor_tokens", "create table workers",
			"create table executors", "create table run_events",
			"create table run_questions", "create table missions",
			"create table commerce_", "create table deliverable_review_actions",
			"create table run_identity_mappings",
		} {
			if strings.Contains(sql, forbidden) {
				t.Errorf("migration %q contains forbidden statement %q", name, forbidden)
			}
		}
	}
}

// Each down file drops exactly the tables its up file creates, newest first,
// so children always go before the tables they reference.
func TestDownFilesDropTheirTablesChildrenFirst(t *testing.T) {
	t.Parallel()
	created := regexp.MustCompile(`(?m)^create table (\w+) \(`)
	dropped := regexp.MustCompile(`(?m)^drop table if exists (\w+) cascade;`)
	for _, name := range baselineFiles {
		var want []string
		for _, match := range created.FindAllStringSubmatch(readBaseline(t, name+".up.sql"), -1) {
			want = append(want, match[1])
		}
		slices.Reverse(want)
		var got []string
		for _, match := range dropped.FindAllStringSubmatch(readBaseline(t, name+".down.sql"), -1) {
			got = append(got, match[1])
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s.down.sql drops %v, want the up file's tables newest first %v", name, got, want)
		}
	}
}
