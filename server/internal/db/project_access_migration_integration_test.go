//go:build integration

package db

import (
	"slices"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// 000086 (ADR 0017) makes every existing Project restricted, gives every
// active owner — and nobody else — every Project permission, and records the
// Project on Task chats and on the Inbox, Activity and report rows that link
// to a Task or its chat. A Task chat whose Task was already deleted keeps no
// Project and stays valid; a new one cannot be created without a Project.
// The down file restores the 000036 output_access body exactly.
func TestProjectAccessMigrationBackfillsAndRollsBack(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	toEightyFive := func(m *migrate.Migrate) error { return m.Migrate(85) }
	if err := runMigration(ctx, pool, "migrate to 85", toEightyFive); err != nil {
		t.Fatal(err)
	}
	var outputAccessBefore string
	if err := pool.QueryRow(ctx, `SELECT pg_get_viewdef('output_access')`).Scan(&outputAccessBefore); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)

	// Company A: UserA is the active owner. An admin, a member and a revoked
	// owner get nothing.
	people := map[string]uuid.UUID{}
	for _, person := range []struct{ name, role, status string }{
		{"admin", "admin", "active"},
		{"member", "member", "active"},
		{"revoked-owner", "owner", "revoked"},
	} {
		id := dbtest.NewUUID(t)
		people[person.name] = id
		dbtest.MustExec(t, ctx, pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
			id, person.name+"@backfill.example.com")
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO company_members (company_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
			f.CompanyA,
			id,
			person.role,
			person.status,
		)
	}
	secondProjectID := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, 'second', 'Second')`,
		secondProjectID,
		f.CompanyA,
	)

	// A second Task whose chat outlives it: deleting the Task detaches the chat.
	detachedConversationID, deletedTaskID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `INSERT INTO conversations (id, company_id, kind, task_id, title)
			VALUES ($1, $2, 'task', $3, 'Deleted')`, detachedConversationID, f.CompanyA, deletedTaskID)
		dbtest.MustExecTx(t, ctx, tx, `INSERT INTO tasks
			(id, company_id, conversation_id, project_id, milestone_type, number, key, title,
			 assignee_user_id, executed_by_agent_id, creator_user_id)
			VALUES ($1, $2, $3, $4, 'product', 2, 'deleted', 'Deleted', $5, $6, $5)`,
			deletedTaskID, f.CompanyA, detachedConversationID, secondProjectID, f.UserA, f.AgentA)
	})
	dbtest.MustExec(t, ctx, pool, `UPDATE conversations SET task_id = NULL WHERE id = $1`, detachedConversationID)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE id = $1`, deletedTaskID)

	// Rows that reach people outside a Project page, each linked one way.
	inbox := func(dedupe string, conversationID any, entityType, entityID string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO inbox_items
			(company_id, recipient_user_id, conversation_id, kind, entity_type, entity_id, title, body, dedupe_key)
			VALUES ($1, $2, $3, 'task_assigned', $4, $5, 'Title', 'Body', $6) RETURNING id`,
			f.CompanyA, f.UserA, conversationID, entityType, entityID, dedupe).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	inboxByConversation := inbox("by-conversation", f.ConversationA, "conversation", f.ConversationA.String())
	inboxByTask := inbox("by-task", nil, "task", f.TaskA.String())
	inboxByRun := inbox("by-run", nil, "run", f.RunA.String())
	inboxDirect := inbox("direct", f.DirectConversationA, "conversation", f.DirectConversationA.String())
	activity := func(taskID, conversationID any, entityType, entityID string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO activity_events
			(company_id, action, entity_type, entity_id, task_id, conversation_id)
			VALUES ($1, 'test.recorded', $2, $3, $4, $5) RETURNING id`,
			f.CompanyA, entityType, entityID, taskID, conversationID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	activityByTaskColumn := activity(f.TaskA, nil, "task", f.TaskA.String())
	activityByProject := activity(nil, nil, "project", secondProjectID.String())
	activityByMessage := activity(nil, nil, "message", f.MessageA.String())
	activityCompany := activity(nil, nil, "company", f.CompanyA.String())
	var linkedFile uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO attachments
		(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
		VALUES ($1, $2, gen_random_uuid(), 'brief.txt', 'text/plain', 1, 'ready') RETURNING id`,
		f.CompanyA, f.UserA).Scan(&linkedFile); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		VALUES ($1, $2, $3, 0)`, f.CompanyA, f.MessageA, linkedFile)
	activityByFile := activity(nil, nil, "attachment", linkedFile.String())
	activityByGoneFile := activity(nil, nil, "attachment", "")
	var reportID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO task_report_events
		(company_id, task_id, task_version, task_key, task_title, source_agent_id, recipient_agent_id,
		 completion_source, completed_at)
		VALUES ($1, $2, 1, 'task_a', 'Task A', $3, $3, 'manual', now()) RETURNING id`,
		f.CompanyA, f.TaskA, f.AgentA).Scan(&reportID); err != nil {
		t.Fatal(err)
	}

	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	type grantKey struct {
		companyID, projectID, userID uuid.UUID
	}
	grants := map[grantKey][]string{}
	rows, err := pool.Query(
		ctx,
		`SELECT company_id, project_id, user_id, permission FROM project_grants ORDER BY permission`,
	)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key grantKey
		var permission string
		if err := rows.Scan(&key.companyID, &key.projectID, &key.userID, &permission); err != nil {
			t.Fatal(err)
		}
		grants[key] = append(grants[key], permission)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// The backfill wrote the catalog of its day; 000102 later removed
	// project.results.review from it, so the head holds the other nine.
	catalog := []string{
		"project.approvals.resolve", "project.archive", "project.files.manage", "project.grants.manage",
		"project.read", "project.settings.update", "project.tasks.execute",
		"project.tasks.write", "project.visibility.update",
	}
	wantGrants := map[grantKey][]string{
		{f.CompanyA, f.ProjectA, f.UserA}:      catalog,
		{f.CompanyA, secondProjectID, f.UserA}: catalog,
		{f.CompanyB, f.ProjectB, f.UserB}:      catalog,
	}
	if len(grants) != len(wantGrants) {
		t.Errorf("backfilled grant holders = %d, want %d: %v", len(grants), len(wantGrants), grants)
	}
	for key, want := range wantGrants {
		if !slices.Equal(grants[key], want) {
			t.Errorf("grants for %v = %v, want %v", key, grants[key], want)
		}
	}

	// Each Project starts at revision 1; 000102 advanced every one whose
	// owner lost project.results.review, which is all three.
	var restricted, versionTwo int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE visibility = 'restricted'),
		count(*) FILTER (WHERE access_version = 2) FROM projects`).Scan(&restricted, &versionTwo); err != nil {
		t.Fatal(err)
	}
	if restricted != 3 || versionTwo != 3 {
		t.Errorf("projects restricted = %d, version 2 = %d, want 3 and 3", restricted, versionTwo)
	}

	projectOf := func(table string, id uuid.UUID) uuid.NullUUID {
		t.Helper()
		var projectID uuid.NullUUID
		if err := pool.QueryRow(ctx, `SELECT project_id FROM `+table+` WHERE id = $1`, id).
			Scan(&projectID); err != nil {
			t.Fatalf("read %s project: %v", table, err)
		}
		return projectID
	}
	for _, test := range []struct {
		name  string
		table string
		id    uuid.UUID
		want  uuid.UUID
	}{
		{"task chat", "conversations", f.ConversationA, f.ProjectA},
		{"detached task chat", "conversations", detachedConversationID, uuid.Nil},
		{"direct chat", "conversations", f.DirectConversationA, uuid.Nil},
		{"inbox by conversation", "inbox_items", inboxByConversation, f.ProjectA},
		{"inbox by task", "inbox_items", inboxByTask, f.ProjectA},
		{"inbox by run", "inbox_items", inboxByRun, f.ProjectA},
		{"inbox in a direct chat", "inbox_items", inboxDirect, uuid.Nil},
		{"activity by task column", "activity_events", activityByTaskColumn, f.ProjectA},
		{"activity about a project", "activity_events", activityByProject, secondProjectID},
		{"activity about a message", "activity_events", activityByMessage, f.ProjectA},
		{"company activity", "activity_events", activityCompany, uuid.Nil},
		{"activity about a linked file", "activity_events", activityByFile, f.ProjectA},
		{"activity about a removed file", "activity_events", activityByGoneFile, uuid.Nil},
		{"task report", "task_report_events", reportID, f.ProjectA},
	} {
		got := projectOf(test.table, test.id)
		if got.UUID != test.want || got.Valid != (test.want != uuid.Nil) {
			t.Errorf("%s project_id = %v, want %v", test.name, got, test.want)
		}
	}

	// The legacy pocket stays valid; a new Task chat must name its Project.
	dbtest.AssertStatementRejected(t, ctx, pool, "23514",
		`INSERT INTO conversations (company_id, kind, title) VALUES ($1, 'task', 'No project')`, f.CompanyA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE conversations SET title = 'Still detached' WHERE id = $1`,
		detachedConversationID,
	)
	// A live Task's chat can never lose its Project, whatever its age.
	dbtest.AssertStatementRejected(t, ctx, pool, "23514",
		`UPDATE conversations SET project_id = NULL WHERE id = $1`, f.ConversationA)
	// Deleting a Task detaches its chat and keeps the Project.
	dbtest.MustExec(t, ctx, pool, `UPDATE conversations SET task_id = NULL WHERE id = $1`, f.ConversationA)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE id = $1`, f.TaskA)
	if got := projectOf("conversations", f.ConversationA); got.UUID != f.ProjectA {
		t.Errorf("detached chat project_id = %v, want %v", got, f.ProjectA)
	}

	toEightyFiveAgain := func(m *migrate.Migrate) error { return m.Migrate(85) }
	if err := runMigration(ctx, pool, "roll back 000086", toEightyFiveAgain); err != nil {
		t.Fatal(err)
	}
	assertTableDoesNotExist(t, ctx, pool, "project_grants")
	for _, table := range []string{"conversations", "inbox_items", "activity_events", "task_report_events"} {
		assertTableOmitsColumns(t, ctx, pool, table, []string{"project_id"})
	}
	assertTableOmitsColumns(t, ctx, pool, "projects", []string{"visibility", "access_version"})
	var outputAccessAfter string
	var leftovers int
	if err := pool.QueryRow(ctx, `SELECT pg_get_viewdef('output_access')`).Scan(&outputAccessAfter); err != nil {
		t.Fatal(err)
	}
	if outputAccessAfter != outputAccessBefore {
		t.Errorf(
			"rolled back output_access differs from 000036:\n%s\nwant\n%s",
			outputAccessAfter,
			outputAccessBefore,
		)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class
		WHERE relname IN ('project_access', 'task_access', 'conversation_readers', 'attachment_access')`).
		Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT $1::int + count(*) FROM pg_proc
		WHERE proname IN ('project_permission_held', 'conversation_audience_reads')`, leftovers).
		Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Errorf("rollback left %d views or functions behind", leftovers)
	}
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
}
