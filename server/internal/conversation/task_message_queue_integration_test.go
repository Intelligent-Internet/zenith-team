//go:build integration

package conversation_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// A colleague's Message in a Task chat waits in the run queue for the
// assignee (ADR 0014). The queue says who wrote it — in the send receipt and
// in the assignee's queue read — so the assignee knows whose words their
// next Run will carry before it starts.
func TestQueuedTaskMessageNamesItsAuthor(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store,
		Bus:   bus,
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
	})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	colleague := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'queue-colleague@example.com', 'Colleague')`,
		colleague)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, colleague)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, colleague)

	// Task A belongs to UserA; the colleague's words are saved for them.
	sent, err := service.Send(ctx,
		auth.UserPrincipal{UserID: colleague},
		conversation.MessageTarget{Kind: conversation.TargetTask, ID: fixture.TaskA},
		conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: "Please include the word TEAL."},
	)
	if err != nil {
		t.Fatalf("send the colleague's Task Message: %v", err)
	}
	if sent.Disposition != conversation.SendDispositionQueued || sent.QueueItem == nil {
		t.Fatalf("colleague's send = %+v, want a queued item", sent)
	}
	want := protocol.ActorRef{ID: colleague, Kind: protocol.ActorKindUser, DisplayName: "Colleague"}
	if sent.QueueItem.InitiatedBy != want {
		t.Fatalf("receipt initiatedBy = %+v, want %+v", sent.QueueItem.InitiatedBy, want)
	}

	queue, err := service.FetchRunQueue(ctx, auth.UserPrincipal{UserID: fixture.UserA}, fixture.ConversationA)
	if err != nil {
		t.Fatalf("FetchRunQueue(assignee) error = %v", err)
	}
	if len(queue.Items) != 1 || queue.Items[0].InitiatedBy != want {
		t.Fatalf("assignee's queue = %+v, want the colleague's item attributed to %+v", queue.Items, want)
	}
}

// The assignee's task_run_requested nudge names the one next step the server
// will accept from where the Task stands, by the same rule task_assigned
// uses: Start where the Run button is offered, a reply in its chat where it
// is not (in progress, in review, failed), and setting the Agent up first
// when it is not on their computer. A colleague may write in all of those
// states, so a nudge that always said "Start it" pointed at a button the
// assignee does not have.
func TestQueuedTaskMessageNudgeNamesANextStepTheServerAccepts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store,
		Bus:   bus,
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{
			Check:    task.CheckMessageRunInTx,
			Prepare:  task.PrepareMessageRunInTx,
			NextStep: task.RunNextStepInTx,
		},
	})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	colleague := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'nudge-colleague@example.com', 'Colleague')`,
		colleague)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, colleague)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, colleague)

	for _, step := range []struct {
		name    string
		status  string
		unbound bool
		want    string
	}{
		{
			name:   "startable",
			status: "todo",
			want:   "Colleague wrote in “Task A”. Start it on your computer: Agent A runs there.",
		},
		{
			name:   "in progress has no start",
			status: "in_progress",
			want:   "Colleague wrote in “Task A”. Reply in its chat to run Agent A on your computer.",
		},
		{
			name:    "the Agent is not on the assignee's computer",
			status:  "in_review",
			unbound: true,
			want:    "Colleague wrote in “Task A”. Set Agent A up on your computer to run it.",
		},
	} {
		dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status=$2 WHERE id=$1`, fixture.TaskA, step.status)
		if step.unbound {
			dbtest.MustExec(t, ctx, pool,
				`DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`,
				fixture.AgentA, fixture.UserA)
		}
		sent, err := service.Send(ctx,
			auth.UserPrincipal{UserID: colleague},
			conversation.MessageTarget{Kind: conversation.TargetTask, ID: fixture.TaskA},
			conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: "Please include the word TEAL."},
		)
		if err != nil {
			t.Fatalf("%s: send the colleague's Task Message: %v", step.name, err)
		}
		if sent.Disposition != conversation.SendDispositionQueued || sent.QueueItem == nil {
			t.Fatalf("%s: colleague's send = %+v, want a queued item", step.name, sent)
		}
		var body string
		if err := pool.QueryRow(ctx, `
			SELECT body FROM inbox_items
			WHERE recipient_user_id=$1 AND kind='task_run_requested' AND entity_id=$2`,
			fixture.UserA, sent.QueueItem.MessageID.String()).Scan(&body); err != nil {
			t.Fatalf("%s: read the assignee's nudge: %v", step.name, err)
		}
		if body != step.want {
			t.Fatalf("%s: nudge body = %q, want %q", step.name, body, step.want)
		}
	}
}

// The assignee's task_run_requested nudge is about one waiting Message. It
// closes whenever that Message stops waiting for them: its writer deletes
// it, the Task is handed to another member, or promotion carries it as the
// writer's own prompt once the writer is the assignee.
func TestQueuedTaskMessageNudgeClosesWhenTheMessageStopsWaiting(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	taskMessageRuns := conversation.TaskMessageRunPreparerFuncs{
		Check:    task.CheckMessageRunInTx,
		Prepare:  task.PrepareMessageRunInTx,
		NextStep: task.RunNextStepInTx,
	}
	service, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: store, Bus: bus, TaskMessageRuns: taskMessageRuns,
	})
	if err != nil {
		t.Fatalf("construct Conversation service: %v", err)
	}
	tasks, err := task.NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("construct Task service: %v", err)
	}
	member := func(email string) uuid.UUID {
		t.Helper()
		id := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool,
			`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`, id, email)
		dbtest.MustExec(t, ctx, pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
			fixture.CompanyA, id)
		// Both work on the fixture Project (ADR 0017): a Task goes only to
		// someone who reads its Project.
		dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, id)
		return id
	}
	writer := member("nudge-writer@example.com")
	carol := member("nudge-carol@example.com")
	send := func(content string) *conversation.RunQueueItem {
		t.Helper()
		sent, err := service.Send(ctx, auth.UserPrincipal{UserID: writer},
			conversation.MessageTarget{Kind: conversation.TargetTask, ID: fixture.TaskA},
			conversation.SendInput{RequestKey: dbtest.NewUUID(t), Content: content})
		if err != nil || sent.QueueItem == nil {
			t.Fatalf("send %q = %+v, %v; want a queued item", content, sent, err)
		}
		return sent.QueueItem
	}
	nudgeOpen := func(recipient uuid.UUID, item *conversation.RunQueueItem) bool {
		t.Helper()
		var open bool
		if err := pool.QueryRow(ctx, `
			SELECT archived_at IS NULL FROM inbox_items
			WHERE recipient_user_id = $1 AND kind = 'task_run_requested' AND entity_id = $2`,
			recipient, item.MessageID.String(),
		).Scan(&open); err != nil {
			t.Fatalf("read the nudge about %s: %v", item.MessageID, err)
		}
		return open
	}
	deleteItem := func(item *conversation.RunQueueItem) {
		t.Helper()
		if _, err := service.DeleteRunQueueItem(
			ctx, auth.UserPrincipal{UserID: writer}, fixture.ConversationA, item.ID,
			conversation.DeleteRunQueueItemInput{ExpectedRevision: item.Revision},
		); err != nil {
			t.Fatalf("DeleteRunQueueItem(%s) error = %v", item.ID, err)
		}
	}

	// The writer takes their Message back: it will never exist.
	first := send("First thought.")
	if !nudgeOpen(fixture.UserA, first) {
		t.Fatalf("nudge about the first Message is closed before anything happened")
	}
	deleteItem(first)
	if nudgeOpen(fixture.UserA, first) {
		t.Fatalf("nudge about a deleted Message is still open")
	}

	// The owner hands the Task to Carol: the owner can no longer start the
	// Run that would carry the Message, which still waits.
	second := send("Second thought.")
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	current, err := tasks.Get(ctx, owner, fixture.TaskA, nil)
	if err != nil {
		t.Fatalf("Get(Task A) error = %v", err)
	}
	if _, err := tasks.Update(ctx, owner, fixture.TaskA, task.UpdateInput{
		ExpectedRevision: current.Revision,
		Assignee:         &task.ActorRef{Kind: task.ActorUser, ID: carol},
		ConfirmHandoff:   true,
	}); err != nil {
		t.Fatalf("Update(hand Task A to Carol) error = %v", err)
	}
	if nudgeOpen(fixture.UserA, second) {
		t.Fatalf("the previous assignee's nudge is still open after the handoff")
	}
	deleteItem(second)

	// Carol has accepted the handoff before colleagues queue more work.
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET assignment_status = 'accepted' WHERE id = $1`, fixture.TaskA)

	// A write that skips the handoff makes the writer the assignee, so a
	// nudge to Carol is still open when promotion carries the Message as the
	// writer's own prompt.
	third := send("Third thought.")
	if !nudgeOpen(carol, third) {
		t.Fatalf("Carol has no open nudge about the third Message")
	}
	runtimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-nudge-writer', 'Writer', 'codex', 'online', $3, now())`,
		runtimeID, fixture.CompanyA, writer)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, writer, runtimeID, "codex")
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET assignee_user_id = $2 WHERE id = $1`, fixture.TaskA, writer)
	var promotion conversation.QueuePromotion
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockConversationForSystemRunQueueMutation(ctx,
			generated.LockConversationForSystemRunQueueMutationParams{
				ConversationID: fixture.ConversationA, CompanyID: fixture.CompanyA,
			}); err != nil {
			return err
		}
		pending := []events.Event{}
		var promoteErr error
		promotion, promoteErr = conversation.PromoteRunQueueInTx(
			ctx, q, &pending, taskMessageRuns, fixture.CompanyA, fixture.ConversationA,
		)
		return promoteErr
	}); err != nil {
		t.Fatalf("PromoteRunQueueInTx() error = %v", err)
	}
	if promotion.RunID == nil {
		t.Fatalf("promotion = %+v, want the writer's prompt promoted", promotion)
	}
	if nudgeOpen(carol, third) {
		t.Fatalf("Carol's nudge is still open after promotion carried the Message")
	}
}
