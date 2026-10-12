package migrations

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

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

// statementSQL lowercases migration content with comment lines removed, so
// doctrine notes ("usage facts are FK-free") never trip statement guards.
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

// Persistent procedures stay limited: plpgsql and trigger functions exist only
// for the discussion integrity checks and channel revision fences. A
// LANGUAGE sql STABLE predicate function stating a single access rule is
// allowed when it is pinned here (project_permission_held, ADR 0017, which
// 000101 gives a Group arm and its down file restores;
// delegate_permission_held and run_permission_held, the delegate rule of D31,
// replaced by 000100; conversation_audience_reads, which 000100 drops and its
// down file restores). Services still own product writes, lifecycle
// transitions and events.
func TestMigrationsLimitProceduralObjectsToDiscussionConstraints(t *testing.T) {
	t.Parallel()
	allowedFunctions := map[string]bool{
		"advance_channel_execution_revision":   false,
		"check_discussion_shape":               false,
		"preserve_discussion_identity":         false,
		"check_discussion_item_shape":          false,
		"reject_discussion_run":                false,
		"check_discussion_collaboration_shape": false,
		"project_permission_held":              false,
		"conversation_audience_reads":          false,
		"delegate_permission_held":             false,
		"run_permission_held":                  false,
	}
	allowedTriggers := map[string]bool{
		"channel_archive_generation/conversations":                      false,
		"channel_context_revision/conversation_items":                   false,
		"discussion_shape/conversations":                                false,
		"discussion_shape/conversation_human_directs":                   false,
		"discussion_shape/conversation_channels":                        false,
		"discussion_shape/conversation_members":                         false,
		"discussion_shape/conversation_agents":                          false,
		"discussion_identity/conversations":                             false,
		"discussion_identity/conversation_human_directs":                false,
		"discussion_identity/conversation_channels":                     false,
		"discussion_identity/conversation_members":                      false,
		"discussion_identity/conversation_agents":                       false,
		"discussion_item_shape/conversation_items":                      false,
		"discussion_run_guard/runs":                                     false,
		"discussion_collaboration_shape/conversation_message_pins":      false,
		"discussion_collaboration_shape/conversation_message_reactions": false,
		"discussion_collaboration_shape/conversation_thread_states":     false,
	}
	declared := make(map[string]bool)
	triggers := regexp.MustCompile(`create (?:constraint )?trigger (\w+) [^;]*? on (\w+)`)
	functions := regexp.MustCompile(`create (?:or replace )?function ([\w.]+)`)
	entries, err := Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		content, err := Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sql := statementSQL(content)
		for _, match := range triggers.FindAllStringSubmatch(sql, -1) {
			key := match[1] + "/" + match[2]
			seen, allowed := allowedTriggers[key]
			origin := "000031_discussions.up.sql"
			if match[1] == "discussion_collaboration_shape" {
				origin = "000033_discussion_collaboration.up.sql"
			}
			if match[1] == "channel_archive_generation" || match[1] == "channel_context_revision" {
				origin = "000042_channel_agent_invocations.up.sql"
			}
			if name != origin || !allowed || seen {
				t.Errorf("%s declares unapproved trigger %s", name, key)
				continue
			}
			allowedTriggers[key] = true
		}
		for _, match := range functions.FindAllStringSubmatch(sql, -1) {
			if strings.HasPrefix(match[1], "pg_temp.") {
				continue
			}
			// Group DM invocations extend the existing item constraint and revision fence.
			if (name == "000056_direct_agent_invocations.up.sql" || name == "000056_direct_agent_invocations.down.sql") &&
				(match[1] == "check_discussion_item_shape" || match[1] == "advance_channel_execution_revision") {
				continue
			}
			// 000053 only moves the channel answer's invocation lookup to a column.
			if (name == "000033_discussion_collaboration.up.sql" || name == "000033_discussion_collaboration.down.sql" ||
				name == "000047_direct_message_system_items.up.sql" || name == "000047_direct_message_system_items.down.sql" ||
				name == "000053_channel_answer_invocation.up.sql" || name == "000053_channel_answer_invocation.down.sql") &&
				match[1] == "check_discussion_item_shape" {
				continue
			}
			// Transcript cards extend only the existing Huddle item constraint.
			if (name == "000083_huddle_transcripts.up.sql" || name == "000083_huddle_transcripts.down.sql" ||
				name == "000084_huddle_source_cards.up.sql" || name == "000084_huddle_source_cards.down.sql") &&
				match[1] == "check_discussion_item_shape" {
				continue
			}
			// Group DMs only revise the existing discussion shape constraint.
			if (name == "000044_group_direct_messages.up.sql" || name == "000044_group_direct_messages.down.sql" ||
				name == "000045_mixed_direct_messages.up.sql" || name == "000045_mixed_direct_messages.down.sql") &&
				match[1] == "check_discussion_shape" {
				continue
			}
			if (name == "000046_direct_message_invitations.up.sql" || name == "000046_direct_message_invitations.down.sql") && match[1] == "preserve_discussion_identity" {
				continue
			}
			// General only revises the existing shape and identity constraints.
			if (name == "000032_general_channel.up.sql" || name == "000032_general_channel.down.sql" ||
				name == "000070_huddles.up.sql" || name == "000070_huddles.down.sql") &&
				(match[1] == "check_discussion_shape" || match[1] == "preserve_discussion_identity") {
				continue
			}
			// A Board's channel only revises the existing shape and identity
			// constraints; its down file restores the 000070 bodies.
			if (name == "000108_whiteboard_collaboration.up.sql" || name == "000108_whiteboard_collaboration.down.sql") &&
				(match[1] == "check_discussion_shape" || match[1] == "preserve_discussion_identity") {
				continue
			}
			if (name == "000042_channel_agent_invocations.up.sql" || name == "000042_channel_agent_invocations.down.sql") && match[1] == "check_discussion_item_shape" {
				continue
			}
			if (name == "000043_channel_answer_collaboration.up.sql" || name == "000043_channel_answer_collaboration.down.sql") && match[1] == "check_discussion_collaboration_shape" {
				continue
			}
			if (name == "000070_huddles.up.sql" || name == "000070_huddles.down.sql") &&
				(match[1] == "check_discussion_item_shape" || match[1] == "reject_discussion_run") {
				continue
			}
			_, allowed := allowedFunctions[match[1]]
			origins := []string{"000031_discussions.up.sql"}
			if match[1] == "check_discussion_collaboration_shape" {
				origins = []string{"000033_discussion_collaboration.up.sql"}
			}
			if match[1] == "advance_channel_execution_revision" {
				origins = []string{"000042_channel_agent_invocations.up.sql"}
			}
			// 000101 adds the Group arm; its down file restores the 000086 body.
			if match[1] == "project_permission_held" {
				origins = []string{
					"000086_project_access.up.sql",
					"000101_groups.up.sql",
					"000101_groups.down.sql",
				}
			}
			// 000100 drops the audience; its down file restores the 000086 body.
			if match[1] == "conversation_audience_reads" {
				origins = []string{
					"000086_project_access.up.sql",
					"000100_runs_use_their_authorizers_access.down.sql",
				}
			}
			// 000100 replaces both delegate functions, and its down file
			// restores the 000099 forms.
			if match[1] == "delegate_permission_held" || match[1] == "run_permission_held" {
				origins = []string{
					"000099_delegate_permission_held.up.sql",
					"000100_runs_use_their_authorizers_access.up.sql",
					"000100_runs_use_their_authorizers_access.down.sql",
				}
			}
			declaration := name + "/" + match[1]
			if !slices.Contains(origins, name) || !allowed || declared[declaration] {
				t.Errorf("%s declares unapproved persistent function %s", name, match[1])
				continue
			}
			declared[declaration] = true
			allowedFunctions[match[1]] = true
		}
	}
	for name, seen := range allowedFunctions {
		if !seen {
			t.Errorf("missing pinned function %s", name)
		}
	}
	predicate := regexp.MustCompile(`\) returns boolean\s+language sql stable\s+as \$\$`)
	for _, pinned := range []struct {
		file, names string
		count       int
	}{
		{"000086_project_access.up.sql", "project_permission_held and conversation_audience_reads", 2},
		{"000099_delegate_permission_held.up.sql", "delegate_permission_held and run_permission_held", 2},
		{"000100_runs_use_their_authorizers_access.up.sql", "delegate_permission_held and run_permission_held", 2},
		{
			"000100_runs_use_their_authorizers_access.down.sql",
			"conversation_audience_reads, delegate_permission_held and run_permission_held", 3,
		},
		{"000101_groups.up.sql", "project_permission_held", 1},
		{"000101_groups.down.sql", "project_permission_held", 1},
	} {
		access, err := Files.ReadFile(pinned.file)
		if err != nil {
			t.Fatal(err)
		}
		if predicates := predicate.FindAllString(statementSQL(access), -1); len(predicates) != pinned.count {
			t.Errorf("%s declares %d LANGUAGE sql STABLE predicates; %s must each stay one",
				pinned.file, len(predicates), pinned.names)
		}
	}
	for name, seen := range allowedTriggers {
		if !seen {
			t.Errorf("missing discussion integrity trigger %s", name)
		}
	}
}

func TestUsageMigrationIsDDLOnlyAndDeclaresImmutableRunUsage(t *testing.T) {
	t.Parallel()
	content, err := Files.ReadFile("000007_usage.up.sql")
	if err != nil {
		t.Fatalf("read usage migration: %v", err)
	}
	sql := statementSQL(content)
	for _, forbidden := range []string{
		"insert into", "update ", "delete from", "create trigger",
		"create function", "language plpgsql", "do $$",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("usage migration contains forbidden data/procedural statement %q", forbidden)
		}
	}
	for _, required := range []string{
		"create table run_usage", "primary key (run_id, provider, model)",
		"raw_json jsonb not null", "created_at timestamp with time zone",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("usage migration is missing %q", required)
		}
	}
	for _, removed := range []string{
		"worker_id", "foreign key", "references ", "updated_at", "reasoning_tokens",
		"provider_cost_state", "rollup_applied_hash", "usage_hourly", "usage_counted_runs",
		"usage_runs_hourly", "usage_run_model_memberships", "usage_tasks_hourly",
		"usage_backfill_state",
	} {
		if strings.Contains(sql, removed) {
			t.Errorf("usage migration retained removed vocabulary or constraint %q", removed)
		}
	}
}

// The identity/tenancy/work/execution migrations carry real FK edges;
// only usage facts stay FK-free, which the usage test above
// pins. This guard now watches for resurrection of deleted tables only.
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
		content, err := Files.ReadFile(name)
		if err != nil {
			t.Fatalf("read migration %q: %v", name, err)
		}
		sql := statementSQL(content)
		for _, forbidden := range []string{
			"create table realtime_outbox", "create table agent_sessions",
			"create table run_inputs", "create table run_input_messages",
			"create table conversation_reads", "create table inbox_user_state",
			"create table executor_tokens", "create table workers",
			"create table executors", "create table run_events",
			"create table run_questions", "create table missions",
		} {
			if strings.Contains(sql, forbidden) {
				t.Errorf("migration %q contains forbidden statement %q", name, forbidden)
			}
		}
	}
}

func TestDeliverableTablesRollbackChildrenFirst(t *testing.T) {
	t.Parallel()
	content, err := Files.ReadFile("000009_outputs.down.sql")
	if err != nil {
		t.Fatalf("read Deliverables rollback: %v", err)
	}
	sql := statementSQL(content)
	childrenFirst := []string{
		"drop table output_publications",
		"drop table deliverable_review_actions",
		"drop table deliverables",
		"drop table output_items",
		"drop table outputs",
		"drop table asset_versions",
		"drop table assets",
		"drop constraint inbox_items_company_id_id_key",
	}
	previous := -1
	for _, statement := range childrenFirst {
		position := strings.Index(sql, statement)
		if position < 0 || position <= previous {
			t.Fatalf("Deliverables rollback is not children-first at %q:\n%s", statement, sql)
		}
		previous = position
	}
}

// D31 (000098): the session client is one typed column with a closed CHECK,
// and its rollback ends every live daemon session before the column goes, so
// a daemon session can never read as its person's own.
func TestDaemonSessionRollbackEndsDaemonSessionsBeforeDroppingTheColumn(t *testing.T) {
	t.Parallel()
	up, err := Files.ReadFile("000098_daemon_sessions.up.sql")
	if err != nil {
		t.Fatalf("read daemon session migration: %v", err)
	}
	if sql := statementSQL(up); !strings.Contains(sql, "add column client text not null default 'person'") ||
		!strings.Contains(sql, "check (client in ('person', 'daemon'))") {
		t.Fatalf("000098 does not add the closed session client column:\n%s", sql)
	}
	down, err := Files.ReadFile("000098_daemon_sessions.down.sql")
	if err != nil {
		t.Fatalf("read daemon session rollback: %v", err)
	}
	sql := statementSQL(down)
	previous := -1
	for _, statement := range []string{
		"update auth_sessions",
		"revocation_reason = 'downgrade'",
		"where client = 'daemon'",
		"and revoked_at is null",
		"drop column client",
	} {
		position := strings.Index(sql, statement)
		if position < 0 || position <= previous {
			t.Fatalf("000098 rollback does not end daemon sessions before dropping the column at %q:\n%s",
				statement, sql)
		}
		previous = position
	}
}
