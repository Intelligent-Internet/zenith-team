//go:build integration

package company

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// Access TECH D4: a Group's grant on a Project reaches every member, adds up
// with their own grants, and every change follows a person grant's rules.

func TestAGroupGrantReachesEveryMemberAndListsItsSources(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	leo := w.person(t, ctx, auth.CompanyRoleMember)
	w.grantPerson(t, ctx, payroll, w.member.UserID, viewTasks)
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID, leo.UserID)
	revisionBefore := w.accessRevision(t, ctx, payroll)

	preview, err := w.projects.PreviewGroupGrant(ctx, w.owner, payroll, finance.ID, workTasks)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Revision != revisionBefore || preview.PeopleAffected != 2 || len(preview.Projects) != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	result, err := w.projects.SetGroupGrant(ctx, w.owner, payroll, ProjectGroupGrantInput{
		GroupID: finance.ID, Permissions: []string{"project.tasks.write", "project.tasks.execute"},
		ExpectedRevision: preview.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != revisionBefore+1 || result.Grant == nil || result.Grant.GroupID != finance.ID ||
		!slices.Equal(
			auth.ProjectPermissionStrings(result.Grant.Granted),
			workTasks,
		) || result.Grant.MemberCount != 2 ||
		result.Grant.GrantedBy == nil || *result.Grant.GrantedBy != w.owner.UserID {
		t.Fatalf("SetGroupGrant() = %+v", result)
	}
	for _, person := range []uuid.UUID{w.member.UserID, leo.UserID} {
		if got := permissionNames(w.held(t, ctx, person, payroll)); !slices.Equal(got, workTasks) {
			t.Fatalf("%s holds %v, want %v", person, got, workTasks)
		}
	}
	if n := w.grantActivity(t, ctx, payroll, projectGrantGroupChanged, w.owner.UserID); n != 1 {
		t.Fatalf("group_changed rows = %d, want 1", n)
	}

	list, err := w.projects.ListGrants(ctx, w.owner, payroll)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Groups) != 1 || list.Groups[0].GroupID != finance.ID || list.Groups[0].Name != "Finance" {
		t.Fatalf("groups = %+v", list.Groups)
	}
	byPerson := map[uuid.UUID]ProjectGrant{}
	for _, grant := range list.Items {
		byPerson[grant.UserID] = grant
	}
	mia, leoGrant := byPerson[w.member.UserID], byPerson[leo.UserID]
	if !slices.Equal(auth.ProjectPermissionStrings(mia.Granted), viewTasks) ||
		!slices.Equal(auth.ProjectPermissionStrings(mia.Effective), workTasks) || len(mia.Sources) != 2 ||
		mia.Sources[0].Via != ProjectGrantSourceGrant || mia.Sources[1].Via != ProjectGrantSourceGroup ||
		*mia.Sources[1].GroupID != finance.ID {
		t.Fatalf("Mia = %+v", mia)
	}
	if len(leoGrant.Granted) != 0 || leoGrant.GrantedBy != nil || len(leoGrant.Sources) != 1 ||
		leoGrant.Sources[0].Via != ProjectGrantSourceGroup {
		t.Fatalf("Leo, through Finance only = %+v", leoGrant)
	}

	// Removing Mia's own grant leaves her a reader through Finance, and the
	// answer still names her, with Finance as her one source.
	removed, err := w.projects.RemoveGrants(
		ctx,
		w.owner,
		payroll,
		w.member.UserID,
		w.accessRevision(t, ctx, payroll),
	)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Grant == nil || len(removed.Grant.Granted) != 0 || len(removed.Grant.Sources) != 1 ||
		removed.Grant.Sources[0].Via != ProjectGrantSourceGroup {
		t.Fatalf("Mia after her own grant went = %+v", removed.Grant)
	}
	if access := w.held(t, ctx, w.member.UserID, payroll); access.Via != auth.ProjectAccessViaGroup {
		t.Fatalf("Mia reads Payroll through %q, want group", access.Via)
	}
}

// When a Group grant loses project.tasks.execute, members who held it only
// through the Group have their Runs there ended; sending the same set again
// changes nothing.
func TestAGroupGrantLosingExecuteStopsTheMembersRuns(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID)
	w.grantGroup(t, ctx, payroll, finance.ID, workTasks)
	run, token := w.liveMemberRun(t, ctx)
	revision := w.accessRevision(t, ctx, payroll)

	same, err := w.projects.SetGroupGrant(ctx, w.owner, payroll, ProjectGroupGrantInput{
		GroupID: finance.ID, Permissions: workTasks, ExpectedRevision: revision,
	})
	if err != nil || same.Revision != revision {
		t.Fatalf("the same set again = %+v, %v; want nothing changed", same, err)
	}
	preview, err := w.projects.PreviewGroupGrant(ctx, w.owner, payroll, finance.ID, viewTasks)
	if err != nil || preview.RunsStopping != 1 {
		t.Fatalf("preview = %+v, %v; want one Run stopping", preview, err)
	}
	w.grantGroup(t, ctx, payroll, finance.ID, viewTasks)
	status, reason, tokens := w.runOutcome(t, ctx, run)
	if status != "cancelled" || reason != execution.RunReasonProjectAccessRevoked || tokens != 0 ||
		w.proofLive(t, ctx, token) {
		t.Fatalf("Mia's Run = %s %q tokens %d", status, reason, tokens)
	}
	if access := w.held(t, ctx, w.member.UserID, payroll); access.Via != auth.ProjectAccessViaGroup ||
		access.Can(auth.ProjectPermissionTasksExecute) {
		t.Fatalf("Mia after = %+v", access)
	}
}

func TestGroupGrantsAreRefusedAsAPersonGrantIs(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID)
	set := func(principal auth.UserPrincipal, groupID uuid.UUID, permissions []string, revision int64) error {
		_, err := w.projects.SetGroupGrant(ctx, principal, payroll, ProjectGroupGrantInput{
			GroupID: groupID, Permissions: permissions, ExpectedRevision: revision,
		})
		return err
	}
	revision := w.accessRevision(t, ctx, payroll)
	// Any revision but the current one conflicts. A fresh Project is at
	// revision 1, and 0 is refused earlier as invalid input, so the test
	// names one ahead.
	if err := set(w.owner, finance.ID, viewTasks, revision+1); !errors.Is(err, ErrProjectGrantRevisionConflict) {
		t.Fatalf("a revision that is not the current one = %v", err)
	}
	if err := set(w.owner, uuid.New(), viewTasks, revision); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("an unknown Group = %v", err)
	}
	// A removed permission is refused by name, on the save and the preview.
	removed := []string{"project.read", "project.results.review"}
	_, previewErr := w.projects.PreviewGroupGrant(ctx, w.owner, payroll, finance.ID, removed)
	for name, err := range map[string]error{
		"save":    set(w.owner, finance.ID, removed, revision),
		"preview": previewErr,
	} {
		var named *auth.ProjectPermissionUnknownError
		if !errors.As(err, &named) || named.Name != "project.results.review" {
			t.Fatalf("%s naming project.results.review = %v, want ProjectPermissionUnknownError", name, err)
		}
	}
	if err := set(w.member, finance.ID, viewTasks, revision); !errors.Is(err, auth.ErrProjectNotVisible) {
		t.Fatalf("someone who cannot see the Project = %v", err)
	}

	// The self-grant cap: Leo manages Payroll's access through a grant of
	// {read, grants.manage}; he may give Finance Can view tasks, not more,
	// and may not change a Group grant that holds more than he does.
	leo := w.person(t, ctx, auth.CompanyRoleMember)
	w.grantPerson(t, ctx, payroll, leo.UserID, []string{"project.grants.manage"})
	if err := set(
		leo,
		finance.ID,
		approveTasks,
		w.accessRevision(t, ctx, payroll),
	); !errors.Is(
		err,
		ErrProjectGrantExceedsOwn,
	) {
		t.Fatalf("Leo gives Can approve tasks = %v, want ErrProjectGrantExceedsOwn", err)
	}
	if _, err := w.projects.PreviewGroupGrant(
		ctx,
		leo,
		payroll,
		finance.ID,
		approveTasks,
	); !errors.Is(
		err,
		ErrProjectGrantExceedsOwn,
	) {
		t.Fatalf("Leo previews Can approve tasks = %v, want ErrProjectGrantExceedsOwn", err)
	}
	if err := set(leo, finance.ID, viewTasks, w.accessRevision(t, ctx, payroll)); err != nil {
		t.Fatalf("Leo gives Can view tasks = %v", err)
	}
	w.grantGroup(t, ctx, payroll, finance.ID, workTasks)
	if _, err := w.projects.RemoveGroupGrant(
		ctx,
		leo,
		payroll,
		finance.ID,
		w.accessRevision(t, ctx, payroll),
	); !errors.Is(
		err,
		ErrProjectGrantExceedsOwn,
	) {
		t.Fatalf("Leo removes a Group grant above his own = %v, want ErrProjectGrantExceedsOwn", err)
	}

	// A Company-visible Project keeps everyone's floor: no removal there.
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
	if _, err := w.projects.RemoveGroupGrant(
		ctx,
		w.owner,
		payroll,
		finance.ID,
		w.accessRevision(t, ctx, payroll),
	); !errors.Is(
		err,
		ErrProjectVisibilityFloor,
	) {
		t.Fatalf("removal on a Company-visible Project = %v, want ErrProjectVisibilityFloor", err)
	}
	if err := set(
		w.owner,
		finance.ID,
		[]string{},
		w.accessRevision(t, ctx, payroll),
	); !errors.Is(
		err,
		ErrProjectVisibilityFloor,
	) {
		t.Fatalf("an empty set on a Company-visible Project = %v, want ErrProjectVisibilityFloor", err)
	}

	// The cap: 50 Group grants per Project.
	dbtest.MustExec(t, ctx, w.pool, `
		WITH seeded AS (
			INSERT INTO company_groups (company_id, name)
			SELECT $1, 'Cap ' || n FROM generate_series(1, $3::int - 1) AS n
			RETURNING id
		)
		INSERT INTO project_group_grants (company_id, project_id, group_id, permission)
		SELECT $1, $2, seeded.id, 'project.read' FROM seeded`, w.f.CompanyA, payroll, maxProjectGroupGrants)
	design := w.newGroup(t, ctx, "Design")
	if err := set(
		w.owner,
		design.ID,
		viewTasks,
		w.accessRevision(t, ctx, payroll),
	); !errors.Is(
		err,
		ErrGroupLimitReached,
	) {
		t.Fatalf("Group grant 51 = %v, want ErrGroupLimitReached", err)
	}
	// Changing a Group that already holds a grant there is not a new one.
	if err := set(w.owner, finance.ID, approveTasks, w.accessRevision(t, ctx, payroll)); err != nil {
		t.Fatalf("changing Finance at the cap = %v", err)
	}
}
