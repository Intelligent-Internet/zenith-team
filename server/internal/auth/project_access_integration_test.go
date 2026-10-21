//go:build integration

package auth

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// projectAccessWorld is Company A of the shared fixture plus the people the
// Project access rule tells apart. ProjectA stays restricted; its only grant
// holder is the owner, UserA (the fixture's owner backfill). companyProject is
// open to the Company.
type projectAccessWorld struct {
	f              dbtest.Fixture
	pool           *pgxpool.Pool
	q              *generated.Queries
	companyProject uuid.UUID
	admin          uuid.UUID
	member         uuid.UUID
	revoked        uuid.UUID
	disabled       uuid.UUID
}

func newProjectAccessWorld(t *testing.T) projectAccessWorld {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	w := projectAccessWorld{f: dbtest.SeedFixture(t, ctx, pool), pool: pool, q: generated.New(pool)}
	for _, person := range []struct {
		id         *uuid.UUID
		name, role string
		status     string
		disabled   bool
	}{
		{&w.admin, "admin", "admin", "active", false},
		{&w.member, "member", "member", "active", false},
		{&w.revoked, "revoked", "member", "revoked", false},
		{&w.disabled, "disabled", "member", "active", true},
	} {
		*person.id = dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `INSERT INTO users (id, email, display_name, disabled_at)
			VALUES ($1, $2, $2, CASE WHEN $3 THEN now() END)`,
			*person.id, person.name+"@project-access.example.com", person.disabled)
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO company_members (company_id, user_id, role, status, revoked_at)
			VALUES ($1, $2, $3, $4, CASE WHEN $4 = 'revoked' THEN now() END)`,
			w.f.CompanyA,
			*person.id,
			person.role,
			person.status,
		)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (company_id, key, title, visibility)
		VALUES ($1, 'open', 'Open', 'company') RETURNING id`, w.f.CompanyA).Scan(&w.companyProject); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w projectAccessWorld) held(t *testing.T, projectID, userID uuid.UUID, permission ProjectPermission) bool {
	t.Helper()
	held, err := HoldsProjectPermission(t.Context(), w.q, w.f.CompanyA, projectID, userID, permission)
	if err != nil {
		t.Fatalf("ProjectPermissionHeld: %v", err)
	}
	return held
}

func (w projectAccessWorld) replace(
	t *testing.T,
	projectID, userID uuid.UUID,
	permissions ...ProjectPermission,
) []string {
	t.Helper()
	kept, err := w.q.ReplaceProjectGrants(t.Context(), generated.ReplaceProjectGrantsParams{
		CompanyID:   w.f.CompanyA,
		ProjectID:   projectID,
		UserID:      userID,
		Permissions: ProjectPermissionStrings(permissions),
		ActorUserID: w.f.UserA,
	})
	if err != nil {
		t.Fatalf("ReplaceProjectGrants: %v", err)
	}
	return kept
}

func (w projectAccessWorld) exists(t *testing.T, query string, args ...any) bool {
	t.Helper()
	var found bool
	if err := w.pool.QueryRow(t.Context(), `SELECT EXISTS (`+query+`)`, args...).Scan(&found); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return found
}

func (w projectAccessWorld) principal(userID uuid.UUID) UserPrincipal {
	return UserPrincipal{UserID: userID, CompanyID: w.f.CompanyA}
}

func TestProjectPermissionHeldTruthTable(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	restricted := w.f.ProjectA
	contentAndDeciding := []ProjectPermission{}
	for _, permission := range ProjectPermissions() {
		if !slices.Contains(ManagementProjectPermissions(), permission) {
			contentAndDeciding = append(contentAndDeciding, permission)
		}
	}
	if len(contentAndDeciding) != 6 {
		t.Fatalf("content and deciding permissions = %v, want six", contentAndDeciding)
	}

	for _, permission := range ProjectPermissions() {
		if !w.held(t, restricted, w.f.UserA, permission) {
			t.Errorf("backfilled owner lacks %s", permission)
		}
		if w.held(t, w.companyProject, w.revoked, permission) {
			t.Errorf("revoked member holds %s on a Company Project", permission)
		}
		if w.held(t, w.companyProject, w.disabled, permission) {
			t.Errorf("disabled account holds %s on a Company Project", permission)
		}
		if w.held(t, restricted, w.member, permission) {
			t.Errorf("member without rows holds %s on a restricted Project", permission)
		}
		wantAdmin := slices.Contains(ManagementProjectPermissions(), permission)
		if got := w.held(t, restricted, w.admin, permission); got != wantAdmin {
			t.Errorf("admin without rows holds %s = %t, want %t", permission, got, wantAdmin)
		}
		wantFloor := slices.Contains(CompanyProjectDefaultPermissions(), permission)
		if got := w.held(t, w.companyProject, w.member, permission); got != wantFloor {
			t.Errorf("member on a Company Project holds %s = %t, want %t", permission, got, wantFloor)
		}
	}
	if w.held(t, uuid.New(), w.f.UserA, ProjectPermissionRead) {
		t.Error("a Project that does not exist grants read")
	}

	// A grant written before a Company rejoin is fenced; a deliberate
	// re-grant after it refreshes updated_at and passes again.
	w.replace(t, restricted, w.member, ProjectPermissionRead)
	if !w.held(t, restricted, w.member, ProjectPermissionRead) {
		t.Fatal("fresh grant does not hold")
	}
	dbtest.MustExec(t, t.Context(), w.pool, `UPDATE company_members SET reactivated_at = clock_timestamp()
		WHERE company_id = $1 AND user_id = $2`, w.f.CompanyA, w.member)
	if w.held(t, restricted, w.member, ProjectPermissionRead) {
		t.Error("grant written before the rejoin still holds")
	}
	if w.exists(t, `SELECT 1 FROM project_access WHERE project_id = $1 AND user_id = $2`, restricted, w.member) {
		t.Error("project_access admits a grant written before the rejoin")
	}
	w.replace(t, restricted, w.member, ProjectPermissionRead)
	if !w.held(t, restricted, w.member, ProjectPermissionRead) {
		t.Error("re-grant after the rejoin does not hold")
	}
}

func TestProjectAccessViews(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()
	restricted := w.f.ProjectA

	via := func(projectID, userID uuid.UUID) ProjectAccessVia {
		t.Helper()
		var got ProjectAccessVia
		err := w.pool.QueryRow(ctx, `SELECT via FROM project_access WHERE project_id = $1 AND user_id = $2`,
			projectID, userID).Scan(&got)
		if errors.Is(err, pgx.ErrNoRows) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, test := range []struct {
		name    string
		project uuid.UUID
		user    uuid.UUID
		want    ProjectAccessVia
	}{
		{"owner by grant", restricted, w.f.UserA, ProjectAccessViaGrant},
		{"bridge holder gets no row", restricted, w.admin, ""},
		{"member without a grant", restricted, w.member, ""},
		{"member on a Company Project", w.companyProject, w.member, ProjectAccessViaCompany},
		{"owner on a Company Project without rows", w.companyProject, w.f.UserA, ProjectAccessViaCompany},
		{"revoked member", w.companyProject, w.revoked, ""},
		{"disabled account", w.companyProject, w.disabled, ""},
	} {
		if got := via(test.project, test.user); got != test.want {
			t.Errorf("%s: via = %q, want %q", test.name, got, test.want)
		}
	}
	w.replace(t, w.companyProject, w.member, ProjectPermissionRead, ProjectPermissionFilesManage)
	if got := via(w.companyProject, w.member); got != ProjectAccessViaGrant {
		t.Errorf("granted member on a Company Project: via = %q, want grant", got)
	}

	// task_access follows the Project.
	taskReadable := func(userID uuid.UUID) bool {
		return w.exists(t, `SELECT 1 FROM task_access WHERE task_id = $1 AND user_id = $2 AND project_id = $3
			AND conversation_id = $4`, w.f.TaskA, userID, restricted, w.f.ConversationA)
	}
	if !taskReadable(w.f.UserA) || taskReadable(w.member) || taskReadable(w.admin) {
		t.Error("task_access does not follow project_access on a restricted Project")
	}

	reads := func(conversationID, userID uuid.UUID) bool {
		return w.exists(t, `SELECT 1 FROM conversation_readers WHERE conversation_id = $1 AND user_id = $2`,
			conversationID, userID)
	}
	// Task chats follow the Project; direct chats their creator.
	if !reads(w.f.ConversationA, w.f.UserA) || reads(w.f.ConversationA, w.member) ||
		reads(w.f.ConversationA, w.admin) {
		t.Error("task chat readers do not follow the Project")
	}
	if !reads(w.f.DirectConversationA, w.f.UserA) || reads(w.f.DirectConversationA, w.member) {
		t.Error("direct chat readers are not its creator alone")
	}
	// A Task chat detached before 000086 recorded no Project: every active
	// member reads it, nobody else.
	var legacy uuid.UUID
	if err := w.pool.QueryRow(ctx, `INSERT INTO conversations (company_id, kind, title, created_at, updated_at)
		VALUES ($1, 'task', 'Legacy', '2020-01-01', '2020-01-01') RETURNING id`, w.f.CompanyA).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if !reads(legacy, w.member) || !reads(legacy, w.admin) || reads(legacy, w.revoked) ||
		reads(legacy, w.disabled) {
		t.Error("legacy detached Task chat readers are not the active members")
	}

	// A channel: members read it; the channel file rule is below.
	channel := dbtest.SeedDiscussionChannel(t, ctx, w.pool, w.f.CompanyA, w.f.UserA, nil)
	dbtest.WithinSeedTx(t, ctx, w.pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_members (company_id, conversation_id, user_id, source_type)
			VALUES ($1, $2, $3, 'channel')`,
			w.f.CompanyA,
			channel,
			w.member,
		)
		dbtest.MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_user_states (company_id, conversation_id, user_id, grant_generation)
			VALUES ($1, $2, $3, 1)`,
			w.f.CompanyA,
			channel,
			w.member,
		)
	})
	if !reads(channel, w.f.UserA) || !reads(channel, w.member) || reads(channel, w.admin) {
		t.Error("channel readers are not its active members")
	}

	// A channel Agent's execution chat: the callers of live invocations.
	var messageID, agentChat, invocation uuid.UUID
	dbtest.WithinSeedTx(t, ctx, w.pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `
			INSERT INTO conversation_items
				(company_id, conversation_id, seq, type, author_kind, author_id,
				 content, body, request_key, send_fingerprint)
			VALUES ($1, $2, 1, 'user_message', 'user', $3, 'See the brief',
				'{"markdown":"See the brief"}'::jsonb, gen_random_uuid(),
				decode(repeat('11', 32), 'hex'))
			RETURNING id`, w.f.CompanyA, channel, w.f.UserA).Scan(&messageID); err != nil {
			t.Fatalf("seed channel message: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO conversations (company_id, kind, channel_id, agent_id, created_by_user_id, title)
			VALUES ($1, 'channel_agent', $2, $3, $4, 'Agent A') RETURNING id`,
			w.f.CompanyA, channel, w.f.AgentA, w.f.UserA).Scan(&agentChat); err != nil {
			t.Fatalf("seed channel-Agent chat: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO channel_invocations
				(company_id, channel_id, conversation_id, agent_id, trigger_message_id,
				 thread_root_id, caller_user_id, agent_generation, member_generation,
				 channel_generation, trigger_content, trigger_revision, status)
			VALUES ($1, $2, $3, $4, $5, $5, $6, 1, 1, 1, 'See the brief', 1, 'waiting_input')
			RETURNING id`, w.f.CompanyA, channel, agentChat, w.f.AgentA, messageID, w.member).
			Scan(&invocation); err != nil {
			t.Fatalf("seed channel invocation: %v", err)
		}
	})
	if !reads(agentChat, w.member) || reads(agentChat, w.f.UserA) {
		t.Error("channel-Agent chat readers are not the live callers")
	}
	dbtest.MustExec(t, ctx, w.pool, `UPDATE channel_invocations SET status = 'completed' WHERE id = $1`, invocation)
	if reads(agentChat, w.member) {
		t.Error("a finished invocation's caller still reads the channel-Agent chat")
	}

	// attachment_access. A file registered as ProjectA's document and shared
	// in the channel: the non-reader channel member cannot open it; once the
	// file is retired from the Project it follows the chat again.
	attachment := func(name string, uploader uuid.UUID) uuid.UUID {
		var id uuid.UUID
		if err := w.pool.QueryRow(ctx, `INSERT INTO attachments
			(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
			VALUES ($1, $2, gen_random_uuid(), $3, 'text/plain', 1, 'ready') RETURNING id`,
			w.f.CompanyA, uploader, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	opens := func(attachmentID, userID uuid.UUID) bool {
		return w.exists(t, `SELECT 1 FROM attachment_access WHERE attachment_id = $1 AND user_id = $2`,
			attachmentID, userID)
	}
	brief := attachment("brief.txt", w.f.UserA)
	dbtest.MustExec(
		t,
		ctx,
		w.pool,
		`INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		VALUES ($1, $2, $3, 0)`,
		w.f.CompanyA,
		messageID,
		brief,
	)
	if !opens(brief, w.member) || !opens(brief, w.f.UserA) {
		t.Error("a chat upload does not follow the chat's readers")
	}
	dbtest.MustExec(t, ctx, w.pool, `INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
		VALUES ($1, $2, $3, 'document')`, w.f.CompanyA, restricted, brief)
	if !opens(brief, w.f.UserA) || opens(brief, w.member) || opens(brief, w.admin) {
		t.Error("a Project file shared in a channel does not follow the Project's readers alone")
	}
	var rows int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM attachment_access WHERE attachment_id = $1`, brief).
		Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("attachment_access rows for the Project file = %d, want the one reader", rows)
	}
	dbtest.MustExec(t, ctx, w.pool, `UPDATE project_files SET removed_at = clock_timestamp()
		WHERE project_id = $1 AND attachment_id = $2`, restricted, brief)
	if !opens(brief, w.member) || !opens(brief, w.f.UserA) {
		t.Error("a retired Project file does not follow the chat again")
	}
	// A file in two Projects yields one row per reader.
	both := attachment("both.txt", w.f.UserA)
	dbtest.MustExec(t, ctx, w.pool, `INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
		VALUES ($1, $2, $4, 'document'), ($1, $3, $4, 'document')`, w.f.CompanyA, restricted, w.companyProject, both)
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM attachment_access WHERE attachment_id = $1 AND user_id = $2`,
		both, w.f.UserA).
		Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || !opens(both, w.member) {
		t.Errorf("file in two Projects: owner rows = %d, member opens = %t", rows, opens(both, w.member))
	}
	// An upload linked nowhere follows its uploader.
	loose := attachment("loose.txt", w.member)
	if !opens(loose, w.member) || opens(loose, w.f.UserA) {
		t.Error("an unlinked upload does not follow its uploader alone")
	}
	// An upload the uploader then registered in a restricted Project they
	// cannot read is closed to them.
	dbtest.MustExec(t, ctx, w.pool, `INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
		VALUES ($1, $2, $3, 'document')`, w.f.CompanyA, restricted, loose)
	if opens(loose, w.member) || !opens(loose, w.f.UserA) {
		t.Error("a Project file is open to its non-reader uploader")
	}
}

// The Company-visibility arm of output_access reaches only readers of the
// source Project when the Artifact came from a restricted Project's Task
// chat. Artifacts from chats with no Project keep the old rule.
func TestOutputAccessCompanyArmFollowsSourceProject(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()
	output := func(runID uuid.UUID) uuid.UUID {
		var id uuid.UUID
		if err := w.pool.QueryRow(ctx, `INSERT INTO outputs
			(company_id, run_id, title, summary, presentation_fingerprint, owner_user_id, visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id) SELECT $1, $2, 'Report', 'Summary', md5(gen_random_uuid()::text), $3, 'company', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.company_id=origin.company_id AND conversation.id=origin.conversation_id WHERE origin.company_id=$1 AND origin.id=$2 RETURNING id`,
			w.f.CompanyA, runID, w.f.UserA).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	sees := func(outputID, userID uuid.UUID) bool {
		return w.exists(
			t,
			`SELECT 1 FROM output_access WHERE output_id = $1 AND user_id = $2`,
			outputID,
			userID,
		)
	}
	fromTask := output(w.f.RunA)
	var directRun uuid.UUID
	if err := w.pool.QueryRow(ctx, `INSERT INTO runs
		(company_id, agent_id, conversation_id, runtime_id, status, run_type, revision)
		VALUES ($1, $2, $3, $4, 'completed', 'direct_chat', 1) RETURNING id`,
		w.f.CompanyA, w.f.AgentA, w.f.DirectConversationA, w.f.RuntimeA).Scan(&directRun); err != nil {
		t.Fatal(err)
	}
	fromDirect := output(directRun)

	if !sees(fromTask, w.f.UserA) || sees(fromTask, w.member) || sees(fromTask, w.admin) {
		t.Error("a restricted Project's Company Artifact reaches non-readers")
	}
	if !sees(fromDirect, w.member) || !sees(fromDirect, w.admin) {
		t.Error("a Company Artifact from a direct chat no longer reaches every member")
	}
	w.replace(t, w.f.ProjectA, w.member, ProjectPermissionRead, ProjectPermissionTasksWrite)
	var rows int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM output_access WHERE output_id = $1 AND user_id = $2`,
		fromTask, w.member).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("granted reader output_access rows = %d, want 1", rows)
	}
	dbtest.MustExec(t, ctx, w.pool, `UPDATE projects SET visibility = 'company' WHERE id = $1`, w.f.ProjectA)
	if !sees(fromTask, w.admin) {
		t.Error("a Company-visible Project's Company Artifact does not reach every member")
	}
}

func TestReplaceProjectGrantsKeepsExactlyThePassedSet(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()
	project := w.f.ProjectA
	stored := func(userID uuid.UUID) []string {
		t.Helper()
		var permissions []string
		if err := w.pool.QueryRow(ctx, `SELECT COALESCE(array_agg(permission ORDER BY permission), '{}')
			FROM project_grants WHERE project_id = $1 AND user_id = $2`, project, userID).Scan(&permissions); err != nil {
			t.Fatal(err)
		}
		return permissions
	}
	updatedAt := func(permission ProjectPermission) time.Time {
		t.Helper()
		var at time.Time
		if err := w.pool.QueryRow(ctx, `SELECT updated_at FROM project_grants
			WHERE project_id = $1 AND user_id = $2 AND permission = $3`, project, w.member, string(permission)).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}

	kept := w.replace(t, project, w.member, ProjectPermissionRead, ProjectPermissionTasksWrite)
	if !slices.Equal(kept, []string{"project.read", "project.tasks.write"}) {
		t.Fatalf("first replace returned %v", kept)
	}
	firstRead := updatedAt(ProjectPermissionRead)
	kept = w.replace(t, project, w.member, ProjectPermissionArchive, ProjectPermissionRead)
	if !slices.Equal(kept, []string{"project.archive", "project.read"}) ||
		!slices.Equal(stored(w.member), []string{"project.archive", "project.read"}) {
		t.Fatalf("second replace returned %v, stored %v", kept, stored(w.member))
	}
	if !updatedAt(ProjectPermissionRead).After(firstRead) {
		t.Error("a kept row's updated_at was not refreshed")
	}
	// Normalization is Go's job; a set without project.read is refused at
	// commit by project_grants_read_fkey and nothing changes.
	_, err := w.q.ReplaceProjectGrants(ctx, generated.ReplaceProjectGrantsParams{
		CompanyID: w.f.CompanyA, ProjectID: project, UserID: w.member,
		Permissions: []string{string(ProjectPermissionTasksWrite)}, ActorUserID: w.f.UserA,
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Errorf("replace without read error = %v, want 23503", err)
	}
	if got := stored(w.member); !slices.Equal(got, []string{"project.archive", "project.read"}) {
		t.Errorf("refused replace changed the rows to %v", got)
	}
	// An empty set is a no-op (removal is DeleteProjectGrantsForUser).
	if kept := w.replace(t, project, w.member); len(kept) != 0 ||
		!slices.Equal(stored(w.member), []string{"project.archive", "project.read"}) {
		t.Errorf("empty replace returned %v, stored %v", kept, stored(w.member))
	}
	kept = w.replace(t, project, w.member, ProjectPermissionRead, ProjectPermissionTasksWrite)
	if !slices.Equal(kept, []string{"project.read", "project.tasks.write"}) {
		t.Fatalf("third replace returned %v", kept)
	}
	// An inactive member or a disabled account gets no rows, and neither arm
	// touches rows they already hold.
	if kept := w.replace(
		t,
		project,
		w.revoked,
		ProjectPermissionRead,
	); len(kept) != 0 ||
		len(stored(w.revoked)) != 0 {
		t.Errorf("revoked member replace returned %v, stored %v", kept, stored(w.revoked))
	}
	dbtest.MustExec(t, ctx, w.pool, `INSERT INTO project_grants (company_id, project_id, user_id, permission)
		VALUES ($1, $2, $3, 'project.read'), ($1, $2, $3, 'project.archive')`, w.f.CompanyA, project, w.disabled)
	if kept := w.replace(t, project, w.disabled, ProjectPermissionRead); len(kept) != 0 ||
		!slices.Equal(stored(w.disabled), []string{"project.archive", "project.read"}) {
		t.Errorf("disabled account replace returned %v, stored %v", kept, stored(w.disabled))
	}
	dbtest.MustExec(
		t,
		ctx,
		w.pool,
		`DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		project,
		w.disabled,
	)
	grants, err := w.q.ListProjectAccessPaths(
		ctx,
		generated.ListProjectAccessPathsParams{
			CompanyID:       w.f.CompanyA,
			ProjectID:       project,
			ManagementRoles: RolesWith(PermissionProjectsManage),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 || grants[0].Via != "grant" || grants[1].Via != "grant" {
		t.Fatalf("ListProjectAccessPaths = %+v, want the owner's and the member's own grants", grants)
	}
	for _, grant := range grants {
		switch grant.UserID {
		case w.f.UserA:
			if len(grant.Permissions) != 9 || grant.GrantedByUserID.UUID != w.f.UserA || !grant.Management {
				t.Errorf("owner grant = %+v", grant)
			}
		case w.member:
			if !slices.Equal(grant.Permissions, []string{"project.read", "project.tasks.write"}) ||
				grant.GrantedByUserID.UUID != w.f.UserA ||
				grant.Management {
				t.Errorf("member grant = %+v", grant)
			}
		default:
			t.Errorf("unexpected grant holder %v", grant.UserID)
		}
	}
	removed, err := w.q.DeleteProjectGrantsForUser(ctx, generated.DeleteProjectGrantsForUserParams{
		CompanyID: w.f.CompanyA, ProjectID: project, UserID: w.member,
	})
	slices.Sort(removed)
	if err != nil || !slices.Equal(removed, []string{"project.read", "project.tasks.write"}) {
		t.Errorf("DeleteProjectGrantsForUser = %v, %v", removed, err)
	}
}

func TestProjectAccessChecks(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()
	restricted := w.f.ProjectA

	owner, err := RequireProjectPermission(
		ctx,
		w.q,
		w.principal(w.f.UserA),
		restricted,
		ProjectPermissionFilesManage,
	)
	if err != nil || !slices.Equal(owner.Permissions, ProjectPermissions()) || owner.Via != ProjectAccessViaGrant ||
		!owner.Management || owner.Revision != 1 || owner.Visibility != ProjectVisibilityRestricted {
		t.Errorf("owner access = %+v, %v", owner, err)
	}

	admin, err := RequireProjectVisible(ctx, w.q, w.principal(w.admin), restricted)
	if err != nil || !admin.Management || admin.Via != "" ||
		!slices.Equal(admin.Permissions, ManagementProjectPermissions()) {
		t.Errorf("admin access = %+v, %v", admin, err)
	}
	if _, err := RequireProjectPermission(
		ctx,
		w.q,
		w.principal(w.admin),
		restricted,
		ProjectPermissionRead,
	); !errors.Is(
		err,
		ErrProjectPermissionRequired,
	) {
		t.Errorf("admin read error = %v, want ErrProjectPermissionRequired", err)
	}
	if _, err := RequireProjectPermission(
		ctx,
		w.q,
		w.principal(w.admin),
		restricted,
		ProjectPermissionGrantsManage,
	); err != nil {
		t.Errorf("admin grants.manage error = %v", err)
	}
	if _, err := RequireTaskPermission(
		ctx,
		w.q,
		w.principal(w.admin),
		w.f.TaskA,
		ProjectPermissionRead,
	); !errors.Is(
		err,
		ErrTaskNotVisible,
	) {
		t.Errorf("admin task error = %v, want ErrTaskNotVisible", err)
	}
	for _, userID := range []uuid.UUID{w.member, w.revoked, w.disabled} {
		if _, err := RequireProjectVisible(
			ctx,
			w.q,
			w.principal(userID),
			restricted,
		); !errors.Is(
			err,
			ErrProjectNotVisible,
		) {
			t.Errorf("%v on a restricted Project: %v, want ErrProjectNotVisible", userID, err)
		}
	}
	if _, err := RequireProjectVisible(
		ctx,
		w.q,
		w.principal(w.f.UserA),
		uuid.New(),
	); !errors.Is(
		err,
		ErrProjectNotVisible,
	) {
		t.Errorf("missing Project error = %v, want ErrProjectNotVisible", err)
	}
	// Another Company's principal holds nothing here.
	other := UserPrincipal{UserID: w.f.UserB, CompanyID: w.f.CompanyB}
	if _, err := RequireProjectVisible(ctx, w.q, other, restricted); !errors.Is(err, ErrProjectNotVisible) {
		t.Errorf("cross-Company error = %v, want ErrProjectNotVisible", err)
	}

	member, err := RequireProjectPermission(
		ctx,
		w.q,
		w.principal(w.member),
		w.companyProject,
		ProjectPermissionTasksExecute,
	)
	if err != nil || member.Via != ProjectAccessViaCompany ||
		!slices.Equal(member.Permissions, CompanyProjectDefaultPermissions()) {
		t.Errorf("member on a Company Project = %+v, %v", member, err)
	}
	if _, err := RequireProjectPermission(
		ctx,
		w.q,
		w.principal(w.member),
		w.companyProject,
		ProjectPermissionFilesManage,
	); !errors.Is(
		err,
		ErrProjectPermissionRequired,
	) {
		t.Errorf("member files.manage error = %v, want ErrProjectPermissionRequired", err)
	}
	if _, err := RequireTaskPermission(
		ctx,
		w.q,
		w.principal(w.f.UserA),
		w.f.TaskA,
		ProjectPermissionTasksExecute,
	); err != nil {
		t.Errorf("owner task execute error = %v", err)
	}

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	locked, err := LockProjectAccess(ctx, w.q.WithTx(tx), w.principal(w.f.UserA), restricted)
	if err != nil || !locked.Can(ProjectPermissionGrantsManage) {
		t.Errorf("LockProjectAccess = %+v, %v", locked, err)
	}
	readers, companyWide, err := ProjectAudienceInTx(ctx, w.q.WithTx(tx), w.f.CompanyA, restricted, false)
	if err != nil || companyWide || !slices.Equal(readers, []uuid.UUID{w.f.UserA}) {
		t.Errorf("reader audience = %v, %t, %v", readers, companyWide, err)
	}
	audience, _, err := ProjectAudienceInTx(ctx, w.q.WithTx(tx), w.f.CompanyA, restricted, true)
	want := []uuid.UUID{w.f.UserA, w.admin}
	slices.SortFunc(want, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	if err != nil || !slices.Equal(audience, want) {
		t.Errorf("management audience = %v, want %v (%v)", audience, want, err)
	}
	if _, companyWide, err := ProjectAudienceInTx(
		ctx,
		w.q.WithTx(tx),
		w.f.CompanyA,
		w.companyProject,
		true,
	); err != nil ||
		!companyWide {
		t.Errorf("Company Project audience companyWide = %t, %v", companyWide, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	w.replace(t, restricted, w.member, ProjectPermissionRead)
	w.replace(t, w.companyProject, w.member, ProjectPermissionRead, ProjectPermissionFilesManage)
	removed, err := DeleteProjectGrantsForUserInTx(ctx, w.q, w.f.CompanyA, w.member)
	wantRemoved := []RemovedProjectGrant{
		{ProjectID: restricted, Permissions: []ProjectPermission{ProjectPermissionRead}},
		{
			ProjectID:   w.companyProject,
			Permissions: []ProjectPermission{ProjectPermissionFilesManage, ProjectPermissionRead},
		},
	}
	slices.SortFunc(
		wantRemoved,
		func(a, b RemovedProjectGrant) int { return bytes.Compare(a.ProjectID[:], b.ProjectID[:]) },
	)
	if err != nil || !slices.EqualFunc(removed, wantRemoved, func(a, b RemovedProjectGrant) bool {
		return a.ProjectID == b.ProjectID && slices.Equal(a.Permissions, b.Permissions)
	}) {
		t.Errorf("DeleteProjectGrantsForUserInTx = %v, want %v (%v)", removed, wantRemoved, err)
	}
	after, err := ResolveProjectAccess(ctx, w.q, w.principal(w.f.UserA), restricted)
	if err != nil || after.Revision != 2 {
		t.Errorf("revision after revocation = %d, want 2 (%v)", after.Revision, err)
	}
}
