//go:build integration

package company

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
)

// A person grant preview answers what the save
// would do, so the app asks first only when a change stops Runs or takes the
// Project away. It counts Runs as the Group preview does, keeps what the
// person's Groups give, and refuses what the save refuses.
func TestAPersonGrantPreviewCountsTheRunsTheSaveStops(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	w.grantPerson(t, ctx, payroll, w.member.UserID, workTasks)
	preview := func(principal auth.UserPrincipal, permissions []string) (ProjectGrantPreview, error) {
		t.Helper()
		return w.projects.PreviewGrant(ctx, principal, payroll, w.member.UserID, permissions)
	}

	// No Run in flight: lowering Mia to View stops nothing, and she keeps
	// the Project, so the app saves without asking.
	got, err := preview(w.owner, viewTasks)
	if err != nil || got.RunsStopping != 0 || got.LosesProject ||
		got.Revision != w.accessRevision(t, ctx, payroll) {
		t.Fatalf("preview with no Run = %+v, %v", got, err)
	}
	// Removing her takes the Project away.
	if got, err := preview(w.owner, []string{}); err != nil || !got.LosesProject || got.RunsStopping != 0 {
		t.Fatalf("preview of a removal = %+v, %v", got, err)
	}

	// With her Run live, lowering her stops it; a change that keeps
	// project.tasks.execute stops nothing.
	run, _ := w.liveMemberRun(t, ctx)
	if got, err := preview(w.owner, viewTasks); err != nil || got.RunsStopping != 1 || got.LosesProject {
		t.Fatalf("preview lowering with a live Run = %+v, %v", got, err)
	}
	if got, err := preview(w.owner, approveTasks); err != nil || got.RunsStopping != 0 {
		t.Fatalf("preview raising with a live Run = %+v, %v", got, err)
	}

	// What her Groups give stays: Finance gives Edit and run, so removing
	// her own grant stops nothing and leaves her the Project.
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID)
	w.grantGroup(t, ctx, payroll, finance.ID, workTasks)
	if got, err := preview(w.owner, []string{}); err != nil || got.RunsStopping != 0 || got.LosesProject {
		t.Fatalf("preview of a removal Finance covers = %+v, %v", got, err)
	}

	// The preview wrote nothing: the Run still runs and Mia holds her set.
	if status, _, _ := w.runOutcome(t, ctx, run); status != "running" {
		t.Fatalf("Mia's Run after the previews = %s", status)
	}
	if got := permissionNames(w.held(t, ctx, w.member.UserID, payroll)); !slices.Equal(got, workTasks) {
		t.Fatalf("Mia holds %v after the previews, want %v", got, workTasks)
	}

	// It refuses what the save refuses: a reader without grants.manage, the
	// self-grant cap, a removed permission, and a removal on a Company-visible
	// Project.
	leo := w.person(t, ctx, auth.CompanyRoleMember)
	w.grantPerson(t, ctx, payroll, leo.UserID, viewTasks)
	if _, err := preview(leo, viewTasks); !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("preview by a reader = %v, want ErrProjectPermissionRequired", err)
	}
	w.grantPerson(t, ctx, payroll, leo.UserID, []string{"project.grants.manage"})
	if _, err := preview(leo, viewTasks); !errors.Is(err, ErrProjectGrantExceedsOwn) {
		t.Fatalf("preview of a grant above Leo's own = %v, want ErrProjectGrantExceedsOwn", err)
	}
	var unknown *auth.ProjectPermissionUnknownError
	if _, err := preview(w.owner, []string{"project.read", "project.results.review"}); !errors.As(err, &unknown) ||
		unknown.Name != "project.results.review" {
		t.Fatalf("preview naming a removed permission = %v", err)
	}
	if _, err := w.projects.PreviewGrant(ctx, w.member, uuid.New(), w.member.UserID, viewTasks); !errors.Is(
		err, auth.ErrProjectNotVisible,
	) {
		t.Fatalf("preview on a Project Mia cannot see = %v, want ErrProjectNotVisible", err)
	}
	current, err := w.projects.Get(ctx, w.owner, payroll)
	if err != nil {
		t.Fatal(err)
	}
	open := auth.ProjectVisibilityCompany
	accessRevision := w.accessRevision(t, ctx, payroll)
	if _, err := w.projects.Update(ctx, w.owner, payroll, ProjectUpdateInput{
		ExpectedRevision: current.Revision, ExpectedAccessRevision: &accessRevision, Visibility: &open,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := preview(w.owner, []string{}); !errors.Is(err, ErrProjectVisibilityFloor) {
		t.Fatalf("preview of a removal on a Company-visible Project = %v, want ErrProjectVisibilityFloor", err)
	}
}

// accessRowNames is one project_grant row's action and the names its
// payload carries.
type accessRowNames struct {
	action, projectTitle, groupName string
}

// Access rows keep the names of their time: a
// grant row carries the Project's title, and a Group's row on a Project also
// the Group's name, as they were when the change was made, so the row still
// reads after the Group is deleted or the Project renamed or archived.
func TestAccessActivityRowsKeepTheNamesOfTheirTime(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	var title string
	if err := w.pool.QueryRow(ctx, `SELECT title FROM projects WHERE id = $1`, payroll).Scan(&title); err != nil {
		t.Fatal(err)
	}
	rows := func() []accessRowNames {
		t.Helper()
		found, err := w.pool.Query(ctx, `
			SELECT action, COALESCE(details->'payload'->>'projectTitle', ''),
			       COALESCE(details->'payload'->>'groupName', '')
			FROM activity_events
			WHERE company_id = $1 AND entity_type = 'project_grant' AND project_id = $2
			ORDER BY created_at, id`, w.f.CompanyA, payroll)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(found, func(row pgx.CollectableRow) (accessRowNames, error) {
			var names accessRowNames
			err := row.Scan(&names.action, &names.projectTitle, &names.groupName)
			return names, err
		})
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	earlier := len(rows())

	finance := w.newGroup(t, ctx, "Finance")
	w.grantGroup(t, ctx, payroll, finance.ID, viewTasks)
	leo := w.person(t, ctx, auth.CompanyRoleMember)
	w.grantPerson(t, ctx, payroll, leo.UserID, viewTasks)
	if _, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs: []uuid.UUID{w.member.UserID}, RemoveUserIDs: []uuid.UUID{},
		ExpectedRevision: w.groupRevision(t, ctx, finance.ID),
	}); err != nil {
		t.Fatal(err)
	}
	const renamed = "Payroll 2027"
	if _, err := w.pool.Exec(ctx, `UPDATE projects SET title = $2 WHERE id = $1`, payroll, renamed); err != nil {
		t.Fatal(err)
	}
	if err := w.groups.Delete(ctx, w.owner, finance.ID, w.groupRevision(t, ctx, finance.ID)); err != nil {
		t.Fatal(err)
	}
	var leoVersion int64
	if err := w.pool.QueryRow(ctx, `SELECT version FROM company_members WHERE company_id = $1 AND user_id = $2`,
		w.f.CompanyA, leo.UserID).Scan(&leoVersion); err != nil {
		t.Fatal(err)
	}
	if err := w.companies.RemoveMember(ctx, w.owner, MembershipMutation{
		UserID: leo.UserID, ExpectedRevision: leoVersion,
	}); err != nil {
		t.Fatal(err)
	}

	want := []accessRowNames{
		{projectGrantGroupChanged, title, "Finance"},
		{projectGrantChanged, title, ""},
		{projectGrantGroupMemberAdded, title, "Finance"},
		{projectGrantGroupChanged, renamed, "Finance"},
		{projectGrantChanged, renamed, ""},
	}
	if got := rows()[earlier:]; !slices.Equal(got, want) {
		t.Fatalf("access rows = %+v, want %+v", got, want)
	}
}
