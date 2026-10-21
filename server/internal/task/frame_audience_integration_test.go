//go:build integration

package task

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Every frame a Task write publishes about the Task, its chat or its Run
// carries the audience of the Task's Project, decided in the write's own
// transaction (ADR 0017). The router drops a Project frame without one, so a
// producer that forgot to stamp would silently stop every client's updates.
func TestTaskWritesStampEveryProjectFrame(t *testing.T) {
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
	dbtest.MustExec(t, ctx, pool, `UPDATE companies SET next_task_number = 2 WHERE id = $1`, fixture.CompanyA)
	bus := newTestBus(t)
	var published []events.Event
	if err := bus.SubscribeAll(func(_ context.Context, event events.Event) error {
		published = append(published, event)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	service, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("construct Task service: %v", err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	readers, err := store.Queries().ListProjectReaderIDs(ctx, generated.ListProjectReaderIDsParams{
		CompanyID: fixture.CompanyA, ProjectID: fixture.ProjectA,
	})
	if err != nil || len(readers) == 0 {
		t.Fatalf("read the Project's readers = %v, %v", readers, err)
	}

	// A create that starts the caller's assignment Run stages task, chat,
	// message and Run frames in one transaction; an update stages more.
	agent := ActorRef{Kind: ActorAgent, ID: fixture.AgentA}
	created, err := service.Create(ctx, principal, CreateInput{
		ProjectID: fixture.ProjectA, Key: "framed", Title: "Framed", Status: StatusTodo,
		MilestoneType: MilestoneTypeProduct, ExecutedBy: &agent,
	})
	if err != nil || created.RunID == nil {
		t.Fatalf("Create() = %+v, %v; want a Task with its Run", created, err)
	}
	title := "Framed again"
	if _, err := service.Update(ctx, principal, created.Task.ID, UpdateInput{
		ExpectedRevision: created.Task.Revision, Title: &title,
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	names := map[string]bool{}
	for _, event := range published {
		carrier, ok := event.Payload.(auth.ProjectAudienceCarrier)
		if !ok {
			continue
		}
		if framed, ok := event.Payload.(auth.ProjectFramePayload); ok {
			if _, scoped := framed.ProjectFrameScope(); !scoped {
				continue
			}
		} else if routed, ok := event.Payload.(interface{ TaskScoped() bool }); ok && !routed.TaskScoped() {
			continue
		}
		audience := carrier.ProjectAudience()
		if audience == nil || audience.CompanyWide || !slices.Equal(audience.UserIDs, readers) {
			t.Errorf("%s audience = %+v, want the Project's readers %v", event.Name, audience, readers)
		}
		names[event.Name] = true
	}
	for _, name := range []string{EventCreated, EventUpdated, "conversation.created", "message.created"} {
		if !names[name] {
			t.Errorf("no stamped %s frame among %d published; stamped: %v", name, len(published), names)
		}
	}
}
