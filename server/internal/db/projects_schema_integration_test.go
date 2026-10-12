//go:build integration

package db

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/migrations"
)

func TestProjectsSchemaEnforcesTaskAndDependencyScope(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var companyID, otherCompanyID, projectID, otherProjectID, foreignProjectID uuid.UUID
	for _, id := range []*uuid.UUID{&companyID, &otherCompanyID} {
		if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug,task_prefix) VALUES ('Projects',gen_random_uuid()::text,'PRJ') RETURNING id`).
			Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []*uuid.UUID{&projectID, &otherProjectID, &foreignProjectID} {
		owner := companyID
		if i == 2 {
			owner = otherCompanyID
		}
		if err := pool.QueryRow(ctx, `INSERT INTO projects(company_id,key,title) VALUES ($1,$2,'Project') RETURNING id`, owner, []string{"research", "website", "research"}[i]).
			Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	assertCode := func(err error, code string) {
		t.Helper()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != code {
			t.Fatalf("error = %v, want SQLSTATE %s", err, code)
		}
	}
	_, err := pool.Exec(
		ctx,
		`INSERT INTO projects(company_id,key,title) VALUES ($1,'research','Duplicate')`,
		companyID,
	)
	assertCode(err, "23505")

	insertTask := func(project any, number int) (uuid.UUID, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var conversationID, taskID uuid.UUID
		err = tx.QueryRow(ctx, `INSERT INTO conversations(company_id,kind,project_id) VALUES ($1,'task',COALESCE($2::uuid,$3::uuid)) RETURNING id`, companyID, project, projectID).
			Scan(&conversationID)
		if err != nil {
			return uuid.Nil, err
		}
		err = tx.QueryRow(ctx, `INSERT INTO tasks(company_id,project_id,conversation_id,number,key,title,milestone_type,assignee_user_id,creator_user_id)
   VALUES ($1,$2,$3,$4,$4::bigint::text,'Task','product',gen_random_uuid(),gen_random_uuid()) RETURNING id`, companyID, project, conversationID, number).
			Scan(&taskID)
		if err != nil {
			return uuid.Nil, err
		}
		if _, err = tx.Exec(
			ctx,
			`UPDATE conversations SET task_id=$1 WHERE id=$2`,
			taskID,
			conversationID,
		); err != nil {
			return uuid.Nil, err
		}
		return taskID, tx.Commit(ctx)
	}
	_, err = insertTask(nil, 1)
	assertCode(err, "23502")
	_, err = insertTask(foreignProjectID, 1)
	assertCode(err, "23503")
	a, err := insertTask(projectID, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := insertTask(projectID, 2)
	if err != nil {
		t.Fatal(err)
	}
	c, err := insertTask(otherProjectID, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range []struct {
		project, dependent, prerequisite uuid.UUID
		want                             string
	}{
		{projectID, a, b, ""},
		{projectID, a, c, "23503"},
		{otherProjectID, a, c, "23503"},
		{projectID, a, a, "23514"},
	} {
		_, err = pool.Exec(
			ctx,
			`INSERT INTO task_dependencies(company_id,project_id,dependent_task_id,prerequisite_task_id) VALUES ($1,$2,$3,$4)`,
			companyID,
			edge.project,
			edge.dependent,
			edge.prerequisite,
		)
		if edge.want != "" {
			assertCode(err, edge.want)
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `DELETE FROM tasks WHERE id=$1`, b); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM task_dependencies`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("dependencies after prerequisite deletion = %d, want 0", count)
	}
	calendarSQL, err := migrations.Files.ReadFile("000109_scheduler.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(calendarSQL)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"000108_whiteboard_collaboration.down.sql",
		"000107_company_whiteboards.down.sql",
		"000106_document_agents.down.sql",
		"000105_document_history.down.sql",
		"000104_artifact_suggestions_and_task_results.down.sql",
	} {
		migration, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	// 000102 only narrows the two grant CHECKs, so its down comes first.
	// 000101's Group grants and project_access_paths read projects and
	// project_grants, so Groups come down next. Its down and up both run on
	// this data before the older steps restore theirs.
	for _, file := range []string{
		"000102_remove_results_review.down.sql",
		"000101_groups.down.sql", "000101_groups.up.sql", "000101_groups.down.sql",
	} {
		groupsSQL, err := migrations.Files.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(groupsSQL)); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
	// 000100 dropped conversation_audience_reads, which 000086's down drops by
	// name, so its down puts the function back first.
	authorizerSQL, err := migrations.Files.ReadFile("000100_runs_use_their_authorizers_access.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(authorizerSQL)); err != nil {
		t.Fatalf("000100 down: %v", err)
	}
	documentsSQL, err := migrations.Files.ReadFile("000096_artifact_documents.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(documentsSQL)); err != nil {
		t.Fatal(err)
	}
	canvasSQL, err := migrations.Files.ReadFile("000089_project_canvas.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(canvasSQL)); err != nil {
		t.Fatal(err)
	}
	// Downgrading this schema step preserves Tasks and restores Company-scoped edges.
	// Remove proposal references before rolling back their target Projects.
	proposalsDownSQL, err := migrations.Files.ReadFile("000080_huddle_work_proposals.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(proposalsDownSQL)); err != nil {
		t.Fatal(err)
	}
	// The ladder comes down newest first: 000059 owns the Task assignment
	// shape and the activity view that reads it (ADR 0014), so its down runs
	// before the older steps can restore theirs.
	// 000087 restores the Huddles (000070) activity view, which reads tasks.
	subjectDownSQL, err := migrations.Files.ReadFile("000087_activity_subject_project.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(subjectDownSQL)); err != nil {
		t.Fatal(err)
	}
	// 000086 drops the access views, which read tasks and projects. The
	// data-only 000060 (Task Output grants) changed no schema, so it has no
	// shape to undo here.
	accessDownSQL, err := migrations.Files.ReadFile("000086_project_access.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(accessDownSQL)); err != nil {
		t.Fatal(err)
	}
	bindingsDownSQL, err := migrations.Files.ReadFile("000059_agent_runtime_bindings.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(bindingsDownSQL)); err != nil {
		t.Fatal(err)
	}
	// Then 000057 removes the newer Project child before the historical rollback.
	filesSQL, err := migrations.Files.ReadFile("000057_project_files.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(filesSQL)); err != nil {
		t.Fatal(err)
	}
	historySQL, err := migrations.Files.ReadFile("000019_task_project_activity.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(historySQL)); err != nil {
		t.Fatal(err)
	}
	downSQL, err := migrations.Files.ReadFile("000018_projects.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(downSQL)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(
		ctx,
		`INSERT INTO task_dependencies(company_id,dependent_task_id,prerequisite_task_id) VALUES ($1,$2,$3)`,
		companyID,
		a,
		c,
	); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM tasks`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("Tasks after Project rollback = %d, want 2", count)
	}
}
