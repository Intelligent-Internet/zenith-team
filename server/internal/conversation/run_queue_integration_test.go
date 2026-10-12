//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
	"boardly.dev/boardly/server/pkg/runfailure"
)

func TestRunQueuePromotionRemovesTaskHeadRejectedDuringPreparationAndContinues(t *testing.T) {
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
	first, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID: fixture.CompanyA, ConversationID: fixture.ConversationA,
		ActorUserID: fixture.UserA, RequestKey: dbtest.NewUUID(t), Content: "stale head",
	})
	if err != nil {
		t.Fatalf("insert stale queue head: %v", err)
	}
	second, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID: fixture.CompanyA, ConversationID: fixture.ConversationA,
		ActorUserID: fixture.UserA, RequestKey: dbtest.NewUUID(t), Content: "admissible successor",
	})
	if err != nil {
		t.Fatalf("insert successor queue item: %v", err)
	}

	checkCalls, prepareCalls := 0, 0
	preparer := TaskMessageRunPreparerFuncs{
		Check: func(context.Context, *generated.Queries, generated.Task) (bool, error) {
			checkCalls++
			return true, nil
		},
		Prepare: func(
			context.Context,
			*generated.Queries,
			*[]events.Event,
			uuid.UUID,
			generated.Task,
		) (bool, error) {
			prepareCalls++
			return prepareCalls > 1, nil
		},
	}
	var promotion QueuePromotion
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, lockErr := q.LockConversationForSystemRunQueueMutation(
			ctx,
			generated.LockConversationForSystemRunQueueMutationParams{
				ConversationID: fixture.ConversationA,
				CompanyID:      fixture.CompanyA,
			},
		); lockErr != nil {
			return lockErr
		}
		pending := []events.Event{}
		var promoteErr error
		promotion, promoteErr = PromoteRunQueueInTx(
			ctx, q, &pending, preparer, fixture.CompanyA, fixture.ConversationA,
		)
		return promoteErr
	})
	if err != nil {
		t.Fatalf("promote after stale Task admission: %v", err)
	}
	if promotion.RemovedCount != 1 || promotion.RunID == nil || checkCalls != 2 || prepareCalls != 2 {
		t.Fatalf(
			"promotion = %+v, Check calls = %d, Prepare calls = %d",
			promotion,
			checkCalls,
			prepareCalls,
		)
	}

	var firstState, firstReason, secondState string
	if err := pool.QueryRow(
		ctx, `
		SELECT state, removal_reason
		FROM conversation_run_queue_items
		WHERE id = $1`, first.ID,
	).Scan(&firstState, &firstReason); err != nil {
		t.Fatalf("read stale queue head: %v", err)
	}
	if err := pool.QueryRow(
		ctx, `
		SELECT state
		FROM conversation_run_queue_items
		WHERE id = $1`, second.ID,
	).Scan(&secondState); err != nil {
		t.Fatalf("read successor queue item: %v", err)
	}
	if firstState != "removed" || firstReason != "authorization_or_target_changed" ||
		secondState != "materialized" {
		t.Fatalf(
			"queue states = first %q/%q, second %q",
			firstState,
			firstReason,
			secondState,
		)
	}
}

func TestTerminalRunPromotesExactlyOneQueuedPromptAndTransfersAttachments(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	conversationUpdates := 0
	if err := bus.Subscribe(Topic, func(
		_ context.Context,
		event events.Event,
	) error {
		if event.Name != EventConversationUpdated {
			return nil
		}
		payload, err := events.PayloadOf[Snapshot](event)
		if err != nil {
			return err
		}
		if payload.Conversation.ID == fixture.DirectConversationA {
			conversationUpdates++
		}
		return nil
	}); err != nil {
		t.Fatalf("subscribe Conversation updates: %v", err)
	}
	runService := execution.NewRunService(execution.RunServiceOptions{
		Queries:       store.Queries(),
		Transactor:    store,
		Bus:           bus,
		RouteResolver: ResolveConversationRoute,
	})
	runService.SetRunQueuePromoter(execution.RunQueuePromoterFunc(func(
		ctx context.Context,
		q *generated.Queries,
		pending *[]events.Event,
		run generated.Run,
		output string,
	) (*uuid.UUID, error) {
		promotion, err := FinalizeRunConversationInTx(
			ctx, q, pending, testTaskMessageRuns(), run, output,
		)
		return promotion.RunID, err
	}))

	currentRunID := seedRun(t, ctx, pool, fixture, fixture.DirectConversationA, "running")

	firstRequestKey := dbtest.NewUUID(t)
	first, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID:      fixture.CompanyA,
		ConversationID: fixture.DirectConversationA,
		ActorUserID:    fixture.UserA,
		RequestKey:     firstRequestKey,
		Content:        "queued first",
	})
	if err != nil {
		t.Fatalf("insert first queue item: %v", err)
	}
	firstQueueID := first.ID
	firstMessageID := first.MessageID
	second, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID:      fixture.CompanyA,
		ConversationID: fixture.DirectConversationA,
		ActorUserID:    fixture.UserA,
		RequestKey:     dbtest.NewUUID(t),
		Content:        "queued second",
	})
	if err != nil {
		t.Fatalf("insert second queue item: %v", err)
	}
	secondQueueID := second.ID

	attachmentID := dbtest.NewUUID(t)
	dbtest.MustExec(
		t, ctx, pool, `
			INSERT INTO attachments (
				id, company_id, uploaded_by_user_id, request_key, filename,
				content_type, size_bytes, status, version
			)
			VALUES ($1, $2, $3, $4, 'input.txt', 'text/plain', 5, 'ready', 2)`,
		attachmentID,
		fixture.CompanyA,
		fixture.UserA,
		dbtest.NewUUID(t),
	)
	if _, err := store.Queries().ReserveRunQueueAttachment(
		ctx,
		generated.ReserveRunQueueAttachmentParams{
			AttachmentID: attachmentID,
			CompanyID:    fixture.CompanyA,
			ActorUserID:  fixture.UserA,
			QueueItemID:  first.ID,
			Position:     0,
		},
	); err != nil {
		t.Fatalf("reserve queue attachment: %v", err)
	}

	// Editing personal filing after admission advances the source before promotion.
	conversationServiceBefore := newTestConversationService(t, store, bus)
	principalBefore := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	initialState, err := conversationServiceBefore.FetchState(ctx, principalBefore, fixture.DirectConversationA)
	if err != nil {
		t.Fatal(err)
	}
	pin := true
	editedState, err := conversationServiceBefore.UpdateState(ctx,
		principalBefore,
		fixture.DirectConversationA,
		UpdateUserStateInput{
			ExpectedRevision: initialState.Revision,
			Pinned:           &pin,
		})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := runService.AppendTranscriptItem(
		ctx,
		fixture.RuntimeA,
		currentRunID,
		execution.TranscriptItemInput{
			ItemKey: "completed-answer",
			Role:    protocol.RunItemRoleAgent,
			Parts: []protocol.RunItemPart{{
				Type: protocol.RunItemPartTypeText,
				Text: "completed answer",
			}},
		},
	)
	if err != nil {
		t.Fatalf("append current run transcript: %v", err)
	}
	if _, err := runService.CompleteRun(
		ctx,
		currentRunID,
		execution.TerminalTranscriptProof{
			ThroughSeq:   receipt.ThroughSeq,
			FinalItemKey: receipt.ItemKey,
		},
		"",
		false,
		"",
	); err != nil {
		t.Fatalf("complete current run: %v", err)
	}

	var firstState, secondState string
	if err := pool.QueryRow(
		ctx, `
		SELECT state FROM conversation_run_queue_items WHERE id = $1`,
		firstQueueID,
	).Scan(&firstState); err != nil {
		t.Fatalf("read first queue state: %v", err)
	}
	if err := pool.QueryRow(
		ctx, `
		SELECT state FROM conversation_run_queue_items WHERE id = $1`,
		secondQueueID,
	).Scan(&secondState); err != nil {
		t.Fatalf("read second queue state: %v", err)
	}
	if firstState != "materialized" || secondState != "queued" {
		t.Fatalf("queue states = (%q, %q), want (materialized, queued)", firstState, secondState)
	}

	// The promoted Run is found through the item it wrote, not through the
	// queue item id: the reserved Run identity is gone, and the item's own
	// run_id is the only edge between the two.
	var promotedRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		SELECT run_id FROM conversation_items WHERE id = $1`, firstMessageID,
	).Scan(&promotedRunID); err != nil {
		t.Fatalf("read promoted user item run_id: %v", err)
	}
	promoted, err := store.Queries().GetRun(ctx, promotedRunID)
	if err != nil || promoted.Status != "queued" || promoted.ConversationID != fixture.DirectConversationA {
		t.Fatalf("promoted run = %+v, %v", promoted, err)
	}
	if promoted.RunType != "direct_chat" ||
		!promoted.Revision.Valid ||
		promoted.Revision.Int64 != editedState.Revision ||
		promoted.Revision.Int64 == initialState.Revision {
		t.Fatalf("promoted source revision = %+v, want edited Conversation revision %d",
			promoted.Revision,
			editedState.Revision)
	}
	var source struct {
		Source struct {
			EntityID uuid.UUID
			Revision int64
		}
	}
	if err := json.Unmarshal(promoted.Context, &source); err != nil {
		t.Fatal(err)
	}
	if source.Source.EntityID != fixture.DirectConversationA || source.Source.Revision != editedState.Revision {
		t.Fatalf("promoted frozen source = %+v", source)
	}
	var queuedRunCount int
	if err := pool.QueryRow(
		ctx, `
		SELECT count(*) FROM runs WHERE conversation_id = $1 AND status = 'queued'`,
		fixture.DirectConversationA,
	).Scan(&queuedRunCount); err != nil {
		t.Fatalf("count queued Runs: %v", err)
	}
	if queuedRunCount != 1 {
		t.Fatalf("queued Runs after promotion = %d, want exactly the promoted head", queuedRunCount)
	}
	var secondItemCount int
	if err := pool.QueryRow(
		ctx, `
		SELECT count(*) FROM conversation_items WHERE id = $1`, second.MessageID,
	).Scan(&secondItemCount); err != nil {
		t.Fatalf("read second reserved item: %v", err)
	}
	if secondItemCount != 0 {
		t.Fatal("second queued prompt materialized its item")
	}
	conversationService := newTestConversationService(t, store, bus)
	replay, err := conversationService.Send(
		ctx,
		auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA},
		SendInput{
			RequestKey: firstRequestKey, Content: "queued first",
			AttachmentIDs: []uuid.UUID{attachmentID},
		},
	)
	if err != nil {
		t.Fatalf("replay materialized queued send: %v", err)
	}
	if replay.Created || replay.Disposition != SendDispositionMessage ||
		replay.View.Message.ID != firstMessageID || replay.RunID == nil ||
		*replay.RunID != promotedRunID || len(replay.View.Attachments) != 1 ||
		replay.View.Attachments[0].ID != attachmentID {
		t.Fatalf("materialized queued replay = %+v", replay)
	}

	// The completed Run's answer is not copied anywhere at terminal time: it is
	// the 'agent_text' item the daemon appended, and the promoted prompt takes
	// the next conversation-wide seq after it.
	var outputSeq, queuedSeq int64
	if err := pool.QueryRow(
		ctx, `
		SELECT seq FROM conversation_items WHERE run_id = $1 AND type = 'agent_text'`,
		currentRunID,
	).Scan(&outputSeq); err != nil {
		t.Fatalf("read completed agent_text item: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT seq FROM conversation_items WHERE id = $1`, firstMessageID).
		Scan(&queuedSeq); err != nil {
		t.Fatalf("read promoted user item: %v", err)
	}
	if queuedSeq != outputSeq+1 {
		t.Fatalf(
			"item sequence answer=%d queued=%d, want answer immediately before queued",
			outputSeq,
			queuedSeq,
		)
	}
	var agentTextCount int
	if err := pool.QueryRow(
		ctx, `
		SELECT count(*) FROM conversation_items WHERE run_id = $1 AND type = 'agent_text'`,
		currentRunID,
	).Scan(&agentTextCount); err != nil {
		t.Fatalf("count completed agent_text items: %v", err)
	}
	if agentTextCount != 1 {
		t.Fatalf("agent_text items for the completed Run = %d, want the single appended item", agentTextCount)
	}

	var linkedCount, activeReservationCount int
	if err := pool.QueryRow(
		ctx, `
		SELECT count(*) FROM message_attachments
		WHERE message_id = $1 AND attachment_id = $2`,
		firstMessageID,
		attachmentID,
	).Scan(&linkedCount); err != nil {
		t.Fatalf("read materialized attachment link: %v", err)
	}
	if err := pool.QueryRow(
		ctx, `
		SELECT count(*) FROM conversation_run_queue_attachments
		WHERE queue_item_id = $1 AND attachment_id = $2 AND released_at IS NULL`,
		firstQueueID,
		attachmentID,
	).Scan(&activeReservationCount); err != nil {
		t.Fatalf("read released queue attachment reservation: %v", err)
	}
	if linkedCount != 1 || activeReservationCount != 0 {
		t.Fatalf(
			"attachment transfer linked=%d activeReservation=%d, want 1 and 0",
			linkedCount,
			activeReservationCount,
		)
	}
	if conversationUpdates != 1 {
		t.Fatalf("Conversation update events = %d, want 1", conversationUpdates)
	}
}

func TestDeferredRetryBlocksQueuedPromptPromotion(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	runService := execution.NewRunService(execution.RunServiceOptions{
		Queries:       store.Queries(),
		Transactor:    store,
		Bus:           bus,
		RouteResolver: ResolveConversationRoute,
	})
	runService.SetRunQueuePromoter(execution.RunQueuePromoterFunc(func(
		ctx context.Context,
		q *generated.Queries,
		pending *[]events.Event,
		run generated.Run,
		output string,
	) (*uuid.UUID, error) {
		promotion, err := FinalizeRunConversationInTx(
			ctx, q, pending, testTaskMessageRuns(), run, output,
		)
		return promotion.RunID, err
	}))

	var currentRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id,
			execution_authorized_by_user_id, status, attempt
		, run_type) VALUES ($1, $2, $3, $4, $5, 'running', 2, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA,
		fixture.DirectConversationA, fixture.RuntimeA, fixture.UserA,
	).Scan(&currentRunID); err != nil {
		t.Fatalf("seed retryable running Run: %v", err)
	}
	queued, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA,
		ActorUserID: fixture.UserA, RequestKey: dbtest.NewUUID(t), Content: "wait behind retry",
	})
	if err != nil {
		t.Fatalf("insert queued prompt: %v", err)
	}
	queueID := queued.ID

	if _, err := runService.FailRun(
		ctx, currentRunID,
		execution.TerminalTranscriptProof{},
		"provider stream disconnected", "",
		string(runfailure.ReasonAgentProviderNetwork), false, "",
	); err != nil {
		t.Fatalf("FailRun() error = %v", err)
	}
	var queueState string
	if err := pool.QueryRow(ctx, `
		SELECT state FROM conversation_run_queue_items WHERE id = $1`, queueID).
		Scan(&queueState); err != nil {
		t.Fatal(err)
	}
	if queueState != "queued" {
		t.Fatalf("queued prompt state = %q, want queued", queueState)
	}
	var deferredCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM runs
		WHERE parent_run_id = $1 AND status = 'deferred'`, currentRunID).
		Scan(&deferredCount); err != nil {
		t.Fatal(err)
	}
	if deferredCount != 1 {
		t.Fatalf("deferred retry count = %d, want 1", deferredCount)
	}
	var promotedItemCount int
	if err := pool.QueryRow(
		ctx, `
		SELECT count(*) FROM conversation_items WHERE id = $1`, queued.MessageID,
	).Scan(&promotedItemCount); err != nil {
		t.Fatal(err)
	}
	if promotedItemCount != 0 {
		t.Fatal("queued prompt materialized while a deferred retry owns the Conversation")
	}
}

func TestRunQueueCanRefillAfterMaterializingAFullQueueHead(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	var head generated.ConversationRunQueueItem
	for position := 1; position <= runQueueLimit; position++ {
		item, err := queries.InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
			CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA,
			ActorUserID: fixture.UserA, RequestKey: dbtest.NewUUID(t), Content: "queued prompt",
		})
		if err != nil {
			t.Fatalf("insert queue position %d: %v", position, err)
		}
		if position == 1 {
			head = item
		}
	}
	if _, err := queries.MaterializeRunQueueItem(ctx, generated.MaterializeRunQueueItemParams{
		QueueItemID: head.ID, ExpectedVersion: head.Version,
	}); err != nil {
		t.Fatalf("materialize queue head: %v", err)
	}
	replacement, err := queries.InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA,
		ActorUserID: fixture.UserA, RequestKey: dbtest.NewUUID(t), Content: "replacement tail",
	})
	if err != nil {
		t.Fatalf("refill queue tail: %v", err)
	}
	if replacement.Position != runQueueLimit+1 {
		t.Fatalf("replacement position = %d, want %d", replacement.Position, runQueueLimit+1)
	}
}

// Admission reserves ONE identity — the future conversation item's id. The Run
// id is not reserved any more: runs.id is a bigserial the promotion
// transaction takes by inserting the Run.
func TestRunQueueAdmissionReservesTheItemIDAndNothingElse(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)
	requestKey := dbtest.NewUUID(t)
	item, err := queries.InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA,
		ActorUserID: fixture.UserA, RequestKey: requestKey, Content: "reserve identities",
	})
	if err != nil {
		t.Fatalf("InsertRunQueueItem() error = %v", err)
	}
	if item.ID == uuid.Nil || item.MessageID == uuid.Nil || item.ID == item.MessageID ||
		item.ID == requestKey || item.MessageID == requestKey {
		t.Fatalf("database-reserved queue identities = %+v, requestKey=%s", item, requestKey)
	}
	// The reserved id names no row yet, which is exactly what makes the
	// promotion's availability probe meaningful.
	available, err := queries.RunQueueReservedIDsAvailable(ctx, item.MessageID)
	if err != nil || !available {
		t.Fatalf("RunQueueReservedIDsAvailable(fresh reservation) = %v, %v", available, err)
	}
}

// The queue mutations answer the way docs/api/conversations.md promises: a
// queue item this Conversation never held is 404 (ErrNotFound), an order that
// is not a permutation of the current queue is 422 (ErrInvalidInput), and only
// a queue that really changed under the write is 409 (ErrRunQueueConflict).
func TestRunQueueMutationsTellUnknownItemsFromConflicts(t *testing.T) {
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
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service := newTestConversationService(t, store, bus)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	conversationID := fixture.DirectConversationA
	seedRun(t, ctx, pool, fixture, conversationID, "running")
	target := MessageTarget{Kind: TargetConversation, ID: conversationID}
	queue := func(content string) RunQueueItem {
		t.Helper()
		sent, err := service.Send(ctx, principal, target, SendInput{
			RequestKey: dbtest.NewUUID(t), Content: content,
		})
		if err != nil {
			t.Fatalf("queue %q: %v", content, err)
		}
		if sent.Disposition != SendDispositionQueued || sent.QueueItem == nil {
			t.Fatalf("queue %q receipt = %+v", content, sent)
		}
		return *sent.QueueItem
	}
	first := queue("first")
	second := queue("second")
	unknownID := dbtest.NewUUID(t)

	// Delete: an item the Conversation never held is not found; a stale
	// revision on a real item is a conflict.
	if _, err := service.DeleteRunQueueItem(
		ctx, principal, conversationID, unknownID, DeleteRunQueueItemInput{ExpectedRevision: 1},
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete unknown queue item error = %v, want %v", err, ErrNotFound)
	}
	if _, err := service.DeleteRunQueueItem(
		ctx, principal, conversationID, first.ID,
		DeleteRunQueueItemInput{ExpectedRevision: first.Revision + 1},
	); !errors.Is(err, ErrRunQueueConflict) {
		t.Fatalf("delete stale queue item error = %v, want %v", err, ErrRunQueueConflict)
	}

	// Reorder: an id from outside the queue or a partial list is not a
	// permutation; a stale revision on a permutation is a conflict.
	if _, err := service.ReorderRunQueue(ctx, principal, conversationID, RunQueueOrderInput{
		Items: []RunQueueOrderItem{
			{ID: unknownID, ExpectedRevision: 1},
			{ID: first.ID, ExpectedRevision: first.Revision},
		},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reorder with unknown id error = %v, want %v", err, ErrInvalidInput)
	}
	if _, err := service.ReorderRunQueue(ctx, principal, conversationID, RunQueueOrderInput{
		Items: []RunQueueOrderItem{{ID: second.ID, ExpectedRevision: second.Revision}},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("partial reorder error = %v, want %v", err, ErrInvalidInput)
	}
	if _, err := service.ReorderRunQueue(ctx, principal, conversationID, RunQueueOrderInput{
		Items: []RunQueueOrderItem{
			{ID: second.ID, ExpectedRevision: second.Revision + 1},
			{ID: first.ID, ExpectedRevision: first.Revision},
		},
	}); !errors.Is(err, ErrRunQueueConflict) {
		t.Fatalf("reorder with stale revision error = %v, want %v", err, ErrRunQueueConflict)
	}
	reordered, err := service.ReorderRunQueue(ctx, principal, conversationID, RunQueueOrderInput{
		Items: []RunQueueOrderItem{
			{ID: second.ID, ExpectedRevision: second.Revision},
			{ID: first.ID, ExpectedRevision: first.Revision},
		},
	})
	if err != nil {
		t.Fatalf("reorder queue: %v", err)
	}
	if len(reordered.Items) != 2 || reordered.Items[0].ID != second.ID ||
		reordered.Items[1].ID != first.ID {
		t.Fatalf("reordered queue = %+v", reordered.Items)
	}

	// A removed item keeps its audit row, so naming it again is a conflict
	// on both mutations, never "unknown".
	if _, err := service.DeleteRunQueueItem(
		ctx, principal, conversationID, first.ID,
		DeleteRunQueueItemInput{ExpectedRevision: reordered.Items[1].Revision},
	); err != nil {
		t.Fatalf("delete queue item: %v", err)
	}
	if _, err := service.DeleteRunQueueItem(
		ctx, principal, conversationID, first.ID,
		DeleteRunQueueItemInput{ExpectedRevision: reordered.Items[1].Revision + 1},
	); !errors.Is(err, ErrRunQueueConflict) {
		t.Fatalf("delete removed queue item error = %v, want %v", err, ErrRunQueueConflict)
	}
	if _, err := service.ReorderRunQueue(ctx, principal, conversationID, RunQueueOrderInput{
		Items: []RunQueueOrderItem{
			{ID: first.ID, ExpectedRevision: reordered.Items[1].Revision + 1},
			{ID: second.ID, ExpectedRevision: reordered.Items[0].Revision},
		},
	}); !errors.Is(err, ErrRunQueueConflict) {
		t.Fatalf("reorder naming removed item error = %v, want %v", err, ErrRunQueueConflict)
	}
}
