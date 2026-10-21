//go:build integration

package calendar

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestRunCalendarIdentityReplayAndLifecycle(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	if _, err := pool.Exec(
		ctx,
		`INSERT INTO company_members(company_id,user_id,role,status) VALUES($1,$2,'member','active')`,
		f.CompanyA,
		f.UserB,
	); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	s := newCalendarTestService(t, store)
	p := auth.UserPrincipal{UserID: f.UserA, CompanyID: f.CompanyA}
	var runID uuid.UUID
	err = pool.QueryRow(ctx, `INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type,revision) VALUES($1,$2,$3,$4,'running','direct_chat',1) RETURNING id`, f.CompanyA, f.AgentA, f.DirectConversationA, f.RuntimeA).
		Scan(&runID)
	if err != nil {
		t.Fatal(err)
	}
	run := RunContext{
		RunID:          runID,
		ConversationID: f.DirectConversationA,
		AgentID:        f.AgentA,
		RequestKey:     uuid.New(),
	}
	command := Command{
		Action: "create",
		Input: &Input{
			Type:            "event",
			Title:           "Agent meeting",
			Color:           "sky",
			StartLocal:      "2026-09-22T09:00",
			EndLocal:        "2026-09-22T10:00",
			TimeZone:        "Asia/Ho_Chi_Minh",
			Recurrence:      "none",
			AttendeeUserIDs: []string{f.UserB.String()},
		},
	}
	var wg sync.WaitGroup
	results := make(chan CommandResult, 2)
	failures := make(chan error, 2)
	for range 2 {
		wg.Go(func() { r, err := s.WriteForRun(ctx, p, run, command); results <- r; failures <- err })
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	for result := range results {
		if result.Event == nil {
			t.Fatal("missing event")
		}
		if id != "" && id != result.Event.ID {
			t.Fatal("retry created duplicate events")
		}
		id = result.Event.ID
	}
	var notifications int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inbox_items WHERE recipient_user_id=$1 AND kind='calendar_update'`, f.UserB).
		Scan(&notifications); err != nil ||
		notifications != 1 {
		t.Fatalf("replayed tool invitations = %d, %v; want 1", notifications, err)
	}
	command.Input.Title = "Different payload"
	if _, err = s.WriteForRun(ctx, p, run, command); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused key = %v", err)
	}
	from, _ := time.Parse(time.RFC3339, "2026-09-22T00:00:00Z")
	data, err := s.ReadForRun(ctx, p, run, from, from.Add(24*time.Hour))
	if err != nil || len(data.Items) != 1 {
		t.Fatalf("read = %v, %v", data, err)
	}
	other := auth.UserPrincipal{UserID: f.UserB, CompanyID: f.CompanyA}
	if _, err = s.ReadForRun(ctx, other, run, from, from.Add(24*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong conversation owner = %v", err)
	}
	taskRun := RunContext{RunID: f.RunA, ConversationID: f.ConversationA, AgentID: f.AgentA, RequestKey: uuid.New()}
	if _, err = s.WriteForRun(ctx, p, taskRun, command); !errors.Is(err, ErrNotFound) {
		t.Fatalf("task personal authority = %v", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE runs SET status='completed' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	command.Input.Title = "Agent meeting"
	if _, err = s.WriteForRun(ctx, p, run, command); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finished Run replay = %v", err)
	}
}
