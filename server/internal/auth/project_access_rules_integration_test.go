//go:build integration

package auth

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// project_grants_read_fkey: no permission is stored without the person's
// project.read row, read cannot be removed while other rows stay, and a
// person is removed by deleting all their rows at once.
func TestProjectGrantsStoreNoPermissionWithoutRead(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()
	project := w.f.ProjectA

	dbtest.AssertStatementRejected(t, ctx, w.pool, "23503",
		`INSERT INTO project_grants (company_id, project_id, user_id, permission)
		 VALUES ($1, $2, $3, 'project.archive')`, w.f.CompanyA, project, w.admin)
	dbtest.AssertStatementRejected(t, ctx, w.pool, "23503",
		`DELETE FROM project_grants WHERE company_id = $1 AND project_id = $2 AND user_id = $3
		   AND permission = 'project.read'`, w.f.CompanyA, project, w.f.UserA)

	w.replace(t, project, w.member, ProjectPermissionRead, ProjectPermissionArchive)
	dbtest.MustExec(
		t,
		ctx,
		w.pool,
		`DELETE FROM project_grants WHERE company_id = $1 AND project_id = $2 AND user_id = $3`,
		w.f.CompanyA,
		project,
		w.member,
	)
	if w.exists(t, `SELECT 1 FROM project_grants WHERE project_id = $1 AND user_id = $2`, project, w.member) {
		t.Error("removing a person left rows behind")
	}
	// The views and the function agree on read for every stored row.
	w.replace(t, project, w.member, ProjectPermissionRead, ProjectPermissionTasksWrite)
	if !w.held(t, project, w.member, ProjectPermissionRead) ||
		!w.exists(
			t,
			`SELECT 1 FROM project_access WHERE project_id = $1 AND user_id = $2 AND via = 'grant'`,
			project,
			w.member,
		) {
		t.Error("a stored grant does not read through both the function and the view")
	}
}

// Grant rows and memberships stamp time with the same clock, so a grant
// written in the same statement as the join passes the rejoin fence.
func TestProjectGrantInSameStatementAsJoinPassesFence(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()
	joiner := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, w.pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
		joiner, "joiner@project-access.example.com")
	dbtest.MustExec(t, ctx, w.pool, `
		WITH joined AS (
			INSERT INTO company_members (company_id, user_id, role, status, created_at)
			VALUES ($1, $2, 'member', 'active', clock_timestamp())
			RETURNING company_id, user_id
		)
		INSERT INTO project_grants (company_id, project_id, user_id, permission)
		SELECT joined.company_id, $3, joined.user_id, 'project.read' FROM joined`,
		w.f.CompanyA, joiner, w.f.ProjectA)
	if !w.exists(t, `SELECT 1 FROM project_access WHERE project_id = $1 AND user_id = $2`, w.f.ProjectA, joiner) {
		t.Error("a grant written with the join is hidden by the rejoin fence")
	}
	if !w.held(t, w.f.ProjectA, joiner, ProjectPermissionRead) {
		t.Error("project_permission_held fences a grant written with the join")
	}
}

// The id-based core answers for someone other than the caller, with the same
// result the principal form gives that person.
func TestResolveMemberProjectAccessReadsAnotherPerson(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()
	w.replace(t, w.f.ProjectA, w.member, ProjectPermissionRead, ProjectPermissionTasksExecute)

	byID, err := ResolveMemberProjectAccess(ctx, w.q, w.f.CompanyA, w.member, w.f.ProjectA)
	if err != nil {
		t.Fatal(err)
	}
	byPrincipal, err := ResolveProjectAccess(ctx, w.q, w.principal(w.member), w.f.ProjectA)
	if err != nil {
		t.Fatal(err)
	}
	want := []ProjectPermission{ProjectPermissionRead, ProjectPermissionTasksExecute}
	if !slices.Equal(byID.Permissions, want) || !slices.Equal(byPrincipal.Permissions, want) ||
		byID.Via != ProjectAccessViaGrant {
		t.Errorf("member access by id = %+v, by principal = %+v", byID, byPrincipal)
	}
	for _, test := range []struct {
		user       uuid.UUID
		permission ProjectPermission
		want       bool
	}{
		{w.member, ProjectPermissionTasksExecute, true},
		{w.member, ProjectPermissionFilesManage, false},
		{w.admin, ProjectPermissionGrantsManage, true},
		{w.admin, ProjectPermissionRead, false},
		{w.revoked, ProjectPermissionRead, false},
	} {
		held, err := HoldsProjectPermission(ctx, w.q, w.f.CompanyA, w.f.ProjectA, test.user, test.permission)
		if err != nil || held != test.want {
			t.Errorf(
				"HoldsProjectPermission(%v, %s) = %t, %v, want %t",
				test.user,
				test.permission,
				held,
				err,
				test.want,
			)
		}
	}
}

// A person's access is the union of their own grant and each of their
// Groups (000101). project_permission_held,
// project_access, output_access, project_access_paths and
// ResolveMemberProjectAccess must agree on every source: an own grant only,
// a Group grant only, both, a Group membership older than the person's
// latest rejoin (it counts for nothing), a disabled account, a person who
// left the Company while still in a Group, and a Group on a Company-visible
// Project (on top of the floor). The bulk read a Group change uses
// (ResolvePeopleProjectAccess) agrees with ResolveMemberProjectAccess on every
// pair, the management bridge included.
func TestGroupAccessAgreesAcrossTheRule(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()
	restricted := w.f.ProjectA
	person := func(name string, disabled bool) uuid.UUID {
		t.Helper()
		id := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, w.pool, `INSERT INTO users (id, email, display_name, disabled_at)
			VALUES ($1, $2, $2, CASE WHEN $3 THEN now() END)`, id, name+"@groups.example.com", disabled)
		dbtest.MustExec(
			t,
			ctx,
			w.pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
			w.f.CompanyA,
			id,
		)
		return id
	}
	ownOnly, groupOnly, both := person("own", false), person("group", false), person("both", false)
	rejoined, floorAndGroup := person("rejoined", false), person("floor", false)
	disabled := person("disabled", true)

	finance := dbtest.SeedGroup(t, ctx, w.pool, w.f.CompanyA, "Finance")
	dbtest.GrantGroupProject(
		t,
		ctx,
		w.pool,
		w.f.CompanyA,
		restricted,
		finance,
		string(ProjectPermissionTasksExecute),
	)
	// w.revoked left the Company: their membership row stays, and so does
	// their Group row until the Company sweep removes it.
	dbtest.AddGroupMembers(t, ctx, w.pool, w.f.CompanyA, finance, groupOnly, both, rejoined, disabled, w.revoked)
	design := dbtest.SeedGroup(t, ctx, w.pool, w.f.CompanyA, "Design")
	dbtest.GrantGroupProject(
		t,
		ctx,
		w.pool,
		w.f.CompanyA,
		w.companyProject,
		design,
		string(ProjectPermissionFilesManage),
	)
	dbtest.AddGroupMembers(t, ctx, w.pool, w.f.CompanyA, design, floorAndGroup)
	w.replace(t, restricted, ownOnly, ProjectPermissionRead, ProjectPermissionTasksWrite)
	w.replace(t, restricted, both, ProjectPermissionRead, ProjectPermissionApprovalsResolve)
	// A rejoin after the membership was written fences it out.
	dbtest.MustExec(t, ctx, w.pool, `UPDATE company_members SET reactivated_at = clock_timestamp()
		WHERE company_id = $1 AND user_id = $2`, w.f.CompanyA, rejoined)

	// A Company Artifact from the restricted Project's Task chat reaches its
	// readers only.
	var output uuid.UUID
	if err := w.pool.QueryRow(ctx, `INSERT INTO outputs
		(company_id, run_id, title, summary, presentation_fingerprint, owner_user_id, visibility, conversation_id, task_id, project_id, task_execution_generation, actor_agent_id)
 SELECT $1, $2, 'Payroll', 'Summary', md5(gen_random_uuid()::text), $3, 'company', origin.conversation_id, origin.task_id, conversation.project_id, origin.task_execution_generation, origin.agent_id
 FROM runs origin JOIN conversations conversation ON conversation.id=origin.conversation_id WHERE origin.id=$2 RETURNING id`,
		w.f.CompanyA, w.f.RunA, w.f.UserA).Scan(&output); err != nil {
		t.Fatal(err)
	}

	type path struct {
		via        string
		group      uuid.UUID
		permission ProjectPermission
	}
	pathsOf := func(projectID, userID uuid.UUID) []path {
		t.Helper()
		rows, err := w.pool.Query(
			ctx,
			`SELECT via, COALESCE(group_id, '00000000-0000-0000-0000-000000000000'), permission
			FROM project_access_paths WHERE company_id = $1 AND project_id = $2 AND user_id = $3`,
			w.f.CompanyA,
			projectID,
			userID,
		)
		if err != nil {
			t.Fatal(err)
		}
		found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (path, error) {
			var p path
			err := row.Scan(&p.via, &p.group, &p.permission)
			return p, err
		})
		if err != nil {
			t.Fatal(err)
		}
		slices.SortFunc(found, func(a, b path) int {
			return strings.Compare(
				a.via+a.group.String()+string(a.permission),
				b.via+b.group.String()+string(b.permission),
			)
		})
		return found
	}
	paths := func(via string, group uuid.UUID, permissions ...ProjectPermission) []path {
		listed := make([]path, len(permissions))
		for i, permission := range permissions {
			listed[i] = path{via: via, group: group, permission: permission}
		}
		return listed
	}

	floor := CompanyProjectDefaultPermissions()
	for _, test := range []struct {
		name      string
		project   uuid.UUID
		user      uuid.UUID
		want      []ProjectPermission
		wantVia   ProjectAccessVia
		wantPaths []path
	}{
		{
			name: "own grant only", project: restricted, user: ownOnly,
			want:      []ProjectPermission{ProjectPermissionRead, ProjectPermissionTasksWrite},
			wantVia:   ProjectAccessViaGrant,
			wantPaths: paths("grant", uuid.Nil, ProjectPermissionRead, ProjectPermissionTasksWrite),
		},
		{
			name: "Group grant only", project: restricted, user: groupOnly,
			want:      []ProjectPermission{ProjectPermissionRead, ProjectPermissionTasksExecute},
			wantVia:   ProjectAccessViaGroup,
			wantPaths: paths("group", finance, ProjectPermissionRead, ProjectPermissionTasksExecute),
		},
		{
			name: "own grant and a Group", project: restricted, user: both,
			want: []ProjectPermission{
				ProjectPermissionApprovalsResolve, ProjectPermissionRead, ProjectPermissionTasksExecute,
			},
			wantVia: ProjectAccessViaGrant,
			wantPaths: append(paths("grant", uuid.Nil, ProjectPermissionApprovalsResolve, ProjectPermissionRead),
				paths("group", finance, ProjectPermissionRead, ProjectPermissionTasksExecute)...),
		},
		{
			name: "a membership older than the latest rejoin", project: restricted, user: rejoined,
			want: []ProjectPermission{}, wantPaths: []path{},
		},
		{
			name: "a disabled account", project: restricted, user: disabled,
			want: []ProjectPermission{}, wantPaths: []path{},
		},
		{
			name: "a person who left the Company", project: restricted, user: w.revoked,
			want: []ProjectPermission{}, wantPaths: []path{},
		},
		{
			name: "a Group on a Company-visible Project", project: w.companyProject, user: floorAndGroup,
			want: func() []ProjectPermission {
				union := append(slices.Clone(floor), ProjectPermissionFilesManage)
				slices.Sort(union)
				return slices.Compact(union)
			}(),
			wantVia:   ProjectAccessViaGroup,
			wantPaths: paths("group", design, ProjectPermissionFilesManage, ProjectPermissionRead),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			access, err := ResolveMemberProjectAccess(ctx, w.q, w.f.CompanyA, test.user, test.project)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(access.Permissions, test.want) || access.Via != test.wantVia {
				t.Errorf("ResolveMemberProjectAccess = %v via %q, want %v via %q",
					access.Permissions, access.Via, test.want, test.wantVia)
			}
			for _, permission := range ProjectPermissions() {
				if got, want := w.held(
					t,
					test.project,
					test.user,
					permission,
				), slices.Contains(
					test.want,
					permission,
				); got != want {
					t.Errorf(
						"project_permission_held(%s) = %t, want %t as ResolveMemberProjectAccess says",
						permission,
						got,
						want,
					)
				}
			}
			var via string
			if err := w.pool.QueryRow(ctx, `SELECT COALESCE((SELECT via FROM project_access
				WHERE company_id = $1 AND project_id = $2 AND user_id = $3), '')`,
				w.f.CompanyA, test.project, test.user).Scan(&via); err != nil {
				t.Fatal(err)
			}
			if ProjectAccessVia(via) != test.wantVia {
				t.Errorf("project_access via = %q, want %q", via, test.wantVia)
			}
			var rows int
			if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM project_access
				WHERE company_id = $1 AND project_id = $2 AND user_id = $3`,
				w.f.CompanyA, test.project, test.user).Scan(&rows); err != nil || rows > 1 {
				t.Errorf(
					"project_access rows = %d, %v; want at most one per person and Project",
					rows,
					err,
				)
			}
			if got := pathsOf(test.project, test.user); !slices.Equal(got, test.wantPaths) {
				t.Errorf("project_access_paths = %v, want %v", got, test.wantPaths)
			}
			if test.project == restricted {
				sees := w.exists(
					t,
					`SELECT 1 FROM output_access WHERE output_id = $1 AND user_id = $2`,
					output,
					test.user,
				)
				if sees != (test.wantVia != "") {
					t.Errorf(
						"output_access = %t, want %t (reads the source Project)",
						sees,
						test.wantVia != "",
					)
				}
				// The delegate rule follows through project_permission_held:
				// a Run this person authorized in the Project's Task chat
				// reads it exactly when they do.
				var runID uuid.UUID
				run := w.pool.QueryRow(ctx, `INSERT INTO runs
					(company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id,
					 status, run_type)
					VALUES ($1, $2, $3, $4, $5, 'completed', 'task') RETURNING id`,
					w.f.CompanyA, w.f.AgentA, w.f.ConversationA, w.f.RuntimeA, test.user)
				if err := run.Scan(&runID); err != nil {
					t.Fatal(err)
				}
				params := generated.ListRunReadableProjectsParams{
					ProjectIds:                []uuid.UUID{restricted},
					CompanyID:                 w.f.CompanyA,
					RunID:                     runID,
					Permission:                string(ProjectPermissionRead),
					ReadPermission:            string(ProjectPermissionRead),
					CompanyDefaultPermissions: ProjectPermissionHeldSets().CompanyDefaultPermissions,
				}
				readable, err := w.q.ListRunReadableProjects(ctx, params)
				if err != nil || (len(readable) == 1) != (test.wantVia != "") {
					t.Errorf("the delegate rule lets the person's Run read = %v, %v; want %t",
						readable, err, test.wantVia != "")
				}
			}
		})
	}

	// The bulk read a Group change uses (ResolvePeopleProjectAccess) answers
	// exactly what ResolveMemberProjectAccess answers, for every pair of these
	// people and Projects: each case above, an admin through the management
	// bridge alone, a member with no source, and a Project outside the
	// Company, which is left out.
	t.Run("the bulk read agrees with the single read", func(t *testing.T) {
		people := []uuid.UUID{
			ownOnly, groupOnly, both, rejoined, floorAndGroup, disabled, w.revoked, w.disabled,
			w.admin, w.member, w.f.UserA,
		}
		projects := []uuid.UUID{restricted, w.companyProject, w.f.ProjectB}
		bulk, err := ResolvePeopleProjectAccess(ctx, w.q, w.f.CompanyA, projects, people)
		if err != nil {
			t.Fatal(err)
		}
		if _, found := bulk[w.f.ProjectB]; found || len(bulk) != 2 {
			t.Fatalf("bulk read answers %d Projects, want the Company's two", len(bulk))
		}
		for _, project := range projects[:2] {
			if answered := len(bulk[project]); answered != len(people) {
				t.Errorf("bulk read answers %d people on %s, want %d", answered, project, len(people))
			}
			for _, person := range people {
				single, err := ResolveMemberProjectAccess(ctx, w.q, w.f.CompanyA, person, project)
				if err != nil {
					t.Fatal(err)
				}
				if got := bulk[project][person]; !reflect.DeepEqual(got, single) {
					t.Errorf("bulk read of %s on %s = %+v,\nsingle read = %+v",
						person, project, got, single)
				}
			}
		}
		if management := bulk[restricted][w.admin]; !management.Management || management.Via != "" {
			t.Errorf("the admin on the restricted Project = %+v, want the bridge alone", management)
		}
	})
}

// attachment_access and conversation_readers are UNION ALL views; a list query
// reaches them through a correlated LATERAL lookup that names the row and the
// person, and that lookup uses each arm's indexes instead of building the
// person's whole readable set (see the note in 000086). A project_access
// lookup finds a person's Group path the same way (000101): one probe of
// company_group_members_user_idx, then the project_group_grants key, per row.
func TestProjectViewCorrelatedLookupsStayIndexed(t *testing.T) {
	t.Parallel()
	w := newProjectAccessWorld(t)
	ctx := t.Context()
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
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversation_items
				(company_id, conversation_id, seq, type, author_kind, author_id,
				 content, body, request_key, send_fingerprint)
			SELECT $1, $2, n, 'user_message', 'user', $3, 'm', '{"markdown":"m"}'::jsonb, gen_random_uuid(),
				decode(repeat('11', 32), 'hex')
			FROM generate_series(1, 5000) AS n`, w.f.CompanyA, channel, w.f.UserA)
	})
	dbtest.MustExec(t, ctx, w.pool, `INSERT INTO attachments
		(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
		SELECT $1, $2, gen_random_uuid(), 'bulk-' || n, 'text/plain', 1, 'ready' FROM generate_series(1, 5000) AS n`,
		w.f.CompanyA, w.f.UserA)
	dbtest.MustExec(t, ctx, w.pool, `
		INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		SELECT $1, item.id, file.id, 0
		FROM (SELECT id, row_number() OVER (ORDER BY id) AS n FROM conversation_items WHERE conversation_id = $2) AS item
		JOIN (SELECT id, row_number() OVER (ORDER BY id) AS n FROM attachments
		      WHERE company_id = $1 AND filename LIKE 'bulk-%') AS file ON file.n = item.n`,
		w.f.CompanyA, channel)
	// Forty more Projects and forty Groups of forty colleagues each holding
	// grants on every Project, so a scan of company_group_members or
	// project_group_grants would show. The member reads one of the Projects
	// through a Group of their own.
	dbtest.MustExec(t, ctx, w.pool, `
		INSERT INTO projects (company_id, key, title)
		SELECT $1, 'bulk_' || n, 'Bulk ' || n FROM generate_series(1, 40) AS n`, w.f.CompanyA)
	dbtest.SeedGroupColleagues(t, ctx, w.pool, w.f.CompanyA, "lookup", 40, string(ProjectPermissionTasksWrite))
	// The same colleagues also read every Project through grants of their
	// own, so project_grants is large enough that a scan of it, rather than a
	// probe, would show.
	dbtest.MustExec(t, ctx, w.pool, `
		INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
		SELECT $1, project.id, member.user_id, 'project.read', $2
		FROM company_members AS member
		JOIN users AS account ON account.id = member.user_id AND account.email LIKE 'lookup-%'
		CROSS JOIN projects AS project
		WHERE member.company_id = $1 AND project.company_id = $1`,
		w.f.CompanyA, w.f.UserA)
	var bulk []uuid.UUID
	rows, err := w.pool.Query(
		ctx,
		`SELECT id FROM projects WHERE company_id = $1 AND key IN ('bulk_7', 'bulk_8') ORDER BY key`,
		w.f.CompanyA,
	)
	if err != nil {
		t.Fatal(err)
	}
	if bulk, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil || len(bulk) != 2 {
		t.Fatalf("bulk Projects = %v, %v", bulk, err)
	}
	finance := dbtest.SeedGroup(t, ctx, w.pool, w.f.CompanyA, "Finance")
	dbtest.AddGroupMembers(t, ctx, w.pool, w.f.CompanyA, finance, w.member)
	dbtest.GrantGroupProject(t, ctx, w.pool, w.f.CompanyA, bulk[0], finance, string(ProjectPermissionTasksWrite))
	dbtest.MustExec(t, ctx, w.pool, `ANALYZE`)

	var messages []uuid.UUID
	rows, err = w.pool.Query(
		ctx,
		`SELECT id FROM conversation_items WHERE conversation_id = $1 ORDER BY seq LIMIT 2`,
		channel,
	)
	if err != nil {
		t.Fatal(err)
	}
	messages, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		query string
		ids   []uuid.UUID
		want  int
		// indexes must each appear in the plan as an index the lookup probes,
		// named in full or by its table's prefix when either of the table's
		// indexes serves (the project_group_grants key and its Group index
		// both lead with the Company, the Project and the Group).
		indexes []string
	}{
		{
			name: "attachment_access",
			query: `SELECT link.attachment_id FROM message_attachments AS link
				CROSS JOIN LATERAL (
					SELECT 1 FROM attachment_access AS access
					WHERE access.company_id = link.company_id
					  AND access.attachment_id = link.attachment_id
					  AND access.user_id = $3
					LIMIT 1
				) AS readable
				WHERE link.company_id = $1 AND link.message_id = ANY($2::uuid[])`,
			ids:  messages,
			want: 2,
		},
		{
			name: "conversation_readers",
			query: `SELECT item.id FROM conversation_items AS item
				CROSS JOIN LATERAL (
					SELECT 1 FROM conversation_readers AS reader
					WHERE reader.company_id = item.company_id
					  AND reader.conversation_id = item.conversation_id
					  AND reader.user_id = $3
					LIMIT 1
				) AS readable
				WHERE item.company_id = $1 AND item.id = ANY($2::uuid[])`,
			ids:  messages,
			want: 2,
		},
		{
			name: "project_access",
			query: `SELECT project.id FROM projects AS project
				CROSS JOIN LATERAL (
					SELECT 1 FROM project_access AS access
					WHERE access.company_id = project.company_id
					  AND access.project_id = project.id
					  AND access.user_id = $3
					LIMIT 1
				) AS readable
				WHERE project.company_id = $1 AND project.id = ANY($2::uuid[])`,
			ids:     bulk,
			want:    1,
			indexes: []string{"company_group_members_user_idx ", "project_group_grants_"},
		},
	} {
		var found int
		if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM (`+test.query+`) AS listed`,
			w.f.CompanyA, test.ids, w.member).Scan(&found); err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if found != test.want {
			t.Errorf("%s: listed %d rows, want %d", test.name, found, test.want)
		}
		plan, err := w.pool.Query(ctx, `EXPLAIN (COSTS OFF) `+test.query, w.f.CompanyA, test.ids, w.member)
		if err != nil {
			t.Fatalf("%s explain: %v", test.name, err)
		}
		lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(lines, "\n")
		for _, table := range []string{
			"message_attachments", "attachments", "conversation_items",
			"project_grants", "company_group_members", "project_group_grants",
		} {
			if strings.Contains(text, "Seq Scan on "+table+" ") {
				t.Errorf("%s: correlated lookup scans all of %s:\n%s", test.name, table, text)
			}
		}
		for _, index := range test.indexes {
			// An index probe prints as "Index Scan using <index>" or, when the
			// planner batches it, "Bitmap Index Scan on <index>".
			if !strings.Contains(text, " using "+index) &&
				!strings.Contains(text, "Bitmap Index Scan on "+index) {
				t.Errorf("%s: correlated lookup does not probe %s:\n%s", test.name, index, text)
			}
		}
	}
}
