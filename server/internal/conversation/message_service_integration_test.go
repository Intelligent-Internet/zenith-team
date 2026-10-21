//go:build integration

package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestSendImmediateMessageLinksReadyAttachments(t *testing.T) {
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
	avatarURL := "https://cdn.example.test/schema-a.png"
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET avatar_url = $1 WHERE id = $2`, avatarURL, fixture.UserA)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service := newTestConversationService(t, store, bus)

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

	requestKey := dbtest.NewUUID(t)
	input := SendInput{
		RequestKey: requestKey, Content: "Inspect the attachment.",
		AttachmentIDs: []uuid.UUID{attachmentID},
	}
	result, err := service.Send(
		ctx,
		auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA},
		input,
	)
	if err != nil {
		t.Fatalf("send immediate Message with Attachment: %v", err)
	}
	if result.Disposition != SendDispositionMessage || result.RunID == nil ||
		*result.RunID == uuid.Nil || result.View.Message.RunID != *result.RunID ||
		result.View.Message.ID == uuid.Nil || result.View.Message.ID == requestKey ||
		result.View.Message.Type != ItemTypeUserMessage ||
		len(result.View.Attachments) != 1 || result.View.Attachments[0].ID != attachmentID {
		t.Fatalf("send result = %+v", result)
	}
	messageID := result.View.Message.ID
	runID := *result.RunID
	replay, err := service.Send(
		ctx,
		auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA},
		input,
	)
	if err != nil {
		t.Fatalf("replay immediate Message send: %v", err)
	}
	if replay.Created || replay.RunID == nil || *replay.RunID != runID ||
		replay.View.Message.ID != messageID {
		t.Fatalf("replayed immediate receipt = %+v", replay)
	}
	drifted := input
	drifted.Content = "Inspect a different attachment."
	if _, err := service.Send(
		ctx,
		auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA},
		drifted,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay immediate Message with drift error = %v, want %v", err, ErrConflict)
	}
	if result.View.Message.Author.DisplayName != "Schema A" ||
		result.View.Message.Author.AvatarURL == nil || *result.View.Message.Author.AvatarURL != avatarURL {
		t.Fatalf("send Message author = %+v", result.View.Message.Author)
	}

	dbtest.MustExec(
		t, ctx, pool, `
		INSERT INTO conversation_items (
			company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		)
		VALUES
			($1, $2, $3, 2, 'agent_text', 'agent', $4, 'Company agent'),
			($1, $2, $3, 3, 'agent_text', 'agent', $5, 'Foreign attribution'),
			($1, $2, $3, 4, 'tool_call', 'agent', $4, 'read_file')`,
		fixture.CompanyA,
		fixture.DirectConversationA,
		runID,
		fixture.AgentA,
		fixture.AgentB,
	)
	page, err := service.ListMessages(
		ctx,
		auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA},
		MessageListOptions{Limit: 10},
	)
	if err != nil {
		t.Fatalf("list Messages with actor projections: %v", err)
	}
	// The tool_call item at seq 4 is run detail, filtered out of the chat page
	// in SQL — but the watermark is the whole stream's head, so the bound still
	// moves past it.
	if len(page.Items) != 3 || page.Items[0].Message.Author.DisplayName != "Schema A" ||
		page.Items[1].Message.Author.DisplayName != "Agent A" ||
		page.Items[2].Message.Author.DisplayName != "Unknown" ||
		page.Items[1].Message.Author.AvatarURL != nil || page.Items[2].Message.Author.AvatarURL != nil ||
		page.Items[1].Message.Type != ItemTypeAgentText {
		t.Fatalf("Message actor projections = %+v", page.Items)
	}
	if page.EarliestAvailableSeq != 1 || page.ThroughSeq != 4 {
		t.Fatalf("Message page bounds = %+v", page)
	}

	var linkedPosition int32
	if err := pool.QueryRow(
		ctx, `
		SELECT position
		FROM message_attachments
		WHERE company_id = $1 AND message_id = $2 AND attachment_id = $3`,
		fixture.CompanyA,
		messageID,
		attachmentID,
	).Scan(&linkedPosition); err != nil {
		t.Fatalf("read Message Attachment link: %v", err)
	}
	if linkedPosition != 0 {
		t.Fatalf("Message Attachment position = %d, want 0", linkedPosition)
	}

	var runStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, runID).Scan(&runStatus); err != nil {
		t.Fatalf("read enqueued Run: %v", err)
	}
	if runStatus != "queued" {
		t.Fatalf("Run status = %q, want queued", runStatus)
	}
}

// A Conversation only takes a turn when it has an Agent: conversation_items
// .run_id is NOT NULL, so words nobody was asked to answer cannot be stored.
// A human Task with no creating Agent and no available CEO still refuses
// the send, rather than silently recording a message no Run will read.
func TestSendImmediateMessageWithoutResolvedAgentIsRefused(t *testing.T) {
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
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET executed_by_agent_id = NULL, assignee_user_id = $1
		WHERE id = $2`, fixture.UserA, fixture.TaskA)
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET archived_at=now() WHERE id=$1`, fixture.AgentA)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service := newTestConversationService(t, store, bus)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	input := SendInput{RequestKey: dbtest.NewUUID(t), Content: "Record this for review."}
	target := MessageTarget{Kind: TargetTask, ID: fixture.TaskA}

	if _, err := service.Send(ctx, principal, target, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("send to user-assigned Task error = %v, want %v", err, ErrConflict)
	}
	// Refused means nothing committed: no orphan item and no orphan Run.
	var itemCount, runCount int
	if err := pool.QueryRow(
		ctx, `
		SELECT count(*) FROM conversation_items WHERE conversation_id = $1 AND seq > 1`,
		fixture.ConversationA,
	).Scan(&itemCount); err != nil {
		t.Fatalf("count Conversation items: %v", err)
	}
	if err := pool.QueryRow(
		ctx, `
		SELECT count(*) FROM runs WHERE conversation_id = $1 AND status = 'queued'`,
		fixture.ConversationA,
	).Scan(&runCount); err != nil {
		t.Fatalf("count queued Runs: %v", err)
	}
	if itemCount != 0 || runCount != 0 {
		t.Fatalf("refused send left items=%d queued runs=%d, want 0 and 0", itemCount, runCount)
	}
}

// TestConversationListSummaryReturnsCompleteLatestRun pins the shared run
// aggregation (list, direct detail, queue mutations, and message sends all
// project it) at its own seam: the latest Run must arrive complete —
// timestamps, attempt ladder — not just as counts.
func TestConversationListSummaryReturnsCompleteLatestRun(t *testing.T) {
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

	listSummary, err := conversationListSummary(
		ctx, store.Queries(), fixture.UserA, fixture.ConversationA, fixture.CompanyA,
	)
	if err != nil {
		t.Fatalf("conversationListSummary() error = %v", err)
	}
	summary := listSummary.Runs
	if summary.QueuedCount != 0 || summary.ActiveCount != 0 || summary.Latest == nil ||
		summary.Latest.RunID != fixture.RunA || summary.Latest.AgentID != fixture.AgentA ||
		summary.Latest.CreatedAt.IsZero() || summary.Latest.Attempt < 1 ||
		summary.Latest.MaxAttempts < summary.Latest.Attempt {
		t.Fatalf("conversationListSummary().Runs = %+v", summary)
	}
}

func TestQueuedSendReturnsStableReceipt(t *testing.T) {
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

	seedRun(t, ctx, pool, fixture, fixture.DirectConversationA, "running")

	requestKey := dbtest.NewUUID(t)
	input := SendInput{RequestKey: requestKey, Content: "Queue this next."}
	target := MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA}
	first, err := service.Send(ctx, principal, target, input)
	if err != nil {
		t.Fatalf("queue Message send: %v", err)
	}
	// A deferred prompt has no Run: runs.id is a bigserial the promotion
	// transaction takes, so there is no Run identity to report until then.
	if first.Disposition != SendDispositionQueued || first.QueueItem == nil ||
		first.RunID != nil ||
		first.QueueItem.ID == requestKey || first.QueueItem.MessageID == requestKey ||
		first.QueueItem.ID == first.QueueItem.MessageID {
		t.Fatalf("fresh queued receipt = %+v", first)
	}
	queueItemID := first.QueueItem.ID
	messageID := first.QueueItem.MessageID

	replay, err := service.Send(ctx, principal, target, input)
	if err != nil {
		t.Fatalf("replay queued Message send: %v", err)
	}
	if replay.Disposition != SendDispositionQueued || replay.QueueItem == nil ||
		replay.RunID != nil ||
		replay.QueueItem.ID != queueItemID || replay.QueueItem.MessageID != messageID {
		t.Fatalf("replayed queued receipt = %+v", replay)
	}
	drifted := input
	drifted.Content = "Queue something else."
	if _, err := service.Send(ctx, principal, target, drifted); !errors.Is(err, ErrRunQueueConflict) {
		t.Fatalf("replay queued Message with drift error = %v, want %v", err, ErrRunQueueConflict)
	}

	if _, err := service.DeleteRunQueueItem(
		ctx,
		principal,
		fixture.DirectConversationA,
		queueItemID,
		DeleteRunQueueItemInput{ExpectedRevision: replay.QueueItem.Revision},
	); err != nil {
		t.Fatalf("delete queued Message: %v", err)
	}
	removed, err := service.Send(ctx, principal, target, input)
	if err != nil {
		t.Fatalf("replay removed Message send: %v", err)
	}
	if removed.Disposition != SendDispositionRemoved || removed.RunID != nil {
		t.Fatalf("removed queued receipt = %+v", removed)
	}
}

// TestConversationSummaryCountsUseTheOneRunCountingVocabulary pins the API's
// run-counting vocabulary at the SQL seam every summary shares: queuedCount is
// queued + deferred (enqueued work no machine has claimed, including retry
// backoff), activeCount is dispatched + waiting_local_directory + running, and
// a terminal run is in neither. Both the single-conversation base and the
// bulk list base must agree.
func TestConversationSummaryCountsUseTheOneRunCountingVocabulary(t *testing.T) {
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

	for _, status := range []string{"queued", "deferred", "running", "completed"} {
		dbtest.MustExec(
			t, ctx, pool, `
			INSERT INTO runs (
				company_id, agent_id, conversation_id, runtime_id, status, fire_at
			, run_type)
			VALUES (
				$1, $2, $3, $4, $5,
				CASE WHEN $5 = 'deferred' THEN now() + interval '1 minute' END
			, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))`,
			fixture.CompanyA, fixture.AgentA,
			fixture.DirectConversationA, fixture.RuntimeA, status,
		)
	}

	summary, err := conversationListSummary(
		ctx, store.Queries(), fixture.UserA, fixture.DirectConversationA, fixture.CompanyA,
	)
	if err != nil {
		t.Fatalf("conversationListSummary() error = %v", err)
	}
	if summary.Runs.QueuedCount != 2 || summary.Runs.ActiveCount != 1 {
		t.Fatalf("summary counts = queued %d / active %d, want 2 / 1",
			summary.Runs.QueuedCount, summary.Runs.ActiveCount)
	}

	bases, err := store.Queries().ListDirectConversationSummaryBasesBulk(
		ctx,
		generatedListDirectConversationSummaryBasesBulkParams(fixture),
	)
	if err != nil {
		t.Fatalf("ListDirectConversationSummaryBasesBulk() error = %v", err)
	}
	if len(bases) != 1 || bases[0].QueuedRunCount != 2 || bases[0].ActiveRunCount != 1 {
		t.Fatalf("bulk summary bases = %+v, want queued 2 / active 1", bases)
	}

	runBases, err := store.Queries().ListRunConversationSummaryBasesBulk(
		ctx,
		generated.ListRunConversationSummaryBasesBulkParams{
			ConversationIds: []uuid.UUID{fixture.DirectConversationA},
			CompanyID:       fixture.CompanyA,
		},
	)
	if err != nil {
		t.Fatalf("ListRunConversationSummaryBasesBulk() error = %v", err)
	}
	if len(runBases) != 1 || runBases[0].QueuedRunCount != 2 || runBases[0].ActiveRunCount != 1 {
		t.Fatalf("run summary bases = %+v, want queued 2 / active 1", runBases)
	}
}

func generatedListDirectConversationSummaryBasesBulkParams(
	fixture dbtest.Fixture,
) generated.ListDirectConversationSummaryBasesBulkParams {
	return generated.ListDirectConversationSummaryBasesBulkParams{
		ActorUserID:     fixture.UserA,
		ConversationIds: []uuid.UUID{fixture.DirectConversationA},
		CompanyID:       fixture.CompanyA,
	}
}

// Onboarding hands the CEO Agent its opening brief, which the founder never
// typed. The Run must still start and carry the words, but the transcript must
// stay empty of a message the founder would not recognize as their own.
func TestSuppressedSendStartsTheRunWithoutWritingATranscriptMessage(t *testing.T) {
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

	const prompt = "Research my business, then set up my business context."
	result, err := service.Send(
		ctx,
		auth.UserPrincipal{
			UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
		},
		MessageTarget{Kind: TargetConversation, ID: fixture.DirectConversationA},
		SendInput{
			RequestKey: dbtest.NewUUID(t), Content: prompt, SuppressUserMessage: true,
		},
	)
	if err != nil {
		t.Fatalf("suppressed send: %v", err)
	}
	if !result.Suppressed || result.RunID == nil || *result.RunID == uuid.Nil {
		t.Fatalf("suppressed send result = %+v", result)
	}
	if result.View.Message.ID != uuid.Nil {
		t.Fatalf("suppressed send returned a message view: %+v", result.View)
	}

	var messageCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM conversation_items
		WHERE conversation_id = $1 AND type = 'user_message'`,
		fixture.DirectConversationA,
	).Scan(&messageCount); err != nil {
		t.Fatalf("count transcript messages: %v", err)
	}
	if messageCount != 0 {
		t.Fatalf("transcript holds %d user messages, want none", messageCount)
	}

	// The Agent still receives the instruction: it is frozen into the Run.
	var frozen string
	if err := pool.QueryRow(ctx, `
		SELECT context->>'prompt' FROM runs WHERE id = $1`, *result.RunID,
	).Scan(&frozen); err != nil {
		t.Fatalf("read frozen Run prompt: %v", err)
	}
	if frozen != prompt {
		t.Fatalf("frozen Run prompt = %q, want %q", frozen, prompt)
	}
}

// A suppressed send writes no message, so the Attachments it names are never
// linked, and linking is what checks them. A Task Run must not freeze them
// either, or a member could hand their Agent another member's upload by
// suppressing the message a plain send is refused for.
func TestSuppressedTaskSendFreezesNoAttachments(t *testing.T) {
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

	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, fixture.UserB)
	seed := func(uploaderID uuid.UUID, filename string) uuid.UUID {
		t.Helper()
		id := dbtest.NewUUID(t)
		dbtest.MustExec(t, ctx, pool, `
			INSERT INTO attachments (
				id, company_id, uploaded_by_user_id, request_key, filename,
				content_type, size_bytes, status, version
			)
			VALUES ($1, $2, $3, $4, $5, 'text/plain', 5, 'ready', 2)`,
			id, fixture.CompanyA, uploaderID, dbtest.NewUUID(t), filename)
		return id
	}
	own := seed(fixture.UserA, "brief.txt")
	others := seed(fixture.UserB, "payroll.txt")
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	target := MessageTarget{Kind: TargetTask, ID: fixture.TaskA}

	if _, err := service.Send(ctx, principal, target, SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Read this.",
		AttachmentIDs: []uuid.UUID{others},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("send with another member's upload error = %v, want %v", err, ErrConflict)
	}
	result, err := service.Send(ctx, principal, target, SendInput{
		RequestKey: dbtest.NewUUID(t), Content: "Read this.",
		AttachmentIDs: []uuid.UUID{own, others}, SuppressUserMessage: true,
	})
	if err != nil {
		t.Fatalf("suppressed Task send: %v", err)
	}
	if !result.Suppressed || result.RunID == nil {
		t.Fatalf("suppressed Task send result = %+v", result)
	}
	var frozen *string
	if err := pool.QueryRow(ctx, `
		SELECT context->>'taskAttachments' FROM runs WHERE id = $1`, *result.RunID,
	).Scan(&frozen); err != nil {
		t.Fatalf("read frozen Task attachments: %v", err)
	}
	if frozen != nil {
		t.Fatalf("suppressed Task send froze taskAttachments %s, want none", *frozen)
	}
	var linked int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM message_attachments WHERE attachment_id = ANY($1::uuid[])`,
		[]uuid.UUID{own, others},
	).Scan(&linked); err != nil {
		t.Fatalf("count Attachment links: %v", err)
	}
	if linked != 0 {
		t.Fatalf("suppressed Task send linked %d Attachments, want none", linked)
	}
}
