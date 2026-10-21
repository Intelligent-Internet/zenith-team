//go:build integration

package task

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// A Run reads only its own Tasks: the ones its Agent does for the member the
// Run works for. Alice's Run of Agent A lists and reads her Tasks that Agent
// A does; Bob's Task that Agent A does for him, and Alice's own Task that
// another Agent does, are refused with the plain sentinel — never their
// content, label, member or Agent. The prerequisites of a Task it can read stay
// readable as that Task's dependencies; of the Tasks waiting on it, only
// the Run's own are. A member's own read, with no Run, sees every Task.
func TestTaskReadsUnderARunScopeSeeOnlyTheRunsOwnTasks(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewTaskService(store, newTestBus(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	alice := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	bob, eli := dbtest.NewUUID(t), dbtest.NewUUID(t)
	bobsTask, elisTask, ownSlugTask, shadowTask := dbtest.NewUUID(t), dbtest.NewUUID(t), dbtest.NewUUID(t),
		dbtest.NewUUID(t)
	olderLaunch, newerLaunch := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx,
			`INSERT INTO users (id, email, display_name) VALUES ($1, 'bob@example.com', 'Bob Member')`, bob)
		dbtest.MustExecTx(t, ctx, tx,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
			fixture.CompanyA, bob)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO agents
				(id, company_id, name, title, model, provider, created_by_user_id, reports_to_agent_id)
			VALUES ($1, $2, 'Eli', 'Engineer', 'test', 'codex', $3, $4)`,
			eli, fixture.CompanyA, fixture.UserA, fixture.AgentA)
		seed := func(id uuid.UUID, number int64, key string, assignee, agent uuid.UUID) {
			conversation := dbtest.NewUUID(t)
			dbtest.MustExecTx(t, ctx, tx, `
				INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
				VALUES ($1, $2, 'task', $3, $4, (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
				conversation, fixture.CompanyA, id, key)
			dbtest.MustExecTx(
				t,
				ctx,
				tx,
				`
				INSERT INTO tasks
					(project_id, id, company_id, conversation_id, milestone_type, number, key,
					 title, description, executed_by_agent_id, assignee_user_id, creator_user_id)
				VALUES ($1, $2, $3, $4, 'product', $5, $6, $6, 'ask the user: red or blue?', $7, $8, $9)`,
				fixture.ProjectA,
				id,
				fixture.CompanyA,
				conversation,
				number,
				key,
				agent,
				assignee,
				fixture.UserA,
			)
		}
		seed(bobsTask, 2, "SCA-2", bob, fixture.AgentA)
		seed(elisTask, 3, "SCA-3", fixture.UserA, eli)
		seed(ownSlugTask, 4, "ship_release", fixture.UserA, fixture.AgentA)
		// Older data: a key that reads like the label of ship_release.
		seed(shadowTask, 5, "SCA-4", bob, eli)
		// Keys are unique only in their exact letter case.
		seed(newerLaunch, 7, "LAUNCH", bob, eli)
		seed(olderLaunch, 6, "Launch", bob, eli)
		// Task A waits on Bob's Task; Bob's Task that Eli does, and Alice's
		// own ship_release, wait on Task A.
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO task_dependencies
				(project_id, company_id, dependent_task_id, prerequisite_task_id, created_by_user_id)
			VALUES ($1, $2, $3, $4, $7), ($1, $2, $5, $3, $7), ($1, $2, $6, $3, $7)`,
			fixture.ProjectA, fixture.CompanyA, fixture.TaskA, bobsTask,
			shadowTask, ownSlugTask, fixture.UserA)
	})
	scope := fixtureDirectRun(t, ctx, pool, fixture, fixture.UserA)
	ids := func(page Page) map[uuid.UUID]bool {
		listed := map[uuid.UUID]bool{}
		for _, value := range page.Tasks {
			listed[value.ID] = true
		}
		return listed
	}

	// The list is the Run's own Tasks, whatever filters the caller sent.
	page, err := service.List(ctx, alice, ListOptions{AssigneeUserID: &bob, ExecutedByAgentID: &eli}, scope)
	if err != nil {
		t.Fatalf("scoped List() error = %v", err)
	}
	if listed := ids(page); len(listed) != 2 || !listed[fixture.TaskA] || !listed[ownSlugTask] {
		t.Fatalf("scoped List() = %v, want only Task A and ship_release", listed)
	}
	if page, err := service.List(ctx, alice, ListOptions{}, nil); err != nil || len(page.Tasks) != 7 {
		t.Fatalf("member List() = %d Tasks, %v; want all 7", len(page.Tasks), err)
	}

	// Its own Task reads in full.
	if value, err := service.Get(ctx, alice, fixture.TaskA, scope); err != nil || value.ID != fixture.TaskA {
		t.Fatalf("own Get() = %+v, %v", value, err)
	}
	// Another member's Task the same Agent does, and the member's own Task
	// another Agent does, are refused with the plain sentinel: the error
	// names no Task, member or Agent.
	for _, id := range []uuid.UUID{bobsTask, elisTask, shadowTask} {
		value, err := service.Get(ctx, alice, id, scope)
		if !errors.Is(err, ErrOutsideRunScope) || value.Description != "" {
			t.Fatalf("out-of-scope Get(%s) = %+v, %v; want a refusal and nothing else", id, value, err)
		}
		for _, name := range []string{"SCA-", "Bob Member", "Schema A", "Agent A", "Eli"} {
			if strings.Contains(err.Error(), name) {
				t.Fatalf("out-of-scope Get(%s) refusal %q names %q", id, err, name)
			}
		}
	}
	if _, err := service.Get(ctx, alice, dbtest.NewUUID(t), scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown Get() error = %v, want ErrNotFound", err)
	}
	if value, err := service.Get(ctx, alice, bobsTask, nil); err != nil || value.Description == "" {
		t.Fatalf("member Get() of a colleague's Task = %+v, %v", value, err)
	}

	// A readable Task's prerequisites stay readable as its dependencies. Of
	// the Tasks waiting on it, only the Run's own come with it: Bob's Task
	// that Eli does is left out, key, title, member and Agent alike.
	// Another member's Task's dependencies are refused like the Task.
	onlyOwnDependents := func(name string, dependencies DependencySet) {
		t.Helper()
		if len(dependencies.BlockedBy) != 1 || dependencies.BlockedBy[0].Task.ID != bobsTask {
			t.Fatalf("%s blockedBy = %+v; want Bob's prerequisite", name, dependencies.BlockedBy)
		}
		if len(dependencies.Blocks) != 1 || dependencies.Blocks[0].Task.ID != ownSlugTask {
			t.Fatalf("%s blocks = %+v; want only ship_release", name, dependencies.Blocks)
		}
	}
	dependencies, err := service.ListDependencies(ctx, alice, fixture.TaskA, scope)
	if err != nil {
		t.Fatalf("own ListDependencies() error = %v", err)
	}
	onlyOwnDependents("own ListDependencies()", dependencies)
	if _, err := service.ListDependencies(ctx, alice, bobsTask, scope); !errors.Is(err, ErrOutsideRunScope) {
		t.Fatalf("out-of-scope ListDependencies() error = %v, want ErrOutsideRunScope", err)
	}
	if dependencies, err := service.ListDependencies(ctx, alice, fixture.TaskA, nil); err != nil ||
		len(dependencies.Blocks) != 2 {
		t.Fatalf("member ListDependencies() = %+v, %v; want both dependents", dependencies.Blocks, err)
	}

	// A Task's workspace carries the whole Task — description, messages,
	// Run timelines — so it answers the scope the way the Task read does,
	// and its dependencies are the ones the Run's dependency read gets.
	window := WorkspaceOptions{MessageLimit: 20}
	if workspace, err := service.Workspace(ctx, alice, fixture.TaskA, window, scope); err != nil ||
		workspace.Task.ID != fixture.TaskA {
		t.Fatalf("own Workspace() = %+v, %v", workspace.Task, err)
	} else {
		onlyOwnDependents("own Workspace()", workspace.Dependencies)
	}
	if workspace, err := service.Workspace(
		ctx,
		alice,
		bobsTask,
		window,
		scope,
	); !errors.Is(err, ErrOutsideRunScope) ||
		workspace.Task.Description != "" ||
		len(workspace.Messages) != 0 {
		t.Fatalf("out-of-scope Workspace() = %+v, %v; want a refusal and nothing else", workspace, err)
	}
	if workspace, err := service.Workspace(ctx, alice, bobsTask, window, nil); err != nil ||
		workspace.Task.Description == "" {
		t.Fatalf("member Workspace() of a colleague's Task = %+v, %v", workspace.Task, err)
	}

	// The reads take a Task as people name it: its key in any letter case,
	// else the label in the Company's prefix. A key that is exactly the
	// string wins over the label of another Task.
	for reference, want := range map[string]uuid.UUID{
		"SCA-2":        bobsTask,
		"sca-3":        elisTask,
		"ship_release": ownSlugTask,
		"SHIP_RELEASE": ownSlugTask,
		"SCA-4":        shadowTask,
		"sca-1":        fixture.TaskA,
		"task_a":       fixture.TaskA,
		// An exact key wins; two keys equal in any case go to the older Task.
		"LAUNCH": newerLaunch,
		"Launch": olderLaunch,
		"launch": olderLaunch,
	} {
		if got, err := service.ResolveReference(ctx, alice, reference); err != nil || got != want {
			t.Fatalf("ResolveReference(%q) = %s, %v; want %s", reference, got, err, want)
		}
	}
	for _, reference := range []string{"SCA-99", "OPS-2", "task_b", "2"} {
		if _, err := service.ResolveReference(ctx, alice, reference); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveReference(%q) error = %v, want ErrNotFound", reference, err)
		}
	}
}

// A Run files work only for the member it works for, and a Task Run only in
// its own Task's Project. The CLI never names another assignee or Project
// inside a Run, but an Agent with a shell can call its channel by hand, so
// the server refuses both with the plain Run-scope sentinel and writes
// nothing — even for an owner, whose own create may assign anyone. A
// Project the Run may not read is refused the same way before the owner's
// own check, so the answer says nothing of what the owner may do there.
func TestTaskCreateUnderARunScopeFilesOnlyForItsMember(t *testing.T) {
	ctx, pool, fixture, service, alice := startTaskHarness(t)
	bob := seedTaskMember(t, ctx, pool, fixture, "bob@example.com")
	otherProject := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, 'ops', 'Ops')`,
		otherProject, fixture.CompanyA)
	// Both work in the second Project too (ADR 0017), so only the Run scope
	// decides here.
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, otherProject, fixture.UserA)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, otherProject, bob)
	// A Project the owner only manages (projects.manage, no project.read),
	// and one the owner reads but cannot write in. A Run's create there is
	// refused by the delegate rule before the owner's own check, so the
	// refusal is the same plain sentinel and says nothing of their access.
	managedOnly, readOnly := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO projects (id, company_id, key, title)
		VALUES ($1, $3, 'managed', 'Managed'), ($2, $3, 'read_only', 'Read only')`,
		managedOnly, readOnly, fixture.CompanyA)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, readOnly, fixture.UserA, "project.read")
	channelRun := fixtureDirectRun(t, ctx, pool, fixture, fixture.UserA)
	taskRun := fixtureTaskRun(t, ctx, pool, fixture)
	bobsRun := fixtureDirectRun(t, ctx, pool, fixture, bob)
	create := func(key string, project, assignee uuid.UUID, run *RunScope) (Response, error) {
		t.Helper()
		input := CreateInput{
			ProjectID: project, Key: key, Title: key, MilestoneType: MilestoneTypeProduct, Run: run,
		}
		if assignee != uuid.Nil {
			input.Assignee = ActorRef{Kind: ActorUser, ID: assignee}
		}
		result, err := service.Create(ctx, alice, input)
		return result.Task, err
	}

	for _, test := range []struct {
		key               string
		project, assignee uuid.UUID
		run               *RunScope
	}{
		{"channel_run_names_bob", fixture.ProjectA, bob, channelRun},
		{"task_run_names_bob", fixture.ProjectA, bob, taskRun},
		{"run_works_for_bob", fixture.ProjectA, uuid.Nil, bobsRun},
		{"task_run_other_project", otherProject, uuid.Nil, taskRun},
		{"task_run_read_only_project", readOnly, uuid.Nil, taskRun},
		{"task_run_managed_project", managedOnly, uuid.Nil, taskRun},
		{"direct_run_managed_project", managedOnly, uuid.Nil, channelRun},
	} {
		_, err := create(test.key, test.project, test.assignee, test.run)
		if !errors.Is(err, ErrOutsideRunScope) {
			t.Fatalf("%s: Create() error = %v, want ErrOutsideRunScope", test.key, err)
		}
		var count int
		err = pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE key = $1`, test.key).Scan(&count)
		if err != nil || count != 0 {
			t.Fatalf("%s: a refused create wrote %d Task(s), %v", test.key, count, err)
		}
	}

	// The Run's own member, in its own Task's Project; a Run outside a
	// Task in any Project; and the owner's own create, which may assign Bob.
	for _, test := range []struct {
		key                     string
		project, assignee, want uuid.UUID
		run                     *RunScope
	}{
		{"task_run_own_project", fixture.ProjectA, uuid.Nil, fixture.UserA, taskRun},
		{"task_run_names_its_member", fixture.ProjectA, fixture.UserA, fixture.UserA, taskRun},
		{"channel_run_other_project", otherProject, uuid.Nil, fixture.UserA, channelRun},
		{"owner_assigns_bob", otherProject, bob, bob, nil},
	} {
		created, err := create(test.key, test.project, test.assignee, test.run)
		if err != nil || created.Assignee.ID != test.want || created.ProjectID != test.project {
			t.Fatalf("%s: Create() = %+v, %v; want %s's Task in %s",
				test.key, created, err, test.want, test.project)
		}
	}
}
