//go:build integration

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/canvas"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestEnqueueRejectsTaskConversationMismatchWithoutAdvancingGeneration(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)

	var before int64
	if err := pool.QueryRow(
		ctx, `SELECT execution_generation FROM tasks WHERE id=$1`, fixture.TaskA,
	).Scan(&before); err != nil {
		t.Fatalf("read Task generation before mismatched enqueue: %v", err)
	}
	if _, err := EnqueueRunInTx(ctx, queries, nil, RunTrigger{
		CompanyID: fixture.CompanyA, ConversationID: fixture.DirectConversationA,
		AgentID: fixture.AgentA, TaskID: util.NullUUIDFromPtr(&fixture.TaskA),
		Source: RunSourceMessage, AuthorizedByUserID: fixture.UserA,
	}); err == nil {
		t.Fatal("EnqueueRunInTx() accepted a Task with another Conversation")
	}
	var after int64
	if err := pool.QueryRow(
		ctx, `SELECT execution_generation FROM tasks WHERE id=$1`, fixture.TaskA,
	).Scan(&after); err != nil {
		t.Fatalf("read Task generation after mismatched enqueue: %v", err)
	}
	if after != before {
		t.Fatalf("mismatched enqueue advanced Task generation from %d to %d", before, after)
	}
}

// A Run's input is its own first 'user_message' item, reached by run id.
// runs.trigger_message_id and the composite foreign key that scoped it are
// both gone, and so is the enqueue-time scope check they needed: the Run row
// is written first and the item is written after, carrying that run_id, so
// the Run's Conversation is the Run's by construction.
//
// What survives is the scoping on the READ. conversation_items.run_id carries
// no foreign key — it is a label, so nothing stops a row in another
// Conversation from wearing this Run's id — and GetRunTriggerMessageContent
// keeps company and conversation in its predicate for exactly that reason.
// This proves the predicate does its job: ErrNoRows there means "out of
// scope", not merely "missing".
func TestRunTriggerContentIsFencedToTheRunsOwnConversation(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)

	var pending []events.Event
	run, err := EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
		CompanyID:          fixture.CompanyA,
		ConversationID:     fixture.DirectConversationA,
		AgentID:            fixture.AgentA,
		TriggerPrompt:      "scope probe",
		Source:             RunSourceMessage,
		AuthorizedByUserID: fixture.UserA,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// A foreign item wearing this Run's label, in a Conversation the Run does
	// not belong to. run_id has no foreign key, so the database accepts it.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items
			(id, company_id, conversation_id, run_id, seq, type, author_kind,
			 author_id, content)
		VALUES ($1, $2, $3, $4, 9, 'user_message', 'user', $5, 'another conversation')`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.ConversationA, run.ID, fixture.UserA)

	if _, err := queries.GetRunTriggerMessageContent(
		ctx,
		generated.GetRunTriggerMessageContentParams{
			RunID:          run.ID,
			CompanyID:      fixture.CompanyA,
			ConversationID: fixture.DirectConversationA,
		},
	); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("out-of-scope trigger read error = %v, want pgx.ErrNoRows", err)
	}

	// The Run's own words, written after the Run as production writes them,
	// are what the scoped read returns.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items
			(id, company_id, conversation_id, run_id, seq, type, author_kind,
			 author_id, content)
		VALUES ($1, $2, $3, $4, 1, 'user_message', 'user', $5, 'scope probe')`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.DirectConversationA, run.ID, fixture.UserA)

	content, err := queries.GetRunTriggerMessageContent(
		ctx,
		generated.GetRunTriggerMessageContentParams{
			RunID:          run.ID,
			CompanyID:      fixture.CompanyA,
			ConversationID: fixture.DirectConversationA,
		},
	)
	if err != nil {
		t.Fatalf("in-scope trigger read error = %v", err)
	}
	if content != "scope probe" {
		t.Fatalf("trigger content = %q, want the Run's own words", content)
	}
}

// A Run is frozen input. The enqueue kernel stamps the trigger message's
// content into the context document as `prompt`, so the daemon never fetches
// the prompt back through the user plane at claim time — the fetch was the
// seam that once delivered a generic continue instruction instead of the
// user's words, and it cannot see another member's direct conversation at
// all. Human-input continuations already compose their own prompt; the
// kernel must preserve it.
func TestEnqueueFreezesTriggerPromptIntoRunContext(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)

	promptOf := func(t *testing.T, run generated.Run) (string, bool) {
		t.Helper()
		if len(run.Context) == 0 {
			return "", false
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal(run.Context, &document); err != nil {
			t.Fatalf("decode run context %s: %v", run.Context, err)
		}
		raw, ok := document["prompt"]
		if !ok {
			return "", false
		}
		var prompt string
		if err := json.Unmarshal(raw, &prompt); err != nil {
			t.Fatalf("decode prompt %s: %v", raw, err)
		}
		return prompt, true
	}
	trigger := func(prompt string) RunTrigger {
		return RunTrigger{
			CompanyID:          fixture.CompanyA,
			ConversationID:     fixture.DirectConversationA,
			AgentID:            fixture.AgentA,
			TriggerPrompt:      prompt,
			Source:             RunSourceMessage,
			AuthorizedByUserID: fixture.UserA,
		}
	}
	var pending []events.Event

	t.Run("the trigger message's content is frozen as the prompt", func(t *testing.T) {
		run, err := EnqueueRunInTx(ctx, queries, &pending, trigger("just tell me a joke"))
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		prompt, ok := promptOf(t, run)
		if !ok || prompt != "just tell me a joke" {
			t.Fatalf("frozen prompt = %q (present=%v), want the trigger message content", prompt, ok)
		}
	})

	t.Run("a caller-composed prompt wins over the trigger message", func(t *testing.T) {
		in := trigger("the raw user words")
		in.Context = []byte(`{"prompt":"the approval verdict, composed by the caller"}`)
		in.Source = RunSourceHumanInput
		run, err := EnqueueRunInTx(ctx, queries, &pending, in)
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		prompt, ok := promptOf(t, run)
		if !ok || prompt != "the approval verdict, composed by the caller" {
			t.Fatalf("frozen prompt = %q (present=%v), want the caller's composition preserved", prompt, ok)
		}
	})

	t.Run("an assignment run carries no prompt", func(t *testing.T) {
		in := trigger("")
		in.Source = RunSourceAssign
		run, err := EnqueueRunInTx(ctx, queries, &pending, in)
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if prompt, ok := promptOf(t, run); ok {
			t.Fatalf("assignment run froze prompt %q, want none — it has no prompt message", prompt)
		}
	})
}

// The daemon decodes context.agent (name, instructions, model, thinkingLevel)
// and has since day one — but nothing ever wrote it, so a user's configured
// model and instructions never reached the provider. The enqueue kernel now
// freezes the agent's execution identity into the context in the SAME
// transaction that creates the run, as it already freezes the prompt: a
// later agent edit must not change what an already-enqueued run
// executes with, and a claim must serve the enqueue-time brief.
func TestEnqueueFreezesAgentBriefIntoRunContext(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	queries := generated.New(pool)

	// Give the fixture agent a full brief so every member's freeze is proved.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents
		SET instructions = 'ship the roadmap', model = 'model-at-enqueue', thinking_level = 'high',
            settings = jsonb_build_object('capabilities', jsonb_build_object('enabledTools', jsonb_build_array('zenith-studio'), 'disabledBuiltIns', jsonb_build_array('planning'), 'disabledPlugins', '[]'::jsonb, 'disabledMcpServers', '[]'::jsonb))
		WHERE id = $1`, fixture.AgentA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE tasks
		SET execution_generation = 7, status = 'in_progress'
		WHERE id = $1`, fixture.TaskA)

	var pending []events.Event
	run, err := EnqueueRunInTx(ctx, queries, &pending, RunTrigger{
		CompanyID:      fixture.CompanyA,
		ConversationID: fixture.ConversationA,
		AgentID:        fixture.AgentA,
		// ConversationA is a task conversation, so the trigger carries its
		// task exactly as the message path does: the capacity lane is read
		// from the conversation, and a task run must hold a task slot.
		TaskID:             util.NullUUIDFromPtr(&fixture.TaskA),
		TriggerPrompt:      "freeze me",
		Source:             RunSourceMessage,
		AuthorizedByUserID: fixture.UserA,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// The Studio lock reads this column, never the frozen context document.
	if !run.StudioEnabled {
		t.Fatal("Run frozen with the zenith-studio tool has studio_enabled = false")
	}
	if !run.TaskExecutionGeneration.Valid || run.TaskExecutionGeneration.Int64 != 8 {
		t.Fatalf(
			"task execution generation = %+v, want fresh generation 8",
			run.TaskExecutionGeneration,
		)
	}
	var taskGeneration int64
	if err := pool.QueryRow(
		ctx, `SELECT execution_generation FROM tasks WHERE id = $1`, fixture.TaskA,
	).Scan(&taskGeneration); err != nil {
		t.Fatalf("read advanced Task execution generation: %v", err)
	}
	if taskGeneration != run.TaskExecutionGeneration.Int64 {
		t.Fatalf("Task generation = %d, Run generation = %d", taskGeneration, run.TaskExecutionGeneration.Int64)
	}

	type capabilityBrief struct {
		EnabledTools     [1]string `json:"enabledTools"`
		DisabledBuiltIns [1]string `json:"disabledBuiltIns"`
	}
	type agentBrief struct {
		Capabilities  capabilityBrief `json:"capabilities"`
		Name          string          `json:"name"`
		Title         string          `json:"title"`
		Instructions  string          `json:"instructions"`
		Model         string          `json:"model"`
		ThinkingLevel string          `json:"thinkingLevel"`
	}
	briefOf := func(t *testing.T, context []byte) agentBrief {
		t.Helper()
		var document struct {
			Agent *agentBrief `json:"agent"`
		}
		if err := json.Unmarshal(context, &document); err != nil {
			t.Fatalf("decode run context %s: %v", context, err)
		}
		if document.Agent == nil {
			t.Fatalf("run context %s carries no agent brief", context)
		}
		return *document.Agent
	}
	wantBrief := agentBrief{
		Capabilities: capabilityBrief{
			EnabledTools:     [1]string{"zenith-studio"},
			DisabledBuiltIns: [1]string{"planning"},
		},
		Name:          "Agent A",
		Title:         "CEO",
		Instructions:  "ship the roadmap",
		Model:         "model-at-enqueue",
		ThinkingLevel: "high",
	}
	if got := briefOf(t, run.Context); got != wantBrief {
		t.Fatalf("enqueue-time brief = %+v, want %+v", got, wantBrief)
	}

	// Mutating the agent AFTER enqueue must not change the frozen brief the
	// claim serves — frozen input must not depend on a later read.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE agents
		SET name = 'Renamed', instructions = 'new orders', model = 'model-after-edit', thinking_level = 'low', settings = '{}'::jsonb
		WHERE id = $1`, fixture.AgentA)

	// TaskLaneOpen is the caller's capacity verdict, computed under the
	// agent's claim lock; this agent has a free slot.
	claimed, err := queries.ClaimRun(ctx, generated.ClaimRunParams{
		AgentID: fixture.AgentA, RuntimeID: fixture.RuntimeA, PrepareLeaseSecs: 45,
		RunID: run.ID,
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != run.ID {
		t.Fatalf("claimed run %d, want the enqueued run %d", claimed.ID, run.ID)
	}
	if got := briefOf(t, claimed.Context); got != wantBrief {
		t.Fatalf("claimed brief = %+v, want the enqueue-time brief %+v — the edit leaked", got, wantBrief)
	}

	// A retry child inherits the frozen brief through CreateRetryRun's
	// context copy, exactly as it inherits the frozen prompt.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status = 'failed', updated_at = now() WHERE id = $1`, run.ID)
	child, err := queries.CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: run.ID})
	if err != nil {
		t.Fatalf("create retry child: %v", err)
	}
	if got := briefOf(t, child.Context); got != wantBrief {
		t.Fatalf("retry child brief = %+v, want the inherited enqueue-time brief %+v", got, wantBrief)
	}
	if !child.TaskExecutionGeneration.Valid ||
		child.TaskExecutionGeneration.Int64 != run.TaskExecutionGeneration.Int64 {
		t.Fatalf(
			"retry generation = %+v, want inherited generation %d",
			child.TaskExecutionGeneration,
			run.TaskExecutionGeneration.Int64,
		)
	}
}

func TestTaskStartCreatesMissingOverviewForMember(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	// Preserve an owner, then start the existing Project's Task as an ordinary member.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'owner')`,
		f.CompanyA,
		f.UserB,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserA,
	)
	store, _ := db.NewStore(pool)
	var pending []events.Event
	var run generated.Run
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		run, err = EnqueueRunInTx(
			ctx,
			q,
			&pending,
			RunTrigger{
				CompanyID:          f.CompanyA,
				ConversationID:     f.ConversationA,
				AgentID:            f.AgentA,
				TaskID:             util.NullUUIDFromPtr(&f.TaskA),
				Source:             RunSourceMessage,
				AuthorizedByUserID: f.UserA,
			},
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Queries().
		GetProjectOverviewVersion(ctx, generated.GetProjectOverviewVersionParams{CompanyID: f.CompanyA, ProjectID: f.ProjectA})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range pending {
		if event.Name == "canvas.changed" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("overview events = %d, want 1", count)
	}
	// Idempotent bootstrap must preserve a Page people have already started editing.
	if err = store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		event, err := canvas.EnsureTaskOverviewInTx(ctx, q, run)
		if event != nil {
			t.Fatal("unchanged overview emitted another creation event")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	again, err := store.Queries().
		GetProjectOverviewVersion(ctx, generated.GetProjectOverviewVersionParams{CompanyID: f.CompanyA, ProjectID: f.ProjectA})
	if err != nil || again.ID != first.ID {
		t.Fatalf("overview was replaced: %+v, %v", again, err)
	}
}
