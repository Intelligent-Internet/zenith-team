//go:build integration

package activity

import (
	"context"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// The activity feed's ordering and scoping, asserted against the queries that
// back it.

func TestActivityQueriesOrderAndScopeTheFeed(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := store.Queries()

	taskA := fixture.TaskA.String()
	// Every row about a Task or its Run names the Task's Project (ADR 0017).
	projectA := uuid.NullUUID{UUID: fixture.ProjectA, Valid: true}
	first, err := queries.CreateActivity(ctx, generated.CreateActivityParams{
		CompanyID: fixture.CompanyA, Visibility: optionalText("company"),
		ActorType: "user", ActorID: pgtype.Text{String: fixture.UserA.String(), Valid: true},
		Action: "task.created", EntityType: "task", EntityID: taskA, Details: []byte(`{}`),
		ProjectID: projectA,
	})
	if err != nil {
		t.Fatalf("CreateActivity(task.created) error = %v", err)
	}
	second, err := queries.CreateActivity(ctx, generated.CreateActivityParams{
		CompanyID: fixture.CompanyA, Visibility: optionalText("company"),
		ActorType:  "agent",
		ActorID:    pgtype.Text{String: fixture.AgentA.String(), Valid: true},
		Action:     "task.updated",
		EntityType: "task",
		EntityID:   taskA,
		Details:    []byte(`{"from":"backlog","to":"in_progress"}`),
		ProjectID:  projectA,
	})
	if err != nil {
		t.Fatalf("CreateActivity(task.updated) error = %v", err)
	}
	taskRun, err := queries.CreateActivity(ctx, generated.CreateActivityParams{
		CompanyID: fixture.CompanyA, Visibility: optionalText("company"),
		ActorType: "system",
		Action:    "run.failed", EntityType: "run", EntityID: fixture.RunA.String(),
		Details:   []byte(`{}`),
		ProjectID: projectA,
	})
	if err != nil {
		t.Fatalf("CreateActivity(other-entity) error = %v", err)
	}

	feed, _, err := NewFeed(queries).List(ctx, fixture.CompanyA, ListOptions{UserID: fixture.UserA, Limit: 10})
	if err != nil || len(feed) != 3 {
		t.Fatalf("List = %d rows, %v; want 3", len(feed), err)
	}
	if feed[0].ID != taskRun.ID.String() {
		t.Fatalf("company feed head = %s, want newest row %s", feed[0].ID, taskRun.ID)
	}

	// The feed over-fetches one row before deciding whether a continuation
	// exists. Exactly limit rows is a complete page, not proof of another one.
	complete, next, err := NewFeed(queries).List(ctx, fixture.CompanyA, ListOptions{UserID: fixture.UserA, Limit: 3})
	if err != nil || len(complete) != 3 || next != nil {
		t.Fatalf("complete activity page = %d rows, cursor %v, %v; want 3, nil, nil",
			len(complete), next, err)
	}
	firstPage, next, err := NewFeed(queries).List(ctx, fixture.CompanyA, ListOptions{UserID: fixture.UserA, Limit: 2})
	if err != nil || len(firstPage) != 2 || next == nil {
		t.Fatalf("first activity page = %d rows, cursor %v, %v; want 2, non-nil, nil",
			len(firstPage), next, err)
	}
	secondPage, lastCursor, err := NewFeed(queries).List(ctx, fixture.CompanyA,
		ListOptions{UserID: fixture.UserA, Before: next, Limit: 2})
	if err != nil || len(secondPage) != 1 || lastCursor != nil {
		t.Fatalf("last activity page = %d rows, cursor %v, %v; want 1, nil, nil",
			len(secondPage), lastCursor, err)
	}
	if firstPage[len(firstPage)-1].ID == secondPage[0].ID {
		t.Fatalf("activity page boundary repeated id %s", secondPage[0].ID)
	}

	// Keyset pagination: everything strictly before the newest row.
	newestCursor := Cursor{CreatedAt: taskRun.CreatedAt.Time, ID: taskRun.ID}
	older, _, err := NewFeed(queries).List(ctx, fixture.CompanyA,
		ListOptions{UserID: fixture.UserA, Before: &newestCursor, Limit: 10})
	if err != nil || len(older) != 2 {
		t.Fatalf("List before = %d rows, %v; want 2", len(older), err)
	}
	if older[0].ID != second.ID.String() {
		t.Fatalf("paginated feed head = %s, want %s", older[0].ID, second.ID)
	}

	// Entity timeline reads newest-first and never leaks other entities.
	timeline, _, err := NewFeed(queries).List(ctx, fixture.CompanyA,
		ListOptions{UserID: fixture.UserA, EntityType: "task", EntityID: taskA, Limit: 10})
	if err != nil || len(timeline) != 2 {
		t.Fatalf("List entity = %d rows, %v; want 2", len(timeline), err)
	}
	if timeline[0].ID != second.ID.String() || timeline[1].ID != first.ID.String() {
		t.Fatalf("entity timeline order = [%s %s], want newest-first [%s %s]",
			timeline[0].ID, timeline[1].ID, second.ID, first.ID)
	}
	if timeline[0].Actor.Kind != "agent" || timeline[0].Actor.ID == nil ||
		*timeline[0].Actor.ID != fixture.AgentA.String() {
		t.Fatalf("entity timeline head actor = %#v, want the acting agent", timeline[0].Actor)
	}

	from, to := second.CreatedAt.Time, taskRun.CreatedAt.Time
	filtered, _, err := NewFeed(queries).List(ctx, fixture.CompanyA, ListOptions{
		UserID: fixture.UserA,
		Limit:  10, From: &from, To: &to, EntityType: "task", EntityID: taskA,
		Action: "task.updated", ActorType: "agent", ActorID: fixture.AgentA.String(),
	})
	if err != nil || len(filtered) != 1 || filtered[0].ID != second.ID.String() {
		t.Fatalf("filtered activity = %+v, %v; want only %s", filtered, err, second.ID)
	}

	// Other tenants see nothing.
	other, _, err := NewFeed(queries).List(ctx, fixture.CompanyB, ListOptions{UserID: fixture.UserA, Limit: 10})
	if err != nil || len(other) != 0 {
		t.Fatalf("List(CompanyB) = %d rows, %v; want 0", len(other), err)
	}

	// Rich refs stay inside the feed row's Company. Attribution columns are
	// deliberately FK-free history, so a bad internal producer could persist
	// another Company's ids; projection joins must not resolve their labels.
	crossCompany, err := queries.CreateActivity(ctx, generated.CreateActivityParams{
		CompanyID: fixture.CompanyA, Visibility: optionalText("company"),
		ActorType:  "user",
		ActorID:    pgtype.Text{String: fixture.UserB.String(), Valid: true},
		Action:     "task.created",
		EntityType: "task",
		EntityID:   fixture.TaskB.String(),
		Details:    []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("CreateActivity(cross-company refs) error = %v", err)
	}
	crossCompanyEntry := entryOf(crossCompany)
	if crossCompanyEntry.Actor.DisplayName != nil ||
		crossCompanyEntry.Entity.Key != nil || crossCompanyEntry.Entity.Title != nil {
		t.Fatalf("cross-company refs resolved labels: %#v", crossCompanyEntry)
	}
}
