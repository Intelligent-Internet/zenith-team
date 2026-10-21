//go:build integration

package db

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
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
}
