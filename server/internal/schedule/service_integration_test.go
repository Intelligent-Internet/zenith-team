//go:build integration

package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	conversationdomain "boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestScheduleRunNowFencesFreshMembershipBeforeScheduleAndConversation(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	created := createDueSchedule(t, ctx, pool, service, principal, fixture.AgentA)

	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Company lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	var companyID uuid.UUID
	if err := holder.QueryRow(ctx, `
		SELECT id FROM companies WHERE id=$1 FOR UPDATE`, fixture.CompanyA).Scan(&companyID); err != nil {
		t.Fatalf("lock Company: %v", err)
	}
	if _, err := holder.Exec(ctx, `
		UPDATE company_members
		SET status='revoked', revoked_at=clock_timestamp(), revoked_by_user_id=$2,
		    updated_at=clock_timestamp(), version=version+1
		WHERE company_id=$1 AND user_id=$2`, fixture.CompanyA, fixture.UserA); err != nil {
		t.Fatalf("stage creator membership revocation: %v", err)
	}

	type runNowResult struct {
		result RunNowResult
		err    error
	}
	requestKey := dbtest.NewUUID(t)
	done := make(chan runNowResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result, runErr := service.RunNow(ctx, principal, created.Schedule.ID, RunNowInput{
			RequestKey: requestKey,
		})
		done <- runNowResult{result: result, err: runErr}
	}()
	waitForScheduleProducerFence(t, ctx, pool, finished)

	checker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Schedule lock checker: %v", err)
	}
	var checkedScheduleID, checkedConversationID uuid.UUID
	checkErr := checker.QueryRow(ctx, `
		SELECT id FROM schedules WHERE id=$1 FOR UPDATE NOWAIT`, created.Schedule.ID).
		Scan(&checkedScheduleID)
	if checkErr == nil {
		checkErr = checker.QueryRow(
			ctx, `
			SELECT id FROM conversations WHERE id=$1 FOR UPDATE NOWAIT`,
			created.Schedule.ConversationID,
		).Scan(&checkedConversationID)
	}
	_ = checker.Rollback(context.Background())
	if checkErr != nil {
		t.Fatalf("RunNow locked Schedule or Conversation before fresh membership: %v", checkErr)
	}
	if checkedScheduleID != created.Schedule.ID ||
		checkedConversationID != created.Schedule.ConversationID {
		t.Fatalf("checked Schedule graph = (%s, %s), want (%s, %s)",
			checkedScheduleID, checkedConversationID,
			created.Schedule.ID, created.Schedule.ConversationID)
	}

	if err := holder.Commit(ctx); err != nil {
		t.Fatalf("commit creator membership revocation: %v", err)
	}
	select {
	case result := <-done:
		if !errors.Is(result.err, ErrNotFound) {
			t.Fatalf("RunNow() = %+v, %v; want ErrNotFound", result.result, result.err)
		}
	case <-ctx.Done():
		t.Fatalf("wait for RunNow(): %v", ctx.Err())
	}
	if got := countScheduleFirings(t, ctx, pool, created.Schedule.ID); got != 0 {
		t.Fatalf("Schedule firings after revoked RunNow = %d, want 0", got)
	}
}

// A Schedule fires on its creator's own machine (ADR 0014), so the creator
// leaving the Company — or having their account disabled — is what takes the
// Agent out of reach, not somebody else's machine going away.
func TestScheduleServiceRejectsAnInactiveCreator(t *testing.T) {
	tests := []struct {
		name     string
		mutation string
	}{
		{
			name: "revoked membership",
			mutation: `UPDATE company_members
				SET status='revoked', revoked_at=clock_timestamp(),
				    revoked_by_user_id=$2, updated_at=clock_timestamp(), version=version+1
				WHERE company_id=$1 AND user_id=$2`,
		},
		{
			name: "disabled account",
			mutation: `UPDATE users
				SET disabled_at=clock_timestamp()
				WHERE id=$2
				  AND EXISTS (
				      SELECT 1 FROM company_members
				      WHERE company_id=$1 AND user_id=$2
				  )`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool, store, fixture := startSchedulePostgres(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			service := newScheduleTestService(t, store)
			principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
			runAt := time.Now().UTC().Truncate(time.Minute).Add(10 * time.Minute)
			input := CreateInput{
				RequestKey:   dbtest.NewUUID(t),
				AgentID:      fixture.AgentA,
				Name:         "Runtime owner eligibility",
				Instructions: "Run only for an eligible Runtime owner.",
				Status:       StatusActive,
				Trigger:      Trigger{Type: TriggerTypeOnce, RunAt: &runAt},
				ConnectorIDs: []uuid.UUID{},
			}
			input.ReviewDigest = reviewScheduleInput(t, service, principal, input)
			dbtest.MustExec(t, ctx, pool, test.mutation, fixture.CompanyA, fixture.UserA)

			// An Agent the creator can no longer run cannot be reviewed:
			// preview says so directly, and a create that echoes the digest
			// reviewed while they were active is stale. No Schedule exists
			// yet, so neither is "Schedule not found".
			_, err := service.Preview(ctx, principal, PreviewInput{
				AgentID: input.AgentID, Name: input.Name, Instructions: input.Instructions,
				Trigger: input.Trigger, ConnectorIDs: input.ConnectorIDs,
			})
			if !errors.Is(err, ErrSelectionUnavailable) {
				t.Fatalf("Preview(inactive creator) error = %v, want %v",
					err, ErrSelectionUnavailable)
			}
			if _, err := service.Create(ctx, principal, input); !errors.Is(err, ErrReviewStale) {
				t.Fatalf("Create(inactive Runtime owner) error = %v, want %v", err, ErrReviewStale)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM schedules WHERE company_id=$1`, fixture.CompanyA).
				Scan(&count); err != nil {
				t.Fatalf("count Schedules after rejected create: %v", err)
			}
			if count != 0 {
				t.Fatalf("Schedules after rejected create = %d, want 0", count)
			}
		})
	}
}

// A preview whose Agent or source cannot be resolved names that selection,
// never a Schedule: there is no Schedule yet. Once a Schedule exists, a write
// that echoes a digest reviewed before the Agent disappeared is stale.
func TestScheduleServiceRejectsUnavailableSelection(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	trigger := Trigger{
		Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"), TimeZone: stringPointer("UTC"),
	}
	draft := func(agentID uuid.UUID, connectorIDs ...uuid.UUID) PreviewInput {
		return PreviewInput{
			AgentID: agentID, Name: "Selection", Instructions: "Review the selection.",
			Trigger: trigger, ConnectorIDs: append([]uuid.UUID{}, connectorIDs...),
		}
	}
	for name, input := range map[string]PreviewInput{
		"unknown Agent":         draft(dbtest.NewUUID(t)),
		"foreign Company Agent": draft(fixture.AgentB),
		"unknown source":        draft(fixture.AgentA, dbtest.NewUUID(t)),
	} {
		if _, err := service.Preview(ctx, principal, input); !errors.Is(err, ErrSelectionUnavailable) {
			t.Fatalf("Preview(%s) error = %v, want %v", name, err, ErrSelectionUnavailable)
		}
	}

	createInput := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: "Selection", Instructions: "Review the selection.", Status: StatusActive,
		Trigger: trigger, ConnectorIDs: []uuid.UUID{},
	}
	createInput.ReviewDigest = reviewScheduleInput(t, service, principal, createInput)
	created, err := service.Create(ctx, principal, createInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE agents SET archived_at=clock_timestamp() WHERE id=$1`, fixture.AgentA)

	if _, err := service.Preview(ctx, principal, draft(fixture.AgentA)); !errors.Is(err, ErrSelectionUnavailable) {
		t.Fatalf("Preview(archived Agent) error = %v, want %v", err, ErrSelectionUnavailable)
	}
	staleCreate := createInput
	staleCreate.RequestKey = dbtest.NewUUID(t)
	if _, err := service.Create(ctx, principal, staleCreate); !errors.Is(err, ErrReviewStale) {
		t.Fatalf("Create(archived Agent) error = %v, want %v", err, ErrReviewStale)
	}
	if _, err := service.Update(ctx, principal, created.Schedule.ID, UpdateInput{
		ExpectedRevision: created.Schedule.Revision, Name: &created.Schedule.Name,
		ReviewDigest: createInput.ReviewDigest,
	}); !errors.Is(err, ErrReviewStale) {
		t.Fatalf("Update(archived Agent) error = %v, want %v", err, ErrReviewStale)
	}
	// Run now reaches an archived Agent in the window before the due
	// worker's drift pass pauses the Schedule: a conflict, never a failure.
	if _, err := service.RunNow(ctx, principal, created.Schedule.ID, RunNowInput{
		RequestKey: dbtest.NewUUID(t),
	}); !errors.Is(err, ErrConflict) || errors.Is(err, ErrMaterialization) {
		t.Fatalf("RunNow(archived Agent) error = %v, want only %v", err, ErrConflict)
	}
	if got := countScheduleFirings(t, ctx, pool, created.Schedule.ID); got != 0 {
		t.Fatalf("Schedule firings after archived-Agent RunNow = %d, want 0", got)
	}
	if _, err := service.Get(ctx, principal, created.Schedule.ID); err != nil {
		t.Fatalf("Get() after archiving the Agent error = %v", err)
	}
}

// A Schedule fires on its creator's own binding of the Agent (ADR 0014). An
// Agent the creator has not set up is a step they can take, not a stale
// review or an unavailable selection, so every write that reviews the Agent
// and Run now name it: ErrAgentBindingRequired. Run now reaches it in the
// window before the due worker's drift pass pauses the Schedule, which is a
// refusal, never a failure.
func TestScheduleServiceRequiresTheCreatorsBinding(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	createInput := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: "Binding", Instructions: "Run on my computer.", Status: StatusActive,
		Trigger: Trigger{
			Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"), TimeZone: stringPointer("UTC"),
		},
		ConnectorIDs: []uuid.UUID{},
	}
	createInput.ReviewDigest = reviewScheduleInput(t, service, principal, createInput)
	created, err := service.Create(ctx, principal, createInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `
		DELETE FROM agent_runtime_bindings WHERE agent_id=$1 AND user_id=$2`,
		fixture.AgentA, fixture.UserA)

	if _, err := service.Preview(ctx, principal, PreviewInput{
		AgentID: createInput.AgentID, Name: createInput.Name, Instructions: createInput.Instructions,
		Trigger: createInput.Trigger, ConnectorIDs: createInput.ConnectorIDs,
	}); !errors.Is(err, ErrAgentBindingRequired) {
		t.Fatalf("Preview(unbound Agent) error = %v, want %v", err, ErrAgentBindingRequired)
	}
	unboundCreate := createInput
	unboundCreate.RequestKey = dbtest.NewUUID(t)
	if _, err := service.Create(ctx, principal, unboundCreate); !errors.Is(err, ErrAgentBindingRequired) {
		t.Fatalf("Create(unbound Agent) error = %v, want %v", err, ErrAgentBindingRequired)
	}
	if _, err := service.Update(ctx, principal, created.Schedule.ID, UpdateInput{
		ExpectedRevision: created.Schedule.Revision, Name: &created.Schedule.Name,
		ReviewDigest: createInput.ReviewDigest,
	}); !errors.Is(err, ErrAgentBindingRequired) {
		t.Fatalf("Update(unbound Agent) error = %v, want %v", err, ErrAgentBindingRequired)
	}
	if _, err := service.RunNow(ctx, principal, created.Schedule.ID, RunNowInput{
		RequestKey: dbtest.NewUUID(t),
	}); !errors.Is(err, ErrAgentBindingRequired) {
		t.Fatalf("RunNow(unbound Agent) error = %v, want %v", err, ErrAgentBindingRequired)
	}
	if got := countScheduleFirings(t, ctx, pool, created.Schedule.ID); got != 0 {
		t.Fatalf("Schedule firings after unbound RunNow = %d, want 0", got)
	}
	paused, err := service.Pause(ctx, principal, created.Schedule.ID, RevisionInput{
		ExpectedRevision: created.Schedule.Revision,
	})
	if err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	if _, err := service.Resume(ctx, principal, created.Schedule.ID, ReviewRevisionInput{
		ExpectedRevision: paused.Schedule.Revision, ReviewDigest: createInput.ReviewDigest,
	}); !errors.Is(err, ErrAgentBindingRequired) {
		t.Fatalf("Resume(unbound Agent) error = %v, want %v", err, ErrAgentBindingRequired)
	}
}

func waitForScheduleProducerFence(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	finished <-chan struct{},
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case <-finished:
			t.Fatal("Schedule producer bypassed the in-flight creator membership revocation")
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE datname=current_database()
				  AND pid <> pg_backend_pid()
				  AND state='active'
				  AND wait_event_type='Lock'
				  AND query LIKE '%LockCompanyForMembershipRead%'
			)`).Scan(&waiting); err != nil {
			t.Fatalf("observe Schedule producer lock wait: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Schedule producer did not reach the Company membership fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestScheduleServicePersistsCreatorPrivateSchedulesAndFrozenSources(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	connectorID := seedScheduleConnector(t, ctx, pool, fixture.CompanyA, "codex", "gmail")
	if _, err := NewScheduleService(ServiceOptions{
		Store: store, Bus: newScheduleTestBus(t),
		Materializer: MaterializerFunc(materializedScheduleRun),
	}); err == nil {
		t.Fatal("NewScheduleService(without Conversation updater) error = nil")
	}
	service, err := NewScheduleService(ServiceOptions{
		Store:               store,
		Bus:                 newScheduleTestBus(t),
		ConversationUpdater: ConversationUpdaterFunc(unchangedScheduleConversation),
		Materializer:        MaterializerFunc(materializedScheduleRun),
	})
	if err != nil {
		t.Fatalf("NewScheduleService() error = %v", err)
	}
	input := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: " Morning brief ", Instructions: " Summarize connected sources. ",
		Trigger: Trigger{
			Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"),
			TimeZone: stringPointer("Asia/Ho_Chi_Minh"),
		},
		ConnectorIDs: []uuid.UUID{connectorID}, Status: StatusActive,
		PresetKey: stringPointer("daily_brief_v1"),
	}
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	input.ReviewDigest = reviewScheduleInput(t, service, principal, input)
	created, err := service.Create(ctx, principal, input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !created.Created || created.Schedule.Name != "Morning brief" ||
		created.Schedule.Instructions != "Summarize connected sources." ||
		created.Schedule.Status != StatusActive || created.Schedule.NextOccurrenceAt == nil ||
		created.Schedule.ReviewedProvider != "codex" || len(created.Schedule.Sources) != 1 {
		t.Fatalf("created Schedule = %+v", created)
	}
	source := created.Schedule.Sources[0]
	if source.ConnectorID != connectorID || source.PolicyRevision != 1 || source.Source != "codex_app" ||
		source.NativeID != "gmail" || source.ReviewedContractDigest != scheduleTestDigest {
		t.Fatalf("frozen source = %+v", source)
	}
	if created.Schedule.ID == input.RequestKey || created.Schedule.ConversationID == input.RequestKey ||
		created.Schedule.ID == created.Schedule.ConversationID {
		t.Fatalf("database-owned identities = Schedule %s, Conversation %s, request %s",
			created.Schedule.ID, created.Schedule.ConversationID, input.RequestKey)
	}
	var conversationKind string
	var conversationScheduleID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		SELECT kind, schedule_id FROM conversations WHERE id=$1`,
		created.Schedule.ConversationID,
	).Scan(&conversationKind, &conversationScheduleID); err != nil {
		t.Fatalf("read database-created Schedule Conversation: %v", err)
	}
	if conversationKind != "schedule" || conversationScheduleID != created.Schedule.ID {
		t.Fatalf("Schedule Conversation = kind %q, Schedule %s", conversationKind, conversationScheduleID)
	}

	replayed, err := service.Create(ctx, companyPrincipal(fixture.UserA, fixture.CompanyA), input)
	if err != nil || replayed.Created || replayed.Schedule.ID != created.Schedule.ID {
		t.Fatalf("Create(replay) = %+v, %v", replayed, err)
	}

	page, err := service.List(ctx, companyPrincipal(fixture.UserA, fixture.CompanyA), ListOptions{})
	if err != nil || len(page.Schedules) != 1 || page.Schedules[0].ID != created.Schedule.ID {
		t.Fatalf("List(owner) = %+v, %v", page, err)
	}
	foreignPage, err := service.List(ctx, companyPrincipal(fixture.UserB, fixture.CompanyB), ListOptions{})
	if err != nil || len(foreignPage.Schedules) != 0 {
		t.Fatalf("List(foreign creator) = %+v, %v", foreignPage, err)
	}
	presetPage, err := service.List(ctx, principal, ListOptions{PresetKey: stringPointer("daily_brief_v1")})
	if err != nil || len(presetPage.Schedules) != 1 {
		t.Fatalf("List(preset) = %+v, %v", presetPage, err)
	}
	emptyPresetPage, err := service.List(ctx, principal, ListOptions{PresetKey: stringPointer("other_preset")})
	if err != nil || len(emptyPresetPage.Schedules) != 0 {
		t.Fatalf("List(other preset) = %+v, %v", emptyPresetPage, err)
	}
	if _, err := service.Get(
		ctx, companyPrincipal(fixture.UserB, fixture.CompanyB), created.Schedule.ID,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(foreign creator) error = %v, want %v", err, ErrNotFound)
	}
}

func TestScheduleServiceAgentUpdateSynchronizesConversationBeforeMaterialization(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	secondAgentID := dbtest.NewUUID(t)
	dbtest.SeedBoundAgent(t, ctx, pool, secondAgentID, fixture.CompanyA,
		"Schedule Agent", "codex", &fixture.AgentA, fixture.UserA, fixture.RuntimeA)
	service, err := NewScheduleService(ServiceOptions{
		Store: store, Bus: newScheduleTestBus(t),
		ConversationUpdater: ConversationUpdaterFunc(func(
			ctx context.Context,
			queries *generated.Queries,
			pending *[]events.Event,
			input UpdateConversationInput,
		) error {
			return conversationdomain.UpdateScheduleConversationInTx(
				ctx,
				queries, pending,
				conversationdomain.UpdateScheduleConversationInput{
					ID: input.ID, ScheduleID: input.ScheduleID, CompanyID: input.CompanyID,
					CreatorUserID: input.CreatorUserID, AgentID: input.AgentID, Title: input.Title,
				},
			)
		}),
		Materializer: MaterializerFunc(materializedScheduleRun),
	})
	if err != nil {
		t.Fatalf("NewScheduleService() error = %v", err)
	}
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	createInput := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: "Original schedule", Instructions: "Summarize the latest activity.", Status: StatusActive,
		Trigger: Trigger{
			Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"), TimeZone: stringPointer("UTC"),
		},
		ConnectorIDs: []uuid.UUID{},
	}
	createInput.ReviewDigest = reviewScheduleInput(t, service, principal, createInput)
	created, err := service.Create(ctx, principal, createInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	updatedName := "Updated schedule"
	preview, err := service.Preview(ctx, principal, PreviewInput{
		AgentID: secondAgentID, Name: updatedName, Instructions: created.Schedule.Instructions,
		Trigger: created.Schedule.Trigger, ConnectorIDs: []uuid.UUID{},
	})
	if err != nil {
		t.Fatalf("Preview(update Agent) error = %v", err)
	}
	updated, err := service.Update(ctx, principal, created.Schedule.ID, UpdateInput{
		ExpectedRevision: created.Schedule.Revision, AgentID: &secondAgentID,
		Name: &updatedName, ReviewDigest: preview.ReviewDigest,
	})
	if err != nil {
		t.Fatalf("Update(Agent) error = %v", err)
	}
	var conversationAgentID uuid.UUID
	var conversationTitle string
	if err := pool.QueryRow(
		ctx, `
		SELECT agent_id, title FROM conversations WHERE id=$1`,
		created.Schedule.ConversationID,
	).Scan(&conversationAgentID, &conversationTitle); err != nil {
		t.Fatalf("read updated Schedule Conversation: %v", err)
	}
	if conversationAgentID != secondAgentID || conversationTitle != updatedName {
		t.Fatalf("updated Schedule Conversation = agent %s, title %q", conversationAgentID, conversationTitle)
	}
	runNow, err := service.RunNow(ctx, principal, updated.Schedule.ID, RunNowInput{
		RequestKey: dbtest.NewUUID(t),
	})
	if err != nil {
		t.Fatalf("RunNow(after Agent update) error = %v", err)
	}
	var runAgentID uuid.UUID
	var prompt string
	if err := pool.QueryRow(ctx, `
		SELECT agent_id, context->>'prompt'
		FROM runs WHERE id=$1`, *runNow.Firing.RunID).Scan(&runAgentID, &prompt); err != nil {
		t.Fatalf("read materialized Schedule Run: %v", err)
	}
	if runAgentID != secondAgentID || prompt != updated.Schedule.Instructions {
		t.Fatalf("materialized Schedule Run = Agent %s, prompt %q; want %s, %q",
			runAgentID, prompt, secondAgentID, updated.Schedule.Instructions)
	}
}

func TestScheduleServiceRejectsStaleReviewAndAppliesReviewedUpdate(t *testing.T) {
	pool, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	connectorID := seedScheduleConnector(t, ctx, pool, fixture.CompanyA, "codex", "gmail")
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	createInput := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: "Reviewed", Instructions: "Initial instructions.", Status: StatusActive,
		Trigger: Trigger{
			Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"), TimeZone: stringPointer("UTC"),
		},
		ConnectorIDs: []uuid.UUID{connectorID},
	}
	createInput.ReviewDigest = reviewScheduleInput(t, service, principal, createInput)
	created, err := service.Create(ctx, principal, createInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	updatedName := "Reviewed update"
	preview, err := service.Preview(ctx, principal, PreviewInput{
		AgentID: created.Schedule.AgentID, Name: updatedName,
		Instructions: created.Schedule.Instructions, Trigger: created.Schedule.Trigger,
		ConnectorIDs: sourceConnectorIDs(created.Schedule.Sources),
	})
	if err != nil {
		t.Fatalf("Preview(update) error = %v", err)
	}
	if _, err := service.Update(ctx, principal, created.Schedule.ID, UpdateInput{
		ExpectedRevision: created.Schedule.Revision, Name: &updatedName,
		ReviewDigest: scheduleTestDigest,
	}); !errors.Is(err, ErrReviewStale) {
		t.Fatalf("Update(stale review) error = %v, want %v", err, ErrReviewStale)
	}
	updated, err := service.Update(ctx, principal, created.Schedule.ID, UpdateInput{
		ExpectedRevision: created.Schedule.Revision, Name: &updatedName,
		ReviewDigest: preview.ReviewDigest,
	})
	if err != nil || updated.Schedule.Name != updatedName ||
		updated.Schedule.Revision != created.Schedule.Revision+1 {
		t.Fatalf("Update(reviewed) = %+v, %v", updated, err)
	}
}

func TestScheduleServiceLifecycleUsesRevisionsAndRecalculatesFutureOccurrences(t *testing.T) {
	_, store, fixture := startSchedulePostgres(t)
	service, err := NewScheduleService(ServiceOptions{
		Store: store, Bus: newScheduleTestBus(t),
		ConversationUpdater: ConversationUpdaterFunc(unchangedScheduleConversation),
		Materializer:        MaterializerFunc(materializedScheduleRun),
	})
	if err != nil {
		t.Fatalf("NewScheduleService() error = %v", err)
	}
	var changed []events.Event
	if err := service.bus.Subscribe("schedule.changed", func(_ context.Context, event events.Event) error {
		changed = append(changed, event)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	createInput := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: "Lifecycle", Instructions: "Run safely.", Status: StatusActive,
		Trigger: Trigger{
			Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"), TimeZone: stringPointer("UTC"),
		},
		ConnectorIDs: []uuid.UUID{},
	}
	createInput.ReviewDigest = reviewScheduleInput(t, service, principal, createInput)
	created, err := service.Create(context.Background(), principal, createInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	paused, err := service.Pause(context.Background(), principal, created.Schedule.ID, RevisionInput{
		ExpectedRevision: created.Schedule.Revision,
	})
	if err != nil || paused.Schedule.Status != StatusPaused || paused.Schedule.PauseReason == nil {
		t.Fatalf("Pause() = %+v, %v", paused, err)
	}
	if _, err := service.Resume(context.Background(), principal, created.Schedule.ID, ReviewRevisionInput{
		ExpectedRevision: created.Schedule.Revision,
		ReviewDigest:     createInput.ReviewDigest,
	}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("Resume(stale version) error = %v, want %v", err, ErrVersionConflict)
	}
	resumePreview, err := service.Preview(
		context.Background(),
		principal,
		previewInputFromResponse(paused.Schedule),
	)
	if err != nil {
		t.Fatalf("Preview(resume) error = %v", err)
	}
	resumed, err := service.Resume(context.Background(), principal, created.Schedule.ID, ReviewRevisionInput{
		ExpectedRevision: paused.Schedule.Revision,
		ReviewDigest:     resumePreview.ReviewDigest,
	})
	if err != nil || resumed.Schedule.Status != StatusActive || resumed.Schedule.PauseReason != nil ||
		resumed.Schedule.NextOccurrenceAt == nil {
		t.Fatalf("Resume() = %+v, %v", resumed, err)
	}
	cancelled, err := service.Cancel(context.Background(), principal, created.Schedule.ID, RevisionInput{
		ExpectedRevision: resumed.Schedule.Revision,
	})
	if err != nil || cancelled.Schedule.Status != StatusCancelled || cancelled.Schedule.NextOccurrenceAt != nil {
		t.Fatalf("Cancel() = %+v, %v", cancelled, err)
	}
	if len(changed) != 4 {
		t.Fatalf("create/pause/resume/cancel events = %+v", changed)
	}
}

func TestScheduleServiceRunNowCreatesManualFiringWithoutAdvancingSchedule(t *testing.T) {
	_, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	createInput := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: "Manual", Instructions: "Run now.", Status: StatusActive,
		Trigger: Trigger{
			Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"), TimeZone: stringPointer("UTC"),
		},
		ConnectorIDs: []uuid.UUID{},
	}
	createInput.ReviewDigest = reviewScheduleInput(t, service, principal, createInput)
	created, err := service.Create(ctx, principal, createInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	runNow, err := service.RunNow(ctx, principal, created.Schedule.ID, RunNowInput{
		RequestKey: dbtest.NewUUID(t),
	})
	if err != nil || !runNow.Created || runNow.Firing.Trigger != FiringTriggerManual ||
		runNow.Firing.Disposition != FiringDispositionMaterialized {
		t.Fatalf("RunNow() = %+v, %v", runNow, err)
	}
	// Run now answers in the history's shape: the prompt's position in the
	// Conversation and the Run's status are known the moment it commits.
	if runNow.Firing.MessageSeq == nil || runNow.Firing.RunStatus == nil ||
		*runNow.Firing.RunStatus != "queued" || runNow.Firing.Output != nil {
		t.Fatalf("RunNow() firing = %+v, want messageSeq and runStatus queued", runNow.Firing)
	}
	stored, err := service.Get(ctx, principal, created.Schedule.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Revision != created.Schedule.Revision || stored.NextOccurrenceAt == nil ||
		created.Schedule.NextOccurrenceAt == nil ||
		!stored.NextOccurrenceAt.Equal(*created.Schedule.NextOccurrenceAt) {
		t.Fatalf("RunNow advanced Schedule: before=%+v after=%+v", created.Schedule, stored)
	}
	// The manual firing is still the Schedule's last run, so a list or
	// detail view does not say it never ran.
	if created.Schedule.LastOccurrenceAt != nil || stored.LastOccurrenceAt == nil ||
		!stored.LastOccurrenceAt.Equal(runNow.Firing.OccurrenceAt) {
		t.Fatalf("RunNow last occurrence: before=%v after=%v, want firing time %v",
			created.Schedule.LastOccurrenceAt, stored.LastOccurrenceAt, runNow.Firing.OccurrenceAt)
	}
	history, err := service.ListFirings(ctx, principal, created.Schedule.ID, FiringListOptions{})
	if err != nil || len(history.Firings) != 1 || history.Firings[0].ID != runNow.Firing.ID ||
		history.Firings[0].RunStatus == nil || *history.Firings[0].RunStatus != "queued" ||
		history.Firings[0].Output != nil || history.Firings[0].MessageSeq == nil ||
		*history.Firings[0].MessageSeq != *runNow.Firing.MessageSeq {
		t.Fatalf("ListFirings() = %+v, %v", history, err)
	}
	// Firing history is a per-Schedule read like Get and RunNow: a Schedule
	// that does not exist, or belongs to another creator, is ErrNotFound —
	// never an empty page a client could mistake for "no firings yet".
	for name, test := range map[string]struct {
		principal  auth.UserPrincipal
		scheduleID uuid.UUID
	}{
		"unknown Schedule": {principal: principal, scheduleID: dbtest.NewUUID(t)},
		"foreign creator": {
			principal: companyPrincipal(fixture.UserB, fixture.CompanyB), scheduleID: created.Schedule.ID,
		},
	} {
		if _, err := service.ListFirings(
			ctx, test.principal, test.scheduleID, FiringListOptions{},
		); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ListFirings(%s) error = %v, want %v", name, err, ErrNotFound)
		}
	}
}

func TestScheduleServiceRunNowReplaysCommittedFiringAfterScheduleIsPaused(t *testing.T) {
	_, store, fixture := startSchedulePostgres(t)
	ctx := context.Background()
	service := newScheduleTestService(t, store)
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	createInput := CreateInput{
		RequestKey: dbtest.NewUUID(t), AgentID: fixture.AgentA,
		Name: "Manual replay", Instructions: "Run now.", Status: StatusActive,
		Trigger: Trigger{
			Type: TriggerTypeCron, Expression: stringPointer("0 8 * * *"), TimeZone: stringPointer("UTC"),
		},
		ConnectorIDs: []uuid.UUID{},
	}
	createInput.ReviewDigest = reviewScheduleInput(t, service, principal, createInput)
	created, err := service.Create(ctx, principal, createInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	input := RunNowInput{RequestKey: dbtest.NewUUID(t)}
	first, err := service.RunNow(ctx, principal, created.Schedule.ID, input)
	if err != nil {
		t.Fatalf("RunNow(first) error = %v", err)
	}
	paused, err := service.Pause(ctx, principal, created.Schedule.ID, RevisionInput{
		ExpectedRevision: created.Schedule.Revision,
	})
	if err != nil || paused.Schedule.Status != StatusPaused {
		t.Fatalf("Pause() = %+v, %v", paused, err)
	}
	replay, err := service.RunNow(ctx, principal, created.Schedule.ID, input)
	if err != nil {
		t.Fatalf("RunNow(replay after pause) error = %v", err)
	}
	if replay.Created || replay.Firing.ID != first.Firing.ID ||
		replay.Firing.MessageID == nil || first.Firing.MessageID == nil ||
		*replay.Firing.MessageID != *first.Firing.MessageID ||
		replay.Firing.RunID == nil || first.Firing.RunID == nil ||
		*replay.Firing.RunID != *first.Firing.RunID ||
		replay.Firing.MessageSeq == nil || first.Firing.MessageSeq == nil ||
		*replay.Firing.MessageSeq != *first.Firing.MessageSeq || replay.Firing.RunStatus == nil {
		t.Fatalf("RunNow(replay) = %+v, want committed firing %+v", replay, first)
	}
}

func startSchedulePostgres(t *testing.T) (*pgxpool.Pool, *db.Store, dbtest.Fixture) {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return pool, store, dbtest.SeedFixture(t, ctx, pool)
}

func companyPrincipal(userID, companyID uuid.UUID) auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID: userID, SessionID: uuid.New(), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
}

func seedScheduleConnector(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
	provider, nativeID string,
) uuid.UUID {
	t.Helper()
	connectorID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO connector_catalog_items (
			id, company_id, provider, source, native_id, name, status, action,
			can_execute, contract_digest, tool_namespace, observed_at
		) VALUES ($1,$2,$3,'codex_app',$4,'Test Connector','connected','none',true,$5,
			'[{"name":"search","description":"Search","inputSchema":{"type":"object"}}]'::jsonb,
			clock_timestamp())`, connectorID, companyID, provider, nativeID, scheduleTestDigest)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO connector_policies (
			connector_id, company_id, desired_enabled,
			reviewed_contract_digest, review_required
		) VALUES ($1,$2,true,$3,false)`, connectorID, companyID, scheduleTestDigest)
	return connectorID
}

func reviewScheduleInput(
	t *testing.T,
	service *DefaultScheduleService,
	principal auth.UserPrincipal,
	input CreateInput,
) string {
	t.Helper()
	preview, err := service.Preview(context.Background(), principal, PreviewInput{
		AgentID: input.AgentID, Name: input.Name, Instructions: input.Instructions,
		PresetKey: input.PresetKey, Trigger: input.Trigger, ConnectorIDs: input.ConnectorIDs,
	})
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	return preview.ReviewDigest
}

func materializedScheduleRun(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input MaterializeInput,
) (MaterializeResult, error) {
	result, err := conversationdomain.MaterializeScheduleRunInTx(
		ctx,
		queries,
		pending,
		conversationdomain.ScheduleRunInput{
			ScheduleID: input.ScheduleID, CompanyID: input.CompanyID,
			ConversationID: input.ConversationID, CreatorUserID: input.CreatorUserID,
			AgentID: input.AgentID, Provider: input.Provider,
			Instructions: input.Instructions,
			OccurrenceAt: input.OccurrenceAt,
		},
	)
	// Mirrors the cmd/server adapter's translation into the Materializer
	// contract.
	if errors.Is(err, conversationdomain.ErrConflict) {
		return MaterializeResult{}, errors.Join(ErrConflict, err)
	}
	if err != nil {
		return MaterializeResult{}, err
	}
	return MaterializeResult{
		Disposition: FiringDispositionMaterialized,
		RunID:       result.RunID,
		MessageID:   result.MessageID,
	}, nil
}

func unchangedScheduleConversation(
	context.Context,
	*generated.Queries,
	*[]events.Event,
	UpdateConversationInput,
) error {
	return nil
}

func newScheduleTestBus(t *testing.T) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("events.NewBus() error = %v", err)
	}
	return bus
}

func TestScheduleHistoryKeepsConversationAndRunPrivateWithFrozenSource(t *testing.T) {
	t.Parallel()
	pool, store, fixture := startSchedulePostgres(t)
	ctx := t.Context()
	dbtest.MustExec(t,
		ctx,
		pool,
		`INSERT INTO company_members(company_id,user_id,role) VALUES($1,$2,'admin')`,
		fixture.CompanyA,
		fixture.UserB)
	service := newScheduleTestService(t, store)
	var announced []activity.Entry
	if err := service.bus.Subscribe(activity.Topic, func(_ context.Context, event events.Event) error {
		entry, err := events.PayloadOf[activity.Entry](event)
		if err == nil {
			announced = append(announced, entry)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	principal := companyPrincipal(fixture.UserA, fixture.CompanyA)
	runAt := time.Now().UTC().Truncate(time.Minute).Add(time.Hour)
	input := CreateInput{
		RequestKey:   uuid.New(),
		AgentID:      fixture.AgentA,
		Name:         "Scheduled source",
		Instructions: "Produce a private brief",
		Status:       StatusActive,
		Trigger: Trigger{
			Type:  TriggerTypeOnce,
			RunAt: &runAt,
		},
		ConnectorIDs: []uuid.UUID{},
	}
	input.ReviewDigest = reviewScheduleInput(t, service, principal, input)
	created, err := service.Create(ctx, principal, input)
	if err != nil {
		t.Fatal(err)
	}
	title := "Private scheduled brief"
	preview, err := service.Preview(ctx,
		principal,
		PreviewInput{
			AgentID:      created.Schedule.AgentID,
			Name:         title,
			Instructions: created.Schedule.Instructions,
			Trigger:      created.Schedule.Trigger,
			ConnectorIDs: []uuid.UUID{},
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx,
		principal,
		created.Schedule.ID,
		UpdateInput{
			ExpectedRevision: created.Schedule.Revision,
			Name:             &title,
			ReviewDigest:     preview.ReviewDigest,
		}); err != nil {
		t.Fatal(err)
	}
	var beforeRevision int64
	err = pool.QueryRow(ctx,
		`SELECT version FROM conversations WHERE id=$1`,
		created.Schedule.ConversationID).Scan(&beforeRevision)
	if err != nil {
		t.Fatal(err)
	}
	fired, err := service.RunNow(ctx, principal, created.Schedule.ID, RunNowInput{RequestKey: uuid.New()})
	if err != nil || fired.Firing.RunID == nil {
		t.Fatalf("manual firing = %+v, %v", fired, err)
	}
	run, err := store.Queries().GetRun(ctx, *fired.Firing.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == uuid.Nil ||
		run.RunType != "schedule" ||
		!run.Revision.Valid ||
		run.Revision.Int64 != beforeRevision ||
		run.TaskID.Valid {
		t.Fatalf("Schedule source = %+v, want Conversation revision %d", run, beforeRevision)
	}
	var source struct {
		Source struct {
			EntityID   uuid.UUID
			EntityType string
			Revision   int64
			Title      string
		}
	}
	if err := json.Unmarshal(run.Context, &source); err != nil {
		t.Fatal(err)
	}
	if source.Source.EntityID != created.Schedule.ConversationID ||
		source.Source.EntityType != "conversation" ||
		source.Source.Revision != beforeRevision ||
		source.Source.Title != title {
		t.Fatalf("Schedule frozen source = %+v", source)
	}
	feed := activity.NewFeed(store.Queries())
	ownerRows, _, err := feed.List(ctx, fixture.CompanyA, activity.ListOptions{UserID: fixture.UserA, Limit: 100})
	if err != nil || len(ownerRows) < 4 || len(ownerRows) != len(announced) {
		t.Fatalf("Schedule history/announcements = %d/%d, %v", len(ownerRows), len(announced), err)
	}
	for _, entry := range ownerRows {
		if !entry.VisibilityUserID.Valid || entry.VisibilityUserID.UUID != fixture.UserA {
			t.Fatalf("Schedule history visibility = %+v", entry)
		}
		if entry.Entity.Type == "run" && (entry.Revision != nil || entry.Actor.Kind != "system") {
			t.Fatalf("Schedule Run actor/revision = %+v", entry)
		}
	}
	runs := execution.NewRunService(execution.RunServiceOptions{
		Queries:       store.Queries(),
		Transactor:    store,
		Bus:           service.bus,
		RouteResolver: conversationdomain.ResolveConversationRoute,
	})
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		userID  uuid.UUID
		visible bool
	}{{"creator", fixture.UserA, true}, {"other admin", fixture.UserB, false}, {"outsider", uuid.New(), false}} {
		t.Run(tc.name, func(t *testing.T) {
			rows, next, err := feed.List(
				ctx,
				fixture.CompanyA,
				activity.ListOptions{UserID: tc.userID, Limit: 100},
			)
			want := 0
			if tc.visible {
				want = len(ownerRows)
			}
			if err != nil || len(rows) != want || next != nil {
				t.Fatalf("Schedule feed count/next = %d/%v, %v; want %d", len(rows), next, err, want)
			}
			calendar, err := feed.Calendar(ctx,
				fixture.CompanyA,
				activity.CalendarOptions{
					UserID:   tc.userID,
					Month:    month.Format("2006-01"),
					TimeZone: "UTC",
					Location: time.UTC,
					StartAt:  month,
					EndAt: month.AddDate(0,
						1,
						0),
				})
			if err != nil || calendar.TotalCount != int64(want) {
				t.Fatalf("Schedule calendar = %+v, %v", calendar, err)
			}
			page, err := runs.List(ctx,
				auth.UserPrincipal{
					CompanyID: fixture.CompanyA,
					UserID:    tc.userID,
					Role:      auth.CompanyRoleAdmin,
				},
				execution.ListOptions{
					AgentID: &fixture.AgentA,
					RunType: "schedule",
					Limit:   1,
				})
			wantRuns := 0
			if tc.visible {
				wantRuns = 1
			}
			if err != nil || len(page.Items) != wantRuns || page.Next != nil {
				t.Fatalf("Schedule Run history = %+v, %v", page, err)
			}
		})
	}
	dbtest.MustExec(t,
		ctx,
		pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		fixture.CompanyA,
		fixture.UserA)
	rows, next, err := feed.List(ctx, fixture.CompanyA, activity.ListOptions{UserID: fixture.UserA, Limit: 1})
	if err != nil || len(rows) != 0 || next != nil {
		t.Fatalf("revoked Schedule history = %d/%v, %v", len(rows), next, err)
	}
	page, err := runs.List(ctx, principal, execution.ListOptions{RunType: "schedule", Limit: 1})
	if err != nil || len(page.Items) != 0 || page.Next != nil {
		t.Fatalf("revoked Schedule Run history = %+v, %v", page, err)
	}
}
