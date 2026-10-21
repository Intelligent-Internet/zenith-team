//go:build integration

package task

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// seedProjectOutsider adds an active member of CompanyA with the given
// Company role and no Project grant: under ADR 0017 they read no restricted
// Project until someone grants them one.
func seedProjectOutsider(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	email string,
	role auth.CompanyRole,
) (uuid.UUID, auth.UserPrincipal) {
	t.Helper()
	id := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`, id, email)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
		fixture.CompanyA, id, string(role))
	return id, auth.UserPrincipal{UserID: id, CompanyID: fixture.CompanyA, Role: role}
}

func countTasksWithKey(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE key = $1`, key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A person who cannot read a Task's Project cannot find the Task anywhere: a
// read, a list, a list filtered by that Project, the dependencies, the
// dependency graph and the workspace all answer as if it did not exist. A
// projects.manage holder is no exception: management is not content.
func TestTasksAreInvisibleOutsideTheirProject(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	_, outsider := seedProjectOutsider(t, ctx, pool, fixture, "outsider@example.com", auth.CompanyRoleMember)
	_, admin := seedProjectOutsider(t, ctx, pool, fixture, "admin@example.com", auth.CompanyRoleAdmin)

	// A dependency inside the Project, so the dependency reads have a row
	// to leak.
	dependent, err := service.Create(ctx, owner, CreateInput{
		ProjectID: fixture.ProjectA, Key: "dependent", Title: "Dependent",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
	})
	if err != nil {
		t.Fatalf("Create(dependent) error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_dependencies (project_id, company_id, dependent_task_id, prerequisite_task_id)
		VALUES ($1, $2, $3, $4)`,
		fixture.ProjectA, fixture.CompanyA, dependent.Task.ID, fixture.TaskA)

	for name, principal := range map[string]auth.UserPrincipal{"member": outsider, "projects.manage holder": admin} {
		if _, err := service.Get(ctx, principal, fixture.TaskA, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: Get() error = %v, want ErrNotFound", name, err)
		}
		page, err := service.List(ctx, principal, ListOptions{}, nil)
		if err != nil || len(page.Tasks) != 0 {
			t.Errorf("%s: List() = %d Tasks, %v; want none", name, len(page.Tasks), err)
		}
		projectID := fixture.ProjectA
		if _, err := service.List(
			ctx,
			principal,
			ListOptions{ProjectID: &projectID},
			nil,
		); !errors.Is(
			err,
			auth.ErrProjectNotVisible,
		) {
			t.Errorf("%s: List(projectId) error = %v, want ErrProjectNotVisible", name, err)
		}
		if _, err := service.ListDependencies(
			ctx,
			principal,
			fixture.TaskA,
			nil,
		); !errors.Is(
			err,
			ErrNotFound,
		) {
			t.Errorf("%s: ListDependencies() error = %v, want ErrNotFound", name, err)
		}
		graph, err := service.ListDependencyGraph(ctx, principal)
		if err != nil || len(graph.Dependencies) != 0 || len(graph.Tasks) != 0 {
			t.Errorf("%s: ListDependencyGraph() = %+v, %v; want empty", name, graph, err)
		}
		if _, err := service.Workspace(
			ctx,
			principal,
			fixture.TaskA,
			WorkspaceOptions{MessageLimit: 10},
			nil,
		); !errors.Is(
			err,
			ErrNotFound,
		) {
			t.Errorf("%s: Workspace() error = %v, want not found", name, err)
		}
	}

	// A list filtered by a Project that does not exist answers the same.
	missing := dbtest.NewUUID(t)
	if _, err := service.List(
		ctx,
		owner,
		ListOptions{ProjectID: &missing},
		nil,
	); !errors.Is(
		err,
		auth.ErrProjectNotVisible,
	) {
		t.Errorf("List(missing projectId) error = %v, want ErrProjectNotVisible", err)
	}

	// The owner reads everything, with their permissions on the Task.
	value, err := service.Get(ctx, owner, fixture.TaskA, nil)
	if err != nil || value.Access == nil ||
		!slices.Contains(value.Access.Permissions, auth.ProjectPermissionTasksExecute) {
		t.Fatalf("owner Get() = access %+v, %v; want every permission", value.Access, err)
	}
	dependencies, err := service.ListDependencies(ctx, owner, fixture.TaskA, nil)
	if err != nil || len(dependencies.Blocks) != 1 {
		t.Fatalf("owner ListDependencies() = %+v, %v; want the dependent", dependencies, err)
	}
	graph, err := service.ListDependencyGraph(ctx, owner)
	if err != nil || len(graph.Dependencies) != 1 {
		t.Fatalf("owner ListDependencyGraph() = %+v, %v; want one edge", graph, err)
	}

	// Made Company-visible, the Project opens to every member with the
	// default set, and to the admin with the management names on top.
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET visibility = 'company' WHERE id = $1`, fixture.ProjectA)
	value, err = service.Get(ctx, outsider, fixture.TaskA, nil)
	if err != nil || value.Access == nil ||
		!slices.Equal(value.Access.Permissions, sortedPermissions(auth.CompanyProjectDefaultPermissions())) {
		t.Fatalf("member Get() on a Company Project = %+v, %v; want the default set", value.Access, err)
	}
	value, err = service.Get(ctx, admin, fixture.TaskA, nil)
	want := sortedPermissions(
		append(auth.CompanyProjectDefaultPermissions(), auth.ManagementProjectPermissions()...),
	)
	if err != nil || value.Access == nil || !slices.Equal(value.Access.Permissions, want) {
		t.Fatalf("admin Get() on a Company Project = %+v, %v; want %v", value.Access, err, want)
	}
}

// Creating and changing a Task needs project.tasks.write on its Project. A
// reader without it gets the permission refusal and nothing is written. A
// create by someone who cannot read the Project, a manager through the
// Company role included, is told the Project is not found, exactly as for a
// Project that does not exist; a change of a Task there is told the Task is
// not found.
func TestTaskWritesNeedProjectTasksWrite(t *testing.T) {
	ctx, pool, fixture, service, _ := startTaskHarness(t)
	readerID, reader := seedProjectOutsider(t, ctx, pool, fixture, "reader@example.com", auth.CompanyRoleMember)
	writerID, writer := seedProjectOutsider(t, ctx, pool, fixture, "writer@example.com", auth.CompanyRoleMember)
	_, outsider := seedProjectOutsider(t, ctx, pool, fixture, "outsider@example.com", auth.CompanyRoleMember)
	_, manager := seedProjectOutsider(t, ctx, pool, fixture, "manager@example.com", auth.CompanyRoleAdmin)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, readerID, "project.read")
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, writerID, "project.tasks.write")

	create := func(principal auth.UserPrincipal, key string) (MutationResult, error) {
		t.Helper()
		return service.Create(ctx, principal, CreateInput{
			ProjectID: fixture.ProjectA, Key: key, Title: key,
			MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
		})
	}
	if _, err := create(reader, "by_reader"); !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("reader Create() error = %v, want ErrProjectPermissionRequired", err)
	}
	for name, principal := range map[string]auth.UserPrincipal{"by_outsider": outsider, "by_manager": manager} {
		_, err := create(principal, name)
		if !errors.Is(err, auth.ErrProjectNotVisible) || errors.Is(err, ErrNotFound) {
			t.Fatalf("Create(%s) error = %v, want ErrProjectNotVisible", name, err)
		}
	}
	if _, err := service.Create(ctx, writer, CreateInput{
		ProjectID: uuid.New(), Key: "in_no_project", Title: "in_no_project",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
	}); !errors.Is(err, auth.ErrProjectNotVisible) {
		t.Fatalf("Create(in a Project that does not exist) error = %v, want ErrProjectNotVisible", err)
	}
	for _, key := range []string{"by_reader", "by_outsider", "by_manager", "in_no_project"} {
		if count := countTasksWithKey(t, ctx, pool, key); count != 0 {
			t.Fatalf("a refused create wrote %d Task(s) with key %s", count, key)
		}
	}
	created, err := create(writer, "by_writer")
	if err != nil {
		t.Fatalf("writer Create() error = %v", err)
	}
	wantWriter := []auth.ProjectPermission{auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite}
	if created.Task.Access == nil || !slices.Equal(created.Task.Access.Permissions, wantWriter) {
		t.Fatalf("writer's receipt access = %+v, want %v", created.Task.Access, wantWriter)
	}

	current, err := service.Get(ctx, reader, created.Task.ID, nil)
	if err != nil {
		t.Fatalf("reader Get() error = %v", err)
	}
	title := "Renamed"
	update := UpdateInput{ExpectedRevision: current.Revision, Title: &title}
	if _, err := service.Update(
		ctx,
		reader,
		created.Task.ID,
		update,
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Fatalf("reader Update() error = %v, want ErrProjectPermissionRequired", err)
	}
	if _, err := service.Update(ctx, outsider, created.Task.ID, update); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider Update() error = %v, want ErrNotFound", err)
	}
	updated, err := service.Update(ctx, writer, created.Task.ID, update)
	if err != nil || updated.Task.Title != title {
		t.Fatalf("writer Update() = %q, %v", updated.Task.Title, err)
	}
}

// The assignee is responsible for the Task and must be able to read its
// Project, on create and on a handoff. One who later loses access keeps the
// Task, and every write receipt lists it as outside the Project.
func TestTaskAssigneeMustReadTheProject(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	outsiderID, _ := seedProjectOutsider(t, ctx, pool, fixture, "outsider@example.com", auth.CompanyRoleMember)
	member := seedTaskMember(t, ctx, pool, fixture, "member@example.com")

	if _, err := service.Create(ctx, owner, CreateInput{
		ProjectID: fixture.ProjectA, Key: "for_outsider", Title: "For outsider",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
		Assignee: ActorRef{Kind: ActorUser, ID: outsiderID},
	}); !errors.Is(err, ErrAssigneeOutsideProject) {
		t.Fatalf("Create(assignee outside) error = %v, want ErrAssigneeOutsideProject", err)
	}
	if count := countTasksWithKey(t, ctx, pool, "for_outsider"); count != 0 {
		t.Fatalf("a refused create wrote %d Task(s)", count)
	}

	current, err := service.Get(ctx, owner, fixture.TaskA, nil)
	if err != nil {
		t.Fatal(err)
	}
	outsiderRef := ActorRef{Kind: ActorUser, ID: outsiderID}
	if _, err := service.Update(ctx, owner, fixture.TaskA, UpdateInput{
		ExpectedRevision: current.Revision, Assignee: &outsiderRef, ConfirmHandoff: true,
	}); !errors.Is(err, ErrAssigneeOutsideProject) {
		t.Fatalf("handoff to an outsider error = %v, want ErrAssigneeOutsideProject", err)
	}

	created, err := service.Create(ctx, owner, CreateInput{
		ProjectID: fixture.ProjectA, Key: "for_member", Title: "For member",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
		Assignee: ActorRef{Kind: ActorUser, ID: member},
	})
	if err != nil || len(created.AssigneeOutsideProject) != 0 {
		t.Fatalf("Create(for member) = outside %v, %v; want none", created.AssigneeOutsideProject, err)
	}
	// The member loses the Project. The Task stays theirs, and the next
	// write says its assignee is outside the Project.
	dbtest.MustExec(t, ctx, pool,
		`DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`, fixture.ProjectA, member)
	title := "Still theirs"
	updated, err := service.Update(ctx, owner, created.Task.ID, UpdateInput{
		ExpectedRevision: created.Task.Revision, Title: &title,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Task.Assignee.ID != member ||
		!slices.Equal(updated.AssigneeOutsideProject, []uuid.UUID{created.Task.ID}) {
		t.Fatalf("Update() = assignee %s outside %v; want %s kept and listed", updated.Task.Assignee.ID,
			updated.AssigneeOutsideProject, member)
	}
	// Every read says so on the Task itself: the read of one Task, the list
	// and the frame the write staged. Access back clears it, and a
	// Company-visible Project never shows it for an active member.
	outside := func(name string) bool {
		t.Helper()
		one, err := service.Get(ctx, owner, created.Task.ID, nil)
		if err != nil {
			t.Fatalf("%s: Get() error = %v", name, err)
		}
		page, err := service.List(ctx, owner, ListOptions{ProjectID: &fixture.ProjectA}, nil)
		if err != nil {
			t.Fatalf("%s: List() error = %v", name, err)
		}
		index := slices.IndexFunc(page.Tasks, func(task Response) bool { return task.ID == created.Task.ID })
		if index < 0 || page.Tasks[index].AssigneeOutsideProject != one.AssigneeOutsideProject {
			t.Fatalf("%s: listed %+v, read %t; want the same flag", name, index, one.AssigneeOutsideProject)
		}
		return one.AssigneeOutsideProject
	}
	if !updated.Task.AssigneeOutsideProject || !outside("assignee lost the Project") {
		t.Fatalf("a Task whose assignee lost the restricted Project must say assigneeOutsideProject")
	}
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, member, "project.read")
	if outside("assignee granted again") {
		t.Fatal("assigneeOutsideProject after the grant came back")
	}
	dbtest.MustExec(t, ctx, pool,
		`DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`, fixture.ProjectA, member)
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET visibility = 'company' WHERE id = $1`, fixture.ProjectA)
	if outside("Company-visible Project") {
		t.Fatal("assigneeOutsideProject on a Company-visible Project")
	}
}

// A Task Run starts only when its assignee holds project.tasks.execute. The
// refusal names the Agent, never the Project, and nothing is enqueued or
// written.
func TestTaskRunNeedsProjectTasksExecute(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	memberID, member := seedProjectOutsider(t, ctx, pool, fixture, "member@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID, "project.tasks.write")
	runtime := seedTaskRuntime(t, ctx, pool, fixture, "daemon-member", memberID)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, memberID, runtime, "codex")
	agent := ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	countRuns := func(taskID uuid.UUID) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id = $1`, taskID).
			Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	assertRefusal := func(what string, err error) {
		t.Helper()
		var refusal *execution.ExecutePermissionError
		if !errors.As(err, &refusal) || refusal.AgentName != "Agent A" {
			t.Fatalf("%s error = %v (%+v), want the execute refusal naming Agent A", what, err, refusal)
		}
	}

	// A self-assigned Agent Task would start at once: refused whole.
	_, err := service.Create(ctx, member, CreateInput{
		ProjectID: fixture.ProjectA, Key: "self_run", Title: "Self run",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo, ExecutedBy: &agent,
	})
	assertRefusal("Create(self-assigned Agent Task)", err)
	if count := countTasksWithKey(t, ctx, pool, "self_run"); count != 0 {
		t.Fatalf("a refused create wrote %d Task(s)", count)
	}

	created, err := service.Create(ctx, owner, CreateInput{
		ProjectID: fixture.ProjectA, Key: "theirs", Title: "Theirs",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
		Assignee: ActorRef{Kind: ActorUser, ID: memberID}, ExecutedBy: &agent,
	})
	if err != nil || created.RunID != nil {
		t.Fatalf("Create(assigned to member) = %+v, %v; want no Run", created.RunID, err)
	}
	_, err = service.StartExecution(
		ctx,
		member,
		created.Task.ID,
		StartExecutionInput{ExpectedRevision: created.Task.Revision},
	)
	assertRefusal("StartExecution()", err)
	if count := countRuns(created.Task.ID); count != 0 {
		t.Fatalf("a refused start enqueued %d Run(s)", count)
	}
	after, err := service.Get(ctx, member, created.Task.ID, nil)
	if err != nil || after.Status != StatusTodo || after.Revision != created.Task.Revision {
		t.Fatalf("a refused start changed the Task: %q revision %d, %v", after.Status, after.Revision, err)
	}

	// A handoff onto the caller with an Agent starts their Run at once: an
	// admin who may assign Tasks and write in the Project, but not run
	// Tasks there, is refused whole and the Task stays where it was.
	adminID, admin := seedProjectOutsider(t, ctx, pool, fixture, "admin@example.com", auth.CompanyRoleAdmin)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, adminID, "project.tasks.write")
	self := ActorRef{Kind: ActorUser, ID: adminID}
	_, err = service.Update(ctx, admin, created.Task.ID, UpdateInput{
		ExpectedRevision: created.Task.Revision, Assignee: &self, ConfirmHandoff: true,
	})
	assertRefusal("Update(handoff onto the caller)", err)
	after, err = service.Get(ctx, owner, created.Task.ID, nil)
	if err != nil || after.Assignee.ID != memberID || after.Revision != created.Task.Revision ||
		countRuns(created.Task.ID) != 0 {
		t.Fatalf("a refused handoff changed the Task: assignee %s revision %d runs %d, %v",
			after.Assignee.ID, after.Revision, countRuns(created.Task.ID), err)
	}

	dbtest.GrantProject(
		t,
		ctx,
		pool,
		fixture.CompanyA,
		fixture.ProjectA,
		memberID,
		"project.tasks.write",
		"project.tasks.execute",
	)
	started, err := service.StartExecution(
		ctx,
		member,
		created.Task.ID,
		StartExecutionInput{ExpectedRevision: created.Task.Revision},
	)
	if err != nil || started.RunID == nil {
		t.Fatalf("StartExecution() with execute = %+v, %v; want a Run", started.RunID, err)
	}
}

// A Task whose assignee may not run tasks in its Project says so before
// anyone presses Start: execution.assigneeReady is false with
// assignee_cannot_run on every read (the member's, a reader's, the list and
// the frames' system read), and it clears when the permission comes back,
// by a grant or by the Company-visible default set. A grant older than a
// Company rejoin does not count, and project_permission_held agrees in
// every state. It wins over a missing setup, since setting the Agent up
// would not let the Run start.
func TestTaskReadinessSaysWhenTheAssigneeCannotRunIt(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	memberID, member := seedProjectOutsider(t, ctx, pool, fixture, "member@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID, "project.tasks.write")
	runtime := seedTaskRuntime(t, ctx, pool, fixture, "daemon-member", memberID)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, memberID, runtime, "codex")
	agent := ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	created, err := service.Create(ctx, owner, CreateInput{
		ProjectID: fixture.ProjectA, Key: "theirs", Title: "Theirs",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
		Assignee: ActorRef{Kind: ActorUser, ID: memberID}, ExecutedBy: &agent,
	})
	if err != nil || created.RunID != nil {
		t.Fatalf("Create(assigned to member) = %+v, %v; want no Run", created.RunID, err)
	}
	queries := generated.New(pool)
	readiness := func(name, want string) {
		t.Helper()
		reads := map[string]Response{}
		for who, principal := range map[string]auth.UserPrincipal{"assignee": member, "owner": owner} {
			one, err := service.Get(ctx, principal, created.Task.ID, nil)
			if err != nil {
				t.Fatalf("%s: Get() as %s error = %v", name, who, err)
			}
			reads["read by "+who] = one
			page, err := service.List(ctx, principal, ListOptions{ProjectID: &fixture.ProjectA}, nil)
			if err != nil {
				t.Fatalf("%s: List() as %s error = %v", name, who, err)
			}
			index := slices.IndexFunc(
				page.Tasks,
				func(task Response) bool { return task.ID == created.Task.ID },
			)
			if index < 0 {
				t.Fatalf("%s: %s's list misses the Task", name, who)
			}
			reads["listed for "+who] = page.Tasks[index]
		}
		system, err := queries.GetTaskProjectionForSystem(
			ctx,
			systemProjectionParams(fixture.CompanyA, created.Task.ID),
		)
		if err != nil {
			t.Fatalf("%s: system projection error = %v", name, err)
		}
		reads["system read"] = responseFromSystemProjectionRow(system)
		// The arm is a hand copy of project_permission_held for one person;
		// the function must agree with it in every state.
		held, err := auth.HoldsProjectPermission(ctx, queries, fixture.CompanyA, fixture.ProjectA, memberID,
			auth.ProjectPermissionTasksExecute)
		if err != nil {
			t.Fatalf("%s: HoldsProjectPermission() error = %v", name, err)
		}
		if wantHeld := want != "assignee_cannot_run" && want != "assignee_inactive"; held != wantHeld {
			t.Fatalf(
				"%s: project_permission_held = %t; the projection's reason %q says %t",
				name,
				held,
				want,
				wantHeld,
			)
		}
		for where, value := range reads {
			got := ""
			if value.Execution.UnreadyReason != nil {
				got = *value.Execution.UnreadyReason
			}
			if got != want || value.Execution.AssigneeReady != (want == "") {
				t.Fatalf("%s: %s execution = ready %t reason %q; want reason %q",
					name, where, value.Execution.AssigneeReady, got, want)
			}
		}
	}

	if created.Task.Execution.AssigneeReady || created.Task.Execution.UnreadyReason == nil ||
		*created.Task.Execution.UnreadyReason != "assignee_cannot_run" {
		t.Fatalf("create receipt execution = %+v; want assignee_cannot_run", created.Task.Execution)
	}
	readiness("read and write, no execute", "assignee_cannot_run")

	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID,
		"project.tasks.write", "project.tasks.execute")
	readiness("execute granted", "")

	// Without the setup and without execute, the permission is the reason:
	// setting the Agent up would not let the Run start.
	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_grants
		WHERE project_id = $1 AND user_id = $2 AND permission = 'project.tasks.execute'`, fixture.ProjectA, memberID)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM agent_runtime_bindings WHERE agent_id = $1 AND user_id = $2`,
		fixture.AgentA, memberID)
	readiness("neither execute nor setup", "assignee_cannot_run")
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID,
		"project.tasks.write", "project.tasks.execute")
	readiness("execute, no setup", "not_bound")
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, memberID, runtime, "codex")

	// A Company-visible Project gives every active member the default set,
	// execute included, with no row of their own.
	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_grants
		WHERE project_id = $1 AND user_id = $2 AND permission <> 'project.read'`, fixture.ProjectA, memberID)
	readiness("read only", "assignee_cannot_run")
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET visibility = 'company' WHERE id = $1`, fixture.ProjectA)
	readiness("Company-visible Project", "")

	// An execute grant written before a Company rejoin is fenced, as
	// project_permission_held fences it; the read and write rows given again
	// after the rejoin keep the Task readable.
	dbtest.MustExec(t, ctx, pool, `UPDATE projects SET visibility = 'restricted' WHERE id = $1`, fixture.ProjectA)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID,
		"project.tasks.write", "project.tasks.execute")
	readiness("restricted again, execute granted", "")
	dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET reactivated_at = clock_timestamp()
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, memberID)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID, "project.tasks.write")
	readiness("execute granted before a rejoin", "assignee_cannot_run")
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID,
		"project.tasks.write", "project.tasks.execute")
	readiness("execute granted after the rejoin", "")

	// A disabled account is no longer an active member: the Task needs
	// another assignee, which no grant can change.
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET disabled_at = now() WHERE id = $1`, memberID)
	system, err := queries.GetTaskProjectionForSystem(
		ctx,
		systemProjectionParams(fixture.CompanyA, created.Task.ID),
	)
	if err != nil {
		t.Fatal(err)
	}
	if value := responseFromSystemProjectionRow(system); value.Execution.AssigneeReady ||
		value.Execution.UnreadyReason == nil || *value.Execution.UnreadyReason != "assignee_inactive" {
		t.Fatalf("disabled assignee execution = %+v; want assignee_inactive", value.Execution)
	}
	if held, err := auth.HoldsProjectPermission(ctx, queries, fixture.CompanyA, fixture.ProjectA, memberID,
		auth.ProjectPermissionTasksExecute); err != nil || held {
		t.Fatalf("disabled assignee project_permission_held = %t, %v; want false", held, err)
	}
}

// A Group's grant counts like a person's. A member who reads
// a Project only through a Group can be its Tasks' assignee, sees them with
// the Group's permissions, and the readiness arm counts the Group's execute,
// as project_permission_held does. A membership older than the member's
// latest rejoin counts for nothing; a membership written after it counts.
func TestTaskAccessAndReadinessCountTheAssigneesGroups(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	memberID, member := seedProjectOutsider(t, ctx, pool, fixture, "finance@example.com", auth.CompanyRoleMember)
	runtime := seedTaskRuntime(t, ctx, pool, fixture, "daemon-finance", memberID)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, memberID, runtime, "codex")
	finance := dbtest.SeedGroup(t, ctx, pool, fixture.CompanyA, "Finance")
	dbtest.AddGroupMembers(t, ctx, pool, fixture.CompanyA, finance, memberID)
	dbtest.GrantGroupProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, finance, "project.tasks.write")

	agent := ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	created, err := service.Create(ctx, owner, CreateInput{
		ProjectID: fixture.ProjectA, Key: "payroll", Title: "Payroll",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
		Assignee: ActorRef{Kind: ActorUser, ID: memberID}, ExecutedBy: &agent,
	})
	if err != nil {
		t.Fatalf("Create(assigned to a Group reader) error = %v", err)
	}
	value, err := service.Get(ctx, member, created.Task.ID, nil)
	want := []auth.ProjectPermission{auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite}
	if err != nil || value.Access == nil || !slices.Equal(value.Access.Permissions, want) {
		t.Fatalf("Group reader Get() = access %+v, %v; want %v", value.Access, err, want)
	}

	queries := generated.New(pool)
	readiness := func(name, want string) {
		t.Helper()
		system, err := queries.GetTaskProjectionForSystem(
			ctx,
			systemProjectionParams(fixture.CompanyA, created.Task.ID),
		)
		if err != nil {
			t.Fatalf("%s: system projection error = %v", name, err)
		}
		reads := map[string]Response{"system read": responseFromSystemProjectionRow(system)}
		one, err := service.Get(ctx, owner, created.Task.ID, nil)
		if err != nil {
			t.Fatalf("%s: owner Get() error = %v", name, err)
		}
		reads["owner read"] = one
		held, err := auth.HoldsProjectPermission(ctx, queries, fixture.CompanyA, fixture.ProjectA, memberID,
			auth.ProjectPermissionTasksExecute)
		if err != nil || held != (want == "") {
			t.Fatalf(
				"%s: project_permission_held = %t, %v; the projection's reason %q says %t",
				name,
				held,
				err,
				want,
				want == "",
			)
		}
		for where, value := range reads {
			got := ""
			if value.Execution.UnreadyReason != nil {
				got = *value.Execution.UnreadyReason
			}
			if got != want || value.Execution.AssigneeReady != (want == "") {
				t.Fatalf("%s: %s execution = ready %t reason %q; want reason %q",
					name, where, value.Execution.AssigneeReady, got, want)
			}
		}
	}
	readiness("the Group may write, not execute", "assignee_cannot_run")
	dbtest.GrantGroupProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, finance,
		"project.tasks.write", "project.tasks.execute")
	readiness("the Group may execute", "")

	dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET reactivated_at = clock_timestamp()
		WHERE company_id = $1 AND user_id = $2`, fixture.CompanyA, memberID)
	if _, err := service.Get(ctx, member, created.Task.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get() through a membership older than the rejoin error = %v, want ErrNotFound", err)
	}
	readiness("a membership older than the rejoin", "assignee_cannot_run")
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM company_group_members WHERE group_id = $1 AND user_id = $2`,
		finance,
		memberID,
	)
	dbtest.AddGroupMembers(t, ctx, pool, fixture.CompanyA, finance, memberID)
	readiness("added again after the rejoin", "")
	if _, err := service.Get(ctx, member, created.Task.ID, nil); err != nil {
		t.Errorf("Get() after the member is added again error = %v", err)
	}
}

// The write statements check the permission again themselves (ADR 0017
// decision 7): a write that reaches them without the service's check, as a
// write racing a revoke would, is refused and changes nothing.
func TestTaskWriteStatementsFenceTheProjectPermission(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	readerID, reader := seedProjectOutsider(t, ctx, pool, fixture, "reader@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, readerID, "project.read")
	queries := generated.New(pool)
	var pending []events.Event

	self := ActorRef{Kind: ActorUser, ID: readerID}
	_, _, err := insertTaskWithConversation(ctx, queries, reader, CreateInput{
		ProjectID: fixture.ProjectA, Key: "fenced_insert", Title: "Fenced",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo, Priority: PriorityMedium,
		Assignee: self, ExecutedBy: &self,
	}, 9001, &pending)
	if !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("insert without tasks.write error = %v, want ErrProjectPermissionRequired", err)
	}
	if count := countTasksWithKey(t, ctx, pool, "fenced_insert"); count != 0 {
		t.Fatalf("the fenced insert wrote %d Task(s)", count)
	}

	current, err := service.Get(ctx, owner, fixture.TaskA, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := current
	target.Title = "Fenced title"
	_, err = updateTask(
		ctx,
		queries,
		target,
		current.Revision,
		&pending,
		events.UserActor(readerID.String()),
		false,
		personWriteFence(readerID, auth.ProjectPermissionTasksWrite),
	)
	if !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("update without tasks.write error = %v, want ErrProjectPermissionRequired", err)
	}
	// A system writer passes no fence; the owner's fence holds.
	after, err := service.Get(ctx, owner, fixture.TaskA, nil)
	if err != nil || after.Title != current.Title || after.Revision != current.Revision {
		t.Fatalf("the fenced update changed the Task: %q revision %d, %v", after.Title, after.Revision, err)
	}
	if _, err := updateTask(
		ctx,
		queries,
		target,
		current.Revision,
		&pending,
		events.UserActor(owner.UserID.String()),
		false,
		personWriteFence(owner.UserID, auth.ProjectPermissionTasksWrite),
	); err != nil {
		t.Fatalf("update with tasks.write error = %v", err)
	}
}

// A Task named by its key or label is looked up only among the Tasks the
// caller reads: a hidden Task never shadows a visible one, and naming only a
// hidden Task answers not found.
func TestTaskReferenceResolvesAmongReadableTasks(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	memberID, member := seedProjectOutsider(t, ctx, pool, fixture, "member@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID, "project.read")
	var hiddenProject uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (company_id, key, title) VALUES ($1, 'hidden', 'Hidden') RETURNING id`,
		fixture.CompanyA).Scan(&hiddenProject); err != nil {
		t.Fatal(err)
	}
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, hiddenProject, owner.UserID)
	create := func(projectID uuid.UUID, key string) uuid.UUID {
		t.Helper()
		created, err := service.Create(ctx, owner, CreateInput{
			ProjectID:     projectID,
			Key:           key,
			Title:         key,
			MilestoneType: MilestoneTypeProduct,
			Status:        StatusTodo,
		})
		if err != nil {
			t.Fatalf("Create(%s) error = %v", key, err)
		}
		return created.Task.ID
	}
	// The exact key wins over a key equal in another letter case, so before
	// ADR 0017 the hidden Task named "shared" took the name from the
	// visible "SHARED".
	hidden := create(hiddenProject, "shared")
	visible := create(fixture.ProjectA, "SHARED")
	create(hiddenProject, "only_hidden")

	if id, err := service.ResolveReference(ctx, owner, "shared"); err != nil || id != hidden {
		t.Fatalf("owner ResolveReference(shared) = %s, %v; want the exact key %s", id, err, hidden)
	}
	if id, err := service.ResolveReference(ctx, member, "shared"); err != nil || id != visible {
		t.Fatalf("member ResolveReference(shared) = %s, %v; want the readable %s", id, err, visible)
	}
	if _, err := service.ResolveReference(ctx, member, "only_hidden"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("member ResolveReference(only_hidden) error = %v, want ErrNotFound", err)
	}
}

// explainingDBTX runs every statement against the pool and keeps the last
// one's text and arguments, so a test can EXPLAIN exactly what sqlc sent.
type explainingDBTX struct {
	pool *pgxpool.Pool
	sql  string
	args []any
}

func (d *explainingDBTX) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	d.sql, d.args = sql, args
	return d.pool.Exec(ctx, sql, args...)
}

func (d *explainingDBTX) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	d.sql, d.args = sql, args
	return d.pool.Query(ctx, sql, args...)
}

func (d *explainingDBTX) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	d.sql, d.args = sql, args
	return d.pool.QueryRow(ctx, sql, args...)
}

// The Task list decides access in the statement through project_access and
// must stay on indexes as the Company grows. A board (the list filtered by a
// Project) reaches that Project's Tasks through the (company, project) index
// and scans no Task of another Project. Both lists read the caller's grants
// through an index, never a scan of every grant. The unfiltered list reads
// the Company's Tasks as it did before Project access, so its Task scan is
// the planner's choice and not pinned here.
func TestTaskListUsesIndexes(t *testing.T) {
	ctx, pool, fixture, _, _ := startTaskHarness(t)
	memberID, member := seedProjectOutsider(t, ctx, pool, fixture, "member@example.com", auth.CompanyRoleMember)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO projects (company_id, key, title)
		SELECT $1, 'bulk_' || n, 'Bulk ' || n FROM generate_series(1, 40) AS n`, fixture.CompanyA)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			WITH seeded AS MATERIALIZED (
				SELECT project.id AS project_id, gen_random_uuid() AS task_id,
				       gen_random_uuid() AS conversation_id,
				       row_number() OVER (ORDER BY project.id, series.n) AS n
				FROM projects AS project
				CROSS JOIN generate_series(1, 400) AS series(n)
				WHERE project.company_id = $1 AND project.key LIKE 'bulk_%'
			), conversation_rows AS (
				INSERT INTO conversations (id, company_id, kind, task_id, project_id, title)
				SELECT conversation_id, $1, 'task', task_id, project_id, 'Bulk' FROM seeded
			)
			INSERT INTO tasks (id, company_id, conversation_id, project_id, milestone_type,
			                   number, key, title, assignee_user_id, creator_user_id)
			SELECT task_id, $1, conversation_id, project_id, 'product',
			       1000 + n, 'bulk_task_' || n, 'Bulk ' || n, $2, $2
			FROM seeded`, fixture.CompanyA, fixture.UserA)
	})
	var readable uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM projects WHERE company_id = $1 AND key = 'bulk_7'`,
		fixture.CompanyA).Scan(&readable); err != nil {
		t.Fatal(err)
	}
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, readable, memberID, "project.read")
	// Colleagues with grants on every Project, so project_grants is large
	// enough that a scan of it would show.
	dbtest.MustExec(t, ctx, pool, `
		WITH colleague AS (
			INSERT INTO users (email, display_name)
			SELECT 'colleague-' || n || '@example.com', 'Colleague ' || n FROM generate_series(1, 40) AS n
			RETURNING id
		), membership AS (
			INSERT INTO company_members (company_id, user_id, role)
			SELECT $1, colleague.id, 'member' FROM colleague
			RETURNING user_id
		)
		INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
		SELECT $1, project.id, membership.user_id, permission, $2
		FROM membership
		CROSS JOIN projects AS project
		CROSS JOIN unnest(ARRAY['project.read', 'project.tasks.write', 'project.tasks.execute']) AS permission
		WHERE project.company_id = $1`, fixture.CompanyA, fixture.UserA)
	// Forty Groups of forty more colleagues, each Group holding grants on
	// every Project, so a scan of company_group_members or
	// project_group_grants would show too.
	dbtest.SeedGroupColleagues(t, ctx, pool, fixture.CompanyA, "group-colleague", 40,
		"project.tasks.write", "project.tasks.execute")

	// Edges inside every Project, so the dependency graph has rows to join.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_dependencies (project_id, company_id, dependent_task_id, prerequisite_task_id)
		SELECT dependent.project_id, $1, dependent.id, prerequisite.id
		FROM tasks AS dependent
		JOIN tasks AS prerequisite
		  ON prerequisite.company_id = dependent.company_id
		 AND prerequisite.project_id = dependent.project_id
		 AND prerequisite.number = dependent.number - 1
		WHERE dependent.company_id = $1 AND dependent.key LIKE 'bulk_task_%' AND dependent.number % 20 = 0`,
		fixture.CompanyA)
	var oneTask uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM tasks WHERE project_id = $1 LIMIT 1`, readable).
		Scan(&oneTask); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `ANALYZE`)

	recorder := &explainingDBTX{pool: pool}
	queries := generated.New(recorder)
	seqScan := func(table string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)Seq Scan on ` + table + `( |$)`)
	}
	assertIndexed := func(name string, tables ...string) {
		t.Helper()
		plan, err := pool.Query(ctx, `EXPLAIN (COSTS OFF) `+recorder.sql, recorder.args...)
		if err != nil {
			t.Fatalf("%s explain: %v", name, err)
		}
		lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(lines, "\n")
		for _, table := range tables {
			if seqScan(table).MatchString(text) {
				t.Errorf("%s scans all of %s:\n%s", name, table, text)
			}
		}
	}
	grants := []string{"project_grants", "company_group_members", "project_group_grants"}
	for name, test := range map[string]struct {
		options   ListOptions
		unscanned []string
	}{
		"whole list":       {ListOptions{}, grants},
		"filtered project": {ListOptions{ProjectID: &readable}, append([]string{"tasks"}, grants...)},
	} {
		page, err := listTasks(ctx, queries, member, test.options)
		if err != nil || len(page.Tasks) != 400 {
			t.Fatalf("%s: listed %d Tasks, %v; want the readable Project's 400", name, len(page.Tasks), err)
		}
		// Their assignee (the fixture owner) holds no grant on the bulk
		// Projects: every row runs the assignee's project_access lookup, and
		// the pins below keep it one indexed probe per row.
		if !page.Tasks[0].AssigneeOutsideProject {
			t.Fatalf("%s: assigneeOutsideProject = false for an assignee who cannot read the Project", name)
		}
		assertIndexed("the Task list ("+name+")", test.unscanned...)
	}
	// A read of one Task decides access on its own Project only, so it
	// reaches that Task, and the caller's grants, by index.
	if _, err := taskProjectionForActiveMember(ctx, queries, member, oneTask); err != nil {
		t.Fatalf("read one Task: %v", err)
	}
	assertIndexed("a read of one Task", append([]string{"tasks"}, grants...)...)
	// The dependency graph joins project_access on the edge's Project and
	// reads the caller's grants by index. Like the whole list, it reads the
	// Company's edges, so its Task joins are the planner's choice.
	graph, err := listDependencyGraph(ctx, queries, member)
	if err != nil || len(graph.Dependencies) != 20 {
		t.Fatalf(
			"dependency graph = %d edges, %v; want the readable Project's 20",
			len(graph.Dependencies),
			err,
		)
	}
	assertIndexed("the dependency graph", grants...)
}
