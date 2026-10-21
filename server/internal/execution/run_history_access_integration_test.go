//go:build integration

package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	internaldb "github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	db "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// Run history follows the chat a Run ran in (ADR 0017): a Task chat's Runs
// are read by its Project's readers, and after the Task is deleted they keep
// following that Project, because the chat keeps its project_id. Company
// membership alone reads none of them.
func TestRunHistoryOfADeletedTaskFollowsItsProject(t *testing.T) {
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
		ExecutionAuthorizedByUserID: fixture.UserA,
		Status:                      "completed", Attempt: 1, Context: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	memberID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'history@example.com', 'History')`, memberID)
	dbtest.MustExec(t, ctx, pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, memberID)
	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	member := auth.UserPrincipal{UserID: memberID, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	service := &DefaultRunService{Queries: queries}
	sees := func(principal auth.UserPrincipal) bool {
		t.Helper()
		page, err := service.List(ctx, principal, ListOptions{Limit: 10, TaskID: &fixture.TaskA})
		if err != nil {
			t.Fatal(err)
		}
		listed := false
		for _, item := range page.Items {
			listed = listed || item.ID == run.ID
		}
		_, getErr := service.Get(ctx, principal, run.ID)
		if listed != (getErr == nil) {
			t.Fatalf("List says %t but Get says %v for the same Run", listed, getErr)
		}
		if getErr != nil && !errors.Is(getErr, ErrRunNotFound) {
			t.Fatal(getErr)
		}
		return listed
	}

	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE id = $1`, fixture.TaskA)
	if !sees(owner) {
		t.Error("the Project's reader lost the deleted Task's Run history")
	}
	if sees(member) {
		t.Error("a member who cannot read the Project reads the deleted Task's Run history")
	}
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, memberID, "project.read")
	if !sees(member) {
		t.Error("a member granted project.read does not see the deleted Task's Run history")
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM project_grants WHERE user_id = $1`, memberID)
	if sees(member) {
		t.Error("revoking project.read left the Run history readable")
	}
}

// Cancelling a Task Run stops work in its Project (ADR 0017): a reader who
// holds only project.read sees the Run but may not cancel it, and someone who
// cannot read the Project does not find it. The check runs inside the
// cancellation, under the Company lock, not only in the read before it.
func TestCancellingATaskRunNeedsExecuteInItsProject(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := internaldb.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	store, err := internaldb.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service := NewRunService(RunServiceOptions{Queries: store.Queries(), Transactor: store, Bus: bus})
	run, err := store.Queries().CreateRun(ctx, db.CreateRunParams{
		CompanyID: fixture.CompanyA, AgentID: fixture.AgentA,
		TaskID:         uuid.NullUUID{UUID: fixture.TaskA, Valid: true},
		ConversationID: fixture.ConversationA, RuntimeID: fixture.RuntimeA,
		ExecutionAuthorizedByUserID: fixture.UserA,
		Status:                      "queued", Attempt: 1, Context: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	member := func(email string) auth.UserPrincipal {
		t.Helper()
		id := dbtest.NewUUID(t)
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
			id,
			email,
		)
		dbtest.MustExec(t, ctx, pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'admin')`,
			fixture.CompanyA, id)
		return auth.UserPrincipal{UserID: id, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleAdmin}
	}
	reader, outsider := member("reader@example.com"), member("outsider@example.com")
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, reader.UserID, "project.read")

	if _, err := service.Cancel(
		ctx,
		reader,
		run.ID,
		"user_cancelled",
	); !errors.Is(
		err,
		auth.ErrProjectPermissionRequired,
	) {
		t.Errorf("Cancel(admin with project.read) error = %v, want ErrProjectPermissionRequired", err)
	}
	if _, err := service.Cancel(ctx, outsider, run.ID, "user_cancelled"); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("Cancel(admin outside the Project) error = %v, want ErrRunNotFound", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, run.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("Run after refused cancellations = %s, want queued", status)
	}
}
