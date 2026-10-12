//go:build integration

package activity_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestHistoryCommitsWithTheMutationAndPreservesBothStates(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback requested")
	var pending []events.Event
	mutate := func(abort bool) error {
		return store.WithinRawTx(ctx, pgx.TxOptions{}, func(raw generated.DBTX, q *generated.Queries) error {
			before, err := activity.ObserveInTx(ctx, q, "task", fixture.TaskA)
			if err != nil {
				return err
			}
			if _, err := raw.Exec(ctx,
				`UPDATE tasks SET title = 'New title', version = version + 1 WHERE id = $1`,
				fixture.TaskA); err != nil {
				return err
			}
			if err := activity.RecordInTx(ctx,
				q,
				&pending,
				events.AgentActor(fixture.AgentA.String()),
				"task.updated",
				before,
				"task",
				fixture.TaskA); err != nil {
				return err
			}
			if abort {
				return rollback
			}
			return nil
		})
	}
	if err := mutate(true); !errors.Is(err, rollback) {
		t.Fatalf("rollback = %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled back history count = %d, %v", count, err)
	}
	pending = nil
	if err := mutate(false); err != nil {
		t.Fatal(err)
	}
	items, _, err := activity.NewFeed(store.Queries()).List(ctx, fixture.CompanyA, activity.ListOptions{
		UserID: fixture.UserA, EntityType: "task", EntityID: fixture.TaskA.String(), Limit: 10,
	})
	if err != nil || len(items) != 1 {
		t.Fatalf("committed history = %+v, %v", items, err)
	}
	entry := items[0]
	if entry.Revision == nil ||
		*entry.Revision != 2 ||
		entry.Actor.ID == nil ||
		*entry.Actor.ID != fixture.AgentA.String() {
		t.Fatalf("revision and verified actor = %+v", entry)
	}
	var details struct {
		SchemaVersion int `json:"schemaVersion"`
		Before        struct {
			Revision int64  `json:"revision"`
			Title    string `json:"title"`
		} `json:"before"`
		After struct {
			Revision int64  `json:"revision"`
			Title    string `json:"title"`
		} `json:"after"`
	}
	if err := json.Unmarshal(entry.Details, &details); err != nil {
		t.Fatal(err)
	}
	if details.SchemaVersion != 1 ||
		details.Before.Revision != 1 ||
		details.After.Revision != 2 ||
		details.Before.Title == details.After.Title ||
		details.After.Title != "New title" {
		t.Fatalf("before/after = %+v", details)
	}
	if len(pending) != 1 || pending[0].EntityType != activity.Topic {
		t.Fatalf("post-commit announcements = %+v", pending)
	}
}

func TestPrivateConversationHistorySurvivesDeletionWithoutCompanyDisclosure(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'admin')`,
		fixture.CompanyA,
		fixture.UserB)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	var conversationID uuid.UUID
	var pending []events.Event
	err = store.WithinRawTx(ctx, pgx.TxOptions{}, func(raw generated.DBTX, q *generated.Queries) error {
		if err := raw.QueryRow(ctx,
			`INSERT INTO conversations (company_id, kind, created_by_user_id, title) VALUES ($1, 'direct', $2, 'Private title') RETURNING id`,
			fixture.CompanyA,
			fixture.UserA).Scan(&conversationID); err != nil {
			return err
		}
		return activity.RecordInTx(ctx,
			q,
			&pending,
			events.UserActor(fixture.UserA.String()),
			"conversation.created",
			nil,
			"conversation",
			conversationID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM conversations WHERE id = $1`, conversationID)
	for _, test := range []struct {
		userID uuid.UUID
		count  int
	}{{fixture.UserA, 1}, {fixture.UserB, 0}} {
		items, _, err := activity.NewFeed(store.Queries()).List(context.Background(), fixture.CompanyA, activity.ListOptions{
			UserID: test.userID, EntityType: "conversation", EntityID: conversationID.String(), Limit: 10,
		})
		if err != nil || len(items) != test.count {
			t.Fatalf("history for %s = %+v, %v; want %d rows", test.userID, items, err, test.count)
		}
	}
}

func TestBaselineIsResumableAndHistoryFailureAbortsTheEntityWrite(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			return activity.BaselineInTx(ctx, q)
		}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM activity_events WHERE entity_type = 'task' AND entity_id = $1 AND revision = 1 AND action = 'task.history_baselined' AND actor_type = 'system' AND details->'before' = 'null'::jsonb`,
		fixture.TaskA.String()).Scan(&count); err != nil ||
		count != 1 {
		t.Fatalf("resumed baseline = %d, %v", count, err)
	}
	// Force a real PostgreSQL insert failure after a valid subject mutation.
	dbtest.MustExec(t,
		ctx,
		pool,
		`ALTER TABLE activity_events ADD CONSTRAINT reject_test_history CHECK (action <> 'task.updated')`)
	err = store.WithinRawTx(ctx, pgx.TxOptions{}, func(raw generated.DBTX, q *generated.Queries) error {
		before, err := activity.ObserveInTx(ctx, q, "task", fixture.TaskA)
		if err != nil {
			return err
		}
		if _, err := raw.Exec(ctx,
			`UPDATE tasks SET title = 'Must roll back', version = version + 1 WHERE id = $1`,
			fixture.TaskA); err != nil {
			return err
		}
		return activity.RecordInTx(ctx,
			q,
			nil,
			events.UserActor(fixture.UserA.String()),
			"task.updated",
			before,
			"task",
			fixture.TaskA)
	})
	if err == nil {
		t.Fatal("history insertion failure committed the mutation")
	}
	var revision int64
	if err := pool.QueryRow(ctx,
		`SELECT version FROM tasks WHERE id = $1 AND title <> 'Must roll back'`,
		fixture.TaskA).Scan(&revision); err != nil ||
		revision != 1 {
		t.Fatalf("rolled back Task revision = %d, %v", revision, err)
	}
	err = store.WithinRawTx(ctx, pgx.TxOptions{}, func(raw generated.DBTX, q *generated.Queries) error {
		before, err := activity.ObserveInTx(ctx, q, "task", fixture.TaskA)
		if err != nil {
			return err
		}
		if _, err := raw.Exec(ctx, `DELETE FROM tasks WHERE id = $1`, fixture.TaskA); err != nil {
			return err
		}
		return activity.RecordInTx(ctx,
			q,
			nil,
			events.UserActor(fixture.UserA.String()),
			"task.deleted",
			before,
			"task",
			fixture.TaskA)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT revision FROM activity_events WHERE entity_id = $1 AND action = 'task.deleted' AND details->'after' = 'null'::jsonb AND (details->'before'->>'revision')::bigint = 1`,
		fixture.TaskA.String()).Scan(&revision); err != nil ||
		revision != 2 {
		t.Fatalf("deletion revision = %d, %v", revision, err)
	}
}

func TestBaselineStartsHistoryForSubjectsWithOnlyEventEvidence(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	// Evidence names the Task but carries no Task state, so it cannot start
	// the Task's history.
	var pending []events.Event
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		return activity.RecordEvidenceInTx(ctx, q, &pending, activity.EvidenceInput{
			CompanyID: fixture.CompanyA, EntityID: fixture.TaskA, Action: "task.evidence_recorded",
			Actor: events.SystemActor(), Payload: map[string]string{"note": "no state"},
		})
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			return activity.BaselineInTx(ctx, q)
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := pool.Query(ctx,
		`SELECT action, schema_version FROM activity_events WHERE entity_type = 'task' AND entity_id = $1 ORDER BY schema_version`,
		fixture.TaskA.String())
	if err != nil {
		t.Fatal(err)
	}
	type stored struct {
		Action        string
		SchemaVersion int32
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[stored])
	if err != nil {
		t.Fatal(err)
	}
	want := []stored{{"task.evidence_recorded", 0}, {"task.history_baselined", 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("Task Activity rows = %+v, want %+v", got, want)
	}
}

func TestRevisionPagingSkipsUnobservedRevisionsAndUnversionedEvidence(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	// History began at revision 7; earlier revisions are deliberately absent.
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET version=7 WHERE id=$1`, fixture.TaskA)
	if err := store.WithinTx(ctx,
		pgx.TxOptions{},
		func(q *generated.Queries) error {
			return activity.BaselineInTx(ctx,
				q)
		}); err != nil {
		t.Fatal(err)
	}
	tasks, err := task.NewTaskService(store, historyTestBus(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	title := "Captured revision eight"
	if _, err := tasks.Update(ctx,
		principal,
		fixture.TaskA,
		task.UpdateInput{
			ExpectedRevision: 7,
			Title:            &title,
		}); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t,
		ctx,
		pool,
		`INSERT INTO activity_events(company_id,actor_type,action,entity_type,entity_id,visibility,details) VALUES($1,'system','task.updated','task',$2,'company','{"schemaVersion":0,"payload":{}}')`,
		fixture.CompanyA,
		fixture.TaskA.String())
	feed := activity.NewFeed(store.Queries())
	upper := int64(9)
	var cursor *activity.Cursor
	for _, revision := range []int64{8, 7} {
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
			t.Fatalf("revision %d page = %d rows, %v", revision, len(rows), err)
		}
		cursor = next
	}
	if cursor != nil {
		t.Fatal("paging fabricated prior history or included legacy evidence")
	}
	missing := int64(6)
	rows,
		next, err := feed.List(ctx,
		fixture.CompanyA,
		activity.ListOptions{
			UserID:     fixture.UserA,
			EntityType: "task",
			EntityID:   fixture.TaskA.String(),
			Revision:   &missing,
			Limit:      10,
		})
	if err != nil || len(rows) != 0 || next != nil {
		t.Fatalf("unobserved revision = %d rows, %v", len(rows), err)
	}
}

func historyTestBus(t *testing.T) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return bus
}
