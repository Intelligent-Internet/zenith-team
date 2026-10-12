//go:build integration

package execution

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	internaldb "boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	db "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
	"boardly.dev/boardly/server/pkg/runfailure"
)

func TestRunSourceRevisionAndRetryPreserveFrozenInput(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := internaldb.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := db.New(pool)
	dbtest.MustExec(t,
		ctx,
		pool,
		`UPDATE tasks SET version = 7, title = 'Original brief', status = 'in_progress' WHERE id = $1`,
		fixture.TaskA)
	parent, err := queries.CreateRun(ctx, db.CreateRunParams{
		CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID:         uuid.NullUUID{UUID: fixture.TaskA, Valid: true},
		ConversationID: fixture.ConversationA, RuntimeID: fixture.RuntimeA,
		ExecutionAuthorizedByUserID: fixture.UserA,
		Status:                      "failed", Attempt: 1, Context: []byte(`{"prompt":"Execute the frozen brief"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET version = 8, title = 'Changed later' WHERE id = $1`, fixture.TaskA)
	retry, err := queries.CreateRetryRun(ctx, db.CreateRetryRunParams{ParentRunID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if parent.ID == uuid.Nil || retry.ID == parent.ID || !retry.ParentRunID.Valid || retry.ParentRunID.UUID != parent.ID {
		t.Fatalf("retry lineage = %s -> %s (parent %v)", parent.ID, retry.ID, retry.ParentRunID)
	}
	for _, run := range []db.Run{parent, db.Run(retry)} {
		if run.RunType != "task" || !run.Revision.Valid || run.Revision.Int64 != 7 {
			t.Fatalf("Run source = %q revision %v, want task revision 7", run.RunType, run.Revision)
		}
		if !run.SourceTaskID.Valid || run.SourceTaskID.UUID != fixture.TaskA {
			t.Fatalf("Run source Task = %v, want %s", run.SourceTaskID, fixture.TaskA)
		}
		var input struct {
			Source struct {
				EntityID uuid.UUID `json:"entityId"`
				Revision int64     `json:"revision"`
				Title    string    `json:"title"`
			} `json:"source"`
		}
		if err := json.Unmarshal(run.Context, &input); err != nil {
			t.Fatal(err)
		}
		if input.Source.EntityID != fixture.TaskA || input.Source.Revision != 7 || input.Source.Title != "Original brief" {
			t.Fatalf("frozen source changed: %+v", input.Source)
		}
	}
	if retry.TaskExecutionGeneration != parent.TaskExecutionGeneration {
		t.Fatal("retry changed Task execution generation")
	}
}

func TestRunListByTaskKeepsRunsAfterTheTaskIsDeleted(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := internaldb.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := db.New(pool)
	run, err := queries.CreateRun(ctx, db.CreateRunParams{
		CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID:         uuid.NullUUID{UUID: fixture.TaskA, Valid: true},
		ConversationID: fixture.ConversationA, RuntimeID: fixture.RuntimeA,
		Status: "completed", Attempt: 1, Context: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	listedByTask := func() (db.ListRunProjectionsRow, bool) {
		t.Helper()
		params := projectionParams(fixture.CompanyA, uuid.Nil, false)
		params.HasTaskID, params.TaskID, params.PageSize = true, fixture.TaskA, 10
		rows, err := queries.ListRunProjections(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == run.ID {
				return row, true
			}
		}
		return db.ListRunProjectionsRow{}, false
	}
	if _, listed := listedByTask(); !listed {
		t.Fatalf("Run %s is missing from its live Task's Runs", run.ID)
	}
	// Deleting the Task clears runs.task_id; the frozen source Task stays.
	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE id = $1`, fixture.TaskA)
	if row, listed := listedByTask(); !listed || row.TaskID.Valid {
		t.Fatalf("Runs of the deleted Task: listed %v, row %+v; want %s with no live Task", listed, row, run.ID)
	}
}

func TestRunLifecyclePublishesTheCommittedProjection(t *testing.T) {
	h := newRunHarness(t)
	conversationID := h.seedDirectConversation(t)
	runID := h.seedQueuedRun(t, conversationID)
	if _, err := h.svc.ClaimRun(t.Context(), h.fixture.AgentA, h.fixture.RuntimeA); err != nil {
		t.Fatal(err)
	}
	if err := h.bus.Subscribe("activity", func(ctx context.Context, event events.Event) error {
		entry, err := events.PayloadOf[activity.Entry](event)
		if err != nil {
			return err
		}
		if entry.Action != "run.running" {
			return nil
		}
		// A later commit wins the live row before the prior transaction's Run
		// frame is delivered. The frame must keep the projection it committed.
		_, err = h.pool.Exec(ctx, `UPDATE runs SET status='failed' WHERE id=$1`, runID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.StartRun(t.Context(), runID, h.fixture.RuntimeA); err != nil {
		t.Fatal(err)
	}
	published, ok := h.recorder.lastEvent(EventRunRunning)
	if !ok || published.Payload.Status != "running" {
		t.Fatalf("committed Run event = %+v", published)
	}
	current, err := h.queries.GetRun(t.Context(), runID)
	if err != nil || current.Status != "failed" {
		t.Fatalf("later live state = %+v, %v", current, err)
	}
}

func TestRunHistoryCapturesLifecycleActorsAndTerminalReplay(t *testing.T) {
	t.Parallel()
	for _, terminal := range []string{"completed", "failed", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()
			h := newRunHarness(t)
			ctx := t.Context()
			dbtest.MustExec(t,
				ctx,
				h.pool,
				`UPDATE tasks SET version=7,status='in_progress',title='Frozen task' WHERE id=$1`,
				h.fixture.TaskA)
			dbtest.MustExec(t, ctx, h.pool, `UPDATE conversations SET version=31 WHERE id=$1`, h.fixture.ConversationA)
			run := enqueueHistoryRun(t, h)
			// Conversation output finalization is exercised by its own service suite.
			h.svc.SetRunQueuePromoter(RunQueuePromoterFunc(func(context.Context,
				*db.Queries,
				*[]events.Event,
				db.Run,
				string) (*uuid.UUID,
				error,
			) {
				return nil, nil
			}))
			if run.ID == uuid.Nil || run.RunType != "task" || !run.Revision.Valid || run.Revision.Int64 != 7 {
				t.Fatalf("enqueued identity/source = %+v", run)
			}
			claimed, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA)
			if err != nil || claimed == nil || claimed.ID != run.ID {
				t.Fatalf("claim = %+v, %v", claimed, err)
			}
			if _, err := h.svc.StartRun(ctx, run.ID, h.fixture.RuntimeA); err != nil {
				t.Fatal(err)
			}
			answer := appendTerminalTestItem(t,
				h,
				run.ID,
				TranscriptItemInput{
					ItemKey: "history-answer",
					Role:    protocol.RunItemRoleAgent,
					Parts: []protocol.RunItemPart{{
						Type: protocol.RunItemPartTypeText,
						Text: "Result",
					}},
				})
			proof := terminalProof(answer, answer.ItemKey)
			if _, err := h.queries.TouchRuntimeHeartbeat(ctx, h.fixture.RuntimeA); err != nil {
				t.Fatal(err)
			}
			finish := func() error {
				switch terminal {
				case "completed":
					_, err := h.svc.CompleteRun(ctx, run.ID, proof, "", false, "")
					return err
				case "failed":
					_, err := h.svc.FailRun(ctx, run.ID, proof, "provider rejected request", "", "invalid_request", false, "")
					return err
				default:
					_, err := h.svc.Cancel(ctx,
						auth.UserPrincipal{
							UserID:    h.fixture.UserA,
							CompanyID: h.fixture.CompanyA,
							Role:      auth.CompanyRoleOwner,
						},
						run.ID,
						"User stopped execution")
					return err
				}
			}
			for range 2 {
				if err := finish(); err != nil {
					t.Fatal(err)
				}
			}
			rows,
				_, err := activity.NewFeed(h.queries).List(ctx,
				h.fixture.CompanyA,
				activity.ListOptions{
					UserID:     h.fixture.UserA,
					EntityType: "run",
					EntityID:   run.ID.String(),
					Limit:      20,
				})
			if err != nil || len(rows) != 4 {
				t.Fatalf("lifecycle history count = %d, %v, want 4", len(rows), err)
			}
			var previous map[string]any
			for i, status := range []string{"queued", "dispatched", "running", terminal} {
				entry := rows[len(rows)-1-i]
				if entry.Action != "run."+status || entry.Entity.ID != run.ID.String() || entry.Revision != nil {
					t.Fatalf("lifecycle entry = %+v", entry)
				}
				wantActor := "system"
				if i == 0 || status == "cancelled" {
					wantActor = "user"
				}
				if entry.Actor.Kind != wantActor {
					t.Fatalf("%s actor = %+v, want %s", status, entry.Actor, wantActor)
				}
				if wantActor == "system" &&
					entry.Actor.ID != nil ||
					wantActor == "user" &&
						(entry.Actor.ID == nil ||
							*entry.Actor.ID != h.fixture.UserA.String()) {
					t.Fatalf("%s actor identity = %+v", status, entry.Actor)
				}
				var details struct {
					SchemaVersion int
					Before        map[string]any
					After         map[string]any
				}
				if err := json.Unmarshal(entry.Details, &details); err != nil {
					t.Fatal(err)
				}
				if details.SchemaVersion != 1 || !reflect.DeepEqual(details.Before, previous) {
					t.Fatalf("%s history continuity = %s", status, entry.Details)
				}
				expectedKeys := []string{
					"id",
					"companyId",
					"runType",
					"revision",
					"conversationId",
					"taskId",
					"agentId",
					"runtimeId",
					"status",
					"failureReason",
					"waitReason",
					"attempt",
					"parentRunId",
					"taskExecutionGeneration",
					"createdAt",
					"fireAt",
					"source",
				}
				if len(details.After) != len(expectedKeys) {
					t.Fatalf("safe Run snapshot keys = %s", entry.Details)
				}
				for _, key := range expectedKeys {
					if _, ok := details.After[key]; !ok {
						t.Fatalf("snapshot omitted %s", key)
					}
				}
				if details.After["id"] != run.ID.String() ||
					details.After["status"] != status ||
					details.After["revision"] != float64(7) ||
					details.After["runType"] != "task" ||
					details.After["conversationId"] != h.fixture.ConversationA.String() ||
					details.After["taskId"] != h.fixture.TaskA.String() ||
					details.After["agentId"] != h.fixture.AgentA.String() ||
					// The feed is every member's, and the machine the Run is
					// placed on is its registrant's alone (ADR 0014).
					details.After["runtimeId"] != nil ||
					details.After["attempt"] != float64(1) ||
					details.After["parentRunId"] != nil {
					t.Fatalf("%s safe state = %s", status, entry.Details)
				}
				source, ok := details.After["source"].(map[string]any)
				if !ok || len(source) != 4 || source["id"] != h.fixture.TaskA.String() ||
					source["type"] != "task" || source["key"] != "task_a" || source["title"] != "Frozen task" {
					t.Fatalf("frozen source = %#v", source)
				}
				var wantFailureReason any
				if status == "failed" {
					wantFailureReason = "invalid_request"
				}
				if details.After["failureReason"] != wantFailureReason || details.After["waitReason"] != nil ||
					details.After["fireAt"] != nil || details.After["companyId"] != h.fixture.CompanyA.String() ||
					details.After["taskExecutionGeneration"] != float64(run.TaskExecutionGeneration.Int64) {
					t.Fatalf("%s safe transition fields = %s", status, entry.Details)
				}
				createdAt, ok := details.After["createdAt"].(string)
				if !ok {
					t.Fatal("Run history omitted creation time")
				}
				parsed, err := time.Parse(time.RFC3339Nano, createdAt)
				if err != nil || !parsed.Equal(run.CreatedAt.Time) {
					t.Fatalf("Run creation time = %q, want %v (%v)", createdAt, run.CreatedAt.Time, err)
				}
				previous = details.After
			}
			taskRows,
				_, err := activity.NewFeed(h.queries).List(ctx,
				h.fixture.CompanyA,
				activity.ListOptions{
					UserID:     h.fixture.UserA,
					EntityType: "task",
					EntityID:   h.fixture.TaskA.String(),
					Limit:      20,
				})
			if err != nil || len(taskRows) != 0 {
				t.Fatalf("mechanical Run writes appeared as Task changes: %d, %v", len(taskRows), err)
			}
		})
	}
}

func enqueueHistoryRun(t *testing.T, h *runHarness) db.Run {
	t.Helper()
	store, err := internaldb.NewStore(h.pool)
	if err != nil {
		t.Fatal(err)
	}
	var run db.Run
	var pending []events.Event
	if err := store.WithinTx(t.Context(), pgx.TxOptions{}, func(q *db.Queries) error {
		var err error
		run,
			err = EnqueueRunInTx(t.Context(),
			q,
			&pending,
			RunTrigger{
				CompanyID:      h.fixture.CompanyA,
				ConversationID: h.fixture.ConversationA,
				AgentID:        h.fixture.AgentA,
				TaskID: uuid.NullUUID{
					UUID:  h.fixture.TaskA,
					Valid: true,
				},
				Source:             RunSourceMessage,
				ActorUserID:        h.fixture.UserA,
				AuthorizedByUserID: h.fixture.UserA,
				TriggerPrompt:      "Original request",
			})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, group := range events.GroupByTopic(pending) {
		h.bus.Publish(t.Context(), group.Topic, group.Events...)
	}
	return run
}

func TestRunHistoryRetryInheritsSourceAndFreshEnqueueUsesEditedRevision(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := t.Context()
	dbtest.MustExec(t,
		ctx,
		h.pool,
		`UPDATE tasks SET version=12,status='in_progress',title='Original source' WHERE id=$1`,
		h.fixture.TaskA)
	parent := enqueueHistoryRun(t, h)
	if _, err := h.svc.ClaimRun(ctx, h.fixture.AgentA, h.fixture.RuntimeA); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.StartRun(ctx, parent.ID, h.fixture.RuntimeA); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, h.pool, `UPDATE tasks SET version=13,title='Edited source' WHERE id=$1`, h.fixture.TaskA)
	if _, err := h.svc.FailRun(ctx,
		parent.ID,
		emptyTerminalProof(),
		"runtime disconnected",
		"",
		string(runfailure.ReasonRuntimeOffline),
		false,
		""); err != nil {
		t.Fatal(err)
	}
	child, err := h.queries.GetLatestRunForConversation(ctx, h.fixture.ConversationA)
	if err != nil {
		t.Fatal(err)
	}
	if child.ID == parent.ID ||
		!child.ParentRunID.Valid ||
		child.ParentRunID.UUID != parent.ID ||
		child.Revision != parent.Revision ||
		child.Revision.Int64 != 12 ||
		child.RunType != "task" ||
		child.TaskExecutionGeneration != parent.TaskExecutionGeneration ||
		!reflect.DeepEqual(child.Context,
			parent.Context) {
		t.Fatalf("retry lost frozen input: parent=%+v, child=%+v", parent, child)
	}
	if _, err := h.svc.CancelRun(ctx, child.ID, "Start fresh"); err != nil {
		t.Fatal(err)
	}
	fresh := enqueueHistoryRun(t, h)
	if fresh.ID == parent.ID ||
		fresh.ID == child.ID ||
		fresh.Revision.Int64 != 13 ||
		fresh.ParentRunID.Valid ||
		fresh.TaskExecutionGeneration.Int64 <= parent.TaskExecutionGeneration.Int64 {
		t.Fatalf("fresh Run source = %+v", fresh)
	}
	var input struct {
		Source struct {
			Title    string
			Revision int64
		}
	}
	if err := json.Unmarshal(fresh.Context, &input); err != nil {
		t.Fatal(err)
	}
	if input.Source.Title != "Edited source" || input.Source.Revision != 13 {
		t.Fatalf("fresh frozen source = %+v", input)
	}
	rows,
		_, err := activity.NewFeed(h.queries).List(ctx,
		h.fixture.CompanyA,
		activity.ListOptions{
			UserID:     h.fixture.UserA,
			EntityType: "run",
			EntityID:   child.ID.String(),
			Limit:      10,
		})
	if err != nil || len(rows) != 2 {
		t.Fatalf("retry creation/cancellation history = %d, %v", len(rows), err)
	}
	for _, row := range rows {
		if row.Revision != nil || row.Actor.Kind != "system" || row.Actor.ID != nil {
			t.Fatalf("automatic retry Activity = %+v", row)
		}
	}
}
