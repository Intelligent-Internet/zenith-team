//go:build integration

package execution

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestResolveRunOriginRequiresAnUnexpiredCurrentlyRunningRun(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	service := NewRunService(RunServiceOptions{Queries: queries})
	token := "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	digest := sha256.Sum256([]byte(token))
	var runID uuid.UUID

	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status
		, run_type, execution_authorized_by_user_id) VALUES ($1, $2, $3, $4, 'running', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ), $5)
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.DirectConversationA, fixture.RuntimeA, fixture.UserA,
	).Scan(&runID); err != nil {
		t.Fatalf("insert running run: %v", err)
	}
	if _, err := queries.CreateRunToken(ctx, generated.CreateRunTokenParams{
		RunID:     runID,
		TokenHash: digest[:],
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("CreateRunToken() error = %v", err)
	}

	origin, err := service.ResolveRunOrigin(ctx, token)
	if err != nil {
		t.Fatalf("ResolveRunOrigin() error = %v", err)
	}
	if origin.RunID != runID || origin.CompanyID != fixture.CompanyA ||
		origin.AgentID != fixture.AgentA || origin.ConversationID != fixture.DirectConversationA ||
		origin.TaskID != nil || origin.OriginUserID == nil || *origin.OriginUserID != fixture.UserA ||
		origin.ConversationKind != "direct" {
		t.Fatalf("ResolveRunOrigin() = %+v", origin)
	}
	// The member the Run works for is its authorizer, on whose binding it
	// executes (ADR 0014): a Run reads only the Tasks it does for them.
	if origin.AuthorizedByUserID == nil || *origin.AuthorizedByUserID != fixture.UserA {
		t.Fatalf("ResolveRunOrigin() authorizer = %v, want %s", origin.AuthorizedByUserID, fixture.UserA)
	}
	// The proof says whether its Agent is the root Agent and whether that
	// Agent's Company Plan tools are on, read live.
	if !origin.RootAgent || origin.PlanningDisabled {
		t.Fatalf(
			"ResolveRunOrigin() root, planning off = %t, %t; want true, false",
			origin.RootAgent,
			origin.PlanningDisabled,
		)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents SET settings = settings || '{"capabilities":{"disabledBuiltIns":["planning"]}}'::jsonb
		WHERE id = $1`, fixture.AgentA)
	if origin, err = service.ResolveRunOrigin(ctx, token); err != nil || !origin.PlanningDisabled {
		t.Fatalf("ResolveRunOrigin() after planning was switched off = %+v, %v; want planning off", origin, err)
	}

	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed', updated_at=now() WHERE id=$1`, runID)
	if _, err := service.ResolveRunOrigin(ctx, token); !errors.Is(err, ErrRunProofInvalid) {
		t.Fatalf("ResolveRunOrigin(terminal) error = %v, want ErrRunProofInvalid", err)
	}
}
