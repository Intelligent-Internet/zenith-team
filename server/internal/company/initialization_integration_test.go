//go:build integration

package company

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type initializationDiscovery struct{ agent.RuntimeService }

func (initializationDiscovery) GetModelDiscovery(
	_ context.Context,
	_ auth.UserPrincipal,
	runtimeID, requestID uuid.UUID,
) (agent.ModelDiscoveryRequest, error) {
	return agent.ModelDiscoveryRequest{
		ID:        requestID,
		RuntimeID: runtimeID,
		Status:    agent.ModelDiscoveryCompleted,
		Models:    []provideragent.RuntimeModel{{ID: "gpt-6-luna", Label: "Luna"}},
	}, nil
}

func newInitializationService(
	t *testing.T,
) (*DefaultInitializationService, *db.Store, auth.UserPrincipal, dbtest.Fixture, *pgxpool.Pool) {
	t.Helper()
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
	s, err := NewInitializationService(
		InitializationServiceOptions{
			Store:           store,
			Bus:             bus,
			Runtimes:        initializationDiscovery{},
			TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return s, store, auth.UserPrincipal{
		CompanyID: f.CompanyA,
		UserID:    f.UserA,
		Role:      auth.CompanyRoleOwner,
	}, f, pool
}

func pending(t *testing.T, store *db.Store, f dbtest.Fixture) bool {
	t.Helper()
	row, err := store.Queries().
		GetCompanyForMember(t.Context(), generated.GetCompanyForMemberParams{CompanyID: f.CompanyA, UserID: f.UserA})
	if err != nil {
		t.Fatal(err)
	}
	return row.InitializationPending
}

func seedWrite(t *testing.T, pool *pgxpool.Pool, f dbtest.Fixture, runID uuid.UUID, key string) uuid.UUID {
	t.Helper()
	pageID, err := generated.New(pool).
		CreateWikiPage(t.Context(), generated.CreateWikiPageParams{CompanyID: f.CompanyA, Type: "wiki", Title: key + "-" + runID.String(), CreatedByUserID: f.UserA})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		t.Context(),
		pool,
		`INSERT INTO run_wiki_writes(run_id,company_id,key,page_id) VALUES($1,$2,$3,$4)`,
		runID,
		f.CompanyA,
		key,
		pageID,
	)
	return pageID
}

func TestInitializationStartsResearchAsyncAndIsIdempotent(t *testing.T) {
	s, store, p, f, pool := newInitializationService(t)
	ctx := t.Context()
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE agents SET model='ceo-model', thinking_level='high' WHERE id=$1`,
		f.AgentA,
	)
	if state, err := s.Get(
		ctx,
		p,
	); err != nil || state.Phase != "not_started" || state.Status != "idle" ||
		state.PagesWritten != 0 {
		t.Fatalf("empty Get = %+v/%v", state, err)
	}
	input := InitializationInput{ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t)}
	if _, err := s.Advance(ctx, p, InitializationInput{}); !errors.Is(err, ErrInitializationConflict) {
		t.Fatalf("a first request without a conversation = %v, want ErrInitializationConflict", err)
	}
	receipt, err := s.Advance(ctx, p, InitializationInput{ConversationID: f.DirectConversationA})
	if err != nil || receipt.Phase != "not_started" || receipt.Status != "awaiting_model" || receipt.RunID != nil {
		t.Fatalf("initial receipt = %+v/%v, want a durable model wait without a Run", receipt, err)
	}
	if !pending(t, store, f) {
		t.Fatal("a receipt without a research Run must be pending: the founder still has to start it")
	}
	first, err := s.Advance(ctx, p, input)
	if err != nil || first.RunID == nil || first.Phase != "research" || first.PagesWritten != 0 ||
		first.OverviewPageID != nil {
		t.Fatalf("start = %+v/%v", first, err)
	}
	if pending(t, store, f) {
		t.Fatal("a started research must not hold the founder in onboarding")
	}
	replay, err := s.Advance(ctx, p, input)
	if err != nil || replay.RunID == nil || *replay.RunID != *first.RunID {
		t.Fatalf("replay = %+v/%v, want the same Run as %+v", replay, err, first)
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
		ExecutionProfile struct {
			Source string `json:"source"`
		} `json:"executionProfile"`
		Company struct {
			ResearchedContext string `json:"researchedContext"`
		} `json:"company"`
	}
	if err := json.Unmarshal(document, &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.Agent.Model != "gpt-6-luna" || frozen.Agent.ThinkingLevel != "" ||
		frozen.ExecutionProfile.Source != "company_research" ||
		frozen.Company.ResearchedContext != "" {
		t.Fatalf("research profile = %+v", frozen)
	}
	var userItems int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM conversation_items WHERE conversation_id=$1 AND type='user_message'`, f.DirectConversationA).
		Scan(&userItems); err != nil ||
		userItems != 0 {
		t.Fatalf("initialization injected %d user messages (%v)", userItems, err)
	}
	var model string
	if err := pool.QueryRow(ctx, `SELECT model FROM agents WHERE id=$1`, f.AgentA).
		Scan(&model); err != nil ||
		model != "ceo-model" {
		t.Fatalf("saved CEO model = %q/%v", model, err)
	}
	var runs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE conversation_id=$1`, f.DirectConversationA).
		Scan(&runs); err != nil ||
		runs != 1 {
		t.Fatalf("research runs = %d/%v, want exactly one", runs, err)
	}
	other := p
	other.UserID = f.UserB
	if _, err := s.Advance(ctx, other, input); err == nil {
		t.Fatal("another member advanced private initialization")
	}
	if _, err := s.Get(ctx, other); err == nil {
		t.Fatal("another member read private initialization")
	}
}

func TestInitializationReportsProgressRetryAndReady(t *testing.T) {
	s, store, p, f, pool := newInitializationService(t)
	ctx := t.Context()
	input := InitializationInput{ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t)}
	first, err := s.Advance(ctx, p, input)
	if err != nil {
		t.Fatal(err)
	}

	// Pages appear while the Run works, and the hub is exposed once written.
	seedWrite(t, pool, f, *first.RunID, "open-questions")
	hub := seedWrite(t, pool, f, *first.RunID, "company-overview")
	progress, err := s.Get(ctx, p)
	if err != nil || progress.PagesWritten != 2 || progress.OverviewPageID == nil ||
		*progress.OverviewPageID != hub {
		t.Fatalf("progress = %+v/%v", progress, err)
	}

	// A failure without Retry stays failed; the pages stay counted.
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='failed' WHERE id=$1`, *first.RunID)
	failed, err := s.Advance(ctx, p, input)
	if err != nil || failed.Phase != "research" || failed.Status != "failed" || *failed.RunID != *first.RunID ||
		failed.PagesWritten != 2 {
		t.Fatalf("failed replay = %+v/%v", failed, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE company_initializations SET completed_at = now()`)
	var notice uuid.UUID
	if err := store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var pendingEvents []events.Event
		created, err := inbox.CreateInTx(ctx, q, &pendingEvents, inbox.CreateInput{
			CompanyID:       f.CompanyA,
			RecipientUserID: f.UserA,
			Type:            inbox.TypeRunFailed,
			Entity:          inbox.Entity{Type: protocol.EntityRun, ID: first.RunID.String()},
			Title:           "Company research could not finish",
			Body:            "Retry it.",
			DedupeKey:       "run:" + first.RunID.String() + ":failed",
		})
		notice = created.Item.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Retry starts a fresh Run, clears completed_at, archives the failure
	// notice it answers, and keeps the earlier attempt's pages: the new Run
	// updates them instead of writing copies.
	input.Retry = true
	retried, err := s.Advance(ctx, p, input)
	if err != nil || retried.RunID == nil || *retried.RunID == *first.RunID || retried.PagesWritten != 2 ||
		retried.OverviewPageID == nil || *retried.OverviewPageID != hub {
		t.Fatalf("retry = %+v/%v", retried, err)
	}
	var completed bool
	if err := pool.QueryRow(ctx, `SELECT completed_at IS NOT NULL FROM company_initializations`).
		Scan(&completed); err != nil ||
		completed {
		t.Fatalf("retry kept completed_at (%v/%v)", completed, err)
	}
	var archived bool
	if err := pool.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM inbox_items WHERE id=$1`, notice).
		Scan(&archived); err != nil ||
		!archived {
		t.Fatalf("retry left the failure notice open (%v/%v)", archived, err)
	}

	// An automatic retry child is still in progress, and every attempt's pages count.
	seedWrite(t, pool, f, *retried.RunID, "products-and-services")
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='failed' WHERE id=$1`, *retried.RunID)
	if _, err := s.Advance(ctx, p, InitializationInput{Retry: true}); !errors.Is(err, ErrInitializationModel) {
		t.Fatalf("retry without a model = %v, want ErrInitializationModel", err)
	}
	child, err := store.Queries().
		CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: *retried.RunID, ForceFreshSession: true})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := s.Advance(ctx, p, input)
	if err != nil || *resumed.RunID != child.ID || resumed.Status == "failed" || resumed.Phase != "research" ||
		resumed.PagesWritten != 3 {
		t.Fatalf("automatic retry = %+v/%v", resumed, err)
	}
	seedWrite(t, pool, f, child.ID, "tools-and-sources")

	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, child.ID)
	done, err := s.Get(ctx, p)
	if err != nil || done.Phase != "ready" || done.Status != "completed" || done.PagesWritten != 4 ||
		done.OverviewPageID == nil ||
		*done.OverviewPageID != hub {
		t.Fatalf("Get = %+v/%v, want ready with every attempt's pages", done, err)
	}
	if again, err := s.Advance(ctx, p, input); err != nil || again.Phase != "ready" || *again.RunID != child.ID {
		t.Fatalf("advance after ready = %+v/%v, want no new Run", again, err)
	}
	if pending(t, store, f) {
		t.Fatal("finished research is still pending")
	}
}

func TestInitializationReconcileCompletesFinalReceiptAndReleasesQueueOnce(t *testing.T) {
	s, store, p, f, pool := newInitializationService(t)
	ctx := t.Context()
	first, err := s.Advance(
		ctx,
		p,
		InitializationInput{ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t)},
	)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := store.Queries().InsertRunQueueItem(ctx, generated.InsertRunQueueItemParams{
		CompanyID: f.CompanyA, ConversationID: f.DirectConversationA, ActorUserID: f.UserA,
		RequestKey: dbtest.NewUUID(t), Content: "Continue after the research",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The terminal hook is bypassed: this is the recovery for a receipt it never reached.
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, *first.RunID)
	worker, err := NewInitializationService(InitializationServiceOptions{
		Store:           s.store,
		Bus:             s.bus,
		Runtimes:        initializationDiscovery{},
		TaskMessageRuns: conversation.TaskMessageRunPreparerFuncs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for index := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			switch {
			case index == 0:
				err = s.Reconcile(ctx)
			case index < 3:
				err = worker.Reconcile(ctx)
			default:
				_, err = s.Advance(ctx, p, InitializationInput{})
			}
			if err != nil {
				t.Errorf("concurrent advance: %v", err)
			}
		}()
	}
	wg.Wait()
	for range 2 {
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || !receipt.CompletedAt.Valid {
		t.Fatalf("receipt = %+v/%v, want completed", receipt, err)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM conversation_run_queue_items WHERE id=$1`, queue.ID).
		Scan(&state); err != nil ||
		state != "materialized" {
		t.Fatalf("queue after completion = %s/%v", state, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE conversation_id=$1`, f.DirectConversationA).
		Scan(&count); err != nil ||
		count != 2 {
		t.Fatalf("runs after completion = %d/%v, want the research and one queued successor", count, err)
	}
	if pending(t, store, f) {
		t.Fatal("finished research is still pending")
	}
}

func TestInitializationReconcilerScansPastUnavailableReceipts(t *testing.T) {
	s, store, p, f, pool := newInitializationService(t)
	ctx := t.Context()
	first, err := s.Advance(
		ctx,
		p,
		InitializationInput{ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t)},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, *first.RunID)
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
	row, err := store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || row.CompletedAt.Valid {
		t.Fatalf("first pass exceeded its bounded page = %+v/%v", row, err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || !row.CompletedAt.Valid {
		t.Fatalf("later receipt starved = %+v/%v", row, err)
	}
}

func TestInitializationReconcilerWaitsForAFinalRunAndNeverRetries(t *testing.T) {
	s, store, p, f, pool := newInitializationService(t)
	ctx := t.Context()
	if _, err := s.Advance(ctx, p, InitializationInput{ConversationID: f.DirectConversationA}); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || row.ResearchRunID.Valid || row.CompletedAt.Valid {
		t.Fatalf("worker started discovery = %+v/%v", row, err)
	}
	first, err := s.Advance(ctx, p, InitializationInput{ModelDiscoveryID: dbtest.NewUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "running"} {
		dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status=$2 WHERE id=$1`, *first.RunID, status)
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		row, err := store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
		if err != nil || row.ResearchRunID.UUID != *first.RunID || row.CompletedAt.Valid {
			t.Fatalf("%s Run completed or retried = %+v/%v", status, row, err)
		}
	}
	// A failed attempt with an automatic retry child is still in progress.
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='failed' WHERE id=$1`, *first.RunID)
	child, err := store.Queries().
		CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: *first.RunID, ForceFreshSession: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || row.CompletedAt.Valid {
		t.Fatalf("retry in progress completed = %+v/%v", row, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='failed' WHERE id=$1`, child.ID)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || !row.CompletedAt.Valid || row.ResearchRunID.UUID != *first.RunID {
		t.Fatalf("final failure = %+v/%v, want completed without a new Run", row, err)
	}
	var runs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runs WHERE conversation_id=$1`, f.DirectConversationA).
		Scan(&runs); err != nil ||
		runs != 2 {
		t.Fatalf("runs = %d/%v, want the research and its retry child", runs, err)
	}
}

func TestInitializationReconcilerRechecksCreatorAndSurvivesBindingRemoval(t *testing.T) {
	s, store, p, f, pool := newInitializationService(t)
	ctx := t.Context()
	first, err := s.Advance(
		ctx,
		p,
		InitializationInput{ConversationID: f.DirectConversationA, ModelDiscoveryID: dbtest.NewUUID(t)},
	)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, *first.RunID)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='member' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserA,
	)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err := store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || row.CompletedAt.Valid {
		t.Fatalf("creator without authority completed = %+v/%v", row, err)
	}
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE company_members SET role='owner' WHERE company_id=$1 AND user_id=$2`,
		f.CompanyA,
		f.UserA,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`,
		f.AgentA,
		f.UserA,
	)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	row, err = store.Queries().GetCompanyInitialization(ctx, f.CompanyA)
	if err != nil || !row.CompletedAt.Valid {
		t.Fatalf("finished research needs no binding = %+v/%v", row, err)
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM companies WHERE id=$1`, f.CompanyA)
	if err := s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Queries().GetCompanyInitialization(ctx, f.CompanyA); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted company receipt = %v", err)
	}
}
