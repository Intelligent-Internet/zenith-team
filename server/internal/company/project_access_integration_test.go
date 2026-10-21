//go:build integration

package company

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// projectAccessWorld is one Company with an owner (the fixture's UserA), a
// member and an admin, and the Project and Company services over it.
type projectAccessWorld struct {
	pool      *pgxpool.Pool
	f         dbtest.Fixture
	projects  *DefaultProjectService
	companies *DefaultCompanyService
	groups    *DefaultGroupService
	log       *topicLog
	groupLog  *topicLog
	runs      *topicLog
	messages  *topicLog
	owner     auth.UserPrincipal
	member    auth.UserPrincipal
	admin     auth.UserPrincipal
	// memberRuntime is the member's own computer, where their Runs execute.
	memberRuntime uuid.UUID
}

func newProjectAccessWorld(t *testing.T) (context.Context, projectAccessWorld) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus := companyTestBus(t)
	projects, err := NewProjectService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	companies, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	groups, err := NewGroupService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := projectAccessWorld{
		pool: pool, f: dbtest.SeedFixture(t, ctx, pool), projects: projects, companies: companies,
		groups: groups, groupLog: recordTopic(t, bus, TopicGroup),
		log: recordTopic(t, bus, TopicProject), runs: recordTopic(t, bus, execution.Topic),
		messages: recordTopic(t, bus, conversation.TopicMessage), memberRuntime: dbtest.NewUUID(t),
	}
	w.owner = auth.UserPrincipal{UserID: w.f.UserA, CompanyID: w.f.CompanyA, Role: auth.CompanyRoleOwner}
	w.member = auth.UserPrincipal{UserID: dbtest.NewUUID(t), CompanyID: w.f.CompanyA, Role: auth.CompanyRoleMember}
	w.admin = auth.UserPrincipal{UserID: dbtest.NewUUID(t), CompanyID: w.f.CompanyA, Role: auth.CompanyRoleAdmin}
	for _, person := range []auth.UserPrincipal{w.member, w.admin} {
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Person')`,
			person.UserID,
			person.UserID.String()+"@project-access.test",
		)
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
			w.f.CompanyA,
			person.UserID,
			string(person.Role),
		)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-member', 'Member runtime', 'codex', 'online', $3, now())`,
		w.memberRuntime, w.f.CompanyA, w.member.UserID)
	return ctx, w
}

// queueMemberRun queues a Run the member authorized on the fixture's Task
// chat in Project A, with a Run token.
func (w projectAccessWorld) queueMemberRun(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	var runID uuid.UUID
	if err := w.pool.QueryRow(ctx, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id, status, run_type)
		VALUES ($1, $2, $3, $4, $5, 'queued', 'task') RETURNING id`,
		w.f.CompanyA, w.f.AgentA, w.f.ConversationA, w.memberRuntime, w.member.UserID,
	).Scan(&runID); err != nil {
		t.Fatalf("queue member Run: %v", err)
	}
	dbtest.MustExec(t, ctx, w.pool, `
		INSERT INTO run_tokens (run_id, token_hash, expires_at)
		VALUES ($1, decode(md5($2), 'hex'), now() + interval '1 hour')`, runID, runID.String())
	return runID
}

// queueMemberTaskRun queues a Run the member authorized for the fixture's
// Task A, in its current execution generation, with the Task in progress, so
// the Run's end reaches the Task.
func (w projectAccessWorld) queueMemberTaskRun(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	runID := w.queueMemberRun(t, ctx)
	dbtest.MustExec(t, ctx, w.pool, `UPDATE tasks SET status = 'in_progress' WHERE id = $1`, w.f.TaskA)
	dbtest.MustExec(t, ctx, w.pool, `
		UPDATE runs SET task_id = task.id, task_execution_generation = task.execution_generation
		FROM tasks AS task WHERE task.id = $1 AND runs.id = $2`, w.f.TaskA, runID)
	return runID
}

func (w projectAccessWorld) taskStatus(t *testing.T, ctx context.Context) string {
	t.Helper()
	var status string
	if err := w.pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id = $1`, w.f.TaskA).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func (w projectAccessWorld) runOutcome(
	t *testing.T,
	ctx context.Context,
	runID uuid.UUID,
) (status, reason string, tokens int) {
	t.Helper()
	if err := w.pool.QueryRow(ctx, `
		SELECT status, COALESCE(error, ''), (SELECT count(*) FROM run_tokens WHERE run_id = runs.id)
		FROM runs WHERE id = $1`, runID).Scan(&status, &reason, &tokens); err != nil {
		t.Fatal(err)
	}
	return status, reason, tokens
}

func (w projectAccessWorld) grantActivity(
	t *testing.T,
	ctx context.Context,
	projectID uuid.UUID,
	action string,
	actor uuid.UUID,
) int {
	t.Helper()
	var count int
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM activity_events
		WHERE company_id = $1 AND entity_type = 'project_grant' AND action = $2
		  AND project_id = $3 AND actor_id = $4::text`,
		w.f.CompanyA, action, projectID, actor).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// lifecycleActivity counts the Project lifecycle rows of one action that name
// the Project in project_id and keep its files and access out of the payload.
func (w projectAccessWorld) lifecycleActivity(
	t *testing.T,
	ctx context.Context,
	projectID uuid.UUID,
	action string,
) int {
	t.Helper()
	var count int
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM activity_events
		WHERE company_id = $1 AND entity_type = 'project' AND action = $2 AND project_id = $3
		  AND entity_id = $3::text
		  AND NOT (details->'payload' ? 'files') AND NOT (details->'payload' ? 'access')`,
		w.f.CompanyA, action, projectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// feedShows reports whether the person's Activity feed lists the Project's
// row of one action.
func (w projectAccessWorld) feedShows(
	t *testing.T,
	ctx context.Context,
	userID, projectID uuid.UUID,
	action string,
) bool {
	t.Helper()
	entries, _, err := activity.NewFeed(generated.New(w.pool)).List(ctx, w.f.CompanyA, activity.ListOptions{
		UserID: userID, EntityType: "project", EntityID: projectID.String(), Action: action, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	return len(entries) > 0
}

func (w projectAccessWorld) accessRevision(t *testing.T, ctx context.Context, projectID uuid.UUID) int64 {
	t.Helper()
	var revision int64
	if err := w.pool.QueryRow(ctx, `SELECT access_version FROM projects WHERE id = $1`, projectID).
		Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestProjectGrantsVisibilityAndJoin(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	p := w.projects

	created, err := p.Create(ctx, w.owner, ProjectCreateInput{Key: "secret", Title: "Secret"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Visibility != auth.ProjectVisibilityRestricted ||
		!slices.Equal(created.Access.Permissions, auth.ProjectPermissions()) ||
		created.Files == nil {
		t.Fatalf("created = %+v", created)
	}
	if n := w.grantActivity(t, ctx, created.ID, projectGrantChanged, w.owner.UserID); n != 1 {
		t.Fatalf("creator grant Activity rows = %d, want 1", n)
	}

	// A member without a grant cannot see the Project anywhere.
	if _, err := p.Get(ctx, w.member, created.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("member Get = %v", err)
	}
	if _, err := p.Authorize(ctx, w.member, created.ID); !errors.Is(err, auth.ErrProjectNotVisible) {
		t.Fatalf("member Authorize = %v", err)
	}
	if items, err := p.List(
		ctx,
		w.member,
		true,
	); err != nil ||
		slices.ContainsFunc(items, func(v ProjectResponse) bool { return v.ID == created.ID }) {
		t.Fatalf("member List = %+v, %v", items, err)
	}
	if items, err := p.ListForManageView(ctx, w.member, true); !errors.Is(err, ErrProjectForbidden) {
		t.Fatalf("member manage view = %+v, %v", items, err)
	}
	// The creation is in Activity, named by its Project, without its files or
	// anyone's access, and only the Project's readers read it.
	if n := w.lifecycleActivity(t, ctx, created.ID, EventProjectCreated); n != 1 {
		t.Fatalf("project.created Activity rows = %d, want 1", n)
	}
	if !w.feedShows(t, ctx, w.owner.UserID, created.ID, EventProjectCreated) ||
		w.feedShows(t, ctx, w.member.UserID, created.ID, EventProjectCreated) {
		t.Fatal("project.created must reach the reader and not the non-reader")
	}
	// Its frame carries the Project's settings only (no files, no access)
	// and goes to the readers and the projects.manage holders, decided in
	// the transaction: the owner and the admin, not the member.
	createdFrames := w.log.named(EventProjectCreated)
	if len(createdFrames) != 1 {
		t.Fatalf("project.created frames = %d, want 1", len(createdFrames))
	}
	lifecycle, ok := createdFrames[0].Payload.(ProjectLifecycle)
	if !ok {
		t.Fatalf("project.created payload = %T, want ProjectLifecycle", createdFrames[0].Payload)
	}
	wantAudience := []uuid.UUID{w.owner.UserID, w.admin.UserID}
	slices.SortFunc(wantAudience, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	if lifecycle.ID != created.ID || lifecycle.Audience == nil || lifecycle.Audience.CompanyWide ||
		!slices.Equal(lifecycle.Audience.UserIDs, wantAudience) {
		t.Fatalf(
			"project.created frame = %+v, audience %+v, want %v",
			lifecycle,
			lifecycle.Audience,
			wantAudience,
		)
	}

	// An admin manages it through the bridge and sees metadata only.
	bridged, err := p.Get(ctx, w.admin, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bridged.Files != nil || !bridged.Access.Management ||
		!slices.Equal(bridged.Access.Permissions, auth.ManagementProjectPermissions()) {
		t.Fatalf("bridge Get = %+v", bridged)
	}
	managed, err := p.ListForManageView(ctx, w.admin, false)
	if err != nil ||
		!slices.ContainsFunc(
			managed,
			func(v ProjectResponse) bool { return v.ID == created.ID && v.Files == nil },
		) {
		t.Fatalf("admin manage view = %+v, %v", managed, err)
	}
	if _, err := p.ReplaceFiles(
		ctx,
		w.admin,
		created.ID,
		ProjectFilesInput{DocumentIDs: []uuid.UUID{}},
		created.Revision,
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Fatalf("bridge files = %v", err)
	}
	company := auth.ProjectVisibilityCompany
	if _, err := p.Update(
		ctx,
		w.admin,
		created.ID,
		ProjectUpdateInput{
			ExpectedRevision:       created.Revision,
			ExpectedAccessRevision: &created.Access.Revision,
			Visibility:             &company,
		},
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Fatalf("bridge visibility = %v", err)
	}
	title := "Renamed by admin"
	renamed, err := p.Update(
		ctx,
		w.admin,
		created.ID,
		ProjectUpdateInput{ExpectedRevision: created.Revision, Title: &title},
	)
	if err != nil || renamed.Title != title || renamed.Files != nil {
		t.Fatalf("bridge rename = %+v, %v", renamed, err)
	}

	// Grants are normalized, revisioned, recorded and announced.
	revision := created.Access.Revision
	// A permission outside the catalog, the removed project.results.review
	// included, is refused by name.
	for _, unknown := range []string{"project.nonsense", "project.results.review"} {
		_, err := p.SetGrants(ctx, w.owner, created.ID, ProjectGrantInput{
			UserID:           w.member.UserID,
			Permissions:      []string{"project.read", unknown},
			ExpectedRevision: revision,
		})
		var named *auth.ProjectPermissionUnknownError
		if !errors.As(err, &named) || named.Name != unknown {
			t.Fatalf("SetGrants(%s) = %v, want ProjectPermissionUnknownError naming it", unknown, err)
		}
	}
	if _, err := p.SetGrants(
		ctx,
		w.owner,
		created.ID,
		ProjectGrantInput{
			UserID:           dbtest.NewUUID(t),
			Permissions:      []string{"project.read"},
			ExpectedRevision: revision,
		},
	); !errors.Is(
		err,
		ErrProjectMemberRequired,
	) {
		t.Fatalf("non-member grant = %v", err)
	}
	if _, err := p.SetGrants(
		ctx,
		w.member,
		created.ID,
		ProjectGrantInput{
			UserID:           w.member.UserID,
			Permissions:      []string{"project.read"},
			ExpectedRevision: revision,
		},
	); !errors.Is(
		err,
		auth.ErrProjectNotVisible,
	) {
		t.Fatalf("self-grant by a non-reader = %v", err)
	}
	before := len(w.log.named(EventProjectAccessChanged))
	granted, err := p.SetGrants(ctx, w.owner, created.ID, ProjectGrantInput{
		UserID: w.member.UserID, Permissions: []string{"project.tasks.write"}, ExpectedRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantGranted := []auth.ProjectPermission{auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite}
	if granted.Grant == nil || !slices.Equal(granted.Grant.Granted, wantGranted) ||
		!slices.Equal(granted.Grant.Effective, wantGranted) || granted.Revision != revision+1 ||
		granted.Grant.GrantedBy == nil || *granted.Grant.GrantedBy != w.owner.UserID {
		t.Fatalf("grant = %+v", granted)
	}
	frames := w.log.named(EventProjectAccessChanged)
	if len(frames) != before+1 {
		t.Fatalf("access frames = %d, want %d", len(frames), before+1)
	}
	change := payloadOf[ProjectAccessChanged](t, frames[len(frames)-1])
	for _, want := range []uuid.UUID{w.owner.UserID, w.member.UserID, w.admin.UserID} {
		if !slices.Contains(change.RecipientUserIDs, want) {
			t.Fatalf("frame recipients %v miss %s", change.RecipientUserIDs, want)
		}
	}
	if change.CompanyWide || change.ProjectID != created.ID || change.Revision != granted.Revision {
		t.Fatalf("frame = %+v", change)
	}
	if n := w.grantActivity(t, ctx, created.ID, projectGrantChanged, w.owner.UserID); n != 2 {
		t.Fatalf("grant Activity rows = %d, want 2", n)
	}
	if _, err := p.SetGrants(
		ctx,
		w.owner,
		created.ID,
		ProjectGrantInput{
			UserID:           w.member.UserID,
			Permissions:      []string{"project.read"},
			ExpectedRevision: revision,
		},
	); !errors.Is(
		err,
		ErrProjectGrantRevisionConflict,
	) {
		t.Fatalf("stale revision = %v", err)
	}
	// The same set again, even from another manager, changes nothing: no
	// revision, no Activity, no frame, and the rows keep their author.
	again, err := p.SetGrants(ctx, w.admin, created.ID, ProjectGrantInput{
		UserID:           w.member.UserID,
		Permissions:      []string{"project.read", "project.tasks.write"},
		ExpectedRevision: granted.Revision,
	})
	if err != nil || again.Revision != granted.Revision ||
		len(w.log.named(EventProjectAccessChanged)) != before+1 ||
		again.Grant == nil ||
		again.Grant.GrantedBy == nil ||
		*again.Grant.GrantedBy != w.owner.UserID ||
		!again.Grant.UpdatedAt.Equal(granted.Grant.UpdatedAt) {
		t.Fatalf("unchanged grant = %+v, %v", again, err)
	}
	if n := w.grantActivity(t, ctx, created.ID, projectGrantChanged, w.admin.UserID); n != 0 {
		t.Fatalf("unchanged grant Activity rows = %d, want 0", n)
	}

	// The member now reads the Project and its grants, and still cannot rename it.
	read, err := p.Get(ctx, w.member, created.ID)
	if err != nil || read.Files == nil || !slices.Equal(read.Access.Permissions, wantGranted) {
		t.Fatalf("member read = %+v, %v", read, err)
	}
	grants, err := p.ListGrants(ctx, w.member, created.ID)
	if err != nil || len(grants.Items) != 2 || len(grants.Groups) != 0 {
		t.Fatalf("grants = %+v, %v", grants, err)
	}
	if _, err := p.Update(
		ctx,
		w.member,
		created.ID,
		ProjectUpdateInput{ExpectedRevision: renamed.Revision, Title: &title},
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Fatalf("member rename = %v", err)
	}

	// Join grants the admin exactly read, recorded as their own grant.
	joined, err := p.Join(ctx, w.admin, created.ID, granted.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !joined.Access.Management || !slices.Contains(joined.Access.Permissions, auth.ProjectPermissionRead) ||
		slices.Contains(joined.Access.Permissions, auth.ProjectPermissionFilesManage) || joined.Files == nil {
		t.Fatalf("joined = %+v", joined)
	}
	if n := w.grantActivity(t, ctx, created.ID, projectGrantJoined, w.admin.UserID); n != 1 {
		t.Fatalf("Join Activity rows = %d, want 1", n)
	}
	if managed, err := p.ListForManageView(ctx, w.admin, false); err != nil ||
		slices.ContainsFunc(managed, func(v ProjectResponse) bool { return v.ID == created.ID }) {
		t.Fatalf("manage view after Join = %+v, %v", managed, err)
	}
	if again, err := p.Join(
		ctx,
		w.admin,
		created.ID,
		1,
	); err != nil ||
		again.Access.Revision != joined.Access.Revision {
		t.Fatalf("second Join = %+v, %v", again, err)
	}
	if _, err := p.Join(ctx, w.member, created.ID, joined.Access.Revision); !errors.Is(err, ErrProjectForbidden) {
		t.Fatalf("member Join = %v", err)
	}

	// A visibility change is an access change: it names the access revision
	// it was decided on, and a stale one is refused.
	if _, err := p.Update(
		ctx,
		w.owner,
		created.ID,
		ProjectUpdateInput{ExpectedRevision: renamed.Revision, Visibility: &company},
	); !errors.Is(
		err,
		ErrProjectInvalidInput,
	) {
		t.Fatalf("visibility without an access revision = %v", err)
	}
	if _, err := p.Update(ctx, w.owner, created.ID, ProjectUpdateInput{
		ExpectedRevision: renamed.Revision, ExpectedAccessRevision: &granted.Revision, Visibility: &company,
	}); !errors.Is(err, ErrProjectGrantRevisionConflict) {
		t.Fatalf("visibility on a stale access revision = %v", err)
	}

	// Opened to the Company, every member holds the floor.
	opened, err := p.Update(ctx, w.owner, created.ID, ProjectUpdateInput{
		ExpectedRevision:       renamed.Revision,
		ExpectedAccessRevision: &joined.Access.Revision,
		Visibility:             &company,
	})
	if err != nil || opened.Visibility != auth.ProjectVisibilityCompany ||
		opened.Access.Revision != joined.Access.Revision+1 {
		t.Fatalf("open = %+v, %v", opened, err)
	}
	if n := w.grantActivity(t, ctx, created.ID, projectGrantVisibilityChanged, w.owner.UserID); n != 1 {
		t.Fatalf("visibility Activity rows = %d, want 1", n)
	}
	frames = w.log.named(EventProjectAccessChanged)
	if change := payloadOf[ProjectAccessChanged](t, frames[len(frames)-1]); !change.CompanyWide {
		t.Fatalf("opening frame = %+v, want Company-wide", change)
	}
	floor, err := p.Get(ctx, w.member, created.ID)
	if err != nil || !slices.Contains(floor.Access.Permissions, auth.ProjectPermissionTasksExecute) {
		t.Fatalf("member on a Company Project = %+v, %v", floor, err)
	}
	// There a stored set only adds on top of the floor: the member keeps the
	// floor whatever is stored, removing them is refused, and making the
	// Project restricted again leaves them exactly what was stored.
	extra, err := p.SetGrants(ctx, w.owner, created.ID, ProjectGrantInput{
		UserID:           w.member.UserID,
		Permissions:      []string{"project.approvals.resolve"},
		ExpectedRevision: opened.Access.Revision,
	})
	stored := []auth.ProjectPermission{auth.ProjectPermissionApprovalsResolve, auth.ProjectPermissionRead}
	if err != nil || extra.Grant == nil || !slices.Equal(extra.Grant.Granted, stored) ||
		!slices.Contains(extra.Grant.Effective, auth.ProjectPermissionTasksExecute) {
		t.Fatalf("extra permission on a Company Project = %+v, %v", extra, err)
	}
	if _, err := p.RemoveGrants(
		ctx,
		w.owner,
		created.ID,
		w.member.UserID,
		extra.Revision,
	); !errors.Is(
		err,
		ErrProjectVisibilityFloor,
	) {
		t.Fatalf("removal on a Company Project = %v", err)
	}
	restricted := auth.ProjectVisibilityRestricted
	if _, err := p.Update(ctx, w.owner, created.ID, ProjectUpdateInput{
		ExpectedRevision: opened.Revision, ExpectedAccessRevision: &extra.Revision, Visibility: &restricted,
	}); err != nil {
		t.Fatal(err)
	}
	closed, err := p.Get(ctx, w.member, created.ID)
	if err != nil || !slices.Equal(closed.Access.Permissions, stored) {
		t.Fatalf("member after restricting = %+v, %v", closed.Access, err)
	}
	if n := w.lifecycleActivity(t, ctx, created.ID, EventProjectUpdated); n < 1 {
		t.Fatalf("project.updated Activity rows = %d", n)
	}
}

// Access stays manageable on an archived Project (projects.md): granting,
// removing a person and Join still work, so an owner can take someone off an
// archive or let a reader into its history. The Project's own settings, its
// files and its visibility stay frozen (project_archived).
func TestArchivedProjectKeepsItsAccessManageable(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	p := w.projects
	created, err := p.Create(ctx, w.owner, ProjectCreateInput{Key: "archive", Title: "Archive"})
	if err != nil {
		t.Fatal(err)
	}
	archived, err := p.Archive(ctx, w.owner, created.ID, created.Revision)
	if err != nil || archived.ArchivedAt == nil {
		t.Fatalf("Archive = %+v, %v", archived, err)
	}
	granted, err := p.SetGrants(ctx, w.owner, created.ID, ProjectGrantInput{
		UserID:           w.member.UserID,
		Permissions:      []string{"project.read"},
		ExpectedRevision: archived.Access.Revision,
	})
	if err != nil {
		t.Fatalf("grant on an archive = %v, want it allowed", err)
	}
	if read, err := p.Get(ctx, w.member, created.ID); err != nil || read.ArchivedAt == nil {
		t.Fatalf("the granted member reads the archive = %+v, %v", read, err)
	}
	removed, err := p.RemoveGrants(ctx, w.owner, created.ID, w.member.UserID, granted.Revision)
	if err != nil {
		t.Fatalf("removal on an archive = %v, want it allowed", err)
	}
	if _, err := p.Get(ctx, w.member, created.ID); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("the removed member reads the archive: %v", err)
	}
	joined, err := p.Join(ctx, w.admin, created.ID, removed.Revision)
	if err != nil || !slices.Contains(joined.Access.Permissions, auth.ProjectPermissionRead) {
		t.Fatalf("Join on an archive = %+v, %v; want the admin a reader", joined.Access, err)
	}
	company := auth.ProjectVisibilityCompany
	if _, err := p.Update(ctx, w.owner, created.ID, ProjectUpdateInput{
		ExpectedRevision:       joined.Revision,
		ExpectedAccessRevision: &joined.Access.Revision,
		Visibility:             &company,
	}); !errors.Is(err, ErrProjectArchived) {
		t.Fatalf("visibility change on an archive = %v, want ErrProjectArchived", err)
	}
	if _, err := p.ReplaceFiles(
		ctx,
		w.owner,
		created.ID,
		ProjectFilesInput{DocumentIDs: []uuid.UUID{}},
		joined.Revision,
	); !errors.Is(
		err,
		ErrProjectArchived,
	) {
		t.Fatalf("files on an archive = %v, want ErrProjectArchived", err)
	}
}

// The self-grant cap: Leo manages Payroll's access through a grant
// of {read, grants.manage}. He can give only what he holds and cannot change
// someone whose own grant holds more; an admin, through projects.manage, can
// do all of it, and Join still works.
func TestSelfGrantCapLimitsAGrantManagerToTheirOwnAccess(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	p := w.projects
	addMember := func() auth.UserPrincipal {
		t.Helper()
		id := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, w.pool, `
			INSERT INTO users (id, email, display_name) VALUES ($1, $2, 'Person')`,
			id, id.String()+"@self-grant-cap.test")
		dbtest.MustExec(t, ctx, w.pool, `
			INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
			w.f.CompanyA, id)
		return auth.UserPrincipal{UserID: id, CompanyID: w.f.CompanyA, Role: auth.CompanyRoleMember}
	}
	leo, mia, olivia := w.member, addMember(), addMember()
	payroll, err := p.Create(ctx, w.owner, ProjectCreateInput{Key: "payroll", Title: "Payroll"})
	if err != nil {
		t.Fatal(err)
	}
	revision := func() int64 { return w.accessRevision(t, ctx, payroll.ID) }
	set := func(actor auth.UserPrincipal, person uuid.UUID, permissions []auth.ProjectPermission) error {
		t.Helper()
		_, err := p.SetGrants(ctx, actor, payroll.ID, ProjectGrantInput{
			UserID: person, Permissions: auth.ProjectPermissionStrings(permissions),
			ExpectedRevision: revision(),
		})
		return err
	}
	remove := func(actor auth.UserPrincipal, person uuid.UUID) error {
		t.Helper()
		_, err := p.RemoveGrants(ctx, actor, payroll.ID, person, revision())
		return err
	}
	granted := func(person uuid.UUID) []auth.ProjectPermission {
		t.Helper()
		q := generated.New(w.pool)
		access, err := auth.ResolveMemberProjectAccess(ctx, q, w.f.CompanyA, person, payroll.ID)
		if err != nil {
			t.Fatal(err)
		}
		return access.Granted
	}
	leoChanges := func() int {
		t.Helper()
		return w.grantActivity(t, ctx, payroll.ID, projectGrantChanged, leo.UserID)
	}
	read, approve := auth.ProjectPermissionRead, auth.ProjectPermissionApprovalsResolve
	canView, onlyApprove := []auth.ProjectPermission{read}, []auth.ProjectPermission{approve}
	leosOwn := []auth.ProjectPermission{auth.ProjectPermissionGrantsManage, read}
	canApprove := []auth.ProjectPermission{
		approve, read, auth.ProjectPermissionTasksExecute, auth.ProjectPermissionTasksWrite,
	}
	canManage := auth.ProjectPermissions()
	if err := set(w.owner, leo.UserID, leosOwn); err != nil {
		t.Fatal(err)
	}
	if err := set(w.owner, olivia.UserID, canManage); err != nil {
		t.Fatal(err)
	}

	// Each refusal writes nothing: no revision, no grant, no Activity row.
	for _, refusal := range []struct {
		name   string
		change func() error
	}{
		{"Leo gives Mia Can approve tasks", func() error { return set(leo, mia.UserID, canApprove) }},
		{"Leo gives Mia approvals.resolve alone", func() error { return set(leo, mia.UserID, onlyApprove) }},
		{"Leo gives himself more", func() error { return set(leo, leo.UserID, append(leosOwn, approve)) }},
		{"Leo lowers Olivia to Can view tasks", func() error { return set(leo, olivia.UserID, canView) }},
		{"Leo removes Olivia", func() error { return remove(leo, olivia.UserID) }},
		{"Leo saves Olivia's own set again", func() error { return set(leo, olivia.UserID, canManage) }},
	} {
		before := revision()
		if err := refusal.change(); !errors.Is(err, ErrProjectGrantExceedsOwn) {
			t.Fatalf("%s = %v, want ErrProjectGrantExceedsOwn", refusal.name, err)
		}
		if after, rows := revision(), leoChanges(); after != before || rows != 0 {
			t.Fatalf("%s wrote: revision %d -> %d, Activity rows %d", refusal.name, before, after, rows)
		}
	}
	if got := granted(mia.UserID); len(got) != 0 {
		t.Fatalf("Mia after the refusals = %v, want nothing", got)
	}
	if got := granted(olivia.UserID); !slices.Equal(got, canManage) {
		t.Fatalf("Olivia after the refusals = %v, want Can manage project", got)
	}

	// What he holds he can give, and take back.
	for _, want := range [][]auth.ProjectPermission{canView, leosOwn} {
		if err := set(leo, mia.UserID, want); err != nil || !slices.Equal(granted(mia.UserID), want) {
			t.Fatalf("Leo gives Mia %v = %v, Mia holds %v", want, err, granted(mia.UserID))
		}
	}
	if err := remove(leo, mia.UserID); err != nil {
		t.Fatalf("Leo removes Mia = %v", err)
	}
	if n := leoChanges(); n != 3 {
		t.Fatalf("Leo's Activity rows = %d, want 3", n)
	}

	// An admin manages Payroll through projects.manage without reading it,
	// and is not capped: the same changes go through.
	for _, change := range []struct {
		person uuid.UUID
		want   []auth.ProjectPermission
	}{{mia.UserID, canApprove}, {olivia.UserID, canView}, {mia.UserID, canView}} {
		err := set(w.admin, change.person, change.want)
		if got := granted(change.person); err != nil || !slices.Equal(got, change.want) {
			t.Fatalf("admin sets %v = %v, they hold %v", change.want, err, got)
		}
	}

	// Join is not capped, and only the joined admin's own grant counts: Leo
	// may take back the read Join gave, though the admin still manages
	// Payroll through the bridge.
	joined, err := p.Join(ctx, w.admin, payroll.ID, revision())
	if err != nil || !slices.Contains(joined.Access.Permissions, read) || !joined.Access.Management {
		t.Fatalf("admin Join = %+v, %v", joined.Access, err)
	}
	err = remove(leo, w.admin.UserID)
	if left := granted(w.admin.UserID); err != nil || len(left) != 0 {
		t.Fatalf("Leo removes the joined admin's own Can view tasks = %v, left %v", err, left)
	}
	again, err := p.Join(ctx, w.admin, payroll.ID, revision())
	if err != nil || !slices.Contains(again.Access.Permissions, read) {
		t.Fatalf("admin Joins again = %+v, %v", again.Access, err)
	}
}

// Access is a union: a member of Finance, which holds read
// and execute on the Project, reads it in the Project list and read with the
// Group's permissions on top of their own grant, and removing their own
// grant leaves them a reader whose Runs go on, because Finance still gives
// execute.
func TestAGroupReaderKeepsTheProjectWhenTheirOwnGrantGoes(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	p := w.projects
	project := w.f.ProjectA
	finance := dbtest.SeedGroup(t, ctx, w.pool, w.f.CompanyA, "Finance")
	dbtest.GrantGroupProject(t, ctx, w.pool, w.f.CompanyA, project, finance, "project.tasks.execute")
	dbtest.AddGroupMembers(t, ctx, w.pool, w.f.CompanyA, finance, w.member.UserID)

	permissionsOf := func(name string) []auth.ProjectPermission {
		t.Helper()
		listed, err := p.List(ctx, w.member, false)
		if err != nil {
			t.Fatalf("%s: List() error = %v", name, err)
		}
		index := slices.IndexFunc(listed, func(value ProjectResponse) bool { return value.ID == project })
		if index < 0 {
			t.Fatalf("%s: the Group reader's Project list misses the Project", name)
		}
		read, err := p.Get(ctx, w.member, project)
		if err != nil || !slices.Equal(read.Access.Permissions, listed[index].Access.Permissions) {
			t.Fatalf(
				"%s: Get() = %+v, %v; want the list's %v",
				name,
				read.Access,
				err,
				listed[index].Access.Permissions,
			)
		}
		return read.Access.Permissions
	}
	groupOnly := []auth.ProjectPermission{auth.ProjectPermissionRead, auth.ProjectPermissionTasksExecute}
	if got := permissionsOf("through Finance"); !slices.Equal(got, groupOnly) {
		t.Fatalf("through Finance: permissions = %v, want %v", got, groupOnly)
	}

	granted, err := p.SetGrants(ctx, w.owner, project, ProjectGrantInput{
		UserID: w.member.UserID, Permissions: []string{"project.tasks.write"},
		ExpectedRevision: w.accessRevision(t, ctx, project),
	})
	if err != nil {
		t.Fatal(err)
	}
	both := []auth.ProjectPermission{
		auth.ProjectPermissionRead, auth.ProjectPermissionTasksExecute, auth.ProjectPermissionTasksWrite,
	}
	// The grant editor's row keeps the person's own rows apart from what
	// Finance adds to their effective set.
	own := []auth.ProjectPermission{auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite}
	if granted.Grant == nil || !slices.Equal(granted.Grant.Granted, own) ||
		!slices.Equal(granted.Grant.Effective, both) {
		t.Fatalf("SetGrants() grant = %+v; want granted %v, effective %v", granted.Grant, own, both)
	}
	if got := permissionsOf("own grant and Finance"); !slices.Equal(got, both) {
		t.Fatalf("own grant and Finance: permissions = %v, want %v", got, both)
	}

	run := w.queueMemberTaskRun(t, ctx)
	if _, err := p.RemoveGrants(
		ctx,
		w.owner,
		project,
		w.member.UserID,
		w.accessRevision(t, ctx, project),
	); err != nil {
		t.Fatal(err)
	}
	if got := permissionsOf("Finance after the own grant went"); !slices.Equal(got, groupOnly) {
		t.Fatalf("Finance after the own grant went: permissions = %v, want %v", got, groupOnly)
	}
	if status, reason, tokens := w.runOutcome(t, ctx, run); status != "queued" || reason != "" || tokens != 1 {
		t.Fatalf(
			"Run after the own grant went = %s %q tokens %d; Finance still gives execute",
			status,
			reason,
			tokens,
		)
	}
}

func TestProjectAccessLossCancelsTheMembersRuns(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	p := w.projects
	project := w.f.ProjectA

	grant := func(permissions ...string) ProjectGrantResult {
		t.Helper()
		result, err := p.SetGrants(ctx, w.owner, project, ProjectGrantInput{
			UserID:           w.member.UserID,
			Permissions:      permissions,
			ExpectedRevision: w.accessRevision(t, ctx, project),
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	grant("project.tasks.execute")
	run := w.queueMemberTaskRun(t, ctx)

	// Taking write keeps the Run; taking execute ends it.
	grant("project.tasks.execute", "project.tasks.write")
	grant("project.tasks.execute")
	if status, _, _ := w.runOutcome(t, ctx, run); status != "queued" {
		t.Fatalf("Run after an unrelated change = %s", status)
	}
	grant("project.read")
	status, reason, tokens := w.runOutcome(t, ctx, run)
	if status != "cancelled" || reason != execution.RunReasonProjectAccessRevoked || tokens != 0 {
		t.Fatalf("Run after losing execute = %s %q tokens %d", status, reason, tokens)
	}
	// The cancelled Run's end reaches its Task, as any terminal Run does.
	if status := w.taskStatus(t, ctx); status != "cancelled" {
		t.Fatalf("Task after its Run was cancelled = %s", status)
	}

	// A Company-visible Project made restricted ends the Runs of members who
	// held it only through the floor.
	if _, err := p.RemoveGrants(
		ctx,
		w.owner,
		project,
		w.member.UserID,
		w.accessRevision(t, ctx, project),
	); err != nil {
		t.Fatal(err)
	}
	current, err := p.Get(ctx, w.owner, project)
	if err != nil {
		t.Fatal(err)
	}
	company, restricted := auth.ProjectVisibilityCompany, auth.ProjectVisibilityRestricted
	accessRevision := w.accessRevision(t, ctx, project)
	opened, err := p.Update(ctx, w.owner, project, ProjectUpdateInput{
		ExpectedRevision: current.Revision, ExpectedAccessRevision: &accessRevision, Visibility: &company,
	})
	if err != nil {
		t.Fatal(err)
	}
	floorRun := w.queueMemberRun(t, ctx)
	if _, err := p.Update(ctx, w.owner, project, ProjectUpdateInput{
		ExpectedRevision:       opened.Revision,
		ExpectedAccessRevision: &opened.Access.Revision,
		Visibility:             &restricted,
	}); err != nil {
		t.Fatal(err)
	}
	status, reason, tokens = w.runOutcome(t, ctx, floorRun)
	if status != "cancelled" || reason != execution.RunReasonProjectAccessRevoked || tokens != 0 {
		t.Fatalf("Run after the Project was restricted = %s %q tokens %d", status, reason, tokens)
	}
	// One run.cancelled frame per cancelled Run.
	if n := len(w.runs.named(execution.EventRunCancelled)); n != 2 {
		t.Fatalf("run.cancelled frames = %d, want 2", n)
	}
}

// A person who may no longer post in a Project's Task chats loses the
// messages they queued there for the assignee: a grant change that takes
// project.tasks.write removes them, and so does restricting a Project the
// person held only through the Company floor. Their words would otherwise
// wait for a Run that may not write them under their name (ADR 0017).
func TestProjectAccessLossRemovesTheMembersQueuedMessages(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	p := w.projects
	project := w.f.ProjectA
	queue := func() uuid.UUID {
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
	outcome := func(id uuid.UUID) (state, reason string) {
		t.Helper()
		if err := w.pool.QueryRow(ctx, `
			SELECT state, COALESCE(removal_reason, '') FROM conversation_run_queue_items WHERE id = $1`,
			id).Scan(&state, &reason); err != nil {
			t.Fatal(err)
		}
		return state, reason
	}
	grant := func(permissions ...string) {
		t.Helper()
		if _, err := p.SetGrants(ctx, w.owner, project, ProjectGrantInput{
			UserID:           w.member.UserID,
			Permissions:      permissions,
			ExpectedRevision: w.accessRevision(t, ctx, project),
		}); err != nil {
			t.Fatal(err)
		}
	}

	grant("project.tasks.write")
	queued := queue()
	grant("project.tasks.write", "project.tasks.execute")
	if state, _ := outcome(queued); state != "queued" {
		t.Fatalf("queued message after a change that kept writing = %s", state)
	}
	grant("project.read")
	if state, reason := outcome(queued); state != "removed" || reason != "project_access_revoked" {
		t.Fatalf("queued message after losing project.tasks.write = %s (%q)", state, reason)
	}

	if _, err := p.RemoveGrants(
		ctx,
		w.owner,
		project,
		w.member.UserID,
		w.accessRevision(t, ctx, project),
	); err != nil {
		t.Fatal(err)
	}
	current, err := p.Get(ctx, w.owner, project)
	if err != nil {
		t.Fatal(err)
	}
	company, restricted := auth.ProjectVisibilityCompany, auth.ProjectVisibilityRestricted
	accessRevision := w.accessRevision(t, ctx, project)
	opened, err := p.Update(ctx, w.owner, project, ProjectUpdateInput{
		ExpectedRevision: current.Revision, ExpectedAccessRevision: &accessRevision, Visibility: &company,
	})
	if err != nil {
		t.Fatal(err)
	}
	floorQueued := queue()
	if _, err := p.Update(ctx, w.owner, project, ProjectUpdateInput{
		ExpectedRevision:       opened.Revision,
		ExpectedAccessRevision: &opened.Access.Revision,
		Visibility:             &restricted,
	}); err != nil {
		t.Fatal(err)
	}
	if state, reason := outcome(floorQueued); state != "removed" || reason != "project_access_revoked" {
		t.Fatalf("queued message after the Project was restricted = %s (%q)", state, reason)
	}
}

// A member whose Run an access change stopped hears about it while they
// still read the Project: they lost only project.tasks.execute, by a grant
// change or by a Company-visible Project made restricted, and get one
// run_failed Inbox item naming the Agent and the Task, saying they can no
// longer run tasks in this work, never the Project. A member who lost
// project.read gets nothing: the Project is hidden from them now.
func TestProjectAccessLossTellsTheMemberWhoseRunStopped(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	p := w.projects
	project := w.f.ProjectA
	// A name no notice sentence could contain by chance.
	dbtest.MustExec(
		t,
		ctx,
		w.pool,
		`UPDATE projects SET key = 'payroll', title = 'Payroll Ledger' WHERE id = $1`,
		project,
	)
	var projectKey, projectTitle, taskTitle, agentName string
	if err := w.pool.QueryRow(ctx, `
		SELECT project.key, project.title, task.title, agent.name
		FROM projects AS project, tasks AS task, agents AS agent
		WHERE project.id = $1 AND task.id = $2 AND agent.id = $3`,
		project, w.f.TaskA, w.f.AgentA).Scan(&projectKey, &projectTitle, &taskTitle, &agentName); err != nil {
		t.Fatal(err)
	}
	grant := func(permissions ...string) {
		t.Helper()
		if _, err := p.SetGrants(ctx, w.owner, project, ProjectGrantInput{
			UserID:           w.member.UserID,
			Permissions:      permissions,
			ExpectedRevision: w.accessRevision(t, ctx, project),
		}); err != nil {
			t.Fatal(err)
		}
	}
	type notice struct {
		kind, title, body string
		projectID         uuid.UUID
	}
	notices := func(runID uuid.UUID) []notice {
		t.Helper()
		rows, err := w.pool.Query(ctx, `
			SELECT kind, title, body, project_id FROM inbox_items
			WHERE company_id = $1 AND recipient_user_id = $2 AND entity_type = 'run' AND entity_id = $3`,
			w.f.CompanyA, w.member.UserID, runID.String())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var found []notice
		for rows.Next() {
			var item notice
			var projectID uuid.NullUUID
			if err := rows.Scan(&item.kind, &item.title, &item.body, &projectID); err != nil {
				t.Fatal(err)
			}
			item.projectID = projectID.UUID
			found = append(found, item)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return found
	}
	assertTold := func(what string, runID uuid.UUID) {
		t.Helper()
		if status, reason, _ := w.runOutcome(t, ctx, runID); status != "cancelled" ||
			reason != execution.RunReasonProjectAccessRevoked {
			t.Fatalf("%s: Run = %s %q, want cancelled for project_access_revoked", what, status, reason)
		}
		found := notices(runID)
		if len(found) != 1 {
			t.Fatalf("%s: %d Inbox item(s) about the Run, want one", what, len(found))
		}
		item := found[0]
		wantBody := agentName + " stopped: your access to this work no longer includes running tasks. " +
			"Ask someone who manages this work to let you run tasks."
		if item.kind != "run_failed" || item.body != wantBody || item.projectID != project ||
			!strings.HasPrefix(item.title, "Run stopped: ") {
			t.Fatalf("%s: Inbox item = %+v, want run_failed %q in the Project", what, item, wantBody)
		}
		for _, text := range []string{item.title, item.body} {
			if strings.Contains(text, projectKey) || strings.Contains(text, projectTitle) {
				t.Fatalf("%s: the notice names the Project: %q", what, text)
			}
		}
	}
	assertNotTold := func(what string, runID uuid.UUID) {
		t.Helper()
		if status, reason, _ := w.runOutcome(t, ctx, runID); status != "cancelled" ||
			reason != execution.RunReasonProjectAccessRevoked {
			t.Fatalf("%s: Run = %s %q, want cancelled for project_access_revoked", what, status, reason)
		}
		if found := notices(runID); len(found) != 0 {
			t.Fatalf("%s: the member was told about a Project they cannot read: %+v", what, found)
		}
	}

	// Lost execute, kept read: told, with the Task they still read.
	grant("project.tasks.execute")
	run := w.queueMemberTaskRun(t, ctx)
	grant("project.read")
	assertTold("execute taken by a grant change", run)
	if found := notices(run); !strings.Contains(found[0].title, taskTitle) {
		t.Fatalf("the notice does not name the Task %q: %q", taskTitle, found[0].title)
	}

	// Lost read: nothing.
	grant("project.tasks.execute")
	hidden := w.queueMemberRun(t, ctx)
	if _, err := p.RemoveGrants(
		ctx,
		w.owner,
		project,
		w.member.UserID,
		w.accessRevision(t, ctx, project),
	); err != nil {
		t.Fatal(err)
	}
	assertNotTold("every permission removed", hidden)

	// A Company-visible Project made restricted: a member who keeps a read
	// row loses execute (it came from the floor) and is told; one who held
	// everything through the floor loses read and is not.
	setVisibility := func(visibility auth.ProjectVisibility) {
		t.Helper()
		current, err := p.Get(ctx, w.owner, project)
		if err != nil {
			t.Fatal(err)
		}
		revision := w.accessRevision(t, ctx, project)
		if _, err := p.Update(ctx, w.owner, project, ProjectUpdateInput{
			ExpectedRevision: current.Revision, ExpectedAccessRevision: &revision, Visibility: &visibility,
		}); err != nil {
			t.Fatal(err)
		}
	}
	setVisibility(auth.ProjectVisibilityCompany)
	floorOnly := w.queueMemberRun(t, ctx)
	setVisibility(auth.ProjectVisibilityRestricted)
	assertNotTold("the floor taken from a member with no rows", floorOnly)

	grant("project.read")
	setVisibility(auth.ProjectVisibilityCompany)
	readRow := w.queueMemberRun(t, ctx)
	setVisibility(auth.ProjectVisibilityRestricted)
	assertTold("the floor taken from a member who keeps read", readRow)
}

func TestCompanyRevocationDeletesProjectGrants(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	project := w.f.ProjectA
	if _, err := w.projects.SetGrants(ctx, w.owner, project, ProjectGrantInput{
		UserID:           w.member.UserID,
		Permissions:      []string{"project.tasks.execute"},
		ExpectedRevision: w.accessRevision(t, ctx, project),
	}); err != nil {
		t.Fatal(err)
	}
	run := w.queueMemberRun(t, ctx)
	revisionBefore := w.accessRevision(t, ctx, project)
	var memberVersion int64
	if err := w.pool.QueryRow(ctx, `SELECT version FROM company_members WHERE company_id = $1 AND user_id = $2`,
		w.f.CompanyA, w.member.UserID).Scan(&memberVersion); err != nil {
		t.Fatal(err)
	}
	framesBefore := len(w.log.named(EventProjectAccessChanged))

	if err := w.companies.RemoveMember(
		ctx,
		w.owner,
		MembershipMutation{UserID: w.member.UserID, ExpectedRevision: memberVersion},
	); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM project_grants WHERE company_id = $1 AND user_id = $2`,
		w.f.CompanyA, w.member.UserID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("grants left after revocation = %d, %v", rows, err)
	}
	if revision := w.accessRevision(t, ctx, project); revision != revisionBefore+1 {
		t.Fatalf("access revision = %d, want %d", revision, revisionBefore+1)
	}
	frames := w.log.named(EventProjectAccessChanged)
	if len(frames) != framesBefore+1 {
		t.Fatalf("access frames = %d, want %d", len(frames), framesBefore+1)
	}
	change := payloadOf[ProjectAccessChanged](t, frames[len(frames)-1])
	if change.ProjectID != project || change.Revision != revisionBefore+1 ||
		!slices.Contains(
			change.RecipientUserIDs,
			w.member.UserID,
		) || !slices.Contains(change.RecipientUserIDs, w.owner.UserID) {
		t.Fatalf("revocation frame = %+v", change)
	}
	// The runtime-owner sweep ends the member's Run; the Project sweep is
	// not run on top of it, so the reason is the runtime owner's.
	status, reason, tokens := w.runOutcome(t, ctx, run)
	if status != "cancelled" || reason != "runtime_owner_inactive" || tokens != 0 {
		t.Fatalf("revoked member's Run = %s %q tokens %d", status, reason, tokens)
	}
	// The removal is a grant change like any other: one project_grant row
	// naming the member, what they held, and nothing after.
	var removal struct{ before, after []string }
	if err := w.pool.QueryRow(ctx, `
		SELECT ARRAY(SELECT jsonb_array_elements_text(details->'payload'->'before')),
		       ARRAY(SELECT jsonb_array_elements_text(details->'payload'->'after'))
		FROM activity_events
		WHERE company_id = $1 AND action = $2 AND project_id = $3 AND actor_id = $4::text
		  AND details->'payload'->>'userId' = $5::text
		ORDER BY created_at DESC LIMIT 1`,
		w.f.CompanyA, projectGrantChanged, project, w.owner.UserID, w.member.UserID,
	).Scan(&removal.before, &removal.after); err != nil {
		t.Fatalf("revocation Activity row: %v", err)
	}
	if !slices.Equal(removal.before, []string{"project.read", "project.tasks.execute"}) || len(removal.after) != 0 {
		t.Fatalf("revocation Activity = %+v", removal)
	}

	// Rejoined, the member holds nothing from before. A row left from the
	// earlier membership does not count, so granting that same set again
	// writes it afresh, past the rejoin, and records it.
	dbtest.MustExec(t, ctx, w.pool, `
		UPDATE company_members SET status = 'active', reactivated_at = clock_timestamp(), version = version + 1
		WHERE company_id = $1 AND user_id = $2`, w.f.CompanyA, w.member.UserID)
	dbtest.MustExec(t, ctx, w.pool, `
		INSERT INTO project_grants (company_id, project_id, user_id, permission, created_at, updated_at)
		VALUES ($1, $2, $3, 'project.read', now() - interval '1 day', now() - interval '1 day')`,
		w.f.CompanyA, project, w.member.UserID)
	changedBefore := w.grantActivity(t, ctx, project, projectGrantChanged, w.owner.UserID)
	regranted, err := w.projects.SetGrants(ctx, w.owner, project, ProjectGrantInput{
		UserID:           w.member.UserID,
		Permissions:      []string{"project.read"},
		ExpectedRevision: w.accessRevision(t, ctx, project),
	})
	if err != nil || regranted.Grant == nil ||
		!slices.Equal(regranted.Grant.Granted, []auth.ProjectPermission{auth.ProjectPermissionRead}) {
		t.Fatalf("re-grant after rejoin = %+v, %v", regranted, err)
	}
	var fenced bool
	if err := w.pool.QueryRow(ctx, `
		SELECT bool_and(grant_row.updated_at >= member.reactivated_at)
		FROM project_grants AS grant_row
		JOIN company_members AS member ON member.company_id = grant_row.company_id AND member.user_id = grant_row.user_id
		WHERE grant_row.company_id = $1 AND grant_row.project_id = $2 AND grant_row.user_id = $3`,
		w.f.CompanyA, project, w.member.UserID).Scan(&fenced); err != nil || !fenced {
		t.Fatalf("re-granted rows past the rejoin = %t, %v", fenced, err)
	}
	if n := w.grantActivity(t, ctx, project, projectGrantChanged, w.owner.UserID); n != changedBefore+1 {
		t.Fatalf("re-grant Activity rows = %d, want %d", n, changedBefore+1)
	}
}
