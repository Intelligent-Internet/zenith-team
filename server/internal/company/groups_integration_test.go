//go:build integration

package company

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// Access TECH D3 and D5: a Group change moves access exactly as a person
// grant change would. The Project is the fixture's Project A ("Payroll"),
// restricted, where the owner holds every permission; the member is Mia and
// her Runs execute on her own computer.

var (
	// The app's levels, as the sorted sets the server stores and answers.
	viewTasks    = []string{"project.read"}
	workTasks    = []string{"project.read", "project.tasks.execute", "project.tasks.write"}
	approveTasks = []string{
		"project.approvals.resolve", "project.read", "project.tasks.execute", "project.tasks.write",
	}
)

// person adds an active Team member with the role.
func (w projectAccessWorld) person(t *testing.T, ctx context.Context, role auth.CompanyRole) auth.UserPrincipal {
	t.Helper()
	person := auth.UserPrincipal{UserID: dbtest.NewUUID(t), CompanyID: w.f.CompanyA, Role: role}
	dbtest.MustExec(t, ctx, w.pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3)`,
		person.UserID, person.UserID.String()+"@groups.test", "Person "+string(role))
	dbtest.MustExec(t, ctx, w.pool, `INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
		w.f.CompanyA, person.UserID, string(role))
	return person
}

// newGroup creates a Group as the owner and puts the people in it.
func (w projectAccessWorld) newGroup(t *testing.T, ctx context.Context, name string, people ...uuid.UUID) GroupDetail {
	t.Helper()
	group, err := w.groups.Create(ctx, w.owner, GroupInput{Name: name})
	if err != nil {
		t.Fatalf("create Group %s: %v", name, err)
	}
	if len(people) == 0 {
		return group
	}
	group, err = w.groups.ChangeMembers(ctx, w.owner, group.ID, GroupMembersInput{
		AddUserIDs: people, RemoveUserIDs: []uuid.UUID{}, ExpectedRevision: group.Revision,
	})
	if err != nil {
		t.Fatalf("fill Group %s: %v", name, err)
	}
	return group
}

// grantGroup sets the Group's grant on the Project as the owner.
func (w projectAccessWorld) grantGroup(
	t *testing.T,
	ctx context.Context,
	projectID, groupID uuid.UUID,
	permissions []string,
) {
	t.Helper()
	if _, err := w.projects.SetGroupGrant(ctx, w.owner, projectID, ProjectGroupGrantInput{
		GroupID: groupID, Permissions: permissions, ExpectedRevision: w.accessRevision(t, ctx, projectID),
	}); err != nil {
		t.Fatalf("grant Group: %v", err)
	}
}

// grantPerson sets a person's own grant on the Project as the owner.
func (w projectAccessWorld) grantPerson(
	t *testing.T,
	ctx context.Context,
	projectID, userID uuid.UUID,
	permissions []string,
) {
	t.Helper()
	if _, err := w.projects.SetGrants(ctx, w.owner, projectID, ProjectGrantInput{
		UserID: userID, Permissions: permissions, ExpectedRevision: w.accessRevision(t, ctx, projectID),
	}); err != nil {
		t.Fatalf("grant person: %v", err)
	}
}

// groupRevision reads the Group's current revision.
func (w projectAccessWorld) groupRevision(t *testing.T, ctx context.Context, groupID uuid.UUID) int64 {
	t.Helper()
	group, err := w.groups.Get(ctx, w.owner, groupID)
	if err != nil {
		t.Fatal(err)
	}
	return group.Revision
}

// liveMemberRun starts a Run Mia authorized on Project A's Task chat and
// mints its Run proof, as the claim would.
func (w projectAccessWorld) liveMemberRun(t *testing.T, ctx context.Context) (uuid.UUID, string) {
	t.Helper()
	runID := w.queueMemberRun(t, ctx)
	token := "brt_" + strings.Repeat("0", 32) + hex.EncodeToString(runID[:])
	digest := sha256.Sum256([]byte(token))
	dbtest.MustExec(t, ctx, w.pool, `UPDATE runs SET status = 'running' WHERE id = $1`, runID)
	dbtest.MustExec(t, ctx, w.pool, `
		INSERT INTO run_tokens (run_id, token_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
		runID, digest[:])
	return runID, token
}

// proofLive reports whether a Run proof still resolves.
func (w projectAccessWorld) proofLive(t *testing.T, ctx context.Context, token string) bool {
	t.Helper()
	_, err := execution.NewRunService(execution.RunServiceOptions{Queries: generated.New(w.pool)}).
		ResolveRunOrigin(ctx, token)
	if err != nil && !errors.Is(err, execution.ErrRunProofInvalid) {
		t.Fatal(err)
	}
	return err == nil
}

// queueMemberMessage queues Mia's message in Project A's Task chat.
func (w projectAccessWorld) queueMemberMessage(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO conversation_run_queue_items
			(company_id, conversation_id, initiated_by_user_id, request_key, content, "position")
		VALUES ($1, $2, $3, gen_random_uuid(), 'Also check the docs.', 1)
		RETURNING id`, w.f.CompanyA, w.f.ConversationA, w.member.UserID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (w projectAccessWorld) queuedState(t *testing.T, ctx context.Context, id uuid.UUID) (state, reason string) {
	t.Helper()
	if err := w.pool.QueryRow(ctx, `
		SELECT state, COALESCE(removal_reason, '') FROM conversation_run_queue_items WHERE id = $1`,
		id).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	return state, reason
}

// held is what the person holds on the Project now.
func (w projectAccessWorld) held(t *testing.T, ctx context.Context, userID, projectID uuid.UUID) auth.ProjectAccess {
	t.Helper()
	access, err := auth.ResolveMemberProjectAccess(ctx, generated.New(w.pool), w.f.CompanyA, userID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return access
}

// memberRows counts one action's project_grant rows on the Project that name
// the person among the people whose access moved.
func (w projectAccessWorld) memberRows(
	t *testing.T,
	ctx context.Context,
	projectID uuid.UUID,
	action string,
	userID uuid.UUID,
) int {
	t.Helper()
	var count int
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM activity_events
		WHERE company_id = $1 AND entity_type = 'project_grant' AND action = $2 AND project_id = $3
		  AND details->'payload'->'userIds' ? $4::text`,
		w.f.CompanyA, action, projectID, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// memberTrail is one project_grant.group_member_* row as the payload states
// it.
type memberTrail struct {
	groupID uuid.UUID
	userIDs []uuid.UUID
	count   int
}

// memberTrails reads one action's project_grant rows on the Project, oldest
// first.
func (w projectAccessWorld) memberTrails(
	t *testing.T,
	ctx context.Context,
	projectID uuid.UUID,
	action string,
) []memberTrail {
	t.Helper()
	rows, err := w.pool.Query(ctx, `
		SELECT (details->'payload'->>'groupId')::uuid,
		       ARRAY(SELECT jsonb_array_elements_text(details->'payload'->'userIds'))::uuid[],
		       (details->'payload'->>'count')::int
		FROM activity_events
		WHERE company_id = $1 AND entity_type = 'project_grant' AND action = $2 AND project_id = $3
		ORDER BY created_at, id`, w.f.CompanyA, action, projectID)
	if err != nil {
		t.Fatal(err)
	}
	trails, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memberTrail, error) {
		var trail memberTrail
		err := row.Scan(&trail.groupID, &trail.userIDs, &trail.count)
		return trail, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return trails
}

// accessNotice is one project_access_granted Inbox item.
type accessNotice struct {
	recipient          uuid.UUID
	projectID          uuid.UUID
	title, body, dedup string
}

// accessNotices reads every project_access_granted item of the Company.
func (w projectAccessWorld) accessNotices(t *testing.T, ctx context.Context) []accessNotice {
	t.Helper()
	rows, err := w.pool.Query(ctx, `
		SELECT recipient_user_id, entity_id::uuid, title, body, dedupe_key FROM inbox_items
		WHERE company_id = $1 AND kind = 'project_access_granted' AND entity_type = 'project'
		ORDER BY created_at, id`, w.f.CompanyA)
	if err != nil {
		t.Fatal(err)
	}
	notices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (accessNotice, error) {
		var notice accessNotice
		err := row.Scan(&notice.recipient, &notice.projectID, &notice.title, &notice.body, &notice.dedup)
		return notice, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return notices
}

func permissionNames(access auth.ProjectAccess) []string {
	return auth.ProjectPermissionStrings(access.Permissions)
}

// Removing Mia from Finance changes nothing on Payroll while her own grant
// holds everything Finance gives: no Run stops, no queued message goes, no
// Payroll row is written about her.
func TestLeavingAGroupKeepsTheOwnGrant(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	w.grantPerson(t, ctx, payroll, w.member.UserID, approveTasks)
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID)
	w.grantGroup(t, ctx, payroll, finance.ID, viewTasks)
	run, token := w.liveMemberRun(t, ctx)
	queued := w.queueMemberMessage(t, ctx)
	before := permissionNames(w.held(t, ctx, w.member.UserID, payroll))
	_, _, tokensBefore := w.runOutcome(t, ctx, run)

	after, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs: []uuid.UUID{}, RemoveUserIDs: []uuid.UUID{w.member.UserID},
		ExpectedRevision: w.groupRevision(t, ctx, finance.ID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.MemberCount != 0 || len(after.Members) != 0 {
		t.Fatalf("Finance after Mia left = %+v", after)
	}
	if got := permissionNames(w.held(t, ctx, w.member.UserID, payroll)); !slices.Equal(got, before) {
		t.Fatalf("Mia on Payroll = %v, want %v", got, before)
	}
	if status, reason, tokens := w.runOutcome(t, ctx, run); status != "running" || reason != "" ||
		tokens != tokensBefore || !w.proofLive(t, ctx, token) {
		t.Fatalf("Mia's Run = %s %q tokens %d", status, reason, tokens)
	}
	if state, _ := w.queuedState(t, ctx, queued); state != "queued" {
		t.Fatalf("Mia's queued message = %s", state)
	}
	if n := w.memberRows(t, ctx, payroll, projectGrantGroupMemberRemoved, w.member.UserID); n != 0 {
		t.Fatalf("Payroll rows about Mia = %d, want 0", n)
	}
}

// Finance was Mia's only source on Payroll: leaving it takes read, so her
// live Run ends with project_access_revoked, its proof stops resolving and
// her queued message goes.
func TestLeavingTheOnlyGroupStopsTheRuns(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID)
	w.grantGroup(t, ctx, payroll, finance.ID, workTasks)
	run, token := w.liveMemberRun(t, ctx)
	queued := w.queueMemberMessage(t, ctx)
	if !w.proofLive(t, ctx, token) {
		t.Fatal("the Run proof does not resolve before the change")
	}
	revisionBefore := w.accessRevision(t, ctx, payroll)

	if _, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs: []uuid.UUID{}, RemoveUserIDs: []uuid.UUID{w.member.UserID},
		ExpectedRevision: w.groupRevision(t, ctx, finance.ID),
	}); err != nil {
		t.Fatal(err)
	}
	if access := w.held(t, ctx, w.member.UserID, payroll); access.Via != "" {
		t.Fatalf("Mia still reads Payroll: %+v", access)
	}
	status, reason, tokens := w.runOutcome(t, ctx, run)
	if status != "cancelled" || reason != execution.RunReasonProjectAccessRevoked || tokens != 0 {
		t.Fatalf("Mia's Run = %s %q tokens %d", status, reason, tokens)
	}
	if w.proofLive(t, ctx, token) {
		t.Fatal("the Run proof still resolves: the next proof read would not be 401 run_token_invalid")
	}
	if state, reason := w.queuedState(t, ctx, queued); state != "removed" || reason != "project_access_revoked" {
		t.Fatalf("Mia's queued message = %s (%q)", state, reason)
	}
	if revision := w.accessRevision(t, ctx, payroll); revision != revisionBefore+1 {
		t.Fatalf("Payroll access revision = %d, want %d", revision, revisionBefore+1)
	}
	if n := w.memberRows(t, ctx, payroll, projectGrantGroupMemberRemoved, w.member.UserID); n != 1 {
		t.Fatalf("Payroll rows about Mia leaving = %d, want 1", n)
	}
	frames := w.log.named(EventProjectAccessChanged)
	change := payloadOf[ProjectAccessChanged](t, frames[len(frames)-1])
	if change.ProjectID != payroll || !slices.Contains(change.RecipientUserIDs, w.member.UserID) {
		t.Fatalf("access frame = %+v, want one reaching Mia", change)
	}
	changed := w.groupLog.named(EventGroupChanged)
	if last := payloadOf[GroupChanged](t, changed[len(changed)-1]); last.GroupID != finance.ID {
		t.Fatalf("group.changed = %+v", last)
	}
}

// A queued Run whose Authorizer lost execute through a Group change is never
// claimed: the change cancels it, and one queued past the change fails the
// claim fence (run_permission_held), while Mia keeps reading through her own
// grant.
func TestAQueuedRunWhoseAuthorizerLostExecuteThroughAGroupIsNeverClaimed(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	w.grantPerson(t, ctx, payroll, w.member.UserID, viewTasks)
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID)
	w.grantGroup(t, ctx, payroll, finance.ID, workTasks)
	queuedRun := w.queueMemberRun(t, ctx)

	if _, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs: []uuid.UUID{}, RemoveUserIDs: []uuid.UUID{w.member.UserID},
		ExpectedRevision: w.groupRevision(t, ctx, finance.ID),
	}); err != nil {
		t.Fatal(err)
	}
	if status, reason, _ := w.runOutcome(t, ctx, queuedRun); status != "cancelled" ||
		reason != execution.RunReasonProjectAccessRevoked {
		t.Fatalf("queued Run = %s %q", status, reason)
	}
	late := w.queueMemberRun(t, ctx)
	sets := auth.ProjectPermissionHeldSets()
	claimable, err := generated.New(w.pool).ListRunReadableProjects(ctx, generated.ListRunReadableProjectsParams{
		CompanyID: w.f.CompanyA, RunID: late, ProjectIds: []uuid.UUID{payroll},
		Permission:                string(auth.ProjectPermissionTasksExecute),
		ReadPermission:            string(auth.ProjectPermissionRead),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
	})
	if err != nil || len(claimable) != 0 {
		t.Fatalf("claim fence for a Run past the change = %v, %v; want it refused", claimable, err)
	}
	if access := w.held(t, ctx, w.member.UserID, payroll); access.Via != auth.ProjectAccessViaGrant {
		t.Fatalf("Mia's own grant = %+v", access)
	}
}

// Deleting a Group sweeps every member: each loses what only the Group gave,
// Payroll records the Group's grant going, and the Group and its rows are
// gone.
func TestDeletingAGroupSweepsEveryMember(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	leo := w.person(t, ctx, auth.CompanyRoleMember)
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID, leo.UserID)
	w.grantGroup(t, ctx, payroll, finance.ID, workTasks)
	run, _ := w.liveMemberRun(t, ctx)
	revisionBefore := w.accessRevision(t, ctx, payroll)

	preview, err := w.groups.Preview(ctx, w.owner, finance.ID, GroupChange{Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	if preview.PeopleAffected != 2 || preview.RunsStopping != 1 || len(preview.Projects) != 1 {
		t.Fatalf("delete preview = %+v", preview)
	}
	if err := w.groups.Delete(ctx, w.owner, finance.ID, preview.Revision); err != nil {
		t.Fatal(err)
	}
	for _, person := range []uuid.UUID{w.member.UserID, leo.UserID} {
		if access := w.held(t, ctx, person, payroll); access.Via != "" {
			t.Fatalf("%s still reads Payroll: %+v", person, access)
		}
	}
	if status, reason, _ := w.runOutcome(t, ctx, run); status != "cancelled" ||
		reason != execution.RunReasonProjectAccessRevoked {
		t.Fatalf("Mia's Run = %s %q", status, reason)
	}
	var rows int
	if err := w.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM company_groups WHERE id = $1)
		     + (SELECT count(*) FROM company_group_members WHERE group_id = $1)
		     + (SELECT count(*) FROM project_group_grants WHERE group_id = $1)`, finance.ID).Scan(&rows); err != nil ||
		rows != 0 {
		t.Fatalf("rows left of the deleted Group = %d, %v", rows, err)
	}
	if n := w.grantActivity(t, ctx, payroll, projectGrantGroupChanged, w.owner.UserID); n != 2 {
		t.Fatalf("Payroll group_changed rows = %d, want 2 (the grant, then the delete)", n)
	}
	if revision := w.accessRevision(t, ctx, payroll); revision != revisionBefore+1 {
		t.Fatalf("Payroll access revision = %d, want %d", revision, revisionBefore+1)
	}
	if _, err := w.groups.Get(ctx, w.owner, finance.ID); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("Get after delete = %v, want ErrGroupNotFound", err)
	}
	if deleted := w.groupLog.named(EventGroupDeleted); len(deleted) != 1 ||
		payloadOf[GroupDeleted](t, deleted[0]).GroupID != finance.ID {
		t.Fatalf("group.deleted frames = %+v", deleted)
	}
}

// A membership written before the person's latest (re)join counts for
// nothing; adding them back stamps it again, so the Group gives them access.
func TestAGroupMembershipOlderThanTheRejoinCountsForNothing(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID)
	w.grantGroup(t, ctx, payroll, finance.ID, viewTasks)
	if access := w.held(t, ctx, w.member.UserID, payroll); access.Via != auth.ProjectAccessViaGroup {
		t.Fatalf("Mia through Finance = %+v", access)
	}
	// A rejoin the revocation sweep never saw: the old row stays.
	dbtest.MustExec(t, ctx, w.pool, `
		UPDATE company_members SET reactivated_at = clock_timestamp(), version = version + 1
		WHERE company_id = $1 AND user_id = $2`, w.f.CompanyA, w.member.UserID)
	if access := w.held(t, ctx, w.member.UserID, payroll); access.Via != "" {
		t.Fatalf("Mia reads Payroll through a membership older than her rejoin: %+v", access)
	}
	group, err := w.groups.Get(ctx, w.owner, finance.ID)
	if err != nil || group.MemberCount != 0 || len(group.Members) != 0 {
		t.Fatalf("Finance lists a fenced member: %+v, %v", group, err)
	}
	if _, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs: []uuid.UUID{
			w.member.UserID,
		},
		RemoveUserIDs:    []uuid.UUID{},
		ExpectedRevision: group.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	if access := w.held(t, ctx, w.member.UserID, payroll); access.Via != auth.ProjectAccessViaGroup {
		t.Fatalf("Mia added back = %+v, want a Group reader", access)
	}
}

// Only active Team members may join; a stale revision, a name in use and a
// full Company are refused, and nothing is written.
func TestGroupChangesAreRefusedAsTheRulesSay(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID)
	change := func(add []uuid.UUID, revision int64) error {
		_, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
			AddUserIDs: add, RemoveUserIDs: []uuid.UUID{}, ExpectedRevision: revision,
		})
		return err
	}
	if err := change([]uuid.UUID{w.f.AgentA}, finance.Revision); !errors.Is(err, ErrGroupMemberRequired) {
		t.Fatalf("an Agent's id = %v, want ErrGroupMemberRequired", err)
	}
	if err := change([]uuid.UUID{w.admin.UserID}, finance.Revision-1); !errors.Is(err, ErrGroupConflict) {
		t.Fatalf("a stale revision = %v, want ErrGroupConflict", err)
	}
	if _, err := w.groups.Create(ctx, w.owner, GroupInput{Name: " finance "}); !errors.Is(err, ErrGroupConflict) {
		t.Fatalf("a name in use = %v, want ErrGroupConflict", err)
	}
	if _, err := w.groups.Create(ctx, w.member, GroupInput{Name: "Design"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member creating a Group = %v, want ErrForbidden", err)
	}
	if group, err := w.groups.Get(ctx, w.member, finance.ID); err != nil || group.Revision != finance.Revision ||
		group.MemberCount != 1 {
		t.Fatalf("Finance after the refusals = %+v, %v", group, err)
	}
	dbtest.MustExec(t, ctx, w.pool, `
		INSERT INTO company_groups (company_id, name)
		SELECT $1, 'Group ' || n FROM generate_series(1, $2::int - 1) AS n`, w.f.CompanyA, maxCompanyGroups)
	if _, err := w.groups.Create(
		ctx,
		w.owner,
		GroupInput{Name: "One too many"},
	); !errors.Is(
		err,
		ErrGroupLimitReached,
	) {
		t.Fatalf("Group 101 = %v, want ErrGroupLimitReached", err)
	}
}

// The preview's numbers match what the save does: the people and Runs it
// counts are exactly those whose access moves and whose Runs stop, and what
// it says each person holds after is what they hold.
func TestGroupPreviewMatchesTheSave(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	ana := w.person(t, ctx, auth.CompanyRoleMember)
	leo := w.person(t, ctx, auth.CompanyRoleMember)
	w.grantPerson(t, ctx, payroll, leo.UserID, approveTasks)
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID, leo.UserID)
	w.grantGroup(t, ctx, payroll, finance.ID, workTasks)
	run, _ := w.liveMemberRun(t, ctx)
	change := GroupChange{
		AddUserIDs: []uuid.UUID{ana.UserID}, RemoveUserIDs: []uuid.UUID{w.member.UserID, leo.UserID},
	}

	preview, err := w.groups.Preview(ctx, w.owner, finance.ID, change)
	if err != nil {
		t.Fatal(err)
	}
	// Ana gains, Mia loses, Leo keeps what his own grant gives.
	if preview.PeopleAffected != 2 || preview.RunsStopping != 1 || len(preview.Projects) != 1 ||
		len(preview.OpenedForCaller) != 0 || preview.ProjectsHidden {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs: change.AddUserIDs, RemoveUserIDs: change.RemoveUserIDs, ExpectedRevision: preview.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	for _, person := range preview.Projects[0].People {
		held := w.held(t, ctx, person.UserID, payroll)
		if got := auth.ProjectPermissionStrings(person.After); !slices.Equal(got, permissionNames(held)) {
			t.Errorf(
				"preview says %s holds %v after; they hold %v",
				person.UserID,
				got,
				permissionNames(held),
			)
		}
	}
	if status, _, _ := w.runOutcome(t, ctx, run); status != "cancelled" {
		t.Fatalf("the Run the preview counted = %s", status)
	}
	if n := w.memberRows(t, ctx, payroll, projectGrantGroupMemberRemoved, leo.UserID); n != 0 {
		t.Fatalf("rows about Leo, whose access did not move = %d", n)
	}
	// A save on the previewed revision after another change is refused.
	if _, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs: []uuid.UUID{leo.UserID}, RemoveUserIDs: []uuid.UUID{}, ExpectedRevision: preview.Revision,
	}); !errors.Is(err, ErrGroupConflict) {
		t.Fatalf("a save on a stale preview = %v, want ErrGroupConflict", err)
	}
}

// An admin may add themself to Finance, which opens restricted Payroll to
// them: it succeeds, Payroll records it, and every other person who manages
// Payroll's access gets one project_access_granted item. The admin gets
// none.
func TestAnAdminAddingThemselfIsRecordedAndEveryGrantManagerIsTold(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	otherAdmin := w.person(t, ctx, auth.CompanyRoleAdmin)
	leo := w.person(t, ctx, auth.CompanyRoleMember)
	w.grantPerson(t, ctx, payroll, leo.UserID, []string{"project.grants.manage"})
	finance := w.newGroup(t, ctx, "Finance")
	w.grantGroup(t, ctx, payroll, finance.ID, viewTasks)
	if access := w.held(t, ctx, w.admin.UserID, payroll); access.Via != "" {
		t.Fatalf("the admin reads Payroll before: %+v", access)
	}

	preview, err := w.groups.Preview(ctx, w.admin, finance.ID, GroupChange{AddUserIDs: []uuid.UUID{w.admin.UserID}})
	if err != nil || !slices.Equal(preview.OpenedForCaller, []uuid.UUID{payroll}) {
		t.Fatalf("preview = %+v, %v; want Payroll opened to the admin", preview, err)
	}
	if _, err := w.groups.ChangeMembers(ctx, w.admin, finance.ID, GroupMembersInput{
		AddUserIDs: []uuid.UUID{
			w.admin.UserID,
		},
		RemoveUserIDs:    []uuid.UUID{},
		ExpectedRevision: preview.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	if access := w.held(t, ctx, w.admin.UserID, payroll); access.Via != auth.ProjectAccessViaGroup {
		t.Fatalf("the admin after = %+v", access)
	}
	if trails := w.memberTrails(t, ctx, payroll, projectGrantGroupMemberAdded); len(trails) != 1 ||
		!slices.Equal(trails[0].userIDs, []uuid.UUID{w.admin.UserID}) || trails[0].count != 1 ||
		trails[0].groupID != finance.ID {
		t.Fatalf("Payroll group_member_added rows = %+v, want one naming the admin", trails)
	}
	notices := map[uuid.UUID]int{}
	revision := strconv.FormatInt(w.accessRevision(t, ctx, payroll), 10)
	dedupe := "project:" + payroll.String() + ":group:" + finance.ID.String() + ":" + revision
	for _, notice := range w.accessNotices(t, ctx) {
		notices[notice.recipient]++
		if notice.projectID != payroll || notice.title != "Person can now see Work" ||
			notice.body != "Added to Finance by Person." || notice.dedup != dedupe {
			t.Errorf("project_access_granted item = %+v", notice)
		}
	}
	want := map[uuid.UUID]int{w.owner.UserID: 1, otherAdmin.UserID: 1, leo.UserID: 1}
	if len(notices) != len(want) || notices[w.owner.UserID] != 1 || notices[otherAdmin.UserID] != 1 ||
		notices[leo.UserID] != 1 || notices[w.admin.UserID] != 0 {
		t.Fatalf("project_access_granted items = %v, want %v and none for the admin", notices, want)
	}
}

// A change of several people is recorded once per Project, naming only the
// people whose stored access there moved, and each grant manager is told
// once per Project the change opened, with a count. Taking people out again
// writes one removal row per Project.
func TestAGroupChangeOfSeveralPeopleIsRecordedOncePerProject(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	var ledger uuid.UUID
	if err := w.pool.QueryRow(ctx, `INSERT INTO projects (company_id, key, title) VALUES ($1, 'ledger', 'Ledger')
		RETURNING id`, w.f.CompanyA).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	ana, leo := w.person(t, ctx, auth.CompanyRoleMember), w.person(t, ctx, auth.CompanyRoleMember)
	w.grantPerson(t, ctx, payroll, leo.UserID, workTasks)
	finance := w.newGroup(t, ctx, "Finance")
	w.grantGroup(t, ctx, payroll, finance.ID, viewTasks)
	w.grantGroup(t, ctx, ledger, finance.ID, viewTasks)
	everyone := sortedUserIDs([]uuid.UUID{w.member.UserID, ana.UserID, leo.UserID})

	if _, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs:       everyone,
		RemoveUserIDs:    []uuid.UUID{},
		ExpectedRevision: w.groupRevision(t, ctx, finance.ID),
	}); err != nil {
		t.Fatal(err)
	}
	// Leo's own grant already held what Finance gives on Payroll.
	newcomers := sortedUserIDs([]uuid.UUID{w.member.UserID, ana.UserID})
	for project, want := range map[uuid.UUID][]uuid.UUID{payroll: newcomers, ledger: everyone} {
		trails := w.memberTrails(t, ctx, project, projectGrantGroupMemberAdded)
		if len(trails) != 1 || !slices.Equal(trails[0].userIDs, want) || trails[0].count != len(want) ||
			trails[0].groupID != finance.ID {
			t.Fatalf("group_member_added rows on %s = %+v, want one naming %v", project, trails, want)
		}
	}
	var memberRows int
	if err := w.pool.QueryRow(
		ctx,
		`SELECT count(*) FROM activity_events
		WHERE company_id = $1 AND action = $2 AND entity_id = $3::text`,
		w.f.CompanyA,
		groupMemberAdded,
		finance.ID,
	).Scan(&memberRows); err != nil || memberRows != len(everyone) {
		t.Fatalf("group.member_added rows = %d, %v; want one per person", memberRows, err)
	}
	// The owner made the change; the admin manages both Projects through the
	// bridge and is told once per Project, with a count.
	notices := w.accessNotices(t, ctx)
	titles := map[uuid.UUID]string{}
	for _, notice := range notices {
		if notice.recipient != w.admin.UserID {
			t.Fatalf(
				"project_access_granted item for %s, want only the admin: %+v",
				notice.recipient,
				notice,
			)
		}
		titles[notice.projectID] = notice.title
	}
	if len(notices) != 2 || titles[payroll] != "2 people can now see Work" ||
		titles[ledger] != "3 people can now see Ledger" {
		t.Fatalf("project_access_granted items = %+v", notices)
	}

	if _, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs: []uuid.UUID{}, RemoveUserIDs: []uuid.UUID{w.member.UserID, leo.UserID},
		ExpectedRevision: w.groupRevision(t, ctx, finance.ID),
	}); err != nil {
		t.Fatal(err)
	}
	leaving := sortedUserIDs([]uuid.UUID{w.member.UserID, leo.UserID})
	for project, want := range map[uuid.UUID][]uuid.UUID{payroll: {w.member.UserID}, ledger: leaving} {
		trails := w.memberTrails(t, ctx, project, projectGrantGroupMemberRemoved)
		if len(trails) != 1 || !slices.Equal(trails[0].userIDs, want) || trails[0].count != len(want) {
			t.Fatalf("group_member_removed rows on %s = %+v, want one naming %v", project, trails, want)
		}
	}
	if access := w.held(t, ctx, leo.UserID, payroll); access.Via != auth.ProjectAccessViaGrant {
		t.Fatalf("Leo keeps his own grant on Payroll: %+v", access)
	}
	if len(w.accessNotices(t, ctx)) != 2 {
		t.Fatal("a removal told someone that a Project opened")
	}
}

// Access TECH D7 at the scale measured on 2026-10-07: adding 250 people to a
// Group granted on 50 restricted Projects, then deleting the Group, each in
// one transaction under the Company lock. The trail stays one row per
// Project and one notice per grant manager per Project, and everyone loses
// what only the Group gave.
func TestAGroupChangeAtScaleStaysOnePerProject(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	const people, projects = 250, 50
	finance := w.newGroup(t, ctx, "Finance")
	dbtest.MustExec(t, ctx, w.pool, `
		WITH added AS (
			INSERT INTO users (id, email, display_name)
			SELECT gen_random_uuid(), 'scale-' || n || '@groups.test', 'Scale ' || n
			FROM generate_series(1, $2::int) AS n
			RETURNING id
		)
		INSERT INTO company_members (company_id, user_id, role) SELECT $1, added.id, 'member' FROM added`,
		w.f.CompanyA, people)
	dbtest.MustExec(t, ctx, w.pool, `
		WITH added AS (
			INSERT INTO projects (company_id, key, title)
			SELECT $1, 'scale_' || n, 'Scale ' || n FROM generate_series(1, $2::int) AS n
			RETURNING id
		)
		INSERT INTO project_group_grants (company_id, project_id, group_id, permission)
		SELECT $1, added.id, $3, permission
		FROM added CROSS JOIN unnest(ARRAY['project.read', 'project.tasks.write']) AS permission`,
		w.f.CompanyA, projects, finance.ID)
	rows, err := w.pool.Query(ctx, `SELECT member.user_id FROM company_members AS member
		JOIN users AS account ON account.id = member.user_id
		WHERE member.company_id = $1 AND account.email LIKE 'scale-%'`, w.f.CompanyA)
	if err != nil {
		t.Fatal(err)
	}
	added, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || len(added) != people {
		t.Fatalf("scale people = %d, %v", len(added), err)
	}

	start := time.Now()
	if _, err := w.groups.ChangeMembers(ctx, w.owner, finance.ID, GroupMembersInput{
		AddUserIDs: added, RemoveUserIDs: []uuid.UUID{}, ExpectedRevision: w.groupRevision(t, ctx, finance.ID),
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("adding %d people to a Group on %d Projects took %s", people, projects, time.Since(start))
	var trailRows, trailPeople int
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum((details->'payload'->>'count')::int), 0) FROM activity_events
		WHERE company_id = $1 AND action = $2`, w.f.CompanyA, projectGrantGroupMemberAdded,
	).Scan(&trailRows, &trailPeople); err != nil || trailRows != projects || trailPeople != people*projects {
		t.Fatalf("group_member_added rows = %d naming %d people, %v; want %d naming %d",
			trailRows, trailPeople, err, projects, people*projects)
	}
	notices := w.accessNotices(t, ctx)
	if len(notices) != projects {
		t.Fatalf("project_access_granted items = %d, want one per Project for the admin", len(notices))
	}
	for _, notice := range notices {
		if notice.recipient != w.admin.UserID ||
			!strings.HasPrefix(notice.title, "250 people can now see Scale ") {
			t.Fatalf("project_access_granted item = %+v", notice)
		}
	}

	start = time.Now()
	if err := w.groups.Delete(ctx, w.owner, finance.ID, w.groupRevision(t, ctx, finance.ID)); err != nil {
		t.Fatal(err)
	}
	t.Logf("deleting a Group of %d people on %d Projects took %s", people, projects, time.Since(start))
	var readers int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM project_access
		WHERE company_id = $1 AND user_id = ANY($2::uuid[])`, w.f.CompanyA, added).Scan(&readers); err != nil ||
		readers != 0 {
		t.Fatalf("readers left after the delete = %d, %v", readers, err)
	}
}

// D5: leaving the Company ends every Group membership; Payroll records what
// the person held there through Finance, and a rejoin restores nothing.
func TestLeavingTheCompanyEndsEveryGroupMembership(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	payroll := w.f.ProjectA
	finance := w.newGroup(t, ctx, "Finance", w.member.UserID)
	w.grantGroup(t, ctx, payroll, finance.ID, workTasks)
	revisionBefore := w.accessRevision(t, ctx, payroll)
	groupRevisionBefore := w.groupRevision(t, ctx, finance.ID)
	var memberVersion int64
	if err := w.pool.QueryRow(ctx, `SELECT version FROM company_members WHERE company_id = $1 AND user_id = $2`,
		w.f.CompanyA, w.member.UserID).Scan(&memberVersion); err != nil {
		t.Fatal(err)
	}

	if err := w.companies.RemoveMember(ctx, w.owner, MembershipMutation{
		UserID: w.member.UserID, ExpectedRevision: memberVersion,
	}); err != nil {
		t.Fatal(err)
	}
	var memberships int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM company_group_members WHERE company_id = $1 AND user_id = $2`,
		w.f.CompanyA, w.member.UserID).
		Scan(&memberships); err != nil ||
		memberships != 0 {
		t.Fatalf("Group memberships left = %d, %v", memberships, err)
	}
	if revision := w.groupRevision(t, ctx, finance.ID); revision != groupRevisionBefore+1 {
		t.Fatalf("Finance revision = %d, want %d", revision, groupRevisionBefore+1)
	}
	if revision := w.accessRevision(t, ctx, payroll); revision != revisionBefore+1 {
		t.Fatalf("Payroll access revision = %d, want %d", revision, revisionBefore+1)
	}
	var removal struct{ before, after []string }
	if err := w.pool.QueryRow(ctx, `
		SELECT ARRAY(SELECT jsonb_array_elements_text(details->'payload'->'before')),
		       ARRAY(SELECT jsonb_array_elements_text(details->'payload'->'after'))
		FROM activity_events
		WHERE company_id = $1 AND action = $2 AND project_id = $3 AND details->'payload'->>'userId' = $4::text`,
		w.f.CompanyA, projectGrantChanged, payroll, w.member.UserID,
	).Scan(&removal.before, &removal.after); err != nil {
		t.Fatalf("revocation Activity row: %v", err)
	}
	if !slices.Equal(removal.before, []string{"project.read", "project.tasks.execute", "project.tasks.write"}) ||
		len(removal.after) != 0 {
		t.Fatalf("revocation Activity = %+v", removal)
	}
	changed := w.groupLog.named(EventGroupChanged)
	if last := payloadOf[GroupChanged](t, changed[len(changed)-1]); last.GroupID != finance.ID ||
		last.Revision != groupRevisionBefore+1 {
		t.Fatalf("group.changed = %+v", last)
	}

	dbtest.MustExec(t, ctx, w.pool, `
		UPDATE company_members SET status = 'active', reactivated_at = clock_timestamp(), version = version + 1
		WHERE company_id = $1 AND user_id = $2`, w.f.CompanyA, w.member.UserID)
	if access := w.held(t, ctx, w.member.UserID, payroll); access.Via != "" {
		t.Fatalf("Mia rejoined reads Payroll: %+v", access)
	}
	if group, err := w.groups.Get(ctx, w.owner, finance.ID); err != nil || group.MemberCount != 0 {
		t.Fatalf("Finance after the rejoin = %+v, %v", group, err)
	}
}
