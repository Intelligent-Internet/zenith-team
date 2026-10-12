//go:build integration

package conversation

import (
	"context"
	"errors"
	"testing"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/jackc/pgx/v5"
)

func TestInitializationPendingFencesNewSendsPreservesReplayAndPausesQueue(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s := newTestConversationService(t, store, bus)
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	target := MessageTarget{Kind: TargetConversation, ID: f.DirectConversationA}
	input := SendInput{RequestKey: dbtest.NewUUID(t), Content: "Existing message"}
	first, err := s.Send(ctx, p, target, input)
	if err != nil {
		t.Fatal(err)
	}
	queuedInput := SendInput{RequestKey: dbtest.NewUUID(t), Content: "Existing queued prompt"}
	queued, err := s.Send(ctx, p, target, queuedInput)
	if err != nil || queued.Disposition != SendDispositionQueued {
		t.Fatalf("queued = %+v/%v", queued, err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_initializations(company_id,created_by_user_id,conversation_id,research_run_id) VALUES($1,$2,$3,$4)`,
		f.CompanyA, f.UserA, f.DirectConversationA, *first.RunID)
	for _, existing := range []SendInput{input, queuedInput} {
		if _, err := s.Send(ctx, p, target, existing); err != nil {
			t.Fatalf("exact replay rejected: %v", err)
		}
	}
	for _, status := range []string{"queued", "completed"} {
		dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status=$2 WHERE id=$1`, *first.RunID, status)
		if _, err := s.Send(ctx, p, target, SendInput{RequestKey: dbtest.NewUUID(t), Content: "Must wait"}); !errors.Is(err, ErrInitializationPending) {
			t.Fatalf("new send while Run %s = %v", status, err)
		}
	}
	var promotion QueuePromotion
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockConversationForSystemRunQueueMutation(ctx, generated.LockConversationForSystemRunQueueMutationParams{
			CompanyID: f.CompanyA, ConversationID: f.DirectConversationA,
		}); err != nil {
			return err
		}
		pending := []events.Event{}
		var err error
		promotion, err = PromoteRunQueueInTx(ctx, q, &pending, testTaskMessageRuns(), f.CompanyA, f.DirectConversationA)
		return err
	})
	if err != nil || promotion.RunID != nil || promotion.RemovedCount != 0 {
		t.Fatalf("pending initialization promotion = %+v/%v", promotion, err)
	}
	var messages, runs, queuedRows int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM conversation_items WHERE conversation_id=$1 AND type='user_message'),
		(SELECT count(*) FROM runs WHERE conversation_id=$1),
		(SELECT count(*) FROM conversation_run_queue_items WHERE conversation_id=$1 AND state='queued')`, f.DirectConversationA).Scan(&messages, &runs, &queuedRows); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || runs != 1 || queuedRows != 1 {
		t.Fatalf("blocked send or promotion wrote data: messages=%d runs=%d queued=%d", messages, runs, queuedRows)
	}
	requestKey := dbtest.NewUUID(t)
	other, err := s.CreateDirect(ctx, p, CreateDirectInput{RequestKey: &requestKey, AgentID: f.AgentA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, p, MessageTarget{Kind: TargetConversation, ID: other.Conversation.ID}, SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Other chat remains usable",
	}); err != nil {
		t.Fatalf("unrelated chat blocked: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE company_initializations SET completed_at=now() WHERE company_id=$1`, f.CompanyA)
	if _, err := s.Send(ctx, p, target, SendInput{RequestKey: dbtest.NewUUID(t), Content: "Preparation is ready"}); err != nil {
		t.Fatalf("completed preparation still blocks: %v", err)
	}
}

func TestResumeInitializationQueueStagesFinalCreatorPrivateSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		removeOnly bool
	}{
		{name: "promotes preserved prompt"},
		{name: "removes unavailable target", removeOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			pool, _ := dbtest.StartPostgres(t)
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			f := dbtest.SeedFixture(t, ctx, pool)
			store, err := db.NewStore(pool)
			if err != nil {
				t.Fatal(err)
			}
			bus, err := events.NewBus(events.BusOptions{})
			if err != nil {
				t.Fatal(err)
			}
			queued, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
				CompanyID: f.CompanyA, ConversationID: f.DirectConversationA,
				ActorUserID: f.UserA, RequestKey: dbtest.NewUUID(t), Content: "Preserved prompt",
			})
			if err != nil {
				t.Fatal(err)
			}
			dbtest.MustExec(t, ctx, pool, `INSERT INTO company_initializations(company_id,created_by_user_id,conversation_id) VALUES($1,$2,$3)`,
				f.CompanyA, f.UserA, f.DirectConversationA)
			if tc.removeOnly {
				dbtest.MustExec(t, ctx, pool, `DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`, f.AgentA, f.UserA)
			}
			var published []Snapshot
			var committedRevision, committedQueuedRuns, remainingPrompts int64
			var committedState string
			var completed bool
			var readErr error
			if err := bus.Subscribe(EventConversationUpdated, func(ctx context.Context, event events.Event) error {
				payload, err := events.PayloadOf[Snapshot](event)
				if err != nil {
					readErr = err
					return err
				}
				published = append(published, payload)
				// The subscriber uses a separate connection, so every projection
				// and queue fact must already be committed when it receives this.
				readErr = pool.QueryRow(ctx, `SELECT c.version,
					(SELECT count(*) FROM runs WHERE conversation_id=c.id AND status='queued'),
					(SELECT count(*) FROM conversation_run_queue_items WHERE conversation_id=c.id AND state='queued'),
					(SELECT state FROM conversation_run_queue_items WHERE id=$2),
					(SELECT completed_at IS NOT NULL FROM company_initializations WHERE company_id=c.company_id)
					FROM conversations c WHERE c.id=$1`, f.DirectConversationA, queued.ID,
				).Scan(&committedRevision, &committedQueuedRuns, &remainingPrompts, &committedState, &completed)
				return readErr
			}); err != nil {
				t.Fatal(err)
			}
			var pending []events.Event
			var promotion QueuePromotion
			var initialRevision int64
			err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
				if _, err := q.LockCompanyForMembershipMutation(ctx, f.CompanyA); err != nil {
					return err
				}
				row, err := q.LockConversationForSystemRunQueueMutation(ctx, generated.LockConversationForSystemRunQueueMutationParams{
					CompanyID: f.CompanyA, ConversationID: f.DirectConversationA,
				})
				if err != nil {
					return err
				}
				initialRevision = row.Version
				if err := q.CompleteCompanyInitialization(ctx, f.CompanyA); err != nil {
					return err
				}
				promotion, err = ResumeInitializationQueueInTx(ctx, q, &pending, testTaskMessageRuns(), f.CompanyA, f.DirectConversationA)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, batch := range events.GroupByTopic(pending) {
				bus.Publish(ctx, batch.Topic, batch.Events...)
			}
			if len(published) != 1 || readErr != nil {
				t.Fatalf("conversation updates = %d, committed read error = %v; want exactly one final snapshot", len(published), readErr)
			}
			payload := published[0]
			if payload.Conversation.ID != f.DirectConversationA || payload.Conversation.CompanyID != f.CompanyA ||
				payload.Conversation.Kind != KindDirect || payload.ProjectAudience() != nil ||
				payload.Conversation.CreatedByUserID == nil || *payload.Conversation.CreatedByUserID != f.UserA {
				t.Fatalf("snapshot routing = %+v; want creator-private direct chat", payload)
			}
			if payload.Conversation.Revision != initialRevision+1 || payload.Conversation.Revision != committedRevision {
				t.Fatalf("snapshot revision = %d, committed = %d, initial = %d", payload.Conversation.Revision, committedRevision, initialRevision)
			}
			wantState, wantQueuedRuns, wantRemoved := "materialized", int64(1), 0
			if tc.removeOnly {
				wantState, wantQueuedRuns, wantRemoved = "removed", 0, 1
			}
			if !completed || remainingPrompts != 0 || committedState != wantState ||
				committedQueuedRuns != wantQueuedRuns || payload.Summary.Runs.QueuedCount != wantQueuedRuns ||
				payload.Summary.Runs.ActiveCount != 0 || promotion.RemovedCount != wantRemoved ||
				(promotion.RunID == nil) != tc.removeOnly {
				t.Fatalf("completed=%t, remaining prompts=%d, queue state=%s, committed queued Runs=%d, summary=%+v, promotion=%+v",
					completed, remainingPrompts, committedState, committedQueuedRuns, payload.Summary, promotion)
			}
			if !tc.removeOnly && (payload.Summary.Runs.Latest == nil || payload.Summary.Runs.Latest.RunID != *promotion.RunID) {
				t.Fatalf("latest Run = %+v, promoted = %s", payload.Summary.Runs.Latest, *promotion.RunID)
			}
		})
	}
}
