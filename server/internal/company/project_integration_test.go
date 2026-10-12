//go:build integration

package company_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestProjectLifecycleAndTaskOwnership(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var userID, companyID, otherCompanyID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,display_name) VALUES ('projects@test.invalid','Owner') RETURNING id`).
		Scan(&userID); err != nil {
		t.Fatal(err)
	}
	for i, id := range []*uuid.UUID{&companyID, &otherCompanyID} {
		if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug,task_prefix) VALUES ('Company',$1,'PRJ') RETURNING id`, []string{"projects", "other-projects"}[i]).
			Scan(id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(
			ctx,
			`INSERT INTO company_members(company_id,user_id,role) VALUES ($1,$2,'owner')`,
			*id,
			userID,
		); err != nil {
			t.Fatal(err)
		}
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := company.NewProjectService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := task.NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: userID, CompanyID: companyID, Role: auth.CompanyRoleOwner}
	p, err := projects.Create(ctx, principal, company.ProjectCreateInput{Key: "research", Title: "Research"})
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == uuid.Nil || p.Revision != 1 || p.ArchivedAt != nil {
		t.Fatalf("created = %+v", p)
	}
	var overviewPages int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM project_canvases c JOIN wiki_canvas_drafts d ON d.page_id=c.overview_page_id WHERE c.project_id=$1`, p.ID).
		Scan(&overviewPages); err != nil ||
		overviewPages != 1 {
		t.Fatalf("overview Pages = %d, %v; want one saved native Page", overviewPages, err)
	}
	_, err = projects.Create(ctx, principal, company.ProjectCreateInput{Key: "research", Title: "Duplicate"})
	if !errors.Is(err, company.ErrProjectConflict) {
		t.Fatalf("duplicate = %v", err)
	}
	other := principal
	other.CompanyID = otherCompanyID
	if _, err = projects.Get(ctx, other, p.ID); !errors.Is(err, company.ErrProjectNotFound) {
		t.Fatalf("foreign read = %v", err)
	}
	title := "Updated research"
	p, err = projects.Update(
		ctx,
		principal,
		p.ID,
		company.ProjectUpdateInput{ExpectedRevision: p.Revision, Title: &title},
	)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != title || p.Revision != 2 {
		t.Fatalf("updated = %+v", p)
	}
	if _, err = projects.Update(
		ctx,
		principal,
		p.ID,
		company.ProjectUpdateInput{ExpectedRevision: 1, Title: &title},
	); !errors.Is(
		err,
		company.ErrProjectConflict,
	) {
		t.Fatalf("stale = %v", err)
	}
	input := task.CreateInput{
		ProjectID:     p.ID,
		Key:           "research_customer",
		Title:         "Research customer",
		MilestoneType: task.MilestoneTypeProduct,
		Assignee:      task.ActorRef{Kind: task.ActorUser, ID: userID},
	}
	created, err := tasks.Create(ctx, principal, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Task.ProjectID != p.ID {
		t.Fatalf("Task project = %v", created.Task.ProjectID)
	}
	if _, err = tasks.Create(ctx, other, input); err == nil {
		t.Fatal("foreign Project accepted")
	}
	page, err := tasks.List(ctx, principal, task.ListOptions{ProjectID: &p.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tasks) != 1 || page.Tasks[0].ProjectID != p.ID {
		t.Fatalf("filtered = %+v", page)
	}
	if _, err = projects.Archive(ctx, principal, p.ID, p.Revision); !errors.Is(err, company.ErrProjectOpenTasks) {
		t.Fatalf("archive open = %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE tasks SET status='done' WHERE id=$1`, created.Task.ID); err != nil {
		t.Fatal(err)
	}
	p, err = projects.Archive(ctx, principal, p.ID, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if p.ArchivedAt == nil {
		t.Fatal("archive timestamp absent")
	}
	input.Key = "new_task"
	if _, err = tasks.Create(ctx, principal, input); !errors.Is(err, task.ErrProjectArchived) {
		t.Fatalf("archived Task create = %v", err)
	}
	items, err := projects.List(ctx, principal, false)
	if err != nil || len(items) != 0 {
		t.Fatalf("active list = %+v, %v", items, err)
	}
	items, err = projects.List(ctx, principal, true)
	if err != nil || len(items) != 1 {
		t.Fatalf("archive list = %+v, %v", items, err)
	}
	if _, err = pool.Exec(
		ctx,
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		companyID,
		userID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = projects.Create(
		ctx,
		principal,
		company.ProjectCreateInput{Key: "denied", Title: "Denied"},
	); !errors.Is(
		err,
		company.ErrProjectForbidden,
	) {
		t.Fatalf("stale owner authority = %v", err)
	}
}
