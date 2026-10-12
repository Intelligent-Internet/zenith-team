//go:build integration

package task

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func listedTaskIDs(page Page) []uuid.UUID {
	ids := make([]uuid.UUID, len(page.Tasks))
	for i, value := range page.Tasks {
		ids[i] = value.ID
	}
	return ids
}

// fixtureTaskRun is the fixture owner's Task Run of Task A, as the handler
// scopes its proof: the delegate rule reads the Run's own row (D31), so the
// scope names a real Run.
func fixtureTaskRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture) *RunScope {
	t.Helper()
	runID := dbtest.SeedRun(t, ctx, pool, dbtest.RunSeed{
		CompanyID: fixture.CompanyA, AgentID: fixture.AgentA, RuntimeID: fixture.RuntimeA,
		ConversationID: fixture.ConversationA, TaskID: fixture.TaskA, AuthorizerID: fixture.UserA,
	})
	return &RunScope{AgentID: fixture.AgentA, UserID: fixture.UserA, RunID: runID, ProjectID: &fixture.ProjectA}
}

// fixtureDirectRun is a Run of the fixture Agent in the owner's direct chat,
// for authorizer: a Run outside a Task, not tagged in any chat.
func fixtureDirectRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	authorizer uuid.UUID,
) *RunScope {
	t.Helper()
	runID := dbtest.SeedRun(t, ctx, pool, dbtest.RunSeed{
		CompanyID: fixture.CompanyA, AgentID: fixture.AgentA, RuntimeID: fixture.RuntimeA,
		ConversationID: fixture.DirectConversationA, AuthorizerID: authorizer,
	})
	return &RunScope{AgentID: fixture.AgentA, UserID: authorizer, RunID: runID}
}

// A Run reads with its authorizer's session (the handler checks the proof's
// bearer is the authorizer), so its reads follow that member's Project
// access (ADR 0017): its own Task in a Project they read reads, the same
// Task after they lose project.read is not found, and `zen task create`
// from a Task Run needs their project.tasks.write in that Project.
func TestRunScopeReadsFollowTheAuthorizersProjectAccess(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	scope := fixtureTaskRun(t, ctx, pool, fixture)

	if _, err := service.Get(ctx, owner, fixture.TaskA, scope); err != nil {
		t.Fatalf("Get(own Task) = %v", err)
	}
	page, err := service.List(ctx, owner, ListOptions{}, scope)
	if err != nil || !slices.Contains(listedTaskIDs(page), fixture.TaskA) {
		t.Fatalf("List() = %v, %v; want the Run's own Task", listedTaskIDs(page), err)
	}
	create := CreateInput{
		ProjectID: fixture.ProjectA, Key: "follow_up", Title: "Follow up",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo, Run: scope,
	}
	if _, err := service.Create(ctx, owner, create); err != nil {
		t.Fatalf("Create() from the Task Run = %v", err)
	}

	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2 AND permission = 'project.tasks.write'`,
		fixture.ProjectA, fixture.UserA)
	create.Key = "second_follow_up"
	if _, err := service.Create(ctx, owner, create); !errors.Is(err, auth.ErrProjectPermissionRequired) {
		t.Fatalf("Create() without project.tasks.write = %v, want ErrProjectPermissionRequired", err)
	}

	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		fixture.ProjectA, fixture.UserA)
	if _, err := service.Get(ctx, owner, fixture.TaskA, scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(own Task) after losing project.read = %v, want ErrNotFound", err)
	}
	if _, err := service.ListDependencies(ctx, owner, fixture.TaskA, scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListDependencies() after losing project.read = %v, want ErrNotFound", err)
	}
	page, err = service.List(ctx, owner, ListOptions{}, scope)
	if err != nil || len(page.Tasks) != 0 {
		t.Fatalf("List() after losing project.read = %v, %v; want nothing", listedTaskIDs(page), err)
	}
}

// A Task Run answers into its Task chat, which only its Project's readers
// read, so it reads and files work only in that Project — not even the
// member's own Task, done by the same Agent, in another Project the member
// reads. Otherwise a reader of Project A could ask, through the Task chat,
// for the member's work in Project B and read the answer. Every refusal is
// the fixed Run-scope sentence, naming nothing.
func TestTaskRunReadsOnlyItsOwnProject(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	secret := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, 'secret', 'Secret')`,
		secret, fixture.CompanyA)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, secret, fixture.UserA)
	elsewhere, err := service.Create(ctx, owner, CreateInput{
		ProjectID: secret, Key: "acquisition", Title: "Secret acquisition plan",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo,
		ExecutedBy: &ActorRef{Kind: ActorAgent, ID: fixture.AgentA},
	})
	if err != nil {
		t.Fatalf("Create(own Task in another Project) = %v", err)
	}
	taskRun := fixtureTaskRun(t, ctx, pool, fixture)

	if _, err := service.Get(ctx, owner, fixture.TaskA, taskRun); err != nil {
		t.Fatalf("Get(own Task in the Run's Project) = %v", err)
	}
	if _, err := service.Get(ctx, owner, elsewhere.Task.ID, taskRun); !errors.Is(err, ErrOutsideRunScope) {
		t.Fatalf("Get(own Task in another Project) = %v, want ErrOutsideRunScope", err)
	}
	if _, err := service.ListDependencies(ctx, owner, elsewhere.Task.ID, taskRun); !errors.Is(err, ErrOutsideRunScope) {
		t.Fatalf("ListDependencies(own Task in another Project) = %v, want ErrOutsideRunScope", err)
	}
	if _, err := service.Workspace(ctx, owner, elsewhere.Task.ID, WorkspaceOptions{}, taskRun); !errors.Is(err, ErrOutsideRunScope) {
		t.Fatalf("Workspace(own Task in another Project) = %v, want ErrOutsideRunScope", err)
	}
	for name, options := range map[string]ListOptions{
		"no Project filter":     {},
		"the other Project":     {ProjectID: &secret},
		"the Run's own Project": {ProjectID: &fixture.ProjectA},
	} {
		page, err := service.List(ctx, owner, options, taskRun)
		if err != nil || slices.Contains(listedTaskIDs(page), elsewhere.Task.ID) {
			t.Fatalf("List(%s) = %v, %v; want the other Project's Task left out", name, listedTaskIDs(page), err)
		}
	}
	// The same member's own read, with no proof, still sees both.
	if _, err := service.Get(ctx, owner, elsewhere.Task.ID, nil); err != nil {
		t.Fatalf("Get() without a Run = %v", err)
	}
}

// A Run tagged in a channel or group DM uses its Authorizer's access, never
// more, and nothing else narrows it (D31, revised 2026-10-06): the person
// who tags the Agent answers for what its answer shares with the chat. A
// colleague in the channel who cannot read the Project no longer refuses
// the Run's get, list, dependencies or create, and neither does someone
// who joins later; a tagger who cannot read the Project is still refused,
// as any Run is. The Run still reads only its own Tasks.
func TestTaggedRunReadsWhatItsAuthorizerReads(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	colleague, _ := seedProjectOutsider(t, ctx, pool, fixture, "colleague@example.com", auth.CompanyRoleMember)
	channel := dbtest.SeedDiscussionChannel(t, ctx, pool, fixture.CompanyA, fixture.UserA, nil)
	dbtest.AddDiscussionMember(t, ctx, pool, fixture.CompanyA, channel, colleague)
	runID := dbtest.SeedTaggedRun(
		t,
		ctx,
		pool,
		fixture.CompanyA,
		channel,
		fixture.AgentA,
		fixture.RuntimeA,
		fixture.UserA,
	)
	scope := &RunScope{AgentID: fixture.AgentA, UserID: fixture.UserA, RunID: runID}
	creates := 0
	assertAdmitted := func(name string) {
		t.Helper()
		if _, err := service.Get(ctx, owner, fixture.TaskA, scope); err != nil {
			t.Fatalf("%s: Get() = %v", name, err)
		}
		if _, err := service.ListDependencies(ctx, owner, fixture.TaskA, scope); err != nil {
			t.Fatalf("%s: ListDependencies() = %v", name, err)
		}
		page, err := service.List(ctx, owner, ListOptions{}, scope)
		if err != nil || !slices.Contains(listedTaskIDs(page), fixture.TaskA) {
			t.Fatalf("%s: List() = %v, %v; want the Task", name, listedTaskIDs(page), err)
		}
		creates++
		if _, err := service.Create(ctx, owner, CreateInput{
			ProjectID: fixture.ProjectA, Key: fmt.Sprintf("from_chat_%d", creates), Title: "From the chat",
			MilestoneType: MilestoneTypeProduct, Status: StatusTodo, Run: scope,
		}); err != nil {
			t.Fatalf("%s: Create() = %v", name, err)
		}
	}

	assertAdmitted("a channel with a member outside the Project")

	// Someone who cannot read the Project joins: the answer reaches them,
	// and the tagger answers for it.
	newcomer, _ := seedProjectOutsider(t, ctx, pool, fixture, "newcomer@example.com", auth.CompanyRoleMember)
	dbtest.AddDiscussionMember(t, ctx, pool, fixture.CompanyA, channel, newcomer)
	assertAdmitted("a channel someone outside the Project joined")

	// A direct message between two people: the same rule.
	dm := dbtest.SeedHumanDirect(t, ctx, pool, fixture.CompanyA, fixture.UserA, newcomer)
	dmRun := dbtest.SeedTaggedRun(
		t,
		ctx,
		pool,
		fixture.CompanyA,
		dm,
		fixture.AgentA,
		fixture.RuntimeA,
		fixture.UserA,
	)
	scope = &RunScope{AgentID: fixture.AgentA, UserID: fixture.UserA, RunID: dmRun}
	assertAdmitted("a direct message with someone outside the Project")

	// The Run still reads only its own Tasks: a colleague's Task the same
	// Agent does, in a Project the tagger reads, is refused.
	colleaguesTask, colleaguesChat := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, colleague, "project.read")
	// The chat and its Task reference each other, and the chat's key to the
	// Task is checked at commit, so both go in one transaction.
	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = seed.Rollback(ctx) }()
	if _, err := seed.Exec(ctx, `
		INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
		VALUES ($1, $2, 'task', $3, 'Colleague task', $4)`,
		colleaguesChat, fixture.CompanyA, colleaguesTask, fixture.ProjectA); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(ctx, `
		INSERT INTO tasks
			(project_id, id, company_id, conversation_id, milestone_type, number, key,
			 title, executed_by_agent_id, assignee_user_id, creator_user_id)
		VALUES ($1, $2, $3, $4, 'product', 900, 'colleague_task', 'Colleague task', $5, $6, $7)`,
		fixture.ProjectA, colleaguesTask, fixture.CompanyA, colleaguesChat,
		fixture.AgentA, colleague, fixture.UserA); err != nil {
		t.Fatal(err)
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, owner, colleaguesTask, scope); !errors.Is(err, ErrOutsideRunScope) {
		t.Fatalf("Get(a colleague's Task) = %v, want ErrOutsideRunScope", err)
	}
	page, err := service.List(ctx, owner, ListOptions{}, scope)
	if err != nil || slices.Contains(listedTaskIDs(page), colleaguesTask) {
		t.Fatalf("List() = %v, %v; want the colleague's Task left out", listedTaskIDs(page), err)
	}

	// A tagger who cannot read the Project is refused, as any Run is.
	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		fixture.ProjectA, fixture.UserA)
	if _, err := service.Get(ctx, owner, fixture.TaskA, scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() for a tagger without the Project = %v, want ErrNotFound", err)
	}
	if _, err := service.ListDependencies(ctx, owner, fixture.TaskA, scope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListDependencies() for a tagger without the Project = %v, want ErrNotFound", err)
	}
	page, err = service.List(ctx, owner, ListOptions{}, scope)
	if err != nil || slices.Contains(listedTaskIDs(page), fixture.TaskA) {
		t.Fatalf(
			"List() for a tagger without the Project = %v, %v; want the Task left out",
			listedTaskIDs(page),
			err,
		)
	}
	// A Run's create asks the delegate rule before the Project is read
	// (admitCreate), so it gets the Run-scope refusal, which names nothing.
	_, err = service.Create(ctx, owner, CreateInput{
		ProjectID: fixture.ProjectA, Key: "refused_from_chat", Title: "From the chat",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo, Run: scope,
	})
	if !errors.Is(err, ErrOutsideRunScope) {
		t.Fatalf("Create() for a tagger without the Project = %v, want ErrOutsideRunScope", err)
	}
}
