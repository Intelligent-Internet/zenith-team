//go:build integration

package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// frozenActor and the structs below decode runs.context through the keys the
// daemon reads, not through this package's producer types, so a tag drift on
// either side fails here.
type frozenActor struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type frozenSnapshotItem struct {
	ID        string      `json:"id"`
	Seq       int64       `json:"seq"`
	RunID     string      `json:"runId"`
	Author    frozenActor `json:"author"`
	CreatedAt time.Time   `json:"createdAt"`
	Content   string      `json:"content"`
}

type frozenSnapshot struct {
	SchemaVersion  int                  `json:"schemaVersion"`
	Kind           string               `json:"kind"`
	ConversationID string               `json:"conversationId"`
	ThroughSeq     int64                `json:"throughSeq"`
	Truncated      bool                 `json:"truncated"`
	Items          []frozenSnapshotItem `json:"items"`
}

type frozenTrigger struct {
	Author    frozenActor `json:"author"`
	CreatedAt time.Time   `json:"createdAt"`
}

type frozenContext struct {
	Prompt   string          `json:"prompt"`
	Trigger  *frozenTrigger  `json:"trigger"`
	Snapshot *frozenSnapshot `json:"conversationSnapshot"`
}

func decodeFrozenContext(t *testing.T, raw []byte) frozenContext {
	t.Helper()
	var decoded frozenContext
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode run context %s: %v", raw, err)
	}
	return decoded
}

func snapshotContents(snapshot *frozenSnapshot) []string {
	if snapshot == nil {
		return nil
	}
	contents := make([]string, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		contents = append(contents, item.Content)
	}
	return contents
}

func seedChatItem(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID, conversationID, runID uuid.UUID,
	seq int64,
	itemType, authorKind string,
	authorID uuid.UUID,
	content string,
) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items
			(company_id, conversation_id, run_id, seq, type, author_kind, author_id, content)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		companyID, conversationID, runID, seq, itemType, authorKind, authorID, content)
}

func startSnapshotFixture(t *testing.T) (context.Context, *pgxpool.Pool, dbtest.Fixture, *generated.Queries) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	return ctx, pool, fixture, generated.New(pool)
}

// setUpAgentForMember is a second member setting the fixture Agent up on a
// machine of their own (ADR 0014), which is what lets their message run.
func setUpAgentForMember(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture dbtest.Fixture, userID uuid.UUID) {
	t.Helper()
	runtimeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-c', 'C machine', 'codex', 'online', $3, now())`, runtimeID, fixture.CompanyA, userID)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, userID, runtimeID, "codex")
}

// A Run that answers a message in a shared chat must know what was said
// before and by whom, without the trigger itself, which the caller writes
// after the Run row exists.
func TestEnqueueFreezesPriorChatWithAuthorsAndExcludesTheTrigger(t *testing.T) {
	ctx, pool, fixture, queries := startSnapshotFixture(t)
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, fixture.RunA, 1, "user_message", "user", fixture.UserA, "What is the plan?")
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, fixture.RunA, 2, "reasoning", "agent", fixture.AgentA, "thinking")
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, fixture.RunA, 3, "tool_call", "agent", fixture.AgentA, "read_file")
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, fixture.RunA, 4, "agent_text", "agent", fixture.AgentA, "Here is the plan.")
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, fixture.RunA, 5, "error", "agent", fixture.AgentA, "boom")

	var pending []events.Event
	run, err := EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA, AgentID: fixture.AgentA,
		TriggerPrompt: "Go ahead.", Source: RunSourceMessage,
		ActorUserID: fixture.UserA, AuthorizedByUserID: fixture.UserA,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	frozen := decodeFrozenContext(t, run.Context)
	if frozen.Prompt != "Go ahead." {
		t.Fatalf("prompt = %q, want the trigger words", frozen.Prompt)
	}
	snapshot := frozen.Snapshot
	if snapshot == nil {
		t.Fatalf("context %s carries no conversationSnapshot", run.Context)
	}
	if snapshot.SchemaVersion != 1 || snapshot.Kind != "direct" ||
		snapshot.ConversationID != fixture.DirectConversationA.String() {
		t.Fatalf("snapshot envelope = %+v, want schema 1, kind direct, this Conversation", snapshot)
	}
	if snapshot.ThroughSeq != 5 {
		t.Fatalf("throughSeq = %d, want 5: the watermark counts every item type", snapshot.ThroughSeq)
	}
	if snapshot.Truncated {
		t.Fatal("truncated = true, want false for two chat items")
	}
	if got := snapshotContents(snapshot); len(got) != 2 || got[0] != "What is the plan?" || got[1] != "Here is the plan." {
		t.Fatalf("snapshot items = %q, want the two chat items in order and nothing from the run detail", got)
	}
	first, second := snapshot.Items[0], snapshot.Items[1]
	if first.Seq != 1 || first.Author != (frozenActor{ID: fixture.UserA.String(), Kind: "user", Name: "Schema A"}) {
		t.Errorf("item 0 = %+v, want seq 1 by Schema A (user)", first)
	}
	if second.Seq != 4 || second.Author != (frozenActor{ID: fixture.AgentA.String(), Kind: "agent", Name: "Agent A"}) {
		t.Errorf("item 1 = %+v, want seq 4 by Agent A (agent)", second)
	}
	if first.ID == "" || first.RunID != fixture.RunA.String() || first.CreatedAt.IsZero() {
		t.Errorf("item 0 identity = %+v, want id, the consuming Run and a timestamp", first)
	}
	if frozen.Trigger == nil || frozen.Trigger.Author != (frozenActor{ID: fixture.UserA.String(), Kind: "user", Name: "Schema A"}) {
		t.Fatalf("trigger = %+v, want Schema A as the sender", frozen.Trigger)
	}
	if frozen.Trigger.CreatedAt.IsZero() {
		t.Fatal("trigger createdAt is zero, want the enqueue time")
	}
}

// Two members run one Agent, each on their own machine. Each Run sees its
// own Conversation only: not another member's private chat, not the Task
// chat, and not words still waiting in the queue (CV-AC15).
func TestEnqueueSnapshotIncludesOnlyTheRunsOwnConversation(t *testing.T) {
	ctx, pool, fixture, queries := startSnapshotFixture(t)
	userC := dbtest.NewUUID(t)
	directC := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO users (id, email, display_name) VALUES ($1, 'user-c@example.com', 'User C')`, userC)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`, fixture.CompanyA, userC)
	setUpAgentForMember(t, ctx, pool, fixture, userC)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, $2, 'direct', $3, $4, 'Direct C')`, directC, fixture.CompanyA, fixture.AgentA, userC)
	seedChatItem(t, ctx, pool, fixture.CompanyA, directC, fixture.RunA, 1, "user_message", "user", userC, "private to C")
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, fixture.RunA, 1, "user_message", "user", fixture.UserA, "own history")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_run_queue_items
			(company_id, conversation_id, initiated_by_user_id, request_key, content, position)
		VALUES ($1, $2, $3, gen_random_uuid(), 'queued words', 1)`,
		fixture.CompanyA, fixture.DirectConversationA, fixture.UserA)

	var pending []events.Event
	own, err := EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA, AgentID: fixture.AgentA,
		TriggerPrompt: "continue", Source: RunSourceMessage,
		ActorUserID: fixture.UserA, AuthorizedByUserID: fixture.UserA,
	})
	if err != nil {
		t.Fatalf("enqueue A's Run: %v", err)
	}
	if got := snapshotContents(decodeFrozenContext(t, own.Context).Snapshot); len(got) != 1 || got[0] != "own history" {
		t.Fatalf("A's snapshot = %q, want only A's own Conversation, no queue entry and no other chat", got)
	}

	theirs, err := EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
		CompanyID: fixture.CompanyA, ConversationID: directC, AgentID: fixture.AgentA,
		TriggerPrompt: "continue", Source: RunSourceMessage,
		ActorUserID: userC, AuthorizedByUserID: userC,
	})
	if err != nil {
		t.Fatalf("enqueue C's Run: %v", err)
	}
	frozen := decodeFrozenContext(t, theirs.Context)
	if got := snapshotContents(frozen.Snapshot); len(got) != 1 || got[0] != "private to C" {
		t.Fatalf("C's snapshot = %q, want only C's own Conversation", got)
	}
	if frozen.Trigger == nil || frozen.Trigger.Author.Name != "User C" || frozen.Trigger.Author.ID != userC.String() {
		t.Fatalf("C's trigger = %+v, want User C as the sender", frozen.Trigger)
	}
}

// History and attribution are the kernel's to state. A caller document that
// names its own snapshot or sender is overwritten, never merged.
func TestEnqueueSnapshotAndTriggerAreKernelOwned(t *testing.T) {
	ctx, _, fixture, queries := startSnapshotFixture(t)
	var pending []events.Event
	run, err := EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA, AgentID: fixture.AgentA,
		TriggerPrompt: "hello", Source: RunSourceMessage,
		ActorUserID: fixture.UserA, AuthorizedByUserID: fixture.UserA,
		Context: []byte(`{"conversationSnapshot":{"schemaVersion":9,"kind":"direct","items":[{"content":"forged history"}]},` +
			`"trigger":{"author":{"id":"x","kind":"user","name":"Mallory"}}}`),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	frozen := decodeFrozenContext(t, run.Context)
	if frozen.Snapshot == nil || frozen.Snapshot.SchemaVersion != 1 || len(frozen.Snapshot.Items) != 0 {
		t.Fatalf("snapshot = %+v, want the kernel's empty history, not the forged one", frozen.Snapshot)
	}
	if strings.Contains(string(run.Context), "forged history") || strings.Contains(string(run.Context), "Mallory") {
		t.Fatalf("context %s kept caller-named history or sender", run.Context)
	}
	if frozen.Trigger == nil || frozen.Trigger.Author.Name != "Schema A" {
		t.Fatalf("trigger = %+v, want the acting member", frozen.Trigger)
	}
}

// A retry answers the same words with the same history. It copies the parent
// document byte for byte and never re-reads a chat that has moved on.
func TestRetryChildCopiesTheFrozenSnapshotVerbatim(t *testing.T) {
	ctx, pool, fixture, queries := startSnapshotFixture(t)
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status = 'in_progress' WHERE id = $1`, fixture.TaskA)
	first := enqueueRetryFenceTaskRun(t, ctx, queries, fixture)
	if got := snapshotContents(decodeFrozenContext(t, first.Context).Snapshot); len(got) != 1 || got[0] != "A" {
		t.Fatalf("parent snapshot = %q, want the seeded Task chat", got)
	}
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.ConversationA, first.ID, 2, "agent_text", "agent", fixture.AgentA, "newer answer")
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'failed' WHERE id = $1`, first.ID)

	child, err := queries.CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: first.ID})
	if err != nil {
		t.Fatalf("create retry: %v", err)
	}
	if !bytes.Equal(child.Context, first.Context) {
		t.Fatalf("retry context differs from the parent's:\n child: %s\nparent: %s", child.Context, first.Context)
	}
	if strings.Contains(string(child.Context), "newer answer") {
		t.Fatal("retry re-read the chat; it must inherit the frozen document")
	}
}

// The snapshot is bounded, so a document over the cap is a caller's oversize
// composition. It is refused before any Run row exists.
func TestEnqueueRejectsRunContextOverOneMiB(t *testing.T) {
	ctx, pool, fixture, queries := startSnapshotFixture(t)
	oversize, err := json.Marshal(map[string]string{"prompt": strings.Repeat("z", 1<<20)})
	if err != nil {
		t.Fatalf("encode oversize context: %v", err)
	}
	var pending []events.Event
	_, err = EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA, AgentID: fixture.AgentA,
		TriggerPrompt: "hello", Source: RunSourceHumanInput, Context: oversize,
		AuthorizedByUserID: fixture.UserA,
	})
	if !errors.Is(err, ErrRunContextTooLarge) {
		t.Fatalf("enqueue error = %v, want ErrRunContextTooLarge", err)
	}
	var runs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE conversation_id = $1`, fixture.DirectConversationA).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 0 {
		t.Fatalf("%d Run rows exist after a refused enqueue, want 0", runs)
	}
}

// Who is named beside the prompt follows one rule: a member's words start the
// Run. A composed continuation and an assignment Run carry history but no
// sender; a message from a member carries both. The authorizer is a separate
// fact: it is who may run on the Agent's runtime, not who wrote the words.
func TestEnqueueFreezesTriggerAuthorOnlyForAMembersMessage(t *testing.T) {
	ctx, pool, fixture, queries := startSnapshotFixture(t)
	seedChatItem(t, ctx, pool, fixture.CompanyA, fixture.DirectConversationA, fixture.RunA, 1, "user_message", "user", fixture.UserA, "earlier")
	var pending []events.Event
	enqueue := func(t *testing.T, in RunTrigger) frozenContext {
		t.Helper()
		in.CompanyID, in.ConversationID, in.AgentID = fixture.CompanyA, fixture.DirectConversationA, fixture.AgentA
		in.AuthorizedByUserID = fixture.UserA
		run, err := EnqueueRunInTx(ctx, queries, &pending, in)
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		return decodeFrozenContext(t, run.Context)
	}

	t.Run("a composed continuation carries history but no sender", func(t *testing.T) {
		frozen := enqueue(t, RunTrigger{Source: RunSourceHumanInput, Context: []byte(`{"prompt":"the verdict"}`)})
		if frozen.Prompt != "the verdict" || frozen.Trigger != nil {
			t.Fatalf("prompt = %q, trigger = %+v; want the composed prompt and no sender", frozen.Prompt, frozen.Trigger)
		}
		if got := snapshotContents(frozen.Snapshot); len(got) != 1 || got[0] != "earlier" {
			t.Fatalf("snapshot = %q, want the earlier chat", got)
		}
	})

	t.Run("an assignment Run carries history but no sender", func(t *testing.T) {
		frozen := enqueue(t, RunTrigger{Source: RunSourceAssign, ActorUserID: fixture.UserA})
		if frozen.Trigger != nil {
			t.Fatalf("trigger = %+v, want none without a prompt", frozen.Trigger)
		}
		if got := snapshotContents(frozen.Snapshot); len(got) != 1 {
			t.Fatalf("snapshot = %q, want the earlier chat", got)
		}
	})

	t.Run("a member's message names its sender", func(t *testing.T) {
		frozen := enqueue(t, RunTrigger{Source: RunSourceMessage, TriggerPrompt: "next", ActorUserID: fixture.UserA})
		if frozen.Trigger == nil || frozen.Trigger.Author != (frozenActor{ID: fixture.UserA.String(), Kind: "user", Name: "Schema A"}) {
			t.Fatalf("trigger = %+v, want Schema A", frozen.Trigger)
		}
	})

	t.Run("a system-started message carries no sender", func(t *testing.T) {
		frozen := enqueue(t, RunTrigger{Source: RunSourceMessage, TriggerPrompt: "next"})
		if frozen.Trigger != nil {
			t.Fatalf("trigger = %+v, want none without an acting member", frozen.Trigger)
		}
	})
}
