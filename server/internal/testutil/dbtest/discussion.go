package dbtest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedDiscussionChannel prepares a one-human private group for lifecycle tests.
// Entity IDs come from PostgreSQL, and the final deferred shape commits atomically.
func SeedDiscussionChannel(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, creatorID uuid.UUID,
	agents []uuid.UUID,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `INSERT INTO conversations(company_id,kind,created_by_user_id,title,request_key) VALUES($1,'channel',$2,'Lifecycle group',gen_random_uuid()) RETURNING id`, companyID, creatorID).
			Scan(&id); err != nil {
			t.Fatal(err)
		}
		MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_channels(company_id,conversation_id,create_fingerprint) VALUES($1,$2,decode(repeat('00',32),'hex'))`,
			companyID,
			id,
		)
		MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type) VALUES($1,$2,$3,'channel')`,
			companyID,
			id,
			creatorID,
		)
		MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_user_states(company_id,conversation_id,user_id,grant_generation) VALUES($1,$2,$3,1)`,
			companyID,
			id,
			creatorID,
		)
		for _, agentID := range agents {
			MustExecTx(
				t,
				ctx,
				tx,
				`INSERT INTO conversation_agents(company_id,conversation_id,agent_id) VALUES($1,$2,$3)`,
				companyID,
				id,
				agentID,
			)
		}
	})
	return id
}

// AddDiscussionMember makes a person an active member of a channel, as an
// invitation would.
func AddDiscussionMember(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, conversationID, userID uuid.UUID,
) {
	t.Helper()
	WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		MustExecTx(t, ctx, tx,
			`INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type) VALUES($1,$2,$3,'channel')`,
			companyID, conversationID, userID)
		MustExecTx(t, ctx, tx,
			`INSERT INTO conversation_user_states(company_id,conversation_id,user_id,grant_generation) VALUES($1,$2,$3,1)`,
			companyID, conversationID, userID)
	})
}

// SeedHumanDirect prepares a direct message between two people: a
// human_direct chat with both of them as active members.
func SeedHumanDirect(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, firstID, secondID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx,
			`INSERT INTO conversations(company_id,kind,created_by_user_id) VALUES($1,'human_direct',$2) RETURNING id`,
			companyID, firstID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		MustExecTx(t, ctx, tx, `INSERT INTO conversation_human_directs(company_id,conversation_id,user_low_id,user_high_id)
			VALUES($1,$2,LEAST($3::uuid,$4::uuid),GREATEST($3::uuid,$4::uuid))`, companyID, id, firstID, secondID)
		MustExecTx(t, ctx, tx, `INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type)
			VALUES($1,$2,$3,'human_direct'),($1,$2,$4,'human_direct')`, companyID, id, firstID, secondID)
	})
	return id
}
