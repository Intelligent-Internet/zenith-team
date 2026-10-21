//go:build integration

package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func TestAppendTranscriptItemCommitsReplaysAndConflictsAtomically(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)

	var (
		publishedMu       sync.Mutex
		published         []TranscriptItem
		visibilityFailure error
	)
	service := h.svc
	if err := service.Bus.Subscribe(Topic, func(
		_ context.Context,
		event events.Event,
	) error {
		if event.Name != EventRunMessageCreated {
			return nil
		}
		item, err := events.PayloadOf[TranscriptItem](event)
		if err != nil {
			return err
		}
		payload := item.RunMessagePayload
		row, err := h.queries.GetRunItemByKey(
			context.Background(),
			generated.GetRunItemByKeyParams{RunID: runID, ItemKey: payload.ItemKey},
		)
		if err != nil {
			visibilityFailure = fmt.Errorf("load item during publish: %w", err)
			return nil
		}
		run, err := h.queries.GetRun(context.Background(), runID)
		if err != nil {
			visibilityFailure = fmt.Errorf("load Run head during publish: %w", err)
			return nil
		}
		if row.Seq != payload.Seq || run.TranscriptThroughSeq != payload.ThroughSeq {
			visibilityFailure = fmt.Errorf(
				"event became visible before commit: row seq %d, Run head %d, payload %+v",
				row.Seq, run.TranscriptThroughSeq, payload,
			)
			return nil
		}
		publishedMu.Lock()
		published = append(published, item)
		publishedMu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("subscribe run.message: %v", err)
	}

	firstInput := TranscriptItemInput{
		ItemKey: "item-1",
		Role:    protocol.RunItemRoleAgent,
		Parts: []protocol.RunItemPart{
			{Type: protocol.RunItemPartTypeText, Text: "Done.", Final: true},
			{
				Type: protocol.RunItemPartTypeToolCall, CallID: "call-1", Name: "read_file",
				Input: json.RawMessage(`{"path":"README.md","line":1}`),
			},
		},
	}
	first, err := h.svc.AppendTranscriptItem(
		ctx, h.fixture.RuntimeA, runID, firstInput,
	)
	if err != nil {
		t.Fatalf("AppendTranscriptItem(first) error = %v", err)
	}
	if visibilityFailure != nil {
		t.Fatal(visibilityFailure)
	}
	if first.ID == uuid.Nil || first.RunID != runID || first.ItemKey != "item-1" ||
		first.Seq != 1 || first.ThroughSeq != 1 || first.CommittedAt.IsZero() {
		t.Fatalf("first receipt = %+v", first)
	}
	row, err := h.queries.GetRunItemByKey(
		ctx,
		generated.GetRunItemByKeyParams{RunID: runID, ItemKey: first.ItemKey},
	)
	if err != nil {
		t.Fatalf("GetRunItemByKey(first) error = %v", err)
	}
	// The agent's own text is stored as 'agent_text' — the type that puts it
	// on the chat page — with its plain-text rendering in content, which is
	// what the terminal path reads back instead of copying it anywhere.
	if row.ID != first.ID || row.Seq != first.Seq || row.Type != "agent_text" ||
		row.AuthorKind != "agent" || row.AuthorID.UUID != h.fixture.AgentA ||
		row.Content != "Done." || row.RunID.UUID != runID ||
		!bytes.Equal(row.ContentSha256, first.ItemDigest[:]) {
		t.Fatalf("stored first item = %+v, receipt = %+v", row, first)
	}
	run, err := h.queries.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.TranscriptThroughSeq != first.ThroughSeq {
		t.Fatalf("Run transcript head = %d, receipt = %+v",
			run.TranscriptThroughSeq, first)
	}
	// The proven final answer is recorded on the Run for the channel publisher.
	if run.FinalAnswerItemKey.String != "item-1" {
		t.Fatalf("Run final answer item = %+v, want item-1", run.FinalAnswerItemKey)
	}
	if len(published) != 1 {
		t.Fatalf("run.message events = %d, want 1", len(published))
	}
	// A direct conversation's transcript is its creator's, and the routing
	// facts ride the item without reaching the wire.
	route := published[0]
	if route.CompanyID != h.fixture.CompanyA || route.ConversationKind == "task" ||
		route.ConversationCreatedBy == nil || *route.ConversationCreatedBy != h.fixture.UserA {
		t.Fatalf("run.message routing = %+v, want the direct conversation's creator", route)
	}
	if published[0].ID != first.ID.String() || published[0].RunID != runID ||
		published[0].ConversationID == "" ||
		published[0].ItemKey != first.ItemKey || published[0].Seq != 1 ||
		published[0].ThroughSeq != 1 ||
		published[0].ItemDigest != fmt.Sprintf("%x", first.ItemDigest) {
		t.Fatalf("run.message payload = %+v", published[0])
	}
	// The broadcast carries exactly the committed parts — normalized, with
	// canonicalized tool-call input — never the daemon's raw input bytes.
	assertBroadcastMatchesStoredParts(t, published[0].Parts, row.Parts)

	// JSON object ordering and whitespace do not create a false conflict.
	replayInput := firstInput
	replayInput.Parts = append([]protocol.RunItemPart(nil), firstInput.Parts...)
	replayInput.Parts[1].Input = json.RawMessage(` { "line" : 1, "path" : "README.md" } `)
	replay, err := h.svc.AppendTranscriptItem(
		ctx, h.fixture.RuntimeA, runID, replayInput,
	)
	if err != nil {
		t.Fatalf("AppendTranscriptItem(replay) error = %v", err)
	}
	if replay != first {
		t.Fatalf("immediate replay receipt = %+v, want %+v", replay, first)
	}
	if len(published) != 1 {
		t.Fatalf("replay published %d total events, want 1", len(published))
	}

	conflictInput := firstInput
	conflictInput.Parts = []protocol.RunItemPart{{
		Type: protocol.RunItemPartTypeText, Text: "Different",
	}}
	if _, err := h.svc.AppendTranscriptItem(
		ctx, h.fixture.RuntimeA, runID, conflictInput,
	); !errors.Is(err, ErrTranscriptItemConflict) {
		t.Fatalf("AppendTranscriptItem(conflict) error = %v, want ErrTranscriptItemConflict", err)
	}
	if len(published) != 1 {
		t.Fatalf("conflict published %d total events, want 1", len(published))
	}

	second, err := h.svc.AppendTranscriptItem(ctx, h.fixture.RuntimeA, runID, TranscriptItemInput{
		ItemKey: "item-2",
		Role:    protocol.RunItemRoleTool,
		Parts: []protocol.RunItemPart{{
			Type: protocol.RunItemPartTypeToolResult, CallID: "call-1", Name: "read_file",
			Output: "contents",
		}},
	})
	if err != nil {
		t.Fatalf("AppendTranscriptItem(second) error = %v", err)
	}
	if second.Seq != 2 || second.ThroughSeq != 2 {
		t.Fatalf("second receipt = %+v", second)
	}
	// A tool result is run detail, never the chat page: the type is what
	// decides that, and there is no column a writer could set wrong.
	toolRow, err := h.queries.GetRunItemByKey(
		ctx, generated.GetRunItemByKeyParams{RunID: runID, ItemKey: "item-2"},
	)
	if err != nil {
		t.Fatalf("GetRunItemByKey(second) error = %v", err)
	}
	if toolRow.Type != "tool_result" || toolRow.Content != "contents" {
		t.Fatalf("stored tool item = %+v, want a tool_result", toolRow)
	}
	// An item without a final part keeps the recorded answer.
	if run, err = h.queries.GetRun(ctx, runID); err != nil || run.FinalAnswerItemKey.String != "item-1" {
		t.Fatalf("Run final answer item after a tool result = %+v, %v", run.FinalAnswerItemKey, err)
	}
	if len(published) != 2 {
		t.Fatalf("fresh second item published %d events, want 2", len(published))
	}

	// A late replay preserves the existing row identity but acknowledges the
	// current durable Run head. No per-row prefix ledger exists.
	lateReplay, err := h.svc.AppendTranscriptItem(
		ctx, h.fixture.RuntimeA, runID, firstInput,
	)
	if err != nil {
		t.Fatalf("AppendTranscriptItem(late replay) error = %v", err)
	}
	if lateReplay.ID != first.ID || lateReplay.Seq != first.Seq ||
		lateReplay.ItemDigest != first.ItemDigest || lateReplay.ThroughSeq != second.ThroughSeq {
		t.Fatalf("late replay receipt = %+v; first = %+v; head = %+v", lateReplay, first, second)
	}
	if len(published) != 2 {
		t.Fatalf("late replay published %d events, want 2", len(published))
	}

	if _, err := h.svc.AppendTranscriptItem(
		ctx, uuid.New(), runID, TranscriptItemInput{
			ItemKey: "wrong-runtime", Role: protocol.RunItemRoleAgent,
			Parts: []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "no"}},
		},
	); !errors.Is(err, ErrTranscriptRunUnavailable) {
		t.Fatalf("wrong-runtime append error = %v, want ErrTranscriptRunUnavailable", err)
	}
	dbtestUpdateRunStatus(t, h, runID, "completed")
	if _, err := h.svc.AppendTranscriptItem(
		ctx, h.fixture.RuntimeA, runID, TranscriptItemInput{
			ItemKey: "terminal", Role: protocol.RunItemRoleAgent,
			Parts: []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "late"}},
		},
	); !errors.Is(err, ErrTranscriptRunUnavailable) {
		t.Fatalf("terminal append error = %v, want ErrTranscriptRunUnavailable", err)
	}
	if len(published) != 2 {
		t.Fatalf("rejected appends published %d events, want 2", len(published))
	}
}

func TestAppendTranscriptItemSerializesFreshSequencesPerRun(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)

	const count = 8
	receipts := make(chan TranscriptItemReceipt, count)
	errs := make(chan error, count)
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			receipt, err := h.svc.AppendTranscriptItem(
				ctx,
				h.fixture.RuntimeA,
				runID,
				TranscriptItemInput{
					ItemKey: fmt.Sprintf("concurrent-%d", index),
					Role:    protocol.RunItemRoleAgent,
					Parts: []protocol.RunItemPart{{
						Type: protocol.RunItemPartTypeText, Text: fmt.Sprintf("item %d", index),
					}},
				},
			)
			if err != nil {
				errs <- err
				return
			}
			receipts <- receipt
		}(index)
	}
	wait.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent AppendTranscriptItem() error = %v", err)
	}
	if t.Failed() {
		return
	}

	sequences := make([]int, 0, count)
	var head TranscriptItemReceipt
	for receipt := range receipts {
		sequences = append(sequences, int(receipt.Seq))
		if receipt.ThroughSeq > head.ThroughSeq {
			head = receipt
		}
	}
	sort.Ints(sequences)
	for index, sequence := range sequences {
		if sequence != index+1 {
			t.Fatalf("server-assigned sequences = %v, want 1..%d", sequences, count)
		}
	}
	if head.ThroughSeq != count {
		t.Fatalf("head receipt through seq = %d, want %d", head.ThroughSeq, count)
	}
	run, err := h.queries.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.TranscriptThroughSeq != count {
		t.Fatalf("Run head = %d, receipt = %+v", run.TranscriptThroughSeq, head)
	}
	if h.recorder.topicCount(EventRunMessageCreated) != count {
		t.Fatalf("run.message events = %d, want %d",
			h.recorder.topicCount(EventRunMessageCreated), count)
	}
}

func TestAppendTranscriptItemRollsBackWhenConversationRouteCannotBeResolved(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	base := h.svc
	routeErr := errors.New("conversation route unavailable")
	service := NewRunService(RunServiceOptions{
		Queries:    base.Queries,
		Transactor: base.Tx,
		Bus:        base.Bus,
		// A transcript append cannot learn its conversation's route from the
		// statement that writes it, so a failing resolver must roll the
		// append back rather than publish an undeliverable frame.
		RouteResolver: func(
			context.Context,
			*generated.Queries,
			uuid.UUID,
			uuid.UUID,
		) (ConversationRoute, error) {
			return ConversationRoute{}, routeErr
		},
	})

	_, err := service.AppendTranscriptItem(
		ctx,
		h.fixture.RuntimeA,
		runID,
		TranscriptItemInput{
			ItemKey: "rolled-back",
			Role:    protocol.RunItemRoleAgent,
			Parts: []protocol.RunItemPart{{
				Type: protocol.RunItemPartTypeText, Text: "must not commit",
			}},
		},
	)
	if !errors.Is(err, routeErr) {
		t.Fatalf("AppendTranscriptItem() error = %v, want route failure", err)
	}
	_, err = h.queries.GetRunItemByKey(
		ctx,
		generated.GetRunItemByKeyParams{RunID: runID, ItemKey: "rolled-back"},
	)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("rolled-back item lookup error = %v, want pgx.ErrNoRows", err)
	}
	run, err := h.queries.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	if run.TranscriptThroughSeq != 0 {
		t.Fatalf("rolled-back Run head = %d, want empty head",
			run.TranscriptThroughSeq)
	}
	if h.recorder.topicCount(EventRunMessageCreated) != 0 {
		t.Fatalf("rolled-back append published run.message")
	}
}

// The durable invariants the one stream still enforces in the database. The
// old run_messages row checks — non-negative seq, non-empty item key, role
// vocabulary, parts-is-a-non-empty-array, 32-byte digest — are deliberately
// gone: a CHECK is for a single column's vocabulary, and shape belongs to the
// service layer that writes it. What must stay in the database is what a
// concurrent writer could otherwise violate.
func TestTranscriptSchemaEnforcesStreamOrderingAndReplayFences(t *testing.T) {
	h := newRunHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runID := seedRunningTranscriptRun(t, h)
	run, err := h.queries.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	digest := make([]byte, 32)
	insertItem := func(seq int64, itemType, itemKey string) error {
		_, execErr := h.pool.Exec(
			ctx, `
			INSERT INTO conversation_items (
				company_id, conversation_id, run_id, seq, type, author_kind,
				author_id, item_key, content, parts, content_sha256
			) VALUES ($1, $2, $3, $4, $5, 'agent', $6, $7, 'x',
			          '[{"type":"text","text":"x"}]'::jsonb, $8)`,
			run.CompanyID, run.ConversationID, runID, seq, itemType,
			run.AgentID, itemKey, digest,
		)
		return execErr
	}

	if err := insertItem(1, "agent_text", "unique-item"); err != nil {
		t.Fatalf("insert valid stream item: %v", err)
	}
	// One ordering authority per Conversation.
	if err := insertItem(1, "agent_text", "other-item"); err == nil {
		t.Fatal("duplicate conversation seq was stored")
	}
	// The provider replay fence: re-appending an item after a reconnect must
	// raise rather than show the user the answer twice.
	if err := insertItem(2, "agent_text", "unique-item"); err == nil {
		t.Fatal("duplicate Run item key was stored")
	}
	// Single-column vocabularies stay in the database: a type outside the set
	// is what would leak an item into — or out of — the chat page.
	if err := insertItem(3, "vendor_shape", "vocabulary"); err == nil {
		t.Fatal("unknown conversation item type was stored")
	}
	if _, err := h.pool.Exec(
		ctx, `
		INSERT INTO conversation_items (
			company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		) VALUES ($1, $2, $3, 4, 'agent_text', 'vendor', $4, 'x')`,
		run.CompanyID, run.ConversationID, runID, run.AgentID,
	); err == nil {
		t.Fatal("unknown author kind was stored")
	}
	// The composite tenancy edge: an item cannot claim a Conversation in
	// another company.
	if _, err := h.pool.Exec(
		ctx, `
		INSERT INTO conversation_items (
			company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		) VALUES ($1, $2, $3, 5, 'agent_text', 'agent', $4, 'x')`,
		h.fixture.CompanyB, run.ConversationID, runID, run.AgentID,
	); err == nil {
		t.Fatal("cross-company conversation item was stored")
	}

	// run_id is a label, not an ownership edge: deleting the Run must never
	// delete the user's words. It carries no foreign key, so an unknown id is
	// accepted — that is the deliberate trade, and it is why the trigger read
	// keeps company and conversation in its predicate.
	if _, err := h.pool.Exec(
		ctx, `
		INSERT INTO conversation_items (
			company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		) VALUES ($1, $2, $3, 6, 'agent_text', 'agent', $4, 'x')`,
		run.CompanyID, run.ConversationID, uuid.New(), run.AgentID,
	); err != nil {
		t.Fatalf("run_id behaved like a foreign key: %v", err)
	}

	// The transcript head carries no CHECK any more — a digest length and a
	// non-negative counter are the shape of what the execution module writes,
	// not a vocabulary a caller chooses from — so the head is guarded by this
	// package, not by a constraint name no caller can act on.
	if _, err := h.pool.Exec(
		ctx, `
		UPDATE runs SET transcript_through_seq = -1 WHERE id = $1`, runID,
	); err != nil {
		t.Fatalf("transcript head still carries a range CHECK: %v", err)
	}
}

func seedRunningTranscriptRun(t *testing.T, h *runHarness) uuid.UUID {
	t.Helper()
	conversationID := h.seedDirectConversation(t)
	runID := h.seedQueuedRun(t, conversationID)
	dbtestUpdateRunStatus(t, h, runID, "running")
	return runID
}

func dbtestUpdateRunStatus(t *testing.T, h *runHarness, runID uuid.UUID, status string) {
	t.Helper()
	if _, err := h.pool.Exec(
		context.Background(),
		`UPDATE runs SET status = $2, updated_at = clock_timestamp() WHERE id = $1`,
		runID,
		status,
	); err != nil {
		t.Fatalf("update Run %d status to %s: %v", runID, status, err)
	}
}

// assertBroadcastMatchesStoredParts proves the run.message broadcast carries
// the committed row's parts. Both sides are compared through a neutral decode
// because jsonb storage does not preserve key order or whitespace.
func assertBroadcastMatchesStoredParts(
	t *testing.T,
	broadcast []protocol.RunTranscriptPart,
	stored []byte,
) {
	t.Helper()
	raw, err := json.Marshal(broadcast)
	if err != nil {
		t.Fatalf("marshal broadcast parts: %v", err)
	}
	var broadcastParts, storedParts []map[string]any
	if err := json.Unmarshal(raw, &broadcastParts); err != nil {
		t.Fatalf("decode broadcast parts: %v", err)
	}
	if err := json.Unmarshal(stored, &storedParts); err != nil {
		t.Fatalf("decode stored parts: %v", err)
	}
	if !reflect.DeepEqual(broadcastParts, storedParts) {
		t.Fatalf("broadcast parts = %s, stored parts = %s", raw, stored)
	}
}
