//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// frozenChat is the part of runs.context these tests read back: the chat the
// Run carries and the member it answers, decoded through the daemon's keys.
type frozenChat struct {
	Trigger *struct {
		Author struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"author"`
	} `json:"trigger"`
	Snapshot struct {
		Kind           string `json:"kind"`
		ConversationID string `json:"conversationId"`
		ThroughSeq     int64  `json:"throughSeq"`
		Items          []struct {
			Seq    int64 `json:"seq"`
			Author struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"author"`
			Content string `json:"content"`
		} `json:"items"`
	} `json:"conversationSnapshot"`
}

func decodeFrozenChat(t *testing.T, raw []byte) frozenChat {
	t.Helper()
	var decoded frozenChat
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode run context %s: %v", raw, err)
	}
	return decoded
}

func frozenContents(chat frozenChat) []string {
	contents := make([]string, 0, len(chat.Snapshot.Items))
	for _, item := range chat.Snapshot.Items {
		contents = append(contents, item.Content)
	}
	return contents
}

// Two members and one Agent in a Task chat. The second member's Run carries
// what the first member and the Agent said, each under its author's name, and
// names the second member as the person it answers.
func TestSendFreezesPriorChatAndSenderIntoRunContext(t *testing.T) {
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
	userC := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'user-c@example.com', 'User C')`,
		userC,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA,
		userC,
	)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, userC)
	target := MessageTarget{Kind: TargetTask, ID: fixture.TaskA}

	first, err := service.Send(ctx,
		auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner},
		target, SendInput{RequestKey: dbtest.NewUUID(t), Content: "Please migrate the auth module."},
	)
	if err != nil {
		t.Fatalf("member A sends: %v", err)
	}
	if first.Disposition != SendDispositionMessage || first.RunID == nil {
		t.Fatalf("member A's send = %+v, want an immediate Message with a Run", first)
	}
	// The Agent answers and the Run ends, so the next send starts a Run of its
	// own instead of joining the queue.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items
			(company_id, conversation_id, run_id, seq, type, author_kind, author_id, content)
		VALUES ($1, $2, $3, 3, 'agent_text', 'agent', $4, 'Done; see PR #12.')`,
		fixture.CompanyA, fixture.ConversationA, *first.RunID, fixture.AgentA)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, *first.RunID)
	// The Task moves to member C, who has the Agent set up on their own
	// computer: their message runs there (ADR 0014).
	runtimeC := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-c', 'User C laptop', 'codex', 'online', $3, now())`,
		runtimeC, fixture.CompanyA, userC)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, userC, runtimeC, "codex")
	dbtest.MustExec(t, ctx, pool,
		`UPDATE tasks SET assignee_user_id = $2, version = version + 1 WHERE id = $1`,
		fixture.TaskA, userC)

	second, err := service.Send(ctx,
		auth.UserPrincipal{UserID: userC, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember},
		target, SendInput{RequestKey: dbtest.NewUUID(t), Content: "Can you also update the README?"},
	)
	if err != nil {
		t.Fatalf("member C sends: %v", err)
	}
	if second.Disposition != SendDispositionMessage || second.RunID == nil {
		t.Fatalf("member C's send = %+v, want an immediate Message with a Run", second)
	}
	run, err := store.Queries().GetRun(ctx, *second.RunID)
	if err != nil {
		t.Fatalf("read member C's Run: %v", err)
	}
	chat := decodeFrozenChat(t, run.Context)
	if got := frozenContents(chat); len(got) != 3 || got[0] != "A" ||
		got[1] != "Please migrate the auth module." || got[2] != "Done; see PR #12." {
		t.Fatalf("frozen chat = %q, want the seeded message, A's message and the Agent's answer in order", got)
	}
	if chat.Snapshot.Kind != "task" || chat.Snapshot.ConversationID != fixture.ConversationA.String() ||
		chat.Snapshot.ThroughSeq != 3 {
		t.Fatalf("snapshot envelope = %+v, want the Task Conversation through seq 3", chat.Snapshot)
	}
	items := chat.Snapshot.Items
	if items[1].Author.Name != "Schema A" || items[1].Author.Kind != "user" ||
		items[2].Author.Name != "Agent A" || items[2].Author.Kind != "agent" {
		t.Fatalf(
			"authors = %+v / %+v, want Schema A (user) and Agent A (agent)",
			items[1].Author,
			items[2].Author,
		)
	}
	if chat.Trigger == nil || chat.Trigger.Author.ID != userC.String() ||
		chat.Trigger.Author.Kind != "user" || chat.Trigger.Author.Name != "User C" {
		t.Fatalf("trigger = %+v, want User C as the sender", chat.Trigger)
	}
	for _, content := range frozenContents(chat) {
		if content == "Can you also update the README?" {
			t.Fatal("the trigger words appear in the snapshot; they must arrive once, as the prompt")
		}
	}
}

// A queued prompt is promoted after the current Run ends. Its snapshot is taken
// then, not when the prompt was accepted: it holds the answer that arrived in
// between, and never the queued words themselves or the prompt still waiting.
func TestRunQueuePromotionFreezesTheSnapshotAtPromotionTime(t *testing.T) {
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
	runService := execution.NewRunService(execution.RunServiceOptions{
		Queries: store.Queries(), Transactor: store, Bus: bus, RouteResolver: ResolveConversationRoute,
	})
	runService.SetRunQueuePromoter(execution.RunQueuePromoterFunc(func(
		ctx context.Context, q *generated.Queries, pending *[]events.Event, run generated.Run, output string,
	) (*uuid.UUID, error) {
		promotion, err := FinalizeRunConversationInTx(ctx, q, pending, testTaskMessageRuns(), run, output)
		return promotion.RunID, err
	}))

	currentRunID := seedRun(t, ctx, pool, fixture, fixture.DirectConversationA, "running")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items
			(company_id, conversation_id, run_id, seq, type, author_kind, author_id, content)
		VALUES ($1, $2, $3, 1, 'user_message', 'user', $4, 'the running question')`,
		fixture.CompanyA, fixture.DirectConversationA, currentRunID, fixture.UserA)
	first, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA,
		ActorUserID: fixture.UserA, RequestKey: dbtest.NewUUID(t), Content: "queued first",
	})
	if err != nil {
		t.Fatalf("insert first queue item: %v", err)
	}
	if _, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA,
		ActorUserID: fixture.UserA, RequestKey: dbtest.NewUUID(t), Content: "queued second",
	}); err != nil {
		t.Fatalf("insert second queue item: %v", err)
	}

	receipt, err := runService.AppendTranscriptItem(
		ctx,
		fixture.RuntimeA,
		currentRunID,
		execution.TranscriptItemInput{
			ItemKey: "completed-answer",
			Role:    protocol.RunItemRoleAgent,
			Parts:   []protocol.RunItemPart{{Type: protocol.RunItemPartTypeText, Text: "completed answer"}},
		},
	)
	if err != nil {
		t.Fatalf("append current run transcript: %v", err)
	}
	if _, err := runService.CompleteRun(ctx, currentRunID, execution.TerminalTranscriptProof{
		ThroughSeq: receipt.ThroughSeq, FinalItemKey: receipt.ItemKey,
	}, "", false, ""); err != nil {
		t.Fatalf("complete current run: %v", err)
	}

	var promotedRunID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT run_id FROM conversation_items WHERE id = $1`, first.MessageID).
		Scan(&promotedRunID); err != nil {
		t.Fatalf("read promoted user item run_id: %v", err)
	}
	promoted, err := store.Queries().GetRun(ctx, promotedRunID)
	if err != nil {
		t.Fatalf("read promoted Run: %v", err)
	}
	chat := decodeFrozenChat(t, promoted.Context)
	if got := frozenContents(
		chat,
	); len(got) != 2 || got[0] != "the running question" ||
		got[1] != "completed answer" {
		t.Fatalf(
			"promoted snapshot = %q, want the running Run's question and its answer, taken at promotion",
			got,
		)
	}
	if chat.Snapshot.ThroughSeq != 2 {
		t.Fatalf("throughSeq = %d, want 2, the watermark before the promoted words", chat.Snapshot.ThroughSeq)
	}
	if chat.Trigger == nil || chat.Trigger.Author.Name != "Schema A" ||
		chat.Trigger.Author.ID != fixture.UserA.String() {
		t.Fatalf("promoted trigger = %+v, want the member who queued the prompt", chat.Trigger)
	}
}
