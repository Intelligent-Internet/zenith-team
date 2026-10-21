//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestDiscussionSchemaShape(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'member')`,
		f.CompanyA,
		f.UserB,
	)
	var dm, channel uuid.UUID
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		if err := tx.QueryRow(ctx, `INSERT INTO conversations(company_id,kind,created_by_user_id) VALUES($1,'human_direct',$2) RETURNING id`, f.CompanyA, f.UserA).
			Scan(&dm); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_human_directs(company_id,conversation_id,user_low_id,user_high_id) VALUES($1,$2,LEAST($3::uuid,$4::uuid),GREATEST($3::uuid,$4::uuid))`,
			f.CompanyA,
			dm,
			f.UserA,
			f.UserB,
		)
		dbtest.MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type) VALUES($1,$2,$3,'human_direct'),($1,$2,$4,'human_direct')`,
			f.CompanyA,
			dm,
			f.UserA,
			f.UserB,
		)
		if err := tx.QueryRow(ctx, `INSERT INTO conversations(company_id,kind,created_by_user_id,title,request_key) VALUES($1,'channel',$2,'Private',gen_random_uuid()) RETURNING id`, f.CompanyA, f.UserA).
			Scan(&channel); err != nil {
			t.Fatal(err)
		}
		dbtest.MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_channels(company_id,conversation_id,create_fingerprint) VALUES($1,$2,decode(repeat('00',32),'hex'))`,
			f.CompanyA,
			channel,
		)
		dbtest.MustExecTx(
			t,
			ctx,
			tx,
			`INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type) VALUES($1,$2,$3,'channel')`,
			f.CompanyA,
			channel,
			f.UserA,
		)
	})
	for _, tc := range []struct {
		name, code, sql string
		args            []any
	}{
		{"discussion cannot own Runs", "23514", `INSERT INTO runs(company_id,conversation_id,agent_id,runtime_id,status,run_type,execution_authorized_by_user_id) VALUES($1,$2,$3,$4,'queued','direct_chat',$5)`, []any{f.CompanyA, dm, f.AgentA, f.RuntimeA, f.UserA}},
		{"pair cannot have invitation notice", "23514", `INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,metadata) VALUES($1,$2,1,'discussion_system','system',jsonb_build_object('action','members_added','actorUserId',$3::text,'subjectUserIds',jsonb_build_array($3::text)))`, []any{f.CompanyA, dm, f.UserA}},
		{"empty invitation notice", "23514", `INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,metadata) VALUES($1,$2,1,'discussion_system','system',jsonb_build_object('action','members_added','actorUserId',$3::text,'subjectUserIds','[]'::jsonb))`, []any{f.CompanyA, channel, f.UserA}},
		{"invitation subjects must be array", "23514", `INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,metadata) VALUES($1,$2,1,'discussion_system','system',jsonb_build_object('action','members_added','actorUserId',$3::text,'subjectUserIds',$3::text))`, []any{f.CompanyA, channel, f.UserA}},
		{"invitation subjects must be strings", "23514", `INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,metadata) VALUES($1,$2,1,'discussion_system','system',jsonb_build_object('action','members_added','actorUserId',$3::text,'subjectUserIds','[null]'::jsonb))`, []any{f.CompanyA, channel, f.UserA}},
		{"system subject is not null", "23514", `INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,metadata) VALUES($1,$2,1,'discussion_system','system','{"action":"member_added","actorUserId":null,"subjectUserId":null}')`, []any{f.CompanyA, channel}},
		{"retained grant identity", "23514", `UPDATE conversation_members SET user_id=$2 WHERE conversation_id=$1`, []any{channel, f.UserB}},
		{"missing detail", "23514", `INSERT INTO conversations(company_id,kind,created_by_user_id) VALUES($1,'human_direct',$2)`, []any{f.CompanyA, f.UserA}},
		{"immutable pair", "23514", `UPDATE conversation_human_directs SET user_low_id=gen_random_uuid() WHERE conversation_id=$1`, []any{dm}},
		{"missing DM member", "23514", `DELETE FROM conversation_members WHERE conversation_id=$1 AND user_id=$2`, []any{dm, f.UserA}},
		{"agent in DM", "23514", `INSERT INTO conversation_agents(company_id,conversation_id,agent_id) VALUES($1,$2,$3)`, []any{f.CompanyA, dm, f.AgentA}},
		{"foreign agent", "23503", `INSERT INTO conversation_agents(company_id,conversation_id,agent_id) VALUES($1,$2,$3)`, []any{f.CompanyA, channel, f.AgentB}},
		{"empty open channel", "23514", `UPDATE conversation_members SET status='revoked',revoked_at=now() WHERE conversation_id=$1`, []any{channel}},
		{"legacy null Run", "23514", `UPDATE conversation_items SET run_id=NULL WHERE id=$1`, []any{f.MessageA}},
		{"legacy discussion body", "23514", `UPDATE conversation_items SET body='{"version":1,"markdown":"x","references":[]}' WHERE id=$1`, []any{f.MessageA}},
		{"type conversion", "23514", `UPDATE conversations SET kind='direct' WHERE id=$1`, []any{dm}},
		{"move item", "23514", `UPDATE conversation_items SET conversation_id=$1 WHERE id=$2`, []any{dm, f.MessageA}},
		{"discussion requires receipt", "23514", `INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,author_id,body) VALUES($1,$2,1,'user_message','user',$3,'{"version":1,"markdown":"x","references":[]}')`, []any{f.CompanyA, dm, f.UserA}},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) { dbtest.AssertStatementRejected(t, ctx, pool, tc.code, tc.sql, tc.args...) },
		)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO conversation_items(company_id,conversation_id,seq,type,author_kind,author_id,request_key,body,send_fingerprint,content) VALUES($1,$2,1,'user_message','user',$3,gen_random_uuid(),'{"version":1,"markdown":"private words","references":[]}',decode(repeat('00',32),'hex'),'private words')`,
		f.CompanyA,
		dm,
		f.UserA,
	)
	var internal bool
	if err := pool.QueryRow(ctx, `SELECT visibility IS NULL AND visibility_user_id IS NULL AND NOT(state ? 'title') AND NOT(context ? 'entityTitle') FROM activity_subject_states WHERE entity_type='conversation' AND entity_id=$1`, channel).
		Scan(&internal); err != nil {
		t.Fatal(err)
	}
	if !internal {
		t.Fatal("discussion Activity snapshot exposes private metadata")
	}
}
