//go:build integration

package company

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	provideragent "boardly.dev/boardly/server/pkg/agent"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type initializationDiscovery struct{ agent.RuntimeService }

func (initializationDiscovery) GetModelDiscovery(_ context.Context, _ auth.UserPrincipal, runtimeID, requestID uuid.UUID) (agent.ModelDiscoveryRequest, error) {
	return agent.ModelDiscoveryRequest{ID: requestID, RuntimeID: runtimeID, Status: agent.ModelDiscoveryCompleted, Models: []provideragent.RuntimeModel{{ID: "gpt-6-luna", Label: "Luna"}}}, nil
}

func TestInitializationIsDurableIdempotentAndUsesSeparateModels(t *testing.T) {
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
	s, err := NewInitializationService(InitializationServiceOptions{Store: store, Bus: bus, Runtimes: initializationDiscovery{}, TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{}})
	if err != nil {
		t.Fatal(err)
	}
	p := auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
	input := InitializationInput{ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t)}
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET model='ceo-model', thinking_level='high' WHERE id=$1`, f.AgentA)
	receipt, err := s.Advance(ctx, p, InitializationInput{ConversationID: f.DirectConversationA})
	if err != nil || receipt.Status != "awaiting_model" || receipt.RunID != nil {
		t.Fatalf("initial receipt = %+v/%v, want durable model wait without a Run", receipt, err)
	}
	first, err := s.Advance(ctx, p, input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Advance(ctx, p, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == nil || replay.RunID == nil || *first.RunID != *replay.RunID {
		t.Fatalf("replay = %+v, want same Run as %+v", replay, first)
	}
	t.Run("concurrent replays", func(t *testing.T) {
		for index := range 4 {
			t.Run(strconv.Itoa(index), func(t *testing.T) {
				t.Parallel()
				got, err := s.Advance(ctx, p, input)
				if err != nil || got.RunID == nil || *got.RunID != *first.RunID {
					t.Fatalf("concurrent advance = %+v/%v", got, err)
				}
			})
		}
	})
	var document []byte
	if err := pool.QueryRow(ctx, `SELECT context FROM runs WHERE id=$1`, *first.RunID).Scan(&document); err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Agent struct {
			Model         string `json:"model"`
			ThinkingLevel string `json:"thinkingLevel"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(document, &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Agent.Model != "gpt-6-luna" || frozen.Agent.ThinkingLevel != "" {
		t.Fatalf("research profile = %+v", frozen.Agent)
	}
	var userItems int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversation_items WHERE conversation_id=$1 AND type='user_message'`, f.DirectConversationA).Scan(&userItems); err != nil {
		t.Fatal(err)
	}
	if userItems != 0 {
		t.Fatal("initialization injected a user message")
	}
	companyRow, err := store.Queries().GetCompanyForMember(ctx, generated.GetCompanyForMemberParams{CompanyID: f.CompanyA, UserID: f.UserA})
	if err != nil {
		t.Fatal(err)
	}
	if !companyRow.InitializationPending {
		t.Fatal("a new session cannot resume initialization")
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='failed' WHERE id=$1`, *first.RunID)
	failed, err := s.Advance(ctx, p, input)
	if err != nil || failed.Status != "failed" || *failed.RunID != *first.RunID {
		t.Fatalf("failed replay = %+v/%v", failed, err)
	}
	input.Retry = true
	retried, err := s.Advance(ctx, p, input)
	if err != nil {
		t.Fatal(err)
	}
	if *retried.RunID == *first.RunID {
		t.Fatal("explicit retry did not create a fresh Run")
	}
	first = retried
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='failed' WHERE id=$1`, *first.RunID)
	child, err := store.Queries().CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: *first.RunID, ForceFreshSession: true})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := s.Advance(ctx, p, input)
	if err != nil || resumed.RunID == nil || *resumed.RunID != child.ID || resumed.Status == "failed" {
		t.Fatalf("automatic retry = %+v/%v", resumed, err)
	}
	first = resumed

	// Successful research must carry the exact final item selected by the terminal proof.
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO conversation_items(company_id,conversation_id,run_id,seq,type,author_kind,author_id,item_key,content) VALUES($1,$2,$3,1,'agent_text','agent',$4,'context-final',$5)`,
		f.CompanyA,
		f.DirectConversationA,
		*first.RunID,
		f.AgentA,
		"# Business context\nFounder-confirmed: tools for shops.\n## Sources\nFounder description.\n## Open questions\nPricing TBD.",
	)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed', final_answer_item_key='context-final' WHERE id=$1`, *first.RunID)
	greeting, err := s.Advance(ctx, p, InitializationInput{})
	if err != nil {
		t.Fatal(err)
	}
	if greeting.Phase != "greeting" || greeting.RunID == nil || *greeting.RunID == *first.RunID {
		t.Fatalf("greeting = %+v", greeting)
	}
	again, err := s.Advance(ctx, p, InitializationInput{})
	if err != nil {
		t.Fatal(err)
	}
	if *again.RunID != *greeting.RunID {
		t.Fatal("duplicate greeting")
	}
	if err := pool.QueryRow(ctx, `SELECT context FROM runs WHERE id=$1`, *greeting.RunID).Scan(&document); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(document, &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Agent.Model != "ceo-model" || frozen.Agent.ThinkingLevel != "high" {
		t.Fatalf("greeting changed CEO profile: %+v", frozen.Agent)
	}
	var welcomeContext struct {
		Company struct {
			ResearchedContext string `json:"researchedContext"`
		} `json:"company"`
	}
	if err := json.Unmarshal(document, &welcomeContext); err != nil || welcomeContext.Company.ResearchedContext == "" {
		t.Fatalf("welcome has no saved context: %s/%v", document, err)
	}
	var model string
	if err := pool.QueryRow(ctx, `SELECT model FROM agents WHERE id=$1`, f.AgentA).Scan(&model); err != nil || model != "ceo-model" {
		t.Fatalf("saved CEO model = %q/%v", model, err)
	}
	other := p
	other.UserID = f.UserB
	if _, err := s.Advance(ctx, other, input); err == nil {
		t.Fatal("another member advanced private initialization")
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO conversation_items(company_id,conversation_id,run_id,seq,type,author_kind,author_id,item_key,content) VALUES($1,$2,$3,2,'agent_text','agent',$4,'hello','Hello! What would you like to focus on?')`, f.CompanyA, f.DirectConversationA, *greeting.RunID, f.AgentA)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed',final_answer_item_key='hello' WHERE id=$1`, *greeting.RunID)
	done, err := s.Get(ctx, p)
	if err != nil || done.Phase != "ready" {
		t.Fatalf("Get = %+v/%v, want ready", done, err)
	}
	if _, err := s.Advance(ctx, p, InitializationInput{}); err != nil {
		t.Fatal(err)
	}
	companyRow, err = store.Queries().GetCompanyForMember(ctx, generated.GetCompanyForMemberParams{CompanyID: f.CompanyA, UserID: f.UserA})
	if err != nil {
		t.Fatal(err)
	}
	if companyRow.InitializationPending {
		t.Fatal("acknowledged welcome still routes to initialization")
	}
}

func newInitializationFixture(t *testing.T) (*DefaultInitializationService, *pgxpool.Pool, dbtest.Fixture, auth.UserPrincipal) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	if err := db.MigrateUp(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, t.Context(), pool)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewInitializationService(InitializationServiceOptions{
		Store: store, Bus: bus, Runtimes: initializationDiscovery{},
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, pool, f, auth.UserPrincipal{CompanyID: f.CompanyA, UserID: f.UserA, Role: auth.CompanyRoleOwner}
}

func finishInitializationRun(t *testing.T, pool *pgxpool.Pool, f dbtest.Fixture, runID uuid.UUID, output string) {
	t.Helper()
	key := dbtest.NewUUID(t).String()
	dbtest.MustExec(t, t.Context(), pool, `INSERT INTO conversation_items(company_id,conversation_id,run_id,seq,type,author_kind,author_id,item_key,content)
		VALUES($1,$2,$3,(SELECT COALESCE(MAX(seq),0)+1 FROM conversation_items WHERE conversation_id=$2),'agent_text','agent',$4,$5,$6)`,
		f.CompanyA, f.DirectConversationA, runID, f.AgentA, key, output)
	dbtest.MustExec(t, t.Context(), pool, `UPDATE runs SET status='completed',final_answer_item_key=$2 WHERE id=$1`, runID, key)
}

func TestInitializationReconcilesWithoutClientAndReleasesQueueOnce(t *testing.T) {
	s, pool, f, p := newInitializationFixture(t)
	ctx := t.Context()
	first, err := s.Advance(ctx, p, InitializationInput{ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	queue, err := s.store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID: f.CompanyA, ConversationID: f.DirectConversationA, ActorUserID: f.UserA,
		RequestKey: dbtest.NewUUID(t), Content: "Continue after preparation",
	})
	if err != nil {
		t.Fatal(err)
	}
	finishInitializationRun(t, pool, f, *first.RunID, "# Business context\nA company.\n## Sources\nFounder.\n## Open questions\nPricing.")
	worker, err := NewInitializationService(InitializationServiceOptions{
		Store: s.store, Bus: s.bus, Runtimes: initializationDiscovery{}, TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	chats, err := conversation.NewConversationService(conversation.ServiceOptions{
		Store: s.store, Bus: s.bus, TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for index := range 9 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if index < 3 {
				if index == 0 {
					err = s.Reconcile(ctx)
				} else {
					err = worker.Reconcile(ctx)
				}
			} else if index < 6 {
				_, err = s.Advance(ctx, p, InitializationInput{})
			} else {
				_, err = chats.Send(ctx, p, conversation.MessageTarget{Kind: conversation.TargetConversation, ID: f.DirectConversationA}, conversation.SendInput{
					RequestKey: uuid.New(), Content: "Race preparation completion",
				})
				if !errors.Is(err, conversation.ErrInitializationPending) {
					t.Errorf("send racing preparation = %v, want preparation fence", err)
				}
				return
			}
			if err != nil {
				t.Errorf("concurrent advance: %v", err)
			}
		}()
	}
	wg.Wait()
	welcome, err := s.Get(ctx, p)
	if err != nil || welcome.Phase != "greeting" || welcome.RunID == nil {
		t.Fatalf("welcome = %+v/%v", welcome, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE conversation_id=$1`, f.DirectConversationA).Scan(&count); err != nil || count != 2 {
		t.Fatalf("initialization runs = %d/%v, want one research and one welcome", count, err)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM conversation_run_queue_items WHERE id=$1`, queue.ID).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("queue before welcome = %s/%v", state, err)
	}
	finishInitializationRun(t, pool, f, *welcome.RunID, "Welcome! What is the first priority?")
	// A retained welcome remains reconcilable if its optional research pointer
	// was cleared by Run retention after the context had already been saved.
	dbtest.MustExec(t, ctx, pool, `UPDATE company_initializations SET research_run_id=NULL WHERE company_id=$1`, f.CompanyA)
	// A real promotion write failure must roll back the completion receipt as
	// well, so a later pass can release the original prompt without losing it.
	dbtest.MustExec(t, ctx, pool, `ALTER TABLE runs ADD CONSTRAINT initialization_test_reject_queue CHECK (status <> 'queued') NOT VALID`)
	if err := s.Reconcile(ctx); err == nil {
		t.Fatal("promotion write failure was ignored")
	}
	receipt, err := s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || receipt.CompletedAt.Valid {
		t.Fatalf("failed queue promotion committed completion = %+v/%v", receipt, err)
	}
	dbtest.MustExec(t, ctx, pool, `ALTER TABLE runs DROP CONSTRAINT initialization_test_reject_queue`)
	for range 3 {
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM conversation_run_queue_items WHERE id=$1`, queue.ID).Scan(&state); err != nil || state != "materialized" {
		t.Fatalf("queue after welcome = %s/%v", state, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE conversation_id=$1`, f.DirectConversationA).Scan(&count); err != nil || count != 3 {
		t.Fatalf("runs after completion = %d/%v, want one queued successor", count, err)
	}
	row, err := s.store.Queries().GetCompanyForMember(ctx, generated.GetCompanyForMemberParams{CompanyID: f.CompanyA, UserID: f.UserA})
	if err != nil || row.InitializationPending {
		t.Fatalf("preparation still pending = %v/%v", row.InitializationPending, err)
	}
}

func TestInitializationReconcilerScansPastUnavailableReceipts(t *testing.T) {
	s, pool, f, p := newInitializationFixture(t)
	ctx := t.Context()
	first, err := s.Advance(ctx, p, InitializationInput{ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	finishInitializationRun(t, pool, f, *first.RunID, "# Business context\nA company.\n## Sources\nFounder.\n## Open questions\nPricing.")
	// These earlier UUIDs belong to receipts whose creator no longer has a
	// membership. They occupy more than a whole pass without being actionable.
	dbtest.MustExec(t, ctx, pool, `
		WITH tenants AS (
		 INSERT INTO companies(id,name,slug,task_prefix)
		 SELECT ('00000000-0000-0000-0000-' || lpad(n::text,12,'0'))::uuid,
		 'Scan '||n, 'scan-'||n, 'SCN' FROM generate_series(1,101) n RETURNING id
		), templates AS (
		 INSERT INTO agents(company_id,name,title,provider)
		 SELECT id,'Former CEO','CEO','codex' FROM tenants RETURNING id,company_id
		), computers AS (
		 INSERT INTO runtimes(company_id,daemon_id,name,provider,owner_user_id)
		 SELECT id,'scan-'||id,'Former computer','codex',$1 FROM tenants RETURNING id,company_id
		), chats AS (
		 INSERT INTO conversations(company_id,kind,agent_id,created_by_user_id)
		 SELECT company_id,'direct',id,$1 FROM templates RETURNING id,company_id,agent_id
		), attempts AS (
		 INSERT INTO runs(company_id,agent_id,conversation_id,runtime_id,status,run_type)
		 SELECT c.company_id,c.agent_id,c.id,r.id,'failed','direct_chat'
		 FROM chats c JOIN computers r ON r.company_id=c.company_id
		 RETURNING id,company_id,conversation_id
		)
		INSERT INTO company_initializations(company_id,created_by_user_id,conversation_id,research_run_id)
		SELECT company_id,$1,conversation_id,id FROM attempts`, f.UserA)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || row.WelcomeRunID.Valid {
		t.Fatalf("first pass exceeded its bounded page = %+v/%v", row, err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || !row.WelcomeRunID.Valid {
		t.Fatalf("later receipt starved = %+v/%v", row, err)
	}
}

func TestInitializationReconcilerWaitsForProofAndExplicitRecovery(t *testing.T) {
	s, pool, f, p := newInitializationFixture(t)
	ctx := t.Context()
	if _, err := s.Advance(ctx, p, InitializationInput{ConversationID: f.DirectConversationA}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || row.ResearchRunID.Valid || row.CompletedAt.Valid {
		t.Fatalf("worker started discovery = %+v/%v", row, err)
	}
	first, err := s.Advance(ctx, p, InitializationInput{ModelDiscoveryID: dbtest.NewUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "running", "failed", "cancelled", "completed"} {
		t.Run(status, func(t *testing.T) {
			dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status=$2 WHERE id=$1`, *first.RunID, status)
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			row, err := s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
			if err != nil || row.ResearchRunID.UUID != *first.RunID || row.WelcomeRunID.Valid || row.CompletedAt.Valid {
				t.Fatalf("worker retried or falsely completed = %+v/%v", row, err)
			}
		})
	}
	finishInitializationRun(t, pool, f, *first.RunID, "# Business context\nA company.\n## Sources\nFounder.\n## Open questions\nPricing.")
	// The newest automatic retry, not the parent's output, controls readiness.
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='failed' WHERE id=$1`, *first.RunID)
	child, err := s.store.Queries().CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: *first.RunID, ForceFreshSession: true})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runtimes SET status='offline' WHERE id=$1`, f.RuntimeA)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := s.Get(ctx, p)
	if err != nil || state.Status != "waiting_runtime" || state.RunID == nil || *state.RunID != child.ID {
		t.Fatalf("retry state = %+v/%v", state, err)
	}
	finishInitializationRun(t, pool, f, child.ID, "# Business context\nA company.\n## Sources\nFounder.\n## Open questions\nPricing.")
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	welcome, err := s.Get(ctx, p)
	if err != nil || welcome.Phase != "greeting" || welcome.Status != "waiting_runtime" {
		t.Fatalf("offline welcome = %+v/%v", welcome, err)
	}
	finishInitializationRun(t, pool, f, *welcome.RunID, "   ")
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || row.CompletedAt.Valid || row.WelcomeRunID.UUID != *welcome.RunID {
		t.Fatalf("empty welcome completed or retried = %+v/%v", row, err)
	}
}

func TestInitializationReconcilerRechecksCreatorAndBinding(t *testing.T) {
	s, pool, f, p := newInitializationFixture(t)
	ctx := t.Context()
	first, err := s.Advance(ctx, p, InitializationInput{ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	finishInitializationRun(t, pool, f, *first.RunID, "# Business context\nA company.\n## Sources\nFounder.\n## Open questions\nPricing.")
	dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`, f.CompanyA, f.UserA)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || row.WelcomeRunID.Valid || row.ContextMarkdown != "" {
		t.Fatalf("revoked creator advanced = %+v/%v", row, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET role='owner' WHERE company_id=$1 AND user_id=$2`, f.CompanyA, f.UserA)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`, f.AgentA, f.UserA)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || row.WelcomeRunID.Valid || row.ContextMarkdown != "" {
		t.Fatalf("unbound creator advanced = %+v/%v", row, err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM companies WHERE id=$1`, f.CompanyA)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted company receipt = %v", err)
	}
}

func TestInitializationCompletesProvenWelcomeAfterBindingRemoval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		withQueuedPrompt bool
		revokeCreator    bool
	}{
		{name: "finished welcome no longer needs a binding"},
		{name: "queued prompt is removed when its binding is unavailable", withQueuedPrompt: true},
		{name: "revoked creator still cannot complete", withQueuedPrompt: true, revokeCreator: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, pool, f, p := newInitializationFixture(t)
			ctx := t.Context()
			research, err := s.Advance(ctx, p, InitializationInput{
				ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			finishInitializationRun(t, pool, f, *research.RunID, "# Business context\nA company.\n## Sources\nFounder.\n## Open questions\nPricing.")
			if err := s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			welcome, err := s.Get(ctx, p)
			if err != nil || welcome.Phase != "greeting" || welcome.RunID == nil {
				t.Fatalf("got welcome = %+v/%v, want greeting", welcome, err)
			}
			var queued generated.ConversationRunQueueItem
			if tc.withQueuedPrompt {
				queued, err = s.store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
					CompanyID: f.CompanyA, ConversationID: f.DirectConversationA, ActorUserID: f.UserA,
					RequestKey: dbtest.NewUUID(t), Content: "Saved prompt before preparation",
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			finishInitializationRun(t, pool, f, *welcome.RunID, "Welcome! Your context is ready.")
			dbtest.MustExec(t, ctx, pool, `DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`, f.AgentA, f.UserA)
			if tc.revokeCreator {
				dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`, f.CompanyA, f.UserA)
			}
			for range 2 {
				if err := s.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			state, err := s.Get(ctx, p)
			if err != nil || state.Phase != "ready" || state.RunID == nil || *state.RunID != *welcome.RunID {
				t.Fatalf("got state = %+v/%v, want proven welcome ready", state, err)
			}
			receipt, err := s.store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
			if err != nil || receipt.CompletedAt.Valid == tc.revokeCreator {
				t.Fatalf("got completed = %t/%v, want %t", receipt.CompletedAt.Valid, err, !tc.revokeCreator)
			}
			var runCount int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE conversation_id=$1`, f.DirectConversationA).Scan(&runCount); err != nil || runCount != 2 {
				t.Fatalf("got Run count = %d/%v, want research and welcome only", runCount, err)
			}
			if tc.withQueuedPrompt {
				var state, reason, content string
				if err := pool.QueryRow(ctx, `SELECT state, COALESCE(removal_reason,''), content FROM conversation_run_queue_items WHERE id=$1`, queued.ID).Scan(&state, &reason, &content); err != nil {
					t.Fatal(err)
				}
				wantState, wantReason := "removed", "authorization_or_target_changed"
				if tc.revokeCreator {
					wantState, wantReason = "queued", ""
				}
				if state != wantState || reason != wantReason || content != queued.Content {
					t.Fatalf("got queue = %s/%s/%q, want %s/%s/%q", state, reason, content, wantState, wantReason, queued.Content)
				}
			}
			if !tc.revokeCreator {
				company, err := s.store.Queries().GetCompanyForMember(ctx, generated.GetCompanyForMemberParams{CompanyID: f.CompanyA, UserID: f.UserA})
				if err != nil || company.InitializationPending {
					t.Fatalf("got initializationPending = %t/%v, want false", company.InitializationPending, err)
				}
			}
		})
	}
}
