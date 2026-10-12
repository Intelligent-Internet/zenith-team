//go:build integration

package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// seedRun inserts one Run of the seeded Company A into conversationID and
// returns the identity runs_id_seq gave it. Every conversation item carries a
// run_id, and runs.id is a bigserial, so a test that wants an item has to take
// a Run id from PostgreSQL instead of minting one.
func seedRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture dbtest.Fixture,
	conversationID uuid.UUID,
	status string,
) uuid.UUID {
	t.Helper()
	var runID uuid.UUID
	// Every Run names the member whose action authorized it and runs on the
	// runtime their binding names (ADR 0014); a Run without an authorizer is
	// a row the enqueue kernel cannot produce.
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id,
			execution_authorized_by_user_id, status
		, run_type)
		VALUES ($1, $2, $3, $4, $6, $5, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, conversationID, fixture.RuntimeA, status, fixture.UserA,
	).Scan(&runID); err != nil {
		t.Fatalf("seed Run in %s: %v", conversationID, err)
	}
	return runID
}

// Conversation-domain fencing that only PostgreSQL enforces: tenant and
// direct-creator isolation, and user-state replay. This package's first
// real-Postgres coverage.

func TestResolveMessageTargetFencesTenantAndDirectCreator(t *testing.T) {
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

	direct, err := store.Queries().
		ResolveMessageTargetForActiveUser(ctx, generated.ResolveMessageTargetForActiveUserParams{
			ActorUserID: fixture.UserA, TargetKind: "conversation", TargetID: fixture.DirectConversationA,
		})
	if err != nil || direct.CompanyID != fixture.CompanyA || direct.ConversationID != fixture.DirectConversationA ||
		direct.ConversationKind != "direct" || direct.TaskID.Valid ||
		!direct.EffectiveAgentID.Valid || direct.EffectiveAgentID.UUID != fixture.AgentA ||
		direct.AssigneeUserID.Valid {
		t.Fatalf("ResolveMessageTargetForActiveUser(direct creator) = %+v, %v", direct, err)
	}
	if _, err := store.Queries().
		ResolveMessageTargetForActiveUser(ctx, generated.ResolveMessageTargetForActiveUserParams{
			ActorUserID: fixture.UserB, TargetKind: "conversation", TargetID: fixture.DirectConversationA,
		}); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("ResolveMessageTargetForActiveUser(direct outsider) error = %v, want pgx.ErrNoRows", err)
	}
	if _, err := store.Queries().
		ResolveMessageTargetForActiveUser(ctx, generated.ResolveMessageTargetForActiveUserParams{
			ActorUserID: fixture.UserB, TargetKind: "task", TargetID: fixture.TaskA,
		}); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("ResolveMessageTargetForActiveUser(Task outsider) error = %v, want pgx.ErrNoRows", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id,user_id,role)
		VALUES ($1,$2,'member')`, fixture.CompanyA, fixture.UserB)
	// Company membership alone does not reach a Task in a restricted Project
	// (ADR 0017), by the Task or by its chat.
	for kind, id := range map[string]uuid.UUID{"task": fixture.TaskA, "conversation": fixture.ConversationA} {
		if _, err := store.Queries().
			ResolveMessageTargetForActiveUser(ctx, generated.ResolveMessageTargetForActiveUserParams{
				ActorUserID: fixture.UserB, TargetKind: kind, TargetID: id,
			}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("ResolveMessageTargetForActiveUser(%s, non-reader) error = %v, want pgx.ErrNoRows", kind, err)
		}
	}
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB)
	taskTarget, err := store.Queries().
		ResolveMessageTargetForActiveUser(ctx, generated.ResolveMessageTargetForActiveUserParams{
			ActorUserID: fixture.UserB, TargetKind: "task", TargetID: fixture.TaskA,
		})
	// The target carries the Task's assignee as well as its executing Agent
	// (ADR 0014): the send decides from them whether this member's message
	// starts a Run on their own computer or waits for the assignee's.
	if err != nil || taskTarget.CompanyID != fixture.CompanyA ||
		taskTarget.ConversationID != fixture.ConversationA ||
		taskTarget.ConversationKind != "task" ||
		!taskTarget.TaskID.Valid ||
		taskTarget.TaskID.UUID != fixture.TaskA ||
		!taskTarget.EffectiveAgentID.Valid ||
		taskTarget.EffectiveAgentID.UUID != fixture.AgentA ||
		!taskTarget.AssigneeUserID.Valid ||
		taskTarget.AssigneeUserID.UUID != fixture.UserA ||
		taskTarget.ProjectID.UUID != fixture.ProjectA {
		t.Fatalf("ResolveMessageTargetForActiveUser(Task member) = %+v, %v", taskTarget, err)
	}
	if _, err := store.Queries().
		ResolveMessageTargetForActiveUser(ctx, generated.ResolveMessageTargetForActiveUserParams{
			ActorUserID: fixture.UserB, TargetKind: "conversation", TargetID: fixture.DirectConversationA,
		}); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf(
			"ResolveMessageTargetForActiveUser(non-creator member direct) error = %v, want pgx.ErrNoRows",
			err,
		)
	}
}

func TestCollaborationQueriesFenceCreatorPrivateConversations(t *testing.T) {
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
	dbtest.MustExec(t, ctx, pool, `
		UPDATE conversations
		SET archived_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
		WHERE id = $1`, fixture.DirectConversationA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)

	create := generated.InsertDirectConversationParams{
		Title:       pgtype.Text{String: "Private", Valid: true},
		ActorUserID: fixture.UserA,
		AgentID:     fixture.AgentA,
		CompanyID:   fixture.CompanyA,
	}
	direct, err := store.Queries().InsertDirectConversation(ctx, create)
	if err != nil {
		t.Fatalf("InsertDirectConversation() error = %v", err)
	}
	if direct.ID == uuid.Nil || direct.Kind != "direct" || direct.CreatedByUserID.UUID != fixture.UserA {
		t.Fatalf("InsertDirectConversation() = %+v", direct)
	}
	directID := direct.ID
	if _, err := store.Queries().InsertDirectConversation(ctx, create); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("InsertDirectConversation(existing pair) error = %v, want pgx.ErrNoRows", err)
	}
	canonical, err := store.Queries().GetDirectConversationByAgent(
		ctx,
		generated.GetDirectConversationByAgentParams{
			ActorUserID: fixture.UserA, AgentID: fixture.AgentA, CompanyID: fixture.CompanyA,
		},
	)
	if err != nil || canonical.ID != directID {
		t.Fatalf("GetDirectConversationByAgent(existing pair) = %+v, %v", canonical, err)
	}
	if _, err := store.Queries().GetDirectConversationUserState(ctx, generated.GetDirectConversationUserStateParams{
		ConversationID: directID, ActorUserID: fixture.UserA, CompanyID: fixture.CompanyA,
	}); err != nil {
		t.Fatalf("GetDirectConversationUserState(direct) error = %v", err)
	}
	if _, err := store.Queries().
		GetDirectConversationForCreator(ctx, generated.GetDirectConversationForCreatorParams{
			ActorUserID: fixture.UserA, ConversationID: directID, CompanyID: fixture.CompanyA,
		}); err != nil {
		t.Fatalf("GetDirectConversationForCreator() error = %v", err)
	}
	privateList, err := store.Queries().
		ListDirectConversationsForCreator(ctx, generated.ListDirectConversationsForCreatorParams{
			ActorUserID: fixture.UserA, CompanyID: fixture.CompanyA,
		})
	if err != nil || len(privateList) != 1 {
		t.Fatalf("ListDirectConversationsForCreator(creator) = %+v, %v", privateList, err)
	}
	privateList, err = store.Queries().
		ListDirectConversationsForCreator(ctx, generated.ListDirectConversationsForCreatorParams{
			ActorUserID: fixture.UserB, CompanyID: fixture.CompanyA,
		})
	if err != nil || len(privateList) != 0 {
		t.Fatalf("ListDirectConversationsForCreator(non-creator) = %+v, %v", privateList, err)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE conversations
		SET archived_at = clock_timestamp(), updated_at = clock_timestamp(), version = version + 1
		WHERE id = $1`, directID)
	gapConversation, err := store.Queries().InsertDirectConversation(ctx, create)
	if err != nil {
		t.Fatalf("InsertDirectConversation(gap check) error = %v", err)
	}
	gapConversationID := gapConversation.ID
	gapRunID := seedRun(t, ctx, pool, fixture, gapConversationID, "queued")
	var gapMessage generated.ConversationItem
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, lockErr := q.LockConversationForActiveUserMutation(
			ctx,
			generated.LockConversationForActiveUserMutationParams{
				ActorUserID:    fixture.UserA,
				ConversationID: gapConversationID,
				CompanyID:      fixture.CompanyA,
			},
		); lockErr != nil {
			return lockErr
		}
		var insertErr error
		gapMessage, insertErr = q.InsertUserMessageAfterConversationLock(
			ctx,
			generated.InsertUserMessageAfterConversationLockParams{
				RunID:          gapRunID,
				Content:        "first",
				ActorUserID:    fixture.UserA,
				ConversationID: gapConversationID,
				CompanyID:      fixture.CompanyA,
			},
		)
		return insertErr
	})
	if err != nil {
		t.Fatalf("InsertUserMessage(gap check) error = %v", err)
	}
	if gapMessage.Seq != 1 {
		t.Fatalf("InsertUserMessage(first) seq = %d, want database-allocated 1", gapMessage.Seq)
	}

	requestKey := dbtest.NewUUID(t)
	directRunID := seedRun(t, ctx, pool, fixture, directID, "queued")
	if _, err := store.Queries().
		InsertRequestedUserMessageAfterConversationLock(ctx, generated.InsertRequestedUserMessageAfterConversationLockParams{
			RunID:      directRunID,
			RequestKey: requestKey, Content: "private",
			ActorUserID: fixture.UserB, ConversationID: directID, CompanyID: fixture.CompanyA,
		}); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("InsertUserMessage(non-creator direct) error = %v, want pgx.ErrNoRows", err)
	}
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockConversationForActiveUserMutation(
			ctx,
			generated.LockConversationForActiveUserMutationParams{
				ActorUserID: fixture.UserA, ConversationID: directID, CompanyID: fixture.CompanyA,
			},
		); err != nil {
			return err
		}
		_, err := q.InsertRequestedUserMessageAfterConversationLock(
			ctx,
			generated.InsertRequestedUserMessageAfterConversationLockParams{
				RunID:      directRunID,
				RequestKey: requestKey, Content: "private",
				ActorUserID: fixture.UserA, ConversationID: directID, CompanyID: fixture.CompanyA,
			},
		)
		return err
	}); err != nil {
		t.Fatalf("append creator message transaction: %v", err)
	}
	existing, err := store.Queries().GetRequestedUserMessageForReplay(
		ctx,
		generated.GetRequestedUserMessageForReplayParams{
			ActorUserID: fixture.UserA, RequestKey: requestKey,
			ConversationID: directID, CompanyID: fixture.CompanyA,
		},
	)
	if err != nil || existing.Seq != 1 || existing.Content != "private" {
		t.Fatalf("GetRequestedUserMessageForReplay() = %+v, %v", existing, err)
	}
}

func TestDirectConversationUserStateReplayDoesNotAdvanceVersion(t *testing.T) {
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
	pinRunID := seedRun(t, ctx, pool, fixture, fixture.DirectConversationA, "queued")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items (
			id, company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		)
		VALUES ($1, $2, $3, $4, 1, 'user_message', 'user', $5, 'pin target')`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.DirectConversationA, pinRunID, fixture.UserA)
	initial, err := store.Queries().
		GetDirectConversationUserState(ctx, generated.GetDirectConversationUserStateParams{
			ConversationID: fixture.DirectConversationA,
			ActorUserID:    fixture.UserA,
			CompanyID:      fixture.CompanyA,
		})
	if err != nil || initial.PinnedAt.Valid {
		t.Fatalf("GetDirectConversationUserState(initial) = %+v, %v", initial, err)
	}
	state, err := store.Queries().
		UpdateDirectConversationUserState(ctx, generated.UpdateDirectConversationUserStateParams{
			Pinned: true, Archived: false, ConversationID: fixture.DirectConversationA,
			ActorUserID: fixture.UserA, ExpectedVersion: initial.Version, CompanyID: fixture.CompanyA,
		})
	if err != nil || !state.PinnedAt.Valid || state.Version != initial.Version+1 {
		t.Fatalf("UpdateDirectConversationUserState() = %+v, %v", state, err)
	}
	if _, err := store.Queries().
		UpdateDirectConversationUserState(ctx, generated.UpdateDirectConversationUserStateParams{
			Pinned: true, Archived: false, ConversationID: fixture.DirectConversationA,
			ActorUserID: fixture.UserA, ExpectedVersion: state.Version, CompanyID: fixture.CompanyA,
		}); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("UpdateDirectConversationUserState(exact replay) error = %v, want pgx.ErrNoRows", err)
	}
	retained, err := store.Queries().
		GetDirectConversationUserState(ctx, generated.GetDirectConversationUserStateParams{
			ConversationID: fixture.DirectConversationA,
			ActorUserID:    fixture.UserA,
			CompanyID:      fixture.CompanyA,
		})
	if err != nil || retained.Version != state.Version {
		t.Fatalf("GetDirectConversationUserState() = %+v, %v, want version %d", retained, err, state.Version)
	}
	dbtest.MustExec(t, ctx, pool, `
		UPDATE company_members
		SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$2,
		    version=version+1, updated_at=clock_timestamp()
		WHERE company_id=$1 AND user_id=$2`, fixture.CompanyA, fixture.UserA)
	if _, err := store.Queries().GetDirectConversationUserState(ctx, generated.GetDirectConversationUserStateParams{
		ConversationID: fixture.DirectConversationA, ActorUserID: fixture.UserA, CompanyID: fixture.CompanyA,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetDirectConversationUserState(revoked member) error = %v, want pgx.ErrNoRows", err)
	}
}
