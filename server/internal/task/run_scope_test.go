package task

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A Run reads only its own Tasks (owner decision, 2026-09-26): the ones its
// Agent does for the member the Run works for. Tagged in a channel and asked
// to "run BSC-8", an Agent read a colleague's Task and acted out its
// description on the wrong computer; that read is now refused instead.
func TestRunScopeAdmitsOnlyTheRunsOwnTasks(t *testing.T) {
	t.Parallel()

	alice := ActorRef{Kind: ActorUser, ID: uuid.New(), DisplayName: "Alice Member"}
	bob := ActorRef{Kind: ActorUser, ID: uuid.New(), DisplayName: "Bob Member"}
	lyra := ActorRef{Kind: ActorAgent, ID: uuid.New(), DisplayName: "Lyra"}
	eli := ActorRef{Kind: ActorAgent, ID: uuid.New(), DisplayName: "Eli"}
	scope := &RunScope{AgentID: lyra.ID, UserID: alice.ID}
	taskFor := func(assignee, executedBy ActorRef) Response {
		return Response{
			Key: "BSC-8", Number: 8, Description: "ask the user: red or blue?",
			Assignee: assignee, ExecutedBy: executedBy,
		}
	}

	if !scope.admits(taskFor(alice, lyra)) {
		t.Fatal("the Run's own Task (its Agent, its member) was refused")
	}
	for name, value := range map[string]Response{
		"a colleague's Task the same Agent does":   taskFor(bob, lyra),
		"another Agent's Task for the same member": taskFor(alice, eli),
		"a Task the member does themself":          taskFor(alice, alice),
		"a colleague's Task another Agent does":    taskFor(bob, eli),
	} {
		if scope.admits(value) {
			t.Fatalf("%s: admitted", name)
		}
	}

	// A member's own read carries no Run and sees every Task, as before.
	var member *RunScope
	if !member.admits(taskFor(bob, eli)) {
		t.Fatal("a read without a Run was scoped")
	}
}

func TestAssistingRunReadsOnlyItsExactHumanTask(t *testing.T) {
	t.Parallel()
	user := ActorRef{Kind: ActorUser, ID: uuid.New()}
	agent := ActorRef{Kind: ActorAgent, ID: uuid.New()}
	id := uuid.New()
	value := Response{ID: id, Assignee: user, ExecutedBy: user, Execution: Execution{Agent: &agent}}
	scope := &RunScope{AgentID: agent.ID, UserID: user.ID, TaskID: &id}
	if !scope.admits(value) {
		t.Fatal("assisting Run cannot read its own human Task")
	}
	other := value
	other.ID = uuid.New()
	if scope.admits(other) {
		t.Fatal("assisting Run can read another human Task")
	}
	other = value
	other.Assignee.ID = uuid.New()
	if scope.admits(other) {
		t.Fatal("assisting Run can read a Task handed to another member")
	}
	scope.TaskID = nil
	if scope.admits(value) {
		t.Fatal("direct Run can read a human Task without being its assisting Run")
	}
}

// The refusal is the plain sentinel. It carries nothing of the Task — not
// its description, its label, or the name of its member or its Agent — so
// the one fixed sentence the boundary serves is all a Run learns.
func TestRunScopeRefusesAReadWithThePlainSentinel(t *testing.T) {
	t.Parallel()

	alice := ActorRef{Kind: ActorUser, ID: uuid.New(), DisplayName: "Alice Member"}
	bob := ActorRef{Kind: ActorUser, ID: uuid.New(), DisplayName: "Bob Member"}
	lyra := ActorRef{Kind: ActorAgent, ID: uuid.New(), DisplayName: "Lyra"}
	eli := ActorRef{Kind: ActorAgent, ID: uuid.New(), DisplayName: "Eli"}
	scope := &RunScope{AgentID: lyra.ID, UserID: alice.ID}

	err := scope.admitRead(context.Background(), nil, Response{
		Key: "BSC-8", Number: 8, Description: "ask the user: red or blue?", Assignee: bob, ExecutedBy: eli,
	})
	if !errors.Is(err, ErrOutsideRunScope) || err.Error() != ErrOutsideRunScope.Error() {
		t.Fatalf("admitRead() = %v, want ErrOutsideRunScope and nothing added", err)
	}
	for _, leaked := range []string{"BSC-8", "red or blue", "Bob Member", "Eli"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("refusal %q carries %q", err, leaked)
		}
	}
	// The Run's own Task passes the ownership half; its Project is then the
	// delegate rule's to decide in the database (D31), which the
	// integration tests read.
	if !scope.admits(Response{Assignee: alice, ExecutedBy: lyra}) {
		t.Fatal("admits() refuses the Run's own Task")
	}
	var member *RunScope
	if err := member.admitRead(context.Background(), nil, Response{Assignee: bob, ExecutedBy: eli}); err != nil {
		t.Fatalf("admitRead() without a Run = %v", err)
	}
}

// Inside a Run the list is the Run's own Tasks whatever the caller asked
// for: the scope replaces the assignee and Agent filters rather than
// joining them, and every other filter still narrows within it.
func TestRunScopeForcesTheListOntoItsOwnTasks(t *testing.T) {
	t.Parallel()

	alice, bob := uuid.New(), uuid.New()
	lyra, eli := uuid.New(), uuid.New()
	project := uuid.New()
	resolved := true
	asked := ListOptions{
		ProjectID: &project, Statuses: []Status{StatusTodo},
		AssigneeUserID: &bob, ExecutedByAgentID: &eli, DependenciesResolved: &resolved,
	}

	got := (&RunScope{AgentID: lyra, UserID: alice}).listOptions(asked)
	if got.AssigneeUserID == nil || *got.AssigneeUserID != alice ||
		got.ExecutedByAgentID == nil || *got.ExecutedByAgentID != lyra {
		t.Fatalf("scoped filters = %v / %v, want the Run's member and Agent",
			got.AssigneeUserID, got.ExecutedByAgentID)
	}
	if got.ProjectID != &project || len(got.Statuses) != 1 || got.DependenciesResolved != &resolved {
		t.Fatalf("scoped list dropped the narrowing filters: %+v", got)
	}
	if *asked.AssigneeUserID != bob || *asked.ExecutedByAgentID != eli {
		t.Fatal("scoping the list rewrote the caller's options")
	}

	var member *RunScope
	if unscoped := member.listOptions(asked); unscoped.AssigneeUserID != &bob || unscoped.ExecutedByAgentID != &eli {
		t.Fatalf("a read without a Run was scoped: %+v", unscoped)
	}

	// A Task Run that names no Project lists its own; one that names another
	// keeps that filter, and the Project bound then leaves everything out.
	taskRun := &RunScope{AgentID: lyra, UserID: alice, ProjectID: &project}
	if got := taskRun.listOptions(ListOptions{}); got.ProjectID == nil || *got.ProjectID != project {
		t.Fatalf("Task Run list Project = %v, want its own %s", got.ProjectID, project)
	}
	other := uuid.New()
	if got := taskRun.listOptions(ListOptions{ProjectID: &other}); got.ProjectID != &other {
		t.Fatalf("Task Run list replaced the asked Project: %v", got.ProjectID)
	}
}

// Every Run's Project bound is the delegate rule in the database
// (run_permission_held, D31), so no Run is bounded in Go and none goes
// unbounded. A member's own request carries no Run: it reads every Project
// its own read found, files only for itself, and needs no database for that
// (the nil queries below would panic if it were asked).
func TestAMembersOwnRequestIsNotBoundedByARun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	own, other := uuid.New(), uuid.New()
	var member *RunScope
	readable, err := member.ReadableProjects(ctx, nil, uuid.New(), []uuid.UUID{own, other})
	if err != nil || !readable[own] || !readable[other] {
		t.Fatalf("ReadableProjects() = %v, %v; want every Project the read found", readable, err)
	}
	if err := member.AdmitProject(ctx, nil, uuid.New(), other); err != nil {
		t.Fatalf("AdmitProject() = %v", err)
	}
	if err := member.admitCreateProject(ctx, nil, uuid.New(), other); err != nil {
		t.Fatalf("admitCreateProject() = %v", err)
	}
	tasks := []Response{{ProjectID: own}, {ProjectID: other}}
	if kept, err := member.filterProjects(ctx, nil, uuid.New(), tasks); err != nil || len(kept) != 2 {
		t.Fatalf("filterProjects() = %+v, %v; want both Tasks", kept, err)
	}
	if err := member.admitCreate(ctx, nil, uuid.New(), CreateInput{
		ProjectID: other, Assignee: ActorRef{Kind: ActorUser, ID: uuid.New()},
	}); err != nil {
		t.Fatalf("admitCreate() = %v", err)
	}

	// A Run files work only for the member it works for, decided before
	// any database read; where it may file is the delegate rule's, which
	// the integration tests read.
	run := &RunScope{AgentID: uuid.New(), UserID: uuid.New(), RunID: uuid.New()}
	if err := run.admitCreate(ctx, nil, uuid.New(), CreateInput{
		ProjectID: own, Assignee: ActorRef{Kind: ActorUser, ID: uuid.New()},
	}); !errors.Is(err, ErrOutsideRunScope) {
		t.Fatalf("admitCreate(another member) = %v, want ErrOutsideRunScope", err)
	}
	// With nothing to check, a Run asks the database nothing.
	if readable, err := run.ReadableProjects(ctx, nil, uuid.New(), nil); err != nil || len(readable) != 0 {
		t.Fatalf("ReadableProjects(none) = %v, %v", readable, err)
	}
}

// The Planner is the root Agent's Run in its person's direct chat with its
// Company Plan tools on (IA-FR12). It reads every Task its person reads: a
// colleague's Task is admitted, the list keeps the caller's own filters, and
// every dependent stays. The same Agent's Run without the flag reads only
// its own Tasks.
func TestPlannerScopeReadsEveryTaskItsPersonReads(t *testing.T) {
	t.Parallel()

	alice := ActorRef{Kind: ActorUser, ID: uuid.New(), DisplayName: "Alice Member"}
	bob := ActorRef{Kind: ActorUser, ID: uuid.New(), DisplayName: "Bob Member"}
	ada := ActorRef{Kind: ActorAgent, ID: uuid.New(), DisplayName: "Ada"}
	eli := ActorRef{Kind: ActorAgent, ID: uuid.New(), DisplayName: "Eli"}
	planner := &RunScope{AgentID: ada.ID, UserID: alice.ID, Planner: true}
	ownOnly := &RunScope{AgentID: ada.ID, UserID: alice.ID}
	colleagues := Response{Key: "BSC-8", Assignee: bob, ExecutedBy: eli}

	// The Project half of the read is still the delegate rule's (D31), in
	// the database; TestPlannerProofReadsEveryTaskItsPersonReads reads it.
	if !planner.admits(colleagues) {
		t.Fatal("the Planner was refused a colleague's Task")
	}
	if ownOnly.admits(colleagues) {
		t.Fatal("a Run that is not the Planner read a colleague's Task")
	}

	asked := ListOptions{AssigneeUserID: &bob.ID, ExecutedByAgentID: &eli.ID, Statuses: []Status{StatusTodo}}
	if got := planner.listOptions(asked); got.AssigneeUserID != asked.AssigneeUserID ||
		got.ExecutedByAgentID != asked.ExecutedByAgentID || len(got.Statuses) != 1 {
		t.Fatalf("Planner list options = %+v, want the caller's own filters", got)
	}
	if got := planner.listOptions(ListOptions{}); got.AssigneeUserID != nil || got.ExecutedByAgentID != nil {
		t.Fatalf("Planner list was narrowed to its own Tasks: %+v", got)
	}

	set := DependencySet{Blocks: []DependencyResponse{
		{Task: DependencyTask{Assignee: alice, ExecutedBy: ada}},
		{Task: DependencyTask{Assignee: bob, ExecutedBy: eli}},
	}}
	if got := planner.dependencies(set); len(got.Blocks) != 2 {
		t.Fatalf("Planner dependents = %d, want both", len(got.Blocks))
	}
	if got := ownOnly.dependencies(set); len(got.Blocks) != 1 {
		t.Fatalf("own-Tasks dependents = %d, want only the Run's own", len(got.Blocks))
	}
}
