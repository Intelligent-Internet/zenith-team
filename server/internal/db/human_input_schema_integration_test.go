//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestHumanInputSchemaOwnsIdentityIdempotencyAndContinuationReceipts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	requestKey := dbtest.NewUUID(t)

	var approvalID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO approvals (
			company_id, type, rule_key, title, preview_markdown,
			requested_by_agent_id, origin_run_id, request_key,
			conversation_id, origin_user_id,
			canonical_action, request_sha256, action_sha256, expires_at
		) VALUES (
			$1, 'company_plan_apply', 'company_plan_apply', 'Apply Company Plan', 'preview',
			$2, $3, $4, $5, $6,  '{}'::jsonb,
			decode(repeat('00', 32), 'hex'), decode(repeat('00', 32), 'hex'),
			now() + interval '1 hour'
		)
		RETURNING id`, fixture.CompanyA, fixture.AgentA, fixture.RunA, requestKey,
		fixture.DirectConversationA, fixture.UserA).Scan(&approvalID); err != nil {
		t.Fatalf("insert Approval with database identity: %v", err)
	}
	if approvalID == uuid.Nil {
		t.Fatal("database-generated Approval id is nil")
	}

	dbtest.AssertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO approvals (
			company_id, type, rule_key, title, preview_markdown,
			requested_by_agent_id, origin_run_id, request_key,
			conversation_id, origin_user_id,
			canonical_action, request_sha256, action_sha256, expires_at
		) VALUES (
			$1, 'company_plan_apply', 'company_plan_apply', 'Replay', 'preview',
			$2, $3, $4, $5, $6,  '{}'::jsonb,
			decode(repeat('00', 32), 'hex'), decode(repeat('00', 32), 'hex'),
			now() + interval '1 hour'
		)`, fixture.CompanyA, fixture.AgentA, fixture.RunA, requestKey,
		fixture.DirectConversationA, fixture.UserA)

	// Idempotency is scoped to each raise operation: the same UUID may key a
	// question from the same run without colliding with the approval slot.
	var questionID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO questions (
			company_id, status, title, asked_by_agent_id, origin_run_id,
			request_key, conversation_id, origin_user_id, prompts,
			request_sha256, expires_at
		) VALUES (
			$1, 'pending', 'Choose a target', $2, $3, $4, $5, $6,
			'[]'::jsonb, decode(repeat('00', 32), 'hex'),
			 now() + interval '1 hour'
		)
		RETURNING id`, fixture.CompanyA, fixture.AgentA, fixture.RunA, requestKey,
		fixture.DirectConversationA, fixture.UserA).Scan(&questionID); err != nil {
		t.Fatalf("insert Question with operation-scoped idempotency: %v", err)
	}
	if questionID == uuid.Nil || questionID == approvalID {
		t.Fatalf("database-generated Question id = %s, Approval id = %s", questionID, approvalID)
	}

	// The terminal transition opens a continuation receipt but names no Run:
	// nothing has inserted one yet, so continuation_run_id stays NULL.
	var continuationRunID uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		UPDATE approvals
		SET status='rejected',
			continuation_prompt='The request was rejected.',
			continuation_status='pending', continuation_next_attempt_at=now(),
			updated_at=now(), version=version+1
		WHERE id=$1
		RETURNING continuation_run_id`, approvalID).Scan(&continuationRunID); err != nil {
		t.Fatalf("open continuation receipt: %v", err)
	}
	if continuationRunID.Valid {
		t.Fatalf("continuation_run_id = %d before any Run was inserted", continuationRunID.UUID)
	}

	// The schema's remaining backstop is the presence invariant: a pending
	// receipt carries no continuation at all. Binding the id to the delivered
	// shape specifically is the writer's job now — the column is only ever
	// set beside the INSERT that creates its Run, in one transaction — so the
	// enumeration that used to live in a CHECK is gone deliberately.
	dbtest.AssertStatementRejected(t, ctx, pool, "23514", `
		UPDATE approvals SET continuation_status=NULL WHERE id=$1`,
		approvalID)
}

func TestHumanInputSchemaClosesStatusPolicyAndOriginScope(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	dbtest.AssertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO approvals (
			company_id, type, status, rule_key, title, preview_markdown,
			requested_by_agent_id, origin_run_id, request_key,
			conversation_id, origin_user_id,
			canonical_action, request_sha256, action_sha256, expires_at
		) VALUES (
			$1, 'company_plan_apply', 'answered', 'company_plan_apply', 'Invalid', 'preview',
			$2, $3, $4, $5, $6,  '{}'::jsonb,
			decode(repeat('00', 32), 'hex'), decode(repeat('00', 32), 'hex'),
			now() + interval '1 hour'
		)`, fixture.CompanyA, fixture.AgentA, fixture.RunA, dbtest.NewUUID(t),
		fixture.DirectConversationA, fixture.UserA)

	dbtest.AssertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO questions (
			company_id, status, title, asked_by_agent_id, origin_run_id,
			request_key, task_id, conversation_id, origin_user_id, prompts,
			request_sha256, expires_at
		) VALUES (
			$1, 'approved', 'Invalid', $2, $3, $4, NULL, $5, NULL,
			'[]'::jsonb, decode(repeat('00', 32), 'hex'),
			 now() + interval '1 hour'
		)`, fixture.CompanyA, fixture.AgentA, fixture.RunA, dbtest.NewUUID(t),
		fixture.DirectConversationA)

	// A direct origin requires its creator, while a task origin never carries
	// an origin-user visibility shortcut.
	dbtest.AssertStatementRejected(t, ctx, pool, "23514", `
		INSERT INTO questions (
			company_id, status, title, asked_by_agent_id, origin_run_id,
			request_key, conversation_id, origin_user_id, prompts,
			request_sha256, expires_at
		) VALUES (
			$1, 'pending', 'Invalid direct scope', $2, $3, $4, $5, NULL,
			'[]'::jsonb, decode(repeat('00', 32), 'hex'),
			 now() + interval '1 hour'
		)`, fixture.CompanyA, fixture.AgentA, fixture.RunA, dbtest.NewUUID(t),
		fixture.DirectConversationA)
}
