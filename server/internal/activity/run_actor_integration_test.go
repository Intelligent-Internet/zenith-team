//go:build integration

package activity_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/schedule"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// runActorWorld is the fixture Company with the Activity recorder on its
// bus, the real Run proof resolver, and Eli, an Agent reporting to the root
// Agent that the owner (Schema A) has bound to their
// computer. Every Run here works for the owner.
type runActorWorld struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	fixture dbtest.Fixture
	store   *db.Store
	bus     *events.Bus
	runs    *execution.DefaultRunService
	owner   auth.UserPrincipal
	eli     uuid.UUID
}

func newRunActorWorld(t *testing.T) runActorWorld {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	// The fixture's task_a holds number 1.
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := activity.RegisterListeners(bus, store.Queries()); err != nil {
		t.Fatal(err)
	}
	eli := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(
		t, ctx, pool, eli, fixture.CompanyA, "Eli", "codex", &fixture.AgentA, fixture.UserA, fixture.RuntimeA,
	)
	return runActorWorld{
		ctx: ctx, pool: pool, fixture: fixture, store: store, bus: bus,
		runs: execution.NewRunService(execution.RunServiceOptions{
			Queries: store.Queries(), Transactor: store, Bus: bus,
		}),
		owner: auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		eli: eli,
	}
}

// directChat opens a direct chat between the owner and an Agent.
func (w runActorWorld) directChat(t *testing.T, agentID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := w.pool.QueryRow(w.ctx, `
		INSERT INTO conversations (company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, 'direct', $2, $3, 'Direct') RETURNING id`,
		w.fixture.CompanyA, agentID, w.fixture.UserA,
	).Scan(&id); err != nil {
		t.Fatalf("open a direct chat: %v", err)
	}
	return id
}

// prove starts a live Run of the Agent in the chat, working for the owner,
// mints its proof and resolves it the way every Run route does. A Task Run
// names its Task and the Task's execution generation.
func (w runActorWorld) prove(t *testing.T, agentID, conversationID uuid.UUID, taskID *uuid.UUID) execution.RunOrigin {
	t.Helper()
	var runID uuid.UUID
	if err := w.pool.QueryRow(w.ctx, `
		INSERT INTO runs (company_id, agent_id, task_id, conversation_id, runtime_id,
			execution_authorized_by_user_id, status, run_type, task_execution_generation)
		SELECT $1, $2, $3::uuid, $4, $5, $6, 'running',
			CASE WHEN $3::uuid IS NULL THEN 'direct_chat' ELSE 'task' END,
			(SELECT execution_generation FROM tasks WHERE id = $3::uuid)
		RETURNING id`,
		w.fixture.CompanyA, agentID, taskID, conversationID, w.fixture.RuntimeA, w.fixture.UserA,
	).Scan(&runID); err != nil {
		t.Fatalf("start the Run: %v", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	proof := "brt_" + hex.EncodeToString(secret)
	digest := sha256.Sum256([]byte(proof))
	if _, err := w.store.Queries().CreateRunToken(w.ctx, generated.CreateRunTokenParams{
		RunID: runID, TokenHash: digest[:],
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	origin, err := w.runs.ResolveRunOrigin(w.ctx, proof)
	if err != nil || origin.RunID != runID {
		t.Fatalf("ResolveRunOrigin() = %+v, %v; want Run %s", origin, err, runID)
	}
	return origin
}

// scope is the Task scope the handler builds from a proof (runProofScope).
func scope(origin execution.RunOrigin) *task.RunScope {
	return &task.RunScope{
		AgentID: origin.AgentID, UserID: *origin.AuthorizedByUserID, RunID: origin.RunID,
		TaskID: origin.TaskID, ProjectID: origin.ProjectID,
	}
}

// storedActor is the three identities one Activity row stores.
type storedActor struct {
	Action     string
	ActorType  string
	ActorID    pgtype.Text
	OnBehalfOf uuid.NullUUID
	RunID      uuid.NullUUID
}

// rowsSince returns the rows the Company's Activity gained after the mark,
// and the new mark.
func (w runActorWorld) rowsSince(t *testing.T, mark []uuid.UUID) ([]storedActor, []uuid.UUID) {
	t.Helper()
	rows, err := w.pool.Query(w.ctx, `
		SELECT id, action, actor_type, actor_id, on_behalf_of_user_id, run_id
		FROM activity_events WHERE company_id = $1 ORDER BY created_at, id`, w.fixture.CompanyA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var added []storedActor
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		var row storedActor
		if err := rows.Scan(
			&id, &row.Action, &row.ActorType, &row.ActorID, &row.OnBehalfOf, &row.RunID,
		); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if !slices.Contains(mark, id) {
			added = append(added, row)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return added, ids
}

// assertTheRunWroteThem checks every row is the Agent's, for the owner, in
// the Run, and that the rows hold each wanted action.
func (w runActorWorld) assertTheRunWroteThem(
	t *testing.T,
	rows []storedActor,
	origin execution.RunOrigin,
	actions ...string,
) {
	t.Helper()
	var written []string
	for _, row := range rows {
		written = append(written, row.Action)
		if row.ActorType != "agent" || row.ActorID.String != origin.AgentID.String() ||
			row.OnBehalfOf != (uuid.NullUUID{UUID: w.fixture.UserA, Valid: true}) ||
			row.RunID != (uuid.NullUUID{UUID: origin.RunID, Valid: true}) {
			t.Errorf("%s stored actor %s %s for %v in Run %v; want Agent %s for %s in Run %s",
				row.Action, row.ActorType, row.ActorID.String, row.OnBehalfOf, row.RunID,
				origin.AgentID, w.fixture.UserA, origin.RunID)
		}
	}
	for _, action := range actions {
		if !slices.Contains(written, action) {
			t.Errorf("the Run's rows %v do not include %s", written, action)
		}
	}
}

// A Question asked in Eli's Run for the owner stores Eli as
// the actor, the owner and the Run, and the feed and its frame read them
// back as "Eli for Schema A" with the Run.
func TestQuestionAskedInARunStoresTheAgentThePersonAndTheRun(t *testing.T) {
	t.Parallel()
	w := newRunActorWorld(t)
	origin := w.prove(t, w.eli, w.directChat(t, w.eli), nil)
	questions, err := approval.NewQuestionService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	var frames []activity.Entry
	if err := w.bus.Subscribe(activity.Topic, func(_ context.Context, e events.Event) error {
		entry, err := events.PayloadOf[activity.Entry](e)
		frames = append(frames, entry)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, mark := w.rowsSince(t, nil)

	if _, _, err := questions.Ask(w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()), approval.QuestionAskInput{
		Title: "Choose a target",
		Prompts: []approval.QuestionPrompt{{
			ID: "target", Prompt: "Which target?", SelectionMode: approval.QuestionSelectionSingle,
			Required: true,
			Options:  []approval.QuestionOption{{ID: "a", Label: "Target A"}, {ID: "b", Label: "Target B"}},
		}},
	}); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	rows, _ := w.rowsSince(t, mark)
	w.assertTheRunWroteThem(t, rows, origin, "question.asked")

	entries, _, err := activity.NewFeed(w.store.Queries()).List(w.ctx, w.fixture.CompanyA, activity.ListOptions{
		UserID: w.fixture.UserA, Action: "question.asked", Limit: 10,
	})
	if err != nil || len(entries) != 1 {
		t.Fatalf("feed question.asked = %+v, %v; want one entry", entries, err)
	}
	for name, entry := range map[string]activity.Entry{"feed": entries[0], "frame": lastFrame(t, frames, "question.asked")} {
		if entry.Actor.ID == nil || *entry.Actor.ID != w.eli.String() || entry.Actor.Kind != "agent" ||
			entry.Actor.DisplayName == nil || *entry.Actor.DisplayName != "Eli" ||
			entry.OnBehalfOf == nil || entry.OnBehalfOf.ID != w.fixture.UserA.String() ||
			entry.OnBehalfOf.DisplayName == nil || *entry.OnBehalfOf.DisplayName != "Schema A" ||
			entry.RunID == nil || *entry.RunID != origin.RunID.String() {
			t.Fatalf("%s entry = actor %+v, onBehalfOf %+v, runId %v; want Eli for Schema A in Run %s",
				name, entry.Actor, entry.OnBehalfOf, entry.RunID, origin.RunID)
		}
	}
	var details struct {
		Context map[string]any `json:"context"`
	}
	if err := json.Unmarshal(entries[0].Details, &details); err != nil ||
		details.Context["onBehalfOfDisplayName"] != "Schema A" {
		t.Fatalf("stored context = %+v, %v; want the person's name frozen", details.Context, err)
	}
}

func lastFrame(t *testing.T, frames []activity.Entry, action string) activity.Entry {
	t.Helper()
	for index := len(frames) - 1; index >= 0; index-- {
		if frames[index].Action == action {
			return frames[index]
		}
	}
	t.Fatalf("no activity.created frame for %s among %d", action, len(frames))
	return activity.Entry{}
}

// A Task Eli's Run files for the owner stores the
// Run's three identities on every row, names both creators, and its chat
// still runs with the Company's root Agent, not with Eli. A Task Eli is to
// do starts its Run at once with its files, and that start, the files
// message and the chat bump are the Run's too. The owner's
// own create names only the owner.
func TestTaskCreatedInARunNamesBothCreatorsAndTheRun(t *testing.T) {
	t.Parallel()
	w := newRunActorWorld(t)
	origin := w.prove(t, w.eli, w.directChat(t, w.eli), nil)
	tasks, err := task.NewTaskService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, mark := w.rowsSince(t, nil)

	human, err := tasks.Create(w.ctx, w.owner, task.CreateInput{
		ProjectID: w.fixture.ProjectA, Key: "call_the_bank", Title: "Call the bank",
		MilestoneType: task.MilestoneTypeProduct, Status: task.StatusTodo, Run: scope(origin),
	})
	if err != nil {
		t.Fatalf("Create(human Task from Eli's Run) error = %v", err)
	}
	rows, mark := w.rowsSince(t, mark)
	w.assertTheRunWroteThem(t, rows, origin, "task.created", "conversation.created")
	var creatorUser, creatorAgent uuid.NullUUID
	var chatAgent uuid.UUID
	if err := w.pool.QueryRow(w.ctx, `
		SELECT tasks.creator_user_id, tasks.creator_agent_id, agents.agent_id
		FROM tasks JOIN task_run_agents AS agents ON agents.task_id = tasks.id
		WHERE tasks.id = $1`, human.Task.ID).Scan(&creatorUser, &creatorAgent, &chatAgent); err != nil {
		t.Fatal(err)
	}
	if creatorUser.UUID != w.fixture.UserA || creatorAgent.UUID != w.eli || chatAgent != w.fixture.AgentA {
		t.Fatalf("Task creators %v and %v, chat Agent %s; want the owner and Eli, chat with the root Agent %s",
			creatorUser, creatorAgent, chatAgent, w.fixture.AgentA)
	}
	if human.Task.Creator.Kind != protocol.ActorKindUser || human.Task.Creator.ID != w.fixture.UserA {
		t.Fatalf("Task shows creator %+v; want the owner", human.Task.Creator)
	}

	// The Task comes with a file the owner uploaded, so its chat is also
	// bumped by the files message.
	file := dbtest.NewUUID(t)
	dbtest.MustExec(t, w.ctx, w.pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key, filename,
			content_type, size_bytes, status, version
		)
		VALUES ($1, $2, $3, $4, 'brief.pdf', 'application/pdf', 42, 'ready', 2)`,
		file, w.fixture.CompanyA, w.fixture.UserA, dbtest.NewUUID(t))
	executor := task.ActorRef{Kind: task.ActorAgent, ID: w.eli}
	started, err := tasks.Create(w.ctx, w.owner, task.CreateInput{
		ProjectID: w.fixture.ProjectA, Key: "draft_the_memo", Title: "Draft the memo",
		MilestoneType: task.MilestoneTypeProduct, Status: task.StatusTodo, ExecutedBy: &executor,
		AttachmentIDs: []uuid.UUID{file}, Run: scope(origin),
	})
	if err != nil || started.RunID == nil {
		t.Fatalf("Create(Eli's Task from Eli's Run) = %+v, %v; want its Run started", started, err)
	}
	rows, mark = w.rowsSince(t, mark)
	w.assertTheRunWroteThem(t, rows, origin,
		"task.created", "conversation.created", "run.queued", "message.created", "conversation.updated")

	at := time.Now().UTC().Add(time.Hour).Truncate(time.Minute)
	if _, err := tasks.Create(w.ctx, w.owner, task.CreateInput{
		ProjectID: w.fixture.ProjectA, Title: "Scheduled from Eli's Run", ExecutedBy: &executor,
		MilestoneType: task.MilestoneTypeProduct, Status: task.StatusTodo, Run: scope(origin),
		Schedule: &schedule.Trigger{Type: schedule.TriggerTypeOnce, RunAt: &at},
	}); err != nil {
		t.Fatal(err)
	}
	rows, mark = w.rowsSince(t, mark)
	w.assertTheRunWroteThem(t, rows, origin, "task.created", "task.updated", "conversation.created")

	if _, err := tasks.Create(w.ctx, w.owner, task.CreateInput{
		ProjectID: w.fixture.ProjectA, Key: "own_task", Title: "Own Task",
		MilestoneType: task.MilestoneTypeProduct, Status: task.StatusTodo,
	}); err != nil {
		t.Fatalf("Create(the owner's own Task) error = %v", err)
	}
	rows, _ = w.rowsSince(t, mark)
	if len(rows) == 0 {
		t.Fatal("the owner's own create wrote no Activity")
	}
	for _, row := range rows {
		if row.ActorType != "user" || row.ActorID.String != w.fixture.UserA.String() ||
			row.OnBehalfOf.Valid || row.RunID.Valid {
			t.Fatalf("own %s stored %+v; want the owner alone", row.Action, row)
		}
	}
}

// startEliTask makes task_a Eli's work, in progress, so a Task Run of Eli
// can raise a plan in its chat and declare it failed.
func (w runActorWorld) startEliTask(t *testing.T) execution.RunOrigin {
	t.Helper()
	dbtest.MustExec(t, w.ctx, w.pool, `
		UPDATE tasks SET executed_by_agent_id = $2, status = 'in_progress', completed_at = NULL,
			execution_generation = 3, updated_at = clock_timestamp()
		WHERE id = $1`, w.fixture.TaskA, w.eli)
	return w.prove(t, w.eli, w.fixture.ConversationA, &w.fixture.TaskA)
}

// Eli's Task Run fails its Task. The Task change and the
// report to Eli's manager are the Run's, for the owner.
func TestTaskFailedInARunStoresTheRun(t *testing.T) {
	t.Parallel()
	w := newRunActorWorld(t)
	origin := w.startEliTask(t)
	tasks, err := task.NewTaskService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, mark := w.rowsSince(t, nil)

	if _, err := tasks.MarkFailed(w.ctx, w.owner, origin, task.FailureInput{
		RequestKey: uuid.Must(uuid.NewV7()), Reason: "The supplier will not ship.",
	}); err != nil {
		t.Fatalf("MarkFailed() error = %v", err)
	}
	rows, _ := w.rowsSince(t, mark)
	w.assertTheRunWroteThem(t, rows, origin, "task.updated", "task.report_ready")
}

// A Company Plan Eli's Task Run raises is the Run's, for the owner.
func TestCompanyPlanRaisedInARunStoresTheRun(t *testing.T) {
	t.Parallel()
	w := newRunActorWorld(t)
	origin := w.startEliTask(t)
	plans, err := company.NewPlanService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := approval.NewApprovalService(w.store, w.bus, plans, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, mark := w.rowsSince(t, nil)

	if _, _, err := approvals.RequestCompanyPlanApply(
		w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()), w.plan(t, "work", "Work"),
	); err != nil {
		t.Fatalf("RequestCompanyPlanApply() error = %v", err)
	}
	rows, _ := w.rowsSince(t, mark)
	w.assertTheRunWroteThem(t, rows, origin, "approval.requested")
}

// In Proactive Mode the root Agent's Run applies the plan it
// raises at once. The Approval's resolution, the new Project, its grant, the
// new Task and its chat are all the Run's, for the owner, and the new Task
// names the owner and the root Agent as its creators.
func TestCompanyPlanAppliedByARunInProactiveModeStoresTheRun(t *testing.T) {
	t.Parallel()
	w := newRunActorWorld(t)
	dbtest.MustExec(t, w.ctx, w.pool, `
		UPDATE tasks SET status = 'done', completed_at = clock_timestamp() WHERE id = $1`, w.fixture.TaskA)
	dbtest.MustExec(t, w.ctx, w.pool, `
		UPDATE companies SET proactive_mode_enabled = true, proactive_mode_enabled_by_user_id = $2,
			proactive_mode_conversation_id = $3
		WHERE id = $1`, w.fixture.CompanyA, w.fixture.UserA, w.fixture.DirectConversationA)
	origin := w.prove(t, w.fixture.AgentA, w.fixture.DirectConversationA, nil)
	plans, err := company.NewPlanService(w.store, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := approval.NewApprovalService(w.store, w.bus, plans, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, mark := w.rowsSince(t, nil)

	applied, _, err := approvals.RequestCompanyPlanApply(
		w.ctx, w.owner, origin, uuid.Must(uuid.NewV7()), w.plan(t, "launch", "Launch"),
	)
	if err != nil || applied.Execution == nil || applied.Execution.Status != approval.ApprovalExecutionExecuted {
		t.Fatalf("RequestCompanyPlanApply(Proactive Mode) = %+v, %v; want it applied", applied, err)
	}
	rows, _ := w.rowsSince(t, mark)
	w.assertTheRunWroteThem(t, rows, origin,
		"approval.resolved", "project.created", "project_grant.changed", "task.created", "conversation.created")
	var creatorUser, creatorAgent uuid.NullUUID
	if err := w.pool.QueryRow(w.ctx, `SELECT creator_user_id, creator_agent_id FROM tasks
		WHERE company_id = $1 AND key = 'follow_up'`, w.fixture.CompanyA).Scan(&creatorUser, &creatorAgent); err != nil {
		t.Fatal(err)
	}
	if creatorUser.UUID != w.fixture.UserA || creatorAgent.UUID != w.fixture.AgentA {
		t.Fatalf(
			"planned Task creators %v and %v; want the owner and the root Agent",
			creatorUser,
			creatorAgent,
		)
	}
}

// plan is a Company Plan that adds the follow_up Task to the Project, and
// creates the Project when it is new.
func (w runActorWorld) plan(t *testing.T, projectKey, projectTitle string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(company.PlanArtifact{
		ArtifactVersion: 1, CompanyID: w.fixture.CompanyA.String(), CompanySlug: "schema-a",
		CreatedAt: time.Now().UTC(),
		Desired: company.PlanDocument{
			FormatVersion: 5, Company: "schema-a",
			Projects: []company.PlanProject{{Key: projectKey, Title: projectTitle}},
			Tasks: []company.PlanTask{{
				Key: "follow_up", ProjectKey: projectKey, MilestoneType: task.MilestoneTypeProduct,
				Title: "Follow up", Status: "todo", Priority: "medium",
				DependsOn: []company.PlanTaskDependency{},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
