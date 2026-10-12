//go:build integration

package task

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// A human Task's chat runs its assisting Agent on the assignee's computer
// (task_run_agents), so starting it, and a message that would start its Run,
// need the assignee's project.tasks.execute like any Task Run (ADR 0017). The
// refusal names that Agent, never the member doing the work, and nothing is
// enqueued.
func TestHumanTaskRunNeedsProjectTasksExecute(t *testing.T) {
	ctx, pool, fixture, service, owner := startTaskHarness(t)
	memberID, member := seedProjectOutsider(t, ctx, pool, fixture, "member@example.com", auth.CompanyRoleMember)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID, "project.tasks.write")
	runtime := seedTaskRuntime(t, ctx, pool, fixture, "daemon-member", memberID)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, memberID, runtime, "codex")
	self := ActorRef{Kind: ActorUser, ID: memberID}
	created, err := service.Create(ctx, owner, CreateInput{
		ProjectID: fixture.ProjectA, Key: "human_work", Title: "Confirm scope",
		MilestoneType: MilestoneTypeProduct, Status: StatusTodo, Assignee: self, ExecutedBy: &self,
	})
	if err != nil || created.RunID != nil || created.Task.ExecutedBy.Kind != ActorUser {
		t.Fatalf("Create(human Task for member) = %+v, %v", created, err)
	}
	countRuns := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE task_id = $1`, created.Task.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	assertRefusal := func(what string, err error) {
		t.Helper()
		var refusal *execution.ExecutePermissionError
		if !errors.As(err, &refusal) || refusal.AgentName != "Agent A" || refusal.AssigneeID != memberID {
			t.Fatalf("%s error = %v (%+v), want the execute refusal naming the assisting Agent A", what, err, refusal)
		}
		if count := countRuns(); count != 0 {
			t.Fatalf("%s enqueued %d Run(s)", what, count)
		}
	}

	_, err = service.StartExecution(ctx, member, created.Task.ID, StartExecutionInput{ExpectedRevision: created.Task.Revision})
	assertRefusal("StartExecution()", err)

	chat, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: service.store, Bus: newTestBus(t),
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check: CheckMessageRunInTx, Prepare: PrepareMessageRunInTx, NextStep: RunNextStepInTx,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = chat.Send(ctx, member, conversation.MessageTarget{Kind: conversation.TargetTask, ID: created.Task.ID},
		conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: "Here is what I decided."})
	assertRefusal("Send(assignee's message)", err)

	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID, "project.tasks.write", "project.tasks.execute")
	started, err := service.StartExecution(ctx, member, created.Task.ID, StartExecutionInput{ExpectedRevision: created.Task.Revision})
	if err != nil || started.RunID == nil {
		t.Fatalf("StartExecution() with execute = %+v, %v; want a Run", started.RunID, err)
	}
	run, err := service.store.Queries().GetRun(ctx, *started.RunID)
	if err != nil || run.AgentID != fixture.AgentA || run.ExecutionAuthorizedByUserID.UUID != memberID {
		t.Fatalf("human Task Run = %+v, %v; want Agent A on the member's computer", run, err)
	}
}

func TestHumanTaskRunsWithAnAgentAndKeepsItsHumanExecutor(t *testing.T) {
	t.Parallel()
	for _, creatorAgent := range []bool{false, true} {
		t.Run(map[bool]string{false: "CEO fallback", true: "creating Agent"}[creatorAgent], func(t *testing.T) {
			t.Parallel()
			ctx, pool, fixture, service, principal := startTaskHarness(t)
			dbtest.MustExec(t, ctx, pool, `UPDATE companies SET proactive_mode_enabled=true,
				proactive_mode_enabled_by_user_id=$2, proactive_mode_conversation_id=$3 WHERE id=$1`,
				fixture.CompanyA, fixture.UserA, fixture.DirectConversationA)
			created, err := service.Create(ctx, principal, CreateInput{
				ProjectID: fixture.ProjectA, MilestoneType: MilestoneTypeProduct,
				Title: "Confirm product scope", Description: "Discuss and record the member's decisions.",
			})
			if err != nil {
				t.Fatal(err)
			}
			wantAgent := fixture.AgentA
			if creatorAgent {
				wantAgent = dbtest.NewUUID(t)
				dbtest.SeedAgent(t, ctx, pool, wantAgent, fixture.CompanyA, "Product Agent", "codex", &fixture.AgentA)
				dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, wantAgent, fixture.UserA, fixture.RuntimeA, "codex")
				dbtest.MustExec(t, ctx, pool,
					`UPDATE tasks SET creator_user_id=NULL, creator_agent_id=$2 WHERE id=$1`, created.Task.ID, wantAgent)
			}
			colleague := seedTaskMember(t, ctx, pool, fixture, "colleague@example.com")
			if _, err := service.StartExecution(ctx, auth.UserPrincipal{
				UserID: colleague, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
			}, created.Task.ID, StartExecutionInput{ExpectedRevision: created.Task.Revision}); !errors.Is(err, ErrAssigneeRequired) {
				t.Fatalf("colleague start = %v, want ErrAssigneeRequired", err)
			}
			started, err := service.StartExecution(ctx, principal, created.Task.ID,
				StartExecutionInput{ExpectedRevision: created.Task.Revision})
			if err != nil {
				t.Fatalf("start human Task: %v", err)
			}
			if started.RunID == nil || started.Task.Status != StatusInProgress || started.Task.ExecutedBy.Kind != ActorUser {
				t.Fatalf("started human Task = %+v", started)
			}
			run, err := service.store.Queries().GetRun(ctx, *started.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if run.AgentID != wantAgent || run.ExecutionAuthorizedByUserID.UUID != fixture.UserA || run.RuntimeID != fixture.RuntimeA {
				t.Fatalf("Run Agent/authorizer/runtime = %s/%s/%s", run.AgentID, run.ExecutionAuthorizedByUserID.UUID, run.RuntimeID)
			}
			var humanWork bool
			if err := pool.QueryRow(ctx, `SELECT (context->'executionProfile'->'task'->>'humanWork')::boolean FROM runs WHERE id=$1`, run.ID).Scan(&humanWork); err != nil || !humanWork {
				t.Fatalf("frozen human work = %v, %v", humanWork, err)
			}
			if _, err := service.Get(ctx, principal, created.Task.ID, &RunScope{
				AgentID: wantAgent, UserID: fixture.UserA, RunID: run.ID, TaskID: &created.Task.ID,
			}); err != nil {
				t.Fatalf("assisting Agent reads its Task: %v", err)
			}
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, run.ID)
			run.Status = "completed"
			var pending []events.Event
			if err := service.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
				return ApplyRunTerminalInTx(ctx, q, &pending, run)
			}); err != nil {
				t.Fatal(err)
			}
			review, err := service.Get(ctx, principal, created.Task.ID, nil)
			if err != nil || review.Status != StatusInReview || review.ExecutedBy.Kind != ActorUser {
				t.Fatalf("human Task after completion = %+v, %v", review, err)
			}
			var mergeJobs int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM task_merge_jobs WHERE task_id=$1`, review.ID).Scan(&mergeJobs); err != nil || mergeJobs != 0 {
				t.Fatalf("human Task proactive merge jobs = %d, %v, want zero", mergeJobs, err)
			}
			if _, err := service.store.Queries().InsertNextProactiveTaskMergeJob(ctx, auth.RolesWith(auth.PermissionCompanyProactiveModeManage)); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("human Task background merge = %v, want no job", err)
			}
			chat, err := conversation.NewConversationService(conversation.ServiceOptions{
				Store: service.store, Bus: newTestBus(t),
				TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
					Check: CheckMessageRunInTx, Prepare: PrepareMessageRunInTx, NextStep: RunNextStepInTx,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			sent, err := chat.Send(ctx, principal, conversation.MessageTarget{Kind: conversation.TargetTask, ID: created.Task.ID},
				conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: "Let's revise the payment model."})
			if err != nil || sent.RunID == nil {
				t.Fatalf("reply in human Task chat = %+v, %v", sent, err)
			}
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, *sent.RunID)
			replyRun, err := service.store.Queries().GetRun(ctx, *sent.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
				return ApplyRunTerminalInTx(ctx, q, &pending, replyRun)
			}); err != nil {
				t.Fatal(err)
			}
			review, err = service.Get(ctx, principal, created.Task.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			done := StatusDone
			completed, err := service.Update(ctx, principal, review.ID, UpdateInput{ExpectedRevision: review.Revision, Status: &done})
			if err != nil || completed.Task.Status != StatusDone || completed.Task.ExecutedBy.Kind != ActorUser {
				t.Fatalf("human marks reviewed Task done = %+v, %v", completed, err)
			}
		})
	}
}
