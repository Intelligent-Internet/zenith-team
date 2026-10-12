//go:build integration

package execution

import (
	"context"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// Saving an Output does not require a separate artifact reviewer.
func TestTaskResultWithoutArtifactReviewerIsSaved(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewDeliverableService(DeliverableServiceOptions{
		Store: store, Bus: bus,
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool,
		`UPDATE runs SET status = 'running', execution_authorized_by_user_id = $2 WHERE id = $1`,
		fixture.RunA, fixture.UserA)
	attachmentID := insertReadyDeliverableAttachment(t, ctx, pool, fixture.CompanyA, fixture.UserA, "report.pdf")
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	origin := RunOrigin{
		RunID: fixture.RunA, CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID: &fixture.TaskA, ConversationID: fixture.ConversationA, ConversationKind: "task",
	}

	present := func(title string) (DeliverablePresentResult, error) {
		return service.Present(ctx, principal, origin, DeliverablePresentRequest{
			Title: title, Summary: "The report.",
			Items: []DeliverablePresentItemRequest{{
				Role: DeliverableItemRolePrimary, Label: "Report", AttachmentID: &attachmentID,
			}},
		})
	}
	// A presentation is stored, and an exact replay returns it.
	first, err := present("Draft")
	if err != nil || !first.Created {
		t.Fatalf("Present(first) = %+v, %v", first, err)
	}
	replayed, err := present("Draft")
	if err != nil || replayed.Created || replayed.OutputID != first.OutputID {
		t.Fatalf("Present(replay) = %+v, %v; want the stored Output %s", replayed, err, first.OutputID)
	}

	if result, err := present("Report"); err != nil || !result.Created {
		t.Fatalf("Present() = %+v, %v, want a saved Output", result, err)
	}
	var status, reason string
	var outputs, notices int
	if err := pool.QueryRow(ctx, `
		SELECT runs.status, COALESCE(runs.failure_reason, ''),
		       (SELECT count(*) FROM outputs WHERE outputs.run_id = runs.id),
		       (SELECT count(*) FROM inbox_items WHERE recipient_user_id = $2 AND kind = 'run_failed'
		          AND entity_type = 'run' AND entity_id = runs.id::text)
		FROM runs WHERE runs.id = $1`, fixture.RunA, fixture.UserA).Scan(&status, &reason, &outputs, &notices); err != nil {
		t.Fatal(err)
	}
	if status != "running" || reason != "" || outputs != 2 {
		t.Errorf("Run = %s (%q) with %d Outputs, want running with two Outputs", status, reason, outputs)
	}
	if notices != 0 {
		t.Errorf("notices = %d, want zero", notices)
	}
}
