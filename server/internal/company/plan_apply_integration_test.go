//go:build integration

package company

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/task"
	taskdomain "boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestApplyCompanyPlanDeletesOmittedEntitiesAndRetainsExecutionHistory(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	// A retained pre-cutover Run has no provable source revision.
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET revision=NULL,context='{}' WHERE id=$1`, fixture.RunA)
	conversationChanges := recordTopic(t, bus, conversation.Topic)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO run_usage (run_id, provider, model, raw_json)
		VALUES ($1, 'codex', 'gpt-test', '{}')`, fixture.RunA)

	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	if err := store.WithinTx(ctx,
		pgx.TxOptions{},
		func(q *generated.Queries) error {
			return activity.BaselineInTx(ctx,
				q)
		}); err != nil {
		t.Fatal(err)
	}
	var retainedRun generated.Run
	var pending []events.Event
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		retainedRun,
			err = execution.EnqueueRunInTx(ctx,
			q,
			&pending,
			execution.RunTrigger{
				CompanyID:      fixture.CompanyA,
				ConversationID: fixture.ConversationA,
				AgentID:        fixture.AgentA,
				TaskID: uuid.NullUUID{
					UUID:  fixture.TaskA,
					Valid: true,
				},
				Source:             execution.RunSourceMessage,
				ActorUserID:        fixture.UserA,
				AuthorizedByUserID: fixture.UserA,
			})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runService := execution.NewRunService(execution.RunServiceOptions{
		Queries:       store.Queries(),
		Transactor:    store,
		Bus:           bus,
		RouteResolver: conversation.ResolveConversationRoute,
	})
	if _, err := runService.CancelRun(ctx, retainedRun.ID, "Finish retained execution"); err != nil {
		t.Fatal(err)
	}
	tasks, err := taskdomain.NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	renamedTitle := "Renamed before deletion"
	if _, err := tasks.Update(ctx,
		principal,
		fixture.TaskA,
		taskdomain.UpdateInput{
			ExpectedRevision: 1,
			Title:            &renamedTitle,
		}); err != nil {
		t.Fatal(err)
	}
	desired := PlanDocument{Projects: []PlanProject{{Key: "work", Title: "Work"}}, FormatVersion: planFormatVersion, Company: "schema-a", Tasks: []PlanTask{}}
	result, err := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, PlanArtifact{
		ArtifactVersion: 1,
		CompanyID:       fixture.CompanyA.String(),
		CompanySlug:     "schema-a",
		CreatedAt:       time.Now().UTC(),
		Base: PlanBase{
			Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
			Tasks: []PlanBaseEntity{{
				ID: fixture.TaskA.String(), Key: "task_a", Revision: 2,
				ConversationID: fixture.ConversationA.String(),
			}},
			Dependencies: []PlanBaseDependency{},
		},
		Desired: desired,
	})
	if err != nil {
		t.Fatalf("ApplyCompanyPlan() error = %v", err)
	}
	if countsOf(result) != (planCounts{Deleted: 1}) {
		t.Fatalf("ApplyCompanyPlan() = %+v, want one deleted Task", result)
	}
	updates := conversationChanges.named(conversation.EventConversationUpdated)
	if len(updates) != 1 {
		t.Fatalf("detached Conversation updates = %d, want 1", len(updates))
	}
	detached := payloadOf[conversation.Snapshot](t, updates[0])
	if detached.Conversation.TaskID != nil || detached.Conversation.Revision != 2 {
		t.Fatalf("detached Conversation projection = %+v", detached)
	}

	var taskCount, historicalConversationCount, messageCount, historicalRunCount, usageCount int
	if err := pool.QueryRow(
		ctx, `
		SELECT
			(SELECT count(*) FROM tasks WHERE id = $1),
			(SELECT count(*) FROM conversations WHERE id = $2 AND task_id IS NULL),
			(SELECT count(*) FROM conversation_items WHERE id = $3),
			(SELECT count(*) FROM runs WHERE id = $4 AND task_id IS NULL),
			(SELECT count(*) FROM run_usage WHERE run_id = $4)`,
		fixture.TaskA, fixture.ConversationA, fixture.MessageA, fixture.RunA,
	).Scan(
		&taskCount,
		&historicalConversationCount,
		&messageCount,
		&historicalRunCount,
		&usageCount,
	); err != nil {
		t.Fatalf("count retained Task execution history: %v", err)
	}
	if taskCount != 0 || historicalConversationCount != 1 || messageCount != 1 ||
		historicalRunCount != 1 || usageCount != 1 {
		t.Fatalf(
			"Task history counts = task:%d conversation:%d message:%d run:%d usage:%d, want 0/1/1/1/1",
			taskCount,
			historicalConversationCount,
			messageCount,
			historicalRunCount,
			usageCount,
		)
	}

	feed := activity.NewFeed(store.Queries())
	upper := int64(4)
	var cursor *activity.Cursor
	for _, revision := range []int64{3, 2, 1} {
		rows,
			next, err := feed.List(ctx,
			fixture.CompanyA,
			activity.ListOptions{
				UserID:         fixture.UserA,
				EntityType:     "task",
				EntityID:       fixture.TaskA.String(),
				BeforeRevision: &upper,
				Before:         cursor,
				Limit:          1,
			})
		if err != nil || len(rows) != 1 || rows[0].Revision == nil || *rows[0].Revision != revision {
			t.Fatalf("retained Task revision %d = %d rows, %v", revision, len(rows), err)
		}
		entry := rows[0]
		var details struct {
			Before map[string]any
			After  map[string]any
		}
		if err := json.Unmarshal(entry.Details, &details); err != nil {
			t.Fatal(err)
		}
		switch revision {
		case 3:
			if entry.Action != "task.deleted" ||
				details.After != nil ||
				details.Before["revision"] != float64(2) ||
				entry.Entity.Title == nil ||
				*entry.Entity.Title != renamedTitle {
				t.Fatalf("Task tombstone = %s", entry.Details)
			}
		case 2:
			if entry.Action != "task.updated" || details.Before["title"] != "Task A" ||
				details.After["title"] != renamedTitle {
				t.Fatalf("retained rename = %s", entry.Details)
			}
		case 1:
			if entry.Action != "task.history_baselined" ||
				entry.Actor.Kind != "system" ||
				details.Before != nil ||
				details.After["title"] != "Task A" ||
				entry.Entity.Title == nil ||
				*entry.Entity.Title != "Task A" {
				t.Fatalf("retained baseline = %s", entry.Details)
			}
		}
		exact,
			_, err := feed.List(ctx,
			fixture.CompanyA,
			activity.ListOptions{
				UserID:     fixture.UserA,
				EntityType: "task",
				EntityID:   fixture.TaskA.String(),
				Revision:   &revision,
				Limit:      10,
			})
		if err != nil || len(exact) != 1 || exact[0].ID != entry.ID {
			t.Fatalf("exact retained revision = %d, %v", len(exact), err)
		}
		cursor = next
	}
	if cursor != nil {
		t.Fatal("unexpected retained revision continuation")
	}
	detachedRevision := int64(2)
	rows,
		_, err := feed.List(ctx,
		fixture.CompanyA,
		activity.ListOptions{
			UserID:     fixture.UserA,
			EntityType: "conversation",
			EntityID:   fixture.ConversationA.String(),
			Revision:   &detachedRevision,
			Limit:      10,
		})
	if err != nil || len(rows) != 1 {
		t.Fatalf("Conversation detachment history = %d, %v", len(rows), err)
	}
	var details struct {
		Before map[string]any
		After  map[string]any
	}
	if err := json.Unmarshal(rows[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	if details.Before["taskId"] != fixture.TaskA.String() {
		t.Fatalf("detachment before = %s", rows[0].Details)
	}
	if value, ok := details.After["taskId"]; !ok || value != nil {
		t.Fatalf("detachment after = %s", rows[0].Details)
	}
	retained, err := runService.Get(ctx, principal, retainedRun.ID)
	if err != nil ||
		retained.TaskID != nil ||
		retained.RunType != "task" ||
		retained.Revision == nil ||
		*retained.Revision != 1 ||
		retained.Source == nil {
		t.Fatalf("retained frozen Run = %+v, %v", retained, err)
	}
	var frozenSource struct {
		ID    uuid.UUID
		Title string
	}
	if err := json.Unmarshal(retained.Source, &frozenSource); err != nil {
		t.Fatal(err)
	}
	if frozenSource.ID != fixture.TaskA || frozenSource.Title != "Task A" {
		t.Fatalf("retained source labels = %+v", frozenSource)
	}
	page, err := runService.List(ctx,
		principal,
		execution.ListOptions{
			TaskID:   &fixture.TaskA,
			AgentID:  &fixture.AgentA,
			RunType:  "task",
			Revision: retained.Revision,
			Limit:    10,
		})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != retainedRun.ID {
		t.Fatalf("deleted Task's Agent Run history = %+v, %v", page, err)
	}
	legacy, err := runService.Get(ctx, principal, fixture.RunA)
	if err != nil ||
		legacy.Revision != nil ||
		string(legacy.Source) != "null" &&
			legacy.Source != nil ||
		legacy.RunType != "task" {
		t.Fatalf("unknown legacy provenance = %+v, %v", legacy, err)
	}
}

func TestApplyCompanyPlanRecordsFirstApplicationWhenTasksAlreadyExist(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}

	desired := PlanDocument{Projects: []PlanProject{{Key: "work", Title: "Work"}},
		FormatVersion: planFormatVersion,
		Company:       "schema-a",
		Tasks: []PlanTask{
			{ProjectKey: "work",
				Key:           "task_a",
				Title:         "Task A",
				Status:        "backlog",
				Priority:      "medium",
				AgentID:       fixture.AgentA.String(),
				MilestoneType: task.MilestoneTypeProduct, DependsOn: []PlanTaskDependency{},
			},
		},
	}

	result, err := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, PlanArtifact{
		ArtifactVersion: 1,
		CompanyID:       fixture.CompanyA.String(),
		CompanySlug:     "schema-a",
		CreatedAt:       time.Now().UTC(),
		Base: PlanBase{
			Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
			Tasks: []PlanBaseEntity{
				{
					ID:             fixture.TaskA.String(),
					Key:            "task_a",
					Revision:       1,
					ConversationID: fixture.ConversationA.String(),
				},
			},
		},
		Desired: desired,
	})
	if err != nil {
		t.Fatalf("ApplyCompanyPlan() error = %v", err)
	}
	if countsOf(result) != (planCounts{}) {
		t.Fatalf("ApplyCompanyPlan() = %+v, want no entity mutations", result)
	}

	var firstPlanAppliedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT first_plan_applied_at
		FROM companies
		WHERE id = $1`, fixture.CompanyA).Scan(&firstPlanAppliedAt); err != nil {
		t.Fatalf("read first applied Plan stamp: %v", err)
	}
	if firstPlanAppliedAt.IsZero() {
		t.Fatalf("first applied Plan stamp = %v, want a timestamp", firstPlanAppliedAt)
	}
}

// The wire contract marks conversationId optional on a base entity, so a
// complete base that omits it is current, not stale.
func TestApplyCompanyPlanAcceptsBaseWithoutConversationID(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}

	result, err := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, PlanArtifact{
		ArtifactVersion: 1,
		CompanyID:       fixture.CompanyA.String(),
		CompanySlug:     "schema-a",
		CreatedAt:       time.Now().UTC(),
		Base: PlanBase{
			Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
			Tasks:    []PlanBaseEntity{{ID: fixture.TaskA.String(), Key: "task_a", Revision: 1}},
		},
		Desired: PlanDocument{
			Projects:      []PlanProject{{Key: "work", Title: "Work"}},
			FormatVersion: planFormatVersion,
			Company:       "schema-a",
			Tasks: []PlanTask{
				{
					ProjectKey:    "work",
					Key:           "task_a",
					Title:         "Task A",
					Status:        "backlog",
					Priority:      "medium",
					AgentID:       fixture.AgentA.String(),
					MilestoneType: task.MilestoneTypeProduct,
					DependsOn:     []PlanTaskDependency{},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ApplyCompanyPlan() error = %v, want the base accepted without conversationId", err)
	}
	if countsOf(result) != (planCounts{}) {
		t.Fatalf("ApplyCompanyPlan() = %+v, want no entity mutations", result)
	}
}

func TestApplyCompanyPlanResolvesAgentAndReassignsTask(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	engineeringAgentID := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, ctx, pool, engineeringAgentID, fixture.CompanyA,
		"Engineering", "codex", &fixture.AgentA, fixture.UserA, fixture.RuntimeA)

	desired := PlanDocument{Projects: []PlanProject{{Key: "work", Title: "Work"}},
		FormatVersion: planFormatVersion,
		Company:       "schema-a",
		Tasks: []PlanTask{{ProjectKey: "work",
			Key: "task_a", Title: "Task A", Status: "backlog", Priority: "medium",
			Description: "Deliver the launch task.",
			AgentID:     engineeringAgentID.String(), MilestoneType: taskdomain.MilestoneTypeProduct, DependsOn: []PlanTaskDependency{},
		}},
	}
	result, err := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, PlanArtifact{
		ArtifactVersion: 1,
		CompanyID:       fixture.CompanyA.String(),
		CompanySlug:     "schema-a",
		CreatedAt:       time.Now().UTC(),
		Base: PlanBase{
			Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
			Tasks: []PlanBaseEntity{{
				ID: fixture.TaskA.String(), Key: "task_a", Revision: 1,
				ConversationID: fixture.ConversationA.String(),
			}},
		},
		Desired: desired,
	})
	if err != nil {
		t.Fatalf("ApplyCompanyPlan() error = %v", err)
	}
	if result.Created != 0 || result.Updated != 1 {
		t.Fatalf("ApplyCompanyPlan() = %+v, want one Task update", result)
	}
	var executedByAgentID, assigneeUserID uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT executed_by_agent_id, assignee_user_id FROM tasks WHERE id=$1`, fixture.TaskA,
	).Scan(&executedByAgentID, &assigneeUserID); err != nil {
		t.Fatalf("read reassigned Task: %v", err)
	}
	// The plan moves the work to another Agent and leaves the Task with its
	// member (ADR 0014): a plan never hands a Task to somebody else.
	if executedByAgentID != engineeringAgentID || assigneeUserID != fixture.UserA {
		t.Fatalf("Task = executed by %s for %s, want Agent %s for %s",
			executedByAgentID, assigneeUserID, engineeringAgentID, fixture.UserA)
	}
	// The Task had never run, so moving its work is an ordinary edit: no
	// generation moves.
	var executionGeneration int64
	if err := pool.QueryRow(ctx,
		`SELECT execution_generation FROM tasks WHERE id=$1`, fixture.TaskA,
	).Scan(&executionGeneration); err != nil || executionGeneration != 0 {
		t.Fatalf("execution generation = %d, %v; want 0 for a Task that never ran", executionGeneration, err)
	}
}

// A plan that moves the work of a Task that has already run to another
// Agent is a handoff (ADR 0014), exactly as the Task PATCH makes it: the
// previous Agent's live Run is cancelled with task_reassigned, both
// generations advance so nothing that Run or its workspace still does can
// touch the Task, and work that was in progress goes back to To do. A
// restatement that keeps the Agent is no handoff.
func TestApplyCompanyPlanHandsOffATaskThatHasRun(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	engineeringAgentID := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, ctx, pool, engineeringAgentID, fixture.CompanyA,
		"Engineering", "codex", &fixture.AgentA, fixture.UserA, fixture.RuntimeA)
	// Agent A is working on the Task right now.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks SET status='in_progress', execution_generation=1, workspace_generation=1
		WHERE id=$1`, fixture.TaskA)
	var liveRunID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			execution_authorized_by_user_id, status, task_execution_generation, run_type
		) VALUES ($1,$2,$3,$4,$5,$6,'running',1,'task')
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA, fixture.UserA,
	).Scan(&liveRunID); err != nil {
		t.Fatalf("seed the live Run: %v", err)
	}
	generations := func() (int64, int64) {
		t.Helper()
		var executionGen, workspaceGen int64
		if err := pool.QueryRow(ctx,
			`SELECT execution_generation, workspace_generation FROM tasks WHERE id=$1`, fixture.TaskA,
		).Scan(&executionGen, &workspaceGen); err != nil {
			t.Fatalf("read Task generations: %v", err)
		}
		return executionGen, workspaceGen
	}
	apply := func(title string, agentID uuid.UUID, revision int64) {
		t.Helper()
		result, err := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		}, PlanArtifact{
			ArtifactVersion: 1,
			CompanyID:       fixture.CompanyA.String(),
			CompanySlug:     "schema-a",
			CreatedAt:       time.Now().UTC(),
			Base: PlanBase{
				Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
				Tasks: []PlanBaseEntity{{
					ID: fixture.TaskA.String(), Key: "task_a", Revision: revision,
					ConversationID: fixture.ConversationA.String(),
				}},
			},
			Desired: PlanDocument{
				Projects:      []PlanProject{{Key: "work", Title: "Work"}},
				FormatVersion: planFormatVersion, Company: "schema-a",
				Tasks: []PlanTask{{
					ProjectKey: "work", Key: "task_a", Title: title,
					Status: "in_progress", Priority: "medium",
					AgentID:       agentID.String(),
					MilestoneType: taskdomain.MilestoneTypeProduct, DependsOn: []PlanTaskDependency{},
				}},
			},
		})
		if err != nil {
			t.Fatalf("ApplyCompanyPlan(%q) error = %v", title, err)
		}
		if countsOf(result) != (planCounts{Updated: 1}) {
			t.Fatalf("ApplyCompanyPlan(%q) = %+v, want one Task update", title, result)
		}
	}

	apply("Task A", engineeringAgentID, 1)
	var status, reason string
	if err := pool.QueryRow(ctx,
		`SELECT status, COALESCE(error, '') FROM runs WHERE id=$1`, liveRunID,
	).Scan(&status, &reason); err != nil {
		t.Fatalf("read the previous Agent's Run: %v", err)
	}
	if status != "cancelled" || reason != execution.RunReasonTaskReassigned {
		t.Fatalf("previous Agent's Run = %s / %q, want cancelled with task_reassigned", status, reason)
	}
	if executionGen, workspaceGen := generations(); executionGen != 2 || workspaceGen != 2 {
		t.Fatalf("generations = %d/%d, want 2/2: the handoff advances both", executionGen, workspaceGen)
	}
	// The plan restated the Task as in progress, but the handoff fenced the
	// earlier Run out of any merge: the work starts fresh from To do.
	var taskStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM tasks WHERE id=$1`, fixture.TaskA).Scan(&taskStatus); err != nil {
		t.Fatalf("read Task status: %v", err)
	}
	if taskStatus != "todo" {
		t.Fatalf("Task status after the handoff = %q, want todo", taskStatus)
	}

	// Renaming the Task with the same Agent moves no work.
	apply("Task A, renamed", engineeringAgentID, 2)
	if executionGen, workspaceGen := generations(); executionGen != 2 || workspaceGen != 2 {
		t.Fatalf("generations after a rename = %d/%d, want 2/2", executionGen, workspaceGen)
	}
}

// A plan that deletes a Task by omission ends the Task's live work with it
// (reason task_deleted, tokens deleted). Otherwise runs.task_id goes NULL
// while the Task chat keeps its Project and its member keeps
// project.tasks.execute, so the claim would still hand the queued Run to a
// computer for work nobody holds any more.
func TestApplyCompanyPlanCancelsTheRunsOfADeletedTask(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status='in_progress' WHERE id=$1`, fixture.TaskA)
	var pending []events.Event
	var queued generated.Run
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		queued, err = execution.EnqueueRunInTx(ctx, q, &pending, execution.RunTrigger{
			CompanyID: fixture.CompanyA, ConversationID: fixture.ConversationA, AgentID: fixture.AgentA,
			TaskID: uuid.NullUUID{UUID: fixture.TaskA, Valid: true}, Source: execution.RunSourceMessage,
			ActorUserID: fixture.UserA, AuthorizedByUserID: fixture.UserA,
		})
		return err
	}); err != nil {
		t.Fatalf("enqueue the Task's Run: %v", err)
	}

	result, err := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, PlanArtifact{
		ArtifactVersion: 1,
		CompanyID:       fixture.CompanyA.String(),
		CompanySlug:     "schema-a",
		CreatedAt:       time.Now().UTC(),
		Base: PlanBase{
			Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
			Tasks: []PlanBaseEntity{{
				ID: fixture.TaskA.String(), Key: "task_a", Revision: 1,
				ConversationID: fixture.ConversationA.String(),
			}},
			Dependencies: []PlanBaseDependency{},
		},
		Desired: PlanDocument{
			Projects: []PlanProject{{Key: "work", Title: "Work"}}, FormatVersion: planFormatVersion,
			Company: "schema-a", Tasks: []PlanTask{},
		},
	})
	if err != nil || countsOf(result) != (planCounts{Deleted: 1}) {
		t.Fatalf("ApplyCompanyPlan() = %+v, %v; want one deleted Task", result, err)
	}
	var status, reason string
	var tokens int
	if err := pool.QueryRow(ctx, `
		SELECT status, COALESCE(error, ''), (SELECT count(*) FROM run_tokens WHERE run_id = runs.id)
		FROM runs WHERE id = $1`, queued.ID,
	).Scan(&status, &reason, &tokens); err != nil {
		t.Fatalf("read the deleted Task's Run: %v", err)
	}
	if status != "cancelled" || reason != execution.RunReasonTaskDeleted || tokens != 0 {
		t.Fatalf("deleted Task's Run = %s / %q / %d tokens, want cancelled with task_deleted", status, reason, tokens)
	}
}

// A Plan apply never moves a Task to another member and never raises
// task_assigned (ADR 0014): it changes who does the work, not whose computer
// the work runs on. A Task another member owns keeps its owner.
func TestApplyCompanyPlanKeepsTheAssigneeAndRaisesNoAssignment(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	// The founder's Task belongs to a colleague, who runs it on their own
	// computer.
	colleague := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'plan-colleague@example.com', 'Colleague')`,
		colleague)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, colleague)
	dbtest.MustExec(t, ctx, pool,
		`UPDATE tasks SET assignee_user_id = $2, version = version + 1 WHERE id = $1`,
		fixture.TaskA, colleague)
	countAssigned := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)::integer FROM inbox_items
			WHERE company_id=$1 AND kind='task_assigned' AND entity_id=$2`,
			fixture.CompanyA, fixture.TaskA.String(),
		).Scan(&count); err != nil {
			t.Fatalf("count task_assigned Inbox items: %v", err)
		}
		return count
	}
	assigneeOf := func() uuid.UUID {
		t.Helper()
		var assignee uuid.UUID
		if err := pool.QueryRow(ctx,
			`SELECT assignee_user_id FROM tasks WHERE id = $1`, fixture.TaskA,
		).Scan(&assignee); err != nil {
			t.Fatalf("read the Task assignee: %v", err)
		}
		return assignee
	}
	apply := func(title string, revision int64) {
		t.Helper()
		result, err := service.ApplyCompanyPlan(ctx, principal, PlanArtifact{
			ArtifactVersion: 1,
			CompanyID:       fixture.CompanyA.String(),
			CompanySlug:     "schema-a",
			CreatedAt:       time.Now().UTC(),
			Base: PlanBase{
				Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
				Tasks: []PlanBaseEntity{{
					ID: fixture.TaskA.String(), Key: "task_a", Revision: revision,
					ConversationID: fixture.ConversationA.String(),
				}},
			},
			Desired: PlanDocument{
				Projects:      []PlanProject{{Key: "work", Title: "Work"}},
				FormatVersion: planFormatVersion, Company: "schema-a",
				Tasks: []PlanTask{
					{
						ProjectKey:    "work",
						Key:           "task_a",
						Title:         title,
						Status:        "todo",
						Priority:      "medium",
						MilestoneType: taskdomain.MilestoneTypeProduct,
						DependsOn:     []PlanTaskDependency{},
					},
				},
			},
		})
		if err != nil {
			t.Fatalf("ApplyCompanyPlan(%q) error = %v", title, err)
		}
		if countsOf(result) != (planCounts{Updated: 1}) {
			t.Fatalf("ApplyCompanyPlan(%q) = %+v, want one Task update", title, result)
		}
	}

	// The plan takes the Agent off the Task: the colleague still owns it and
	// hears nothing, because nothing was handed to anyone.
	apply("Task A the colleague does themself", 2)
	if got := countAssigned(); got != 0 {
		t.Fatalf("task_assigned Inbox items after the apply = %d, want none", got)
	}
	if got := assigneeOf(); got != colleague {
		t.Fatalf("Task assignee after the apply = %s, want the colleague %s", got, colleague)
	}
	// A second restatement changes nothing either.
	apply("Task A, renamed", 3)
	if got := countAssigned(); got != 0 {
		t.Fatalf("task_assigned Inbox items after the restatement = %d, want none", got)
	}
	if got := assigneeOf(); got != colleague {
		t.Fatalf("Task assignee after the restatement = %s, want the colleague %s", got, colleague)
	}
}

// Archiving an Agent leaves it on the Tasks it finished: only open work
// blocks an archive, and a done Task cannot drop its Agent. An export made
// after the archive restates that Task with the archived Agent, so a plan
// that adds one Task beside it must submit and apply as exactly one create.
// Only a Task that keeps the Agent it already names may name an archived
// one, as in a Task PATCH: a new Task, or a Task moved to that Agent, is
// still refused.
func TestApplyCompanyPlanKeepsAnArchivedAgentOnItsFinishedTask(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	// Lyra finished task_a and was archived afterwards; Agent A does the
	// open Task "review".
	lyraID := dbtest.NewUUID(t)
	dbtest.SeedAgent(t, ctx, pool, lyraID, fixture.CompanyA, "Lyra", "codex", &fixture.AgentA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks SET status='done', executed_by_agent_id=$2, completed_at=now(), version=version+1
		WHERE id=$1`, fixture.TaskA, lyraID)
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET archived_at=now() WHERE id=$1`, lyraID)
	reviewTaskID := dbtest.NewUUID(t)
	reviewConversationID := dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, task_id, project_id, title)
			VALUES ($1, $2, 'task', $3, $4, 'Review')`,
			reviewConversationID, fixture.CompanyA, reviewTaskID, fixture.ProjectA)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO tasks (
				project_id, id, company_id, conversation_id,
				milestone_type, number, key, title, description,
				status, priority, executed_by_agent_id, assignee_user_id, creator_user_id
			) VALUES ($6, $1, $2, $3, 'product', 2,
				'review', 'Review', '', 'backlog', 'medium', $4, $5, $5)`,
			reviewTaskID, fixture.CompanyA, reviewConversationID,
			fixture.AgentA, fixture.UserA, fixture.ProjectA)
	})
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number = 3 WHERE id = $1`, fixture.CompanyA)

	planTask := func(key, title, status string, agentID uuid.UUID) PlanTask {
		return PlanTask{
			ProjectKey: "work", Key: key, Title: title, Status: status, Priority: "medium",
			AgentID: agentID.String(), MilestoneType: task.MilestoneTypeProduct,
			DependsOn: []PlanTaskDependency{},
		}
	}
	finished := planTask("task_a", "Task A", "done", lyraID)
	review := planTask("review", "Review", "backlog", fixture.AgentA)
	prepare := func(tasks ...PlanTask) (approval.PreparedCompanyPlanAction, error) {
		t.Helper()
		encoded, err := json.Marshal(PlanArtifact{
			ArtifactVersion: 1,
			CompanyID:       fixture.CompanyA.String(),
			CompanySlug:     "schema-a",
			CreatedAt:       time.Now().UTC(),
			Desired: PlanDocument{
				Projects:      []PlanProject{{Key: "work", Title: "Work"}},
				FormatVersion: planFormatVersion, Company: "schema-a", Tasks: tasks,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		var prepared approval.PreparedCompanyPlanAction
		err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			var err error
			prepared, err = service.PrepareCompanyPlanApply(ctx, q, approval.CompanyPlanActor{Principal: principal}, encoded)
			return err
		})
		return prepared, err
	}

	// A new Task, or a Task moved to the archived Agent, is refused.
	for name, tasks := range map[string][]PlanTask{
		"a new Task":   {finished, review, planTask("launch", "Launch", "backlog", lyraID)},
		"a moved Task": {finished, planTask("review", "Review", "backlog", lyraID)},
	} {
		if _, err := prepare(tasks...); !errors.Is(err, ErrPlanInvalidInput) {
			t.Fatalf("PrepareCompanyPlanApply(%s for the archived Agent) error = %v, want %v",
				name, err, ErrPlanInvalidInput)
		}
	}

	// The exported plan plus one new Task is one create, and nothing else.
	prepared, err := prepare(finished, review, planTask("launch", "Launch", "backlog", fixture.AgentA))
	if err != nil {
		t.Fatalf("PrepareCompanyPlanApply() error = %v, want the finished Task kept as it is", err)
	}
	var presentation CompanyPlanApprovalPresentation
	if err := json.Unmarshal(prepared.Presentation, &presentation); err != nil {
		t.Fatal(err)
	}
	want := []PlanChange{{EntityType: "task", Operation: "create", Key: "launch"}}
	if !reflect.DeepEqual(presentation.Changes, want) {
		t.Fatalf("previewed changes = %+v, want %+v", presentation.Changes, want)
	}
	var executed approval.CompanyPlanActionResult
	var pending []events.Event
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		executed, err = service.ExecuteCompanyPlanApply(
			ctx, q, &pending, approval.CompanyPlanActor{Principal: principal}, prepared.CanonicalAction,
		)
		return err
	}); err != nil {
		t.Fatalf("ExecuteCompanyPlanApply() error = %v", err)
	}
	if executed.SkippedReason != nil {
		t.Fatalf("ExecuteCompanyPlanApply() skipped: %s", *executed.SkippedReason)
	}
	var result PlanApplyResult
	if err := json.Unmarshal(executed.Result, &result); err != nil {
		t.Fatal(err)
	}
	if countsOf(result) != (planCounts{Created: 1}) {
		t.Fatalf("ExecuteCompanyPlanApply() = %+v, want exactly one create", result)
	}
	var executedBy uuid.UUID
	var status string
	var version int64
	if err := pool.QueryRow(ctx,
		`SELECT executed_by_agent_id, status, version FROM tasks WHERE id=$1`, fixture.TaskA,
	).Scan(&executedBy, &status, &version); err != nil {
		t.Fatalf("read the finished Task: %v", err)
	}
	if executedBy != lyraID || status != "done" || version != 2 {
		t.Fatalf("finished Task = executed by %s, %s, version %d; want Lyra, done, version 2 (untouched)",
			executedBy, status, version)
	}
}

func TestApplyCompanyPlanLocksTaskConversationBeforeTask(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Conversation lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	var lockedConversationID uuid.UUID
	if err := holder.QueryRow(
		ctx,
		`SELECT id FROM conversations WHERE company_id=$1 AND id=$2 FOR UPDATE`,
		fixture.CompanyA,
		fixture.ConversationA,
	).Scan(&lockedConversationID); err != nil {
		t.Fatalf("lock Task Conversation: %v", err)
	}
	if lockedConversationID != fixture.ConversationA {
		t.Fatalf("locked Conversation = %s, want %s", lockedConversationID, fixture.ConversationA)
	}

	done := make(chan error, 1)
	go func() {
		_, applyErr := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		}, PlanArtifact{
			ArtifactVersion: 1,
			CompanyID:       fixture.CompanyA.String(),
			CompanySlug:     "schema-a",
			CreatedAt:       time.Now().UTC(),
			Base: PlanBase{
				Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
				Tasks: []PlanBaseEntity{{
					ID: fixture.TaskA.String(), Key: "task_a", Revision: 1,
					ConversationID: fixture.ConversationA.String(),
				}},
				Dependencies: []PlanBaseDependency{},
			},
			Desired: PlanDocument{Projects: []PlanProject{{Key: "work", Title: "Work"}},
				FormatVersion: planFormatVersion,
				Company:       "schema-a",
				Tasks: []PlanTask{{ProjectKey: "work",
					Key:           "task_a",
					Title:         "Renamed Task A",
					Status:        "backlog",
					Priority:      "medium",
					Description:   "Deliver the renamed task.",
					AgentID:       fixture.AgentA.String(),
					MilestoneType: taskdomain.MilestoneTypeProduct, DependsOn: []PlanTaskDependency{},
				}},
			},
		})
		done <- applyErr
	}()

	waitForCompanyPlanConversationLockWait(t, ctx, pool, done)

	checker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Task lock checker: %v", err)
	}
	var checkedTaskID uuid.UUID
	checkErr := checker.QueryRow(
		ctx,
		`SELECT id FROM tasks WHERE company_id=$1 AND id=$2 FOR UPDATE NOWAIT`,
		fixture.CompanyA,
		fixture.TaskA,
	).Scan(&checkedTaskID)
	_ = checker.Rollback(context.Background())
	if checkErr != nil {
		t.Fatalf("Task was locked before its canonical Conversation lock completed: %v", checkErr)
	}
	if checkedTaskID != fixture.TaskA {
		t.Fatalf("checked Task = %s, want %s", checkedTaskID, fixture.TaskA)
	}

	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("release Task Conversation lock: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ApplyCompanyPlan() error = %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for ApplyCompanyPlan(): %v", ctx.Err())
	}
}

func TestApplyCompanyPlanDependencyChangesRespectExecutingTaskMergeFence(t *testing.T) {
	tests := []struct {
		name               string
		seedDependency     bool
		desiredDependency  bool
		wantConflict       bool
		skipMerge          bool
		deletePrerequisite bool
	}{
		{name: "add", desiredDependency: true, wantConflict: true},
		{name: "remove", seedDependency: true, wantConflict: true},
		{name: "unchanged", seedDependency: true, desiredDependency: true},
		{name: "accepted removal", seedDependency: true, skipMerge: true},
		{name: "deleted prerequisite", seedDependency: true, skipMerge: true, deletePrerequisite: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool, _ := dbtest.StartPostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatalf("MigrateUp() error = %v", err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			store, err := db.NewStore(pool)
			if err != nil {
				t.Fatalf("NewStore() error = %v", err)
			}
			bus, err := events.NewBus(events.BusOptions{})
			if err != nil {
				t.Fatalf("NewBus() error = %v", err)
			}
			service, err := NewPlanService(store, bus, nil)
			if err != nil {
				t.Fatalf("NewPlanService() error = %v", err)
			}

			prerequisiteTaskID := dbtest.NewUUID(t)
			prerequisiteConversationID := dbtest.NewUUID(t)
			dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
				dbtest.MustExecTx(t, ctx, tx, `
					INSERT INTO conversations (
						id, company_id, kind, task_id, title, project_id
					) VALUES ($1, $2, 'task', $3, 'Prerequisite Task',
						(SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
					prerequisiteConversationID, fixture.CompanyA, prerequisiteTaskID)
				dbtest.MustExecTx(t, ctx, tx, `
										INSERT INTO tasks (project_id,
						id, company_id, conversation_id,
						milestone_type, number, key, title, description,
						status, priority, executed_by_agent_id, assignee_user_id, creator_user_id
					) VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'),
						$1, $2, $3, 'product', 2,
						'prerequisite', 'Prerequisite Task', 'Prepare the dependency.',
						'backlog', 'medium', $4, $5, $5
					)`, prerequisiteTaskID, fixture.CompanyA, prerequisiteConversationID,
					fixture.AgentA, fixture.UserA)
			})
			dbtest.MustExec(t, ctx, pool, `
				UPDATE tasks
				SET description='Deliver the reviewed task.',
				    status='in_review',
				    execution_generation=1
				WHERE id=$1`, fixture.TaskA)
			dbtest.MustExec(t, ctx, pool, `
				UPDATE runs
				SET task_execution_generation=1
				WHERE id=$1`, fixture.RunA)
			if test.seedDependency {
				dbtest.MustExec(t, ctx, pool, `
					INSERT INTO task_dependencies (project_id,
						company_id, dependent_task_id, prerequisite_task_id, created_by_user_id
					) VALUES ((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $3, $4)`, fixture.CompanyA, fixture.TaskA,
					prerequisiteTaskID, fixture.UserA)
			}
			dbtest.MustExec(t, ctx, pool, `
				INSERT INTO task_merge_jobs (
					company_id, task_id, run_id, task_execution_generation,
					authority_user_id, request_type, status, attempt_count,
					lease_expires_at
				) VALUES ($1, $2, $3, 1, $4, 'manual', 'executing', 1,
					clock_timestamp() + interval '5 minutes')`, fixture.CompanyA,
				fixture.TaskA, fixture.RunA, fixture.UserA)

			if test.skipMerge {
				dbtest.MustExec(
					t,
					ctx,
					pool,
					`DELETE FROM task_merge_jobs WHERE task_id=$1`,
					fixture.TaskA,
				)
			}
			dependsOn := []PlanTaskDependency{}
			if test.desiredDependency {
				dependsOn = []PlanTaskDependency{{TaskKey: "prerequisite"}}
			}
			baseDependencies := []PlanBaseDependency{}
			if test.seedDependency {
				baseDependencies = []PlanBaseDependency{{
					DependentTaskID:    fixture.TaskA.String(),
					PrerequisiteTaskID: prerequisiteTaskID.String(),
					Revision:           1,
				}}
			}
			artifact := PlanArtifact{
				ArtifactVersion: 1,
				CompanyID:       fixture.CompanyA.String(),
				CompanySlug:     "schema-a",
				CreatedAt:       time.Now().UTC(),
				Base: PlanBase{
					Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
					Tasks: []PlanBaseEntity{
						{
							ID: fixture.TaskA.String(), Key: "task_a", Revision: 1,
							ConversationID: fixture.ConversationA.String(),
						},
						{
							ID:             prerequisiteTaskID.String(),
							Key:            "prerequisite",
							Revision:       1,
							ConversationID: prerequisiteConversationID.String(),
						},
					},
					Dependencies: baseDependencies,
				},
				Desired: PlanDocument{Projects: []PlanProject{{Key: "work", Title: "Work"}},
					FormatVersion: planFormatVersion,
					Company:       "schema-a",
					Tasks: []PlanTask{{ProjectKey: "work",
						Key:           "task_a",
						AgentID:       fixture.AgentA.String(),
						MilestoneType: taskdomain.MilestoneTypeProduct, Title: "Task A",
						Description: "Deliver the reviewed task.",
						Status:      "in_review",
						Priority:    "medium",
						DependsOn:   dependsOn,
					}, {ProjectKey: "work",
						Key:           "prerequisite",
						AgentID:       fixture.AgentA.String(),
						MilestoneType: taskdomain.MilestoneTypeProduct, Title: "Prerequisite Task",
						Description: "Prepare the dependency.",
						Status:      "backlog",
						Priority:    "medium",
						DependsOn:   []PlanTaskDependency{},
					}},
				},
			}

			if test.deletePrerequisite {
				artifact.Desired.Tasks = artifact.Desired.Tasks[:1]
			}
			result, err := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
				UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
			}, artifact)
			if test.wantConflict {
				if !errors.Is(err, ErrPlanConflict) {
					t.Fatalf("ApplyCompanyPlan() error = %v, want %v", err, ErrPlanConflict)
				}
			} else {
				if err != nil {
					t.Fatalf("ApplyCompanyPlan() error = %v", err)
				}
				wantResult := planCounts{}
				if test.skipMerge {
					wantResult.Updated = 1
				}
				if test.deletePrerequisite {
					wantResult.Deleted = 1
				}
				if countsOf(result) != wantResult {
					t.Fatalf("ApplyCompanyPlan() = %+v, want no changes", result)
				}
			}

			var dependencyCount, taskRevision int64
			if err := pool.QueryRow(
				ctx, `
				SELECT
					(SELECT count(*) FROM task_dependencies
					 WHERE company_id=$1 AND dependent_task_id=$2),
					(SELECT version FROM tasks WHERE company_id=$1 AND id=$2)`,
				fixture.CompanyA, fixture.TaskA,
			).Scan(&dependencyCount, &taskRevision); err != nil {
				t.Fatalf("read fenced dependency state: %v", err)
			}
			wantDependencyCount := int64(0)
			if test.seedDependency {
				wantDependencyCount = 1
			}
			wantRevision := int64(1)
			if test.skipMerge {
				wantDependencyCount = 0
				wantRevision = 2
			}
			if dependencyCount != wantDependencyCount || taskRevision != wantRevision {
				t.Fatalf(
					"dependency state = count %d revision %d, want %d/1",
					dependencyCount,
					taskRevision,
					wantDependencyCount,
				)
			}
			rows,
				_, err := activity.NewFeed(store.Queries()).List(ctx,
				fixture.CompanyA,
				activity.ListOptions{
					UserID:     fixture.UserA,
					EntityType: "task",
					EntityID:   fixture.TaskA.String(),
					Limit:      10,
				})
			if err != nil {
				t.Fatal(err)
			}
			if !test.skipMerge {
				if len(rows) != 0 {
					t.Fatalf("unchanged/conflicting plan wrote %d Task activities", len(rows))
				}
			} else {
				if len(rows) != 1 || rows[0].Revision == nil || *rows[0].Revision != 2 {
					t.Fatalf("dependency change history = %d rows", len(rows))
				}
				var details struct {
					Before map[string]any
					After  map[string]any
				}
				if err := json.Unmarshal(rows[0].Details, &details); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(details.Before["prerequisiteIds"],
					[]any{prerequisiteTaskID.String()}) ||
					!reflect.DeepEqual(details.After["prerequisiteIds"],
						[]any{}) {
					t.Fatalf("dependency snapshots = %s", rows[0].Details)
				}
			}
		})
	}
}

func waitForCompanyPlanConversationLockWait(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	done <-chan error,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("Company Plan apply bypassed the held Task Conversation: %v", err)
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1
			    FROM pg_stat_activity
			    WHERE datname = current_database()
			      AND state = 'active'
			      AND wait_event_type = 'Lock'
			      AND query ILIKE '%conversations%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe blocked Company Plan apply: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Company Plan apply did not reach the held Task Conversation lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A plan's slugs are its own identity; the {taskPrefix}-{number} label
// namespace is the server's (it mints those keys from the number it
// allocates). A new slug shaped like a label is refused before a person is
// asked to approve it, and again at apply, so it can never take the label of
// a later Task.
func TestApplyCompanyPlanRefusesNewSlugsShapedLikeLabels(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)
	planWith := func(key string) PlanArtifact {
		return PlanArtifact{
			ArtifactVersion: 1,
			CompanyID:       fixture.CompanyA.String(),
			CompanySlug:     "schema-a",
			CreatedAt:       time.Now().UTC(),
			Base: PlanBase{
				Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
				Tasks: []PlanBaseEntity{{
					ID: fixture.TaskA.String(), Key: "task_a", Revision: 1,
					ConversationID: fixture.ConversationA.String(),
				}},
			},
			Desired: PlanDocument{
				FormatVersion: planFormatVersion,
				Company:       "schema-a",
				Projects:      []PlanProject{{Key: "work", Title: "Work"}},
				Tasks: []PlanTask{
					{
						ProjectKey: "work",
						Key:        "task_a", Title: "Task A", Status: "backlog", Priority: "medium",
						AgentID: fixture.AgentA.String(), MilestoneType: task.MilestoneTypeProduct, DependsOn: []PlanTaskDependency{},
					},
					{
						ProjectKey: "work",
						Key:        key, Title: "Shaped like a label", Status: "backlog", Priority: "medium",
						AgentID: fixture.AgentA.String(), MilestoneType: task.MilestoneTypeProduct, DependsOn: []PlanTaskDependency{},
					},
				},
			},
		}
	}

	// The fixture company's prefix is SCA.
	for _, shaped := range []string{"SCA-7", "sca-7"} {
		if _, err := service.ApplyCompanyPlan(ctx, principal, planWith(shaped)); !errors.Is(err, ErrPlanInvalidInput) {
			t.Fatalf("ApplyCompanyPlan(slug %q) error = %v, want %v", shaped, err, ErrPlanInvalidInput)
		}
		encoded, err := json.Marshal(planWith(shaped))
		if err != nil {
			t.Fatal(err)
		}
		err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			_, err := service.PrepareCompanyPlanApply(ctx, q, approval.CompanyPlanActor{Principal: principal}, encoded)
			return err
		})
		if !errors.Is(err, ErrPlanInvalidInput) {
			t.Fatalf("PrepareCompanyPlanApply(slug %q) error = %v, want %v", shaped, err, ErrPlanInvalidInput)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE company_id = $1`, fixture.CompanyA).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("refused plans created Tasks: %d rows, want the fixture's 1", count)
	}

	// A slug that merely contains the prefix is a slug.
	result, err := service.ApplyCompanyPlan(ctx, principal, planWith("sca_7_launch"))
	if err != nil || result.Created != 1 {
		t.Fatalf("ApplyCompanyPlan(slug sca_7_launch) = %+v, %v; want one created Task", result, err)
	}
}

// A plan that moves a prerequisite to done resolves its dependents. Each
// retained dependent gets its own task.updated frame at an advanced revision,
// the same way an edge change announces it, so a client keeps the blocker
// chip live from frames alone.
func TestApplyCompanyPlanPrerequisiteCrossingDonePublishesDependentTaskUpdated(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	taskChanges := recordTopic(t, bus, task.Topic)
	dependentTaskID := dbtest.NewUUID(t)
	dependentConversationID := dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
			VALUES ($1, $2, 'task', $3, 'Dependent Task', (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
			dependentConversationID, fixture.CompanyA, dependentTaskID)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO tasks (
				project_id, id, company_id, conversation_id,
				milestone_type, number, key, title, description,
				status, priority, executed_by_agent_id, assignee_user_id, creator_user_id
			) VALUES ($6, $1, $2, $3, 'product', 2,
				'dependent', 'Dependent Task', 'Wait for task_a.',
				'backlog', 'medium', $4, $5, $5)`,
			dependentTaskID, fixture.CompanyA, dependentConversationID,
			fixture.AgentA, fixture.UserA, fixture.ProjectA)
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO task_dependencies (
				project_id, company_id, dependent_task_id, prerequisite_task_id, created_by_user_id
			) VALUES ($5, $1, $2, $3, $4)`,
			fixture.CompanyA, dependentTaskID, fixture.TaskA, fixture.UserA, fixture.ProjectA)
	})

	planTask := func(key, title, description, status string, dependsOn ...PlanTaskDependency) PlanTask {
		if dependsOn == nil {
			dependsOn = []PlanTaskDependency{}
		}
		return PlanTask{
			ProjectKey: "work", Key: key, Title: title, Description: description, Status: status,
			Priority: "medium", AgentID: fixture.AgentA.String(),
			MilestoneType: task.MilestoneTypeProduct,
			DependsOn:     dependsOn,
		}
	}
	result, err := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, PlanArtifact{
		ArtifactVersion: 1,
		CompanyID:       fixture.CompanyA.String(),
		CompanySlug:     "schema-a",
		CreatedAt:       time.Now().UTC(),
		Base: PlanBase{
			Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
			Tasks: []PlanBaseEntity{
				{
					ID: fixture.TaskA.String(), Key: "task_a", Revision: 1,
					ConversationID: fixture.ConversationA.String(),
				},
				{
					ID: dependentTaskID.String(), Key: "dependent", Revision: 1,
					ConversationID: dependentConversationID.String(),
				},
			},
			Dependencies: []PlanBaseDependency{{
				DependentTaskID:    dependentTaskID.String(),
				PrerequisiteTaskID: fixture.TaskA.String(),
				Revision:           1,
			}},
		},
		Desired: PlanDocument{
			Projects:      []PlanProject{{Key: "work", Title: "Work"}},
			FormatVersion: planFormatVersion,
			Company:       "schema-a",
			Tasks: []PlanTask{
				planTask("task_a", "Task A", "", "done"),
				planTask("dependent", "Dependent Task", "Wait for task_a.", "backlog",
					PlanTaskDependency{TaskKey: "task_a"}),
			},
		},
	})
	if err != nil {
		t.Fatalf("ApplyCompanyPlan() error = %v", err)
	}
	if countsOf(result) != (planCounts{Updated: 2}) {
		t.Fatalf("ApplyCompanyPlan() = %+v, want the prerequisite and its dependent updated", result)
	}
	var dependentUpdates []task.Response
	for _, event := range taskChanges.snapshot() {
		if event.Name == task.EventUpdated && event.TaskID == dependentTaskID {
			dependentUpdates = append(dependentUpdates, event.Payload.(task.Response))
		}
	}
	if len(dependentUpdates) != 1 || !dependentUpdates[0].DependenciesResolved ||
		dependentUpdates[0].Revision != 2 {
		t.Fatalf("dependent updates = %+v, want one resolved at revision 2", dependentUpdates)
	}
	var version int64
	if err := pool.QueryRow(
		ctx, `SELECT version FROM tasks WHERE id=$1`, dependentTaskID,
	).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("dependent version = %d, want 2", version)
	}
}

// A Task the plan creates already done or cancelled is finished on arrival,
// so it carries its outcome time from the insert, the way a Task that reaches
// the state through a restatement does.
func TestApplyCompanyPlanCreatedTerminalTaskCarriesItsOutcomeTime(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewPlanService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewPlanService() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool,
		`UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)

	planTask := func(key, status string) PlanTask {
		return PlanTask{
			ProjectKey: "work", Key: key, Title: key, Status: status, Priority: "medium",
			MilestoneType: task.MilestoneTypeProduct,
			DependsOn:     []PlanTaskDependency{},
		}
	}
	result, err := service.ApplyCompanyPlan(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, PlanArtifact{
		ArtifactVersion: 1,
		CompanyID:       fixture.CompanyA.String(),
		CompanySlug:     "schema-a",
		CreatedAt:       time.Now().UTC(),
		Base: PlanBase{
			Projects: []PlanBaseEntity{{ID: fixture.ProjectA.String(), Key: "work", Revision: 1}},
			Tasks:    []PlanBaseEntity{{ID: fixture.TaskA.String(), Key: "task_a", Revision: 1}},
		},
		Desired: PlanDocument{
			Projects:      []PlanProject{{Key: "work", Title: "Work"}},
			FormatVersion: planFormatVersion,
			Company:       "schema-a",
			Tasks: []PlanTask{
				planTask("born_done", "done"),
				planTask("born_cancelled", "cancelled"),
				planTask("born_open", "todo"),
			},
		},
	})
	if err != nil {
		t.Fatalf("ApplyCompanyPlan() error = %v", err)
	}
	if countsOf(result) != (planCounts{Created: 3, Deleted: 1}) {
		t.Fatalf("ApplyCompanyPlan() = %+v, want 3 created and task_a deleted", result)
	}
	rows, err := pool.Query(ctx, `
		SELECT key, completed_at IS NOT NULL, completed_at = created_at
		FROM tasks WHERE company_id = $1 ORDER BY key`, fixture.CompanyA)
	if err != nil {
		t.Fatalf("read created Tasks: %v", err)
	}
	defer rows.Close()
	got := map[string][2]bool{}
	for rows.Next() {
		var key string
		var finished bool
		var atCreation *bool
		if err := rows.Scan(&key, &finished, &atCreation); err != nil {
			t.Fatalf("scan created Task: %v", err)
		}
		got[key] = [2]bool{finished, atCreation != nil && *atCreation}
	}
	want := map[string][2]bool{
		"born_done": {true, true}, "born_cancelled": {true, true}, "born_open": {false, false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("completed_at by key = %v, want %v", got, want)
	}
}
