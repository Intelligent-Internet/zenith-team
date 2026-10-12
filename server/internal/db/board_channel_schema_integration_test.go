//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

// A Board's chat is one channel bound to the Board: only a channel may name a
// Board, a Board has one, it never archives or moves to another Board, it may
// have no people while its readers come and go, and it goes with its Board.
func TestBoardChannelShape(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	newBoard := func() uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO boards (company_id, title, created_by) VALUES ($1, 'Plan', $2) RETURNING id`,
			f.CompanyA, f.UserA).
			Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	board, other := newBoard(), newBoard()

	dbtest.AssertStatementRejected(
		t,
		ctx,
		pool,
		"23514",
		`INSERT INTO conversations (company_id, kind, agent_id, created_by_user_id, title, request_key, board_id)
		 VALUES ($1, 'direct', $2, $3, 'Plan', gen_random_uuid(), $4)`,
		f.CompanyA,
		f.AgentA,
		f.UserA,
		board,
	)

	channel := dbtest.SeedBoardChannel(t, ctx, pool, f.CompanyA, board, f.UserA, []uuid.UUID{f.AgentA})
	dbtest.AssertStatementRejected(t, ctx, pool, "23505",
		`INSERT INTO conversations (company_id, kind, created_by_user_id, title, request_key, board_id)
		 VALUES ($1, 'channel', $2, 'whiteboard', gen_random_uuid(), $3)`, f.CompanyA, f.UserA, board)
	dbtest.AssertStatementRejected(t, ctx, pool, "23514",
		`UPDATE conversations SET archived_at = now() WHERE id = $1`, channel)
	dbtest.AssertStatementRejected(t, ctx, pool, "23514",
		`UPDATE conversations SET board_id = $2 WHERE id = $1`, channel, other)

	// Its last reader leaving keeps it open, unlike an ordinary channel.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE conversation_members SET status = 'revoked', revoked_at = now() WHERE conversation_id = $1`,
		channel,
	)
	plain := dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, f.UserA, nil)
	dbtest.AssertStatementRejected(
		t,
		ctx,
		pool,
		"23514",
		`UPDATE conversation_members SET status = 'revoked', revoked_at = now() WHERE conversation_id = $1`,
		plain,
	)

	dbtest.MustExec(t, ctx, pool,
		`UPDATE conversation_members SET status = 'active', revoked_at = NULL, generation = generation + 1
		 WHERE conversation_id = $1`, channel)
	run := dbtest.SeedTaggedRun(t, ctx, pool, f.CompanyA, channel, f.AgentA, f.RuntimeA, f.UserA)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, run)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM boards WHERE id = $1`, board)
	var left int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM conversations WHERE id = $1 OR channel_id = $1)
		     + (SELECT count(*) FROM conversation_members WHERE conversation_id = $1)
		     + (SELECT count(*) FROM conversation_items WHERE conversation_id = $1)
		     + (SELECT count(*) FROM channel_invocations WHERE channel_id = $1)
		     + (SELECT count(*) FROM runs WHERE id = $2)`, channel, run).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d rows of the Board's chat outlived the Board", left)
	}
}
