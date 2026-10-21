//go:build integration

package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/schedule"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestUpdateAgentAtomicallyPersistsCanonicalLifecycle(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, eventLog := mustAgentService(t, store)
	agentID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Researcher")
	newParentID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Research Lead")

	var companyVersion int64
	var companyUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT version, updated_at FROM companies WHERE id = $1`, fixture.companyID).
		Scan(&companyVersion, &companyUpdatedAt); err != nil {
		t.Fatalf("read Company before Agent update: %v", err)
	}
	name, title := "Principal Researcher", "Research Principal"
	// The provider is the template's, fixed at creation (ADR 0014): the
	// fixture Agent is codex, so the overrides are codex's vocabulary.
	instructions, provider := "Lead the assigned investigation.", "codex"
	model, thinking := "gpt-5-codex", "high"
	updateInput := UpdateInput{
		ExpectedRevision: 1,
		Name:             &name,
		Title:            &title,
		Instructions:     &instructions,
		ReportsToAgentID: &newParentID,
		Model:            NullableTextChange{Present: true, Value: &model},
		ThinkingLevel:    NullableTextChange{Present: true, Value: &thinking},
	}
	result, err := service.Update(ctx, fixture.principal, agentID, updateInput)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.Revision != 2 ||
		result.Name != name || result.Title != title || result.Instructions != instructions ||
		result.ReportsToAgentID == nil || *result.ReportsToAgentID != newParentID ||
		result.Provider != provider || result.Model == nil || *result.Model != model ||
		result.ThinkingLevel == nil || *result.ThinkingLevel != thinking {
		t.Fatalf("Update() = %+v", result)
	}

	assertAgentUpdatedEvent(t, eventLog, fixture.companyID, fixture.userID, result, 1)
	var gotCompanyVersion int64
	var gotCompanyUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT version, updated_at FROM companies WHERE id = $1`, fixture.companyID).
		Scan(&gotCompanyVersion, &gotCompanyUpdatedAt); err != nil {
		t.Fatalf("read Company after Agent update: %v", err)
	}
	if gotCompanyVersion != companyVersion || !gotCompanyUpdatedAt.Equal(companyUpdatedAt) {
		t.Fatalf(
			"Company mutation = version %d updated %s",
			gotCompanyVersion,
			gotCompanyUpdatedAt,
		)
	}
	replayed, err := service.Update(ctx, fixture.principal, agentID, updateInput)
	if err != nil {
		t.Fatalf("Update(lost-response replay) error = %v", err)
	}
	if replayed.Revision != 2 || !replayed.UpdatedAt.Equal(result.UpdatedAt) {
		t.Fatalf("Update(lost-response replay) = %+v", replayed)
	}

	noChange, err := service.Update(ctx, fixture.principal, agentID, UpdateInput{
		ExpectedRevision: 2,
		Name:             &name,
		Model:            NullableTextChange{Present: true, Value: &model},
	})
	if err != nil {
		t.Fatalf("Update(no-op) error = %v", err)
	}
	if noChange.Revision != 2 || !noChange.UpdatedAt.Equal(result.UpdatedAt) {
		t.Fatalf("Update(no-op) = %+v", noChange)
	}
	staleName := "Stale write"
	if _, err := service.Update(ctx, fixture.principal, agentID, UpdateInput{
		ExpectedRevision: 1, Name: &staleName,
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("Update(stale) error = %v, want ErrRevisionConflict", err)
	}
	// The replay, no-op, and stale attempts above must not stage new events.
	assertAgentUpdatedEvent(t, eventLog, fixture.companyID, fixture.userID, result, 1)
}

func TestRootAgentCreationDurablyCompletesAgentSetup(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, _ := mustAgentService(t, store)

	// One active root per company: retire the fixture's seeded root so this
	// create IS the org's root creation.
	mustAgentExec(t, ctx, pool,
		`UPDATE agents SET archived_at = clock_timestamp() WHERE id = $1`,
		fixture.rootAgentID)
	input := validCreateInput()
	input.ReportsToAgentID = nil
	input.Provider = "codex"
	input.RuntimeID = &fixture.runtimeID
	created, err := service.Create(ctx, fixture.principal, input)
	if err != nil {
		t.Fatalf("Create(root Agent) error = %v", err)
	}
	if created.ReportsToAgentID != nil || created.Provider != "codex" ||
		created.Binding == nil || created.Binding.Runtime.ID != fixture.runtimeID {
		t.Fatalf("Create(root Agent) = %+v, want a root the founder runs on their runtime", created)
	}

	companyRow, err := store.Queries().GetCompanyByID(ctx, fixture.companyID)
	if err != nil {
		t.Fatalf("GetCompanyByID() error = %v", err)
	}
	if !companyRow.AgentSetupCompletedAt.Valid {
		t.Fatal("Company agent_setup_completed_at is NULL after root Agent creation")
	}

	agents, err := service.List(ctx, fixture.principal, ListOptions{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	// Root creation provisions the starter Agents as ordinary reports of the
	// new root: each carries its own appearance and nothing else marks it.
	wantStarters := map[string]string{
		"Lyra": "fold-orchid", "Eli": "fold-periwinkle",
		"Susan": "fold-coral", "Ariana": "fold-lagoon",
	}
	seen := 0
	for _, starter := range agents {
		avatarKey, isStarter := wantStarters[starter.Name]
		if !isStarter {
			continue
		}
		if starter.ReportsToAgentID == nil || *starter.ReportsToAgentID != created.ID ||
			starter.AvatarKey == nil || *starter.AvatarKey != avatarKey ||
			starter.AccentColor == nil || starter.Provider != "codex" ||
			starter.Binding == nil || starter.Binding.Runtime.ID != fixture.runtimeID {
			t.Fatalf("starter Agent %q = %+v", starter.Name, starter)
		}
		seen++
	}
	if seen != len(wantStarters) {
		t.Fatalf("starter Agents provisioned = %d of %d: %+v", seen, len(wantStarters), agents)
	}
}

func TestUpdateAgentRejectsInvalidReportsToTopology(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, _ := mustAgentService(t, store)
	managerID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Manager")
	reportID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, managerID, "Report")

	if _, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, UpdateInput{
		ExpectedRevision: 1, ReportsToAgentID: &managerID,
	}); !errors.Is(err, ErrRootImmutable) {
		t.Fatalf("Update(root parent) error = %v, want ErrRootImmutable", err)
	}
	rootTitle := "Chief Strategy Officer"
	rootUpdate, err := service.Update(ctx, fixture.principal, fixture.rootAgentID, UpdateInput{
		ExpectedRevision: 1, Title: &rootTitle,
	})
	if err != nil || rootUpdate.Revision != 2 || rootUpdate.ReportsToAgentID != nil ||
		rootUpdate.Title != rootTitle {
		t.Fatalf("Update(root metadata) = %+v, %v", rootUpdate, err)
	}
	for name, parentID := range map[string]uuid.UUID{"self": managerID, "descendant": reportID} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Update(ctx, fixture.principal, managerID, UpdateInput{
				ExpectedRevision: 1, ReportsToAgentID: &parentID,
			}); !errors.Is(err, ErrCycle) {
				t.Fatalf("Update(%s parent) error = %v, want ErrCycle", name, err)
			}
		})
	}
	missingParent := uuid.Must(uuid.NewV7())
	if _, err := service.Update(ctx, fixture.principal, managerID, UpdateInput{
		ExpectedRevision: 1, ReportsToAgentID: &missingParent,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(missing parent) error = %v, want ErrNotFound", err)
	}
	archivedParentID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Archived Parent")
	mustAgentExec(t, ctx, pool, `
		WITH statement_clock AS MATERIALIZED (SELECT clock_timestamp() AS now_at)
		UPDATE agents SET archived_at = now_at, updated_at = now_at
		FROM statement_clock WHERE id = $1`, archivedParentID)
	if _, err := service.Update(ctx, fixture.principal, managerID, UpdateInput{
		ExpectedRevision: 1, ReportsToAgentID: &archivedParentID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(archived parent) error = %v, want ErrNotFound", err)
	}
	foreign := seedAgentCompany(t, ctx, pool, "owner")
	if _, err := service.Update(ctx, fixture.principal, managerID, UpdateInput{
		ExpectedRevision: 1, ReportsToAgentID: &foreign.rootAgentID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(foreign parent) error = %v, want ErrNotFound", err)
	}
}

func TestArchiveAndRestoreAgentEnforceDependencies(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, eventLog := mustAgentService(t, store)
	managerID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Manager")
	leafID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, managerID, "Leaf")
	taskAgentID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Task Agent")
	runAgentID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Run Agent")
	seedAgentTask(t, ctx, pool, fixture, taskAgentID, 1, "in_progress", false)
	seedAgentTask(t, ctx, pool, fixture, runAgentID, 2, "done", true)

	if _, err := service.Archive(
		ctx,
		fixture.principal,
		fixture.rootAgentID,
		RevisionInput{ExpectedRevision: 1},
	); !errors.Is(
		err,
		ErrRootImmutable,
	) {
		t.Fatalf("Archive(root) error = %v, want ErrRootImmutable", err)
	}
	if _, err := service.Archive(
		ctx,
		fixture.principal,
		managerID,
		RevisionInput{ExpectedRevision: 1},
	); !errors.Is(
		err,
		ErrArchiveBlocked,
	) {
		t.Fatalf("Archive(active reports) error = %v, want ErrArchiveBlocked", err)
	}
	if _, err := service.Archive(
		ctx,
		fixture.principal,
		taskAgentID,
		RevisionInput{ExpectedRevision: 1},
	); !errors.Is(
		err,
		ErrArchiveBlocked,
	) {
		t.Fatalf("Archive(active Task) error = %v, want ErrArchiveBlocked", err)
	}
	if _, err := service.Archive(
		ctx,
		fixture.principal,
		runAgentID,
		RevisionInput{ExpectedRevision: 1},
	); !errors.Is(
		err,
		ErrArchiveBlocked,
	) {
		t.Fatalf("Archive(active Run) error = %v, want ErrArchiveBlocked", err)
	}

	archivedLeaf, err := service.Archive(ctx, fixture.principal, leafID, RevisionInput{ExpectedRevision: 1})
	if err != nil {
		t.Fatalf("Archive(leaf) error = %v", err)
	}
	if archivedLeaf.ArchivedAt == nil || !archivedLeaf.ArchivedAt.Equal(archivedLeaf.UpdatedAt) ||
		archivedLeaf.Revision != 2 {
		t.Fatalf("Archive(leaf) = %+v", archivedLeaf)
	}
	assertAgentUpdatedEvent(t, eventLog, fixture.companyID, fixture.userID, archivedLeaf, 1)
	archiveReplay, err := service.Archive(ctx, fixture.principal, leafID, RevisionInput{ExpectedRevision: 1})
	if err != nil || archiveReplay.Revision != 2 {
		t.Fatalf("Archive(lost-response replay) = %+v, %v", archiveReplay, err)
	}
	archivedNoop, err := service.Archive(ctx, fixture.principal, leafID, RevisionInput{ExpectedRevision: 2})
	if err != nil || archivedNoop.Revision != 2 {
		t.Fatalf("Archive(current no-op) = %+v, %v", archivedNoop, err)
	}

	archivedManager, err := service.Archive(ctx, fixture.principal, managerID, RevisionInput{ExpectedRevision: 1})
	if err != nil || archivedManager.Revision != 2 {
		t.Fatalf("Archive(manager) = %+v, %v", archivedManager, err)
	}
	// Count 2: the replay and no-op archives above event nothing.
	assertAgentUpdatedEvent(t, eventLog, fixture.companyID, fixture.userID, archivedManager, 2)
	if _, err := service.Restore(
		ctx,
		fixture.principal,
		leafID,
		RevisionInput{ExpectedRevision: 2},
	); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("Restore(with archived parent) error = %v, want ErrNotFound", err)
	}
	restoredManager, err := service.Restore(ctx, fixture.principal, managerID, RevisionInput{ExpectedRevision: 2})
	if err != nil || restoredManager.ArchivedAt != nil || restoredManager.Revision != 3 {
		t.Fatalf("Restore(manager) = %+v, %v", restoredManager, err)
	}
	assertAgentUpdatedEvent(t, eventLog, fixture.companyID, fixture.userID, restoredManager, 3)
	restoredLeaf, err := service.Restore(ctx, fixture.principal, leafID, RevisionInput{ExpectedRevision: 2})
	if err != nil || restoredLeaf.ArchivedAt != nil || restoredLeaf.Revision != 3 {
		t.Fatalf("Restore(leaf) = %+v, %v", restoredLeaf, err)
	}
	assertAgentUpdatedEvent(t, eventLog, fixture.companyID, fixture.userID, restoredLeaf, 4)
	restoreReplay, err := service.Restore(ctx, fixture.principal, leafID, RevisionInput{ExpectedRevision: 2})
	if err != nil || restoreReplay.Revision != 3 {
		t.Fatalf("Restore(lost-response replay) = %+v, %v", restoreReplay, err)
	}
	if _, err := service.Archive(
		ctx,
		fixture.principal,
		leafID,
		RevisionInput{ExpectedRevision: 1},
	); !errors.Is(
		err,
		ErrRevisionConflict,
	) {
		t.Fatalf("Archive(divergent stale request) error = %v, want ErrRevisionConflict", err)
	}
	rearchivedLeaf, err := service.Archive(ctx, fixture.principal, leafID, RevisionInput{ExpectedRevision: 3})
	if err != nil || rearchivedLeaf.ArchivedAt == nil || rearchivedLeaf.Revision != 4 {
		t.Fatalf("Archive(leaf again) = %+v, %v", rearchivedLeaf, err)
	}
	assertAgentUpdatedEvent(t, eventLog, fixture.companyID, fixture.userID, rearchivedLeaf, 5)
	if _, err := service.Archive(
		ctx,
		fixture.principal,
		leafID,
		RevisionInput{ExpectedRevision: 1},
	); !errors.Is(
		err,
		ErrRevisionConflict,
	) {
		t.Fatalf("Archive(multi-generation stale target replay) error = %v, want ErrRevisionConflict", err)
	}
	updatedEvents := len(agentEventsForCompany(eventLog.updated, fixture.companyID))
	if updatedEvents != 5 {
		t.Fatalf("agent.updated events = %d, want 5", updatedEvents)
	}
}

func TestConcurrentAgentUpdateAllowsOneCASWinner(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, eventLog := mustAgentService(t, store)
	agentID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Researcher")
	first, second := "First winner", "Second winner"
	inputs := []UpdateInput{{ExpectedRevision: 1, Name: &first}, {ExpectedRevision: 1, Name: &second}}
	start := make(chan struct{})
	results := make(chan error, len(inputs))
	for _, input := range inputs {
		go func() {
			<-start
			_, err := service.Update(ctx, fixture.principal, agentID, input)
			results <- err
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range inputs {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrRevisionConflict):
			conflicts++
		default:
			t.Errorf("Update(concurrent) error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results = success %d conflict %d", successes, conflicts)
	}
	var version int64
	if err := pool.QueryRow(ctx, `SELECT version FROM agents WHERE id = $1`, agentID).Scan(&version); err != nil {
		t.Fatalf("read concurrent Agent: %v", err)
	}
	updatedEvents := len(agentEventsForCompany(eventLog.updated, fixture.companyID))
	if version != 2 || updatedEvents != 1 {
		t.Fatalf("concurrent persisted state = version %d events %d", version, updatedEvents)
	}
}

func TestAgentMutationReauthorizesStalePrincipal(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	service, _ := mustAgentService(t, store)

	t.Run("stale owner principal", func(t *testing.T) {
		fixture := seedAgentCompany(t, ctx, pool, "owner")
		agentID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Researcher")
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatalf("begin membership race: %v", err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(
			ctx,
			`SELECT id FROM companies WHERE id = $1 FOR UPDATE`,
			fixture.companyID,
		); err != nil {
			t.Fatalf("lock Company: %v", err)
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE company_members SET role = 'member', updated_at = clock_timestamp(), version = version + 1 WHERE company_id = $1 AND user_id = $2`,
			fixture.companyID,
			fixture.userID,
		); err != nil {
			t.Fatalf("demote owner: %v", err)
		}
		name := "Rejected stale write"
		result := make(chan error, 1)
		go func() {
			_, updateErr := service.Update(
				ctx,
				fixture.principal,
				agentID,
				UpdateInput{ExpectedRevision: 1, Name: &name},
			)
			result <- updateErr
		}()
		select {
		case err := <-result:
			t.Fatalf("Update() returned before Company mutation committed: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit membership race: %v", err)
		}
		if err := <-result; !errors.Is(err, ErrForbidden) {
			t.Fatalf("Update(stale owner) error = %v, want ErrForbidden", err)
		}
	})
}

func seedLifecycleAgent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	companyID uuid.UUID,
	parentID uuid.UUID,
	name string,
) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool, `
		INSERT INTO agents (id, company_id, name, title, reports_to_agent_id, provider)
		VALUES ($1, $2, $3, $3, $4,
			(SELECT provider FROM agents WHERE id = $4))`, id, companyID, name, parentID)
	return id
}

func seedAgentTask(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture agentCompanyFixture,
	agentID uuid.UUID,
	number int64,
	status string,
	withRun bool,
) {
	t.Helper()
	conversationID := uuid.New()
	taskID := uuid.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin agent task seed: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO projects(company_id,key,title) VALUES ($1,'work','Work') ON CONFLICT (company_id,key) DO NOTHING`,
		fixture.companyID,
	); err != nil {
		t.Fatal(err)
	}
	// The circular task <-> conversation FK is DEFERRABLE and validates at
	// commit, so the pair seeds inside one transaction.
	if _, err := tx.Exec(ctx, `
		INSERT INTO conversations (id, company_id, kind, task_id, title, project_id)
		VALUES ($1, $2, 'task', $3, 'Agent lifecycle fixture', (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`, conversationID, fixture.companyID, taskID); err != nil {
		t.Fatalf("execute Agent test statement: %v", err)
	}
	if _, err := tx.Exec(
		ctx, `
				INSERT INTO tasks (project_id,
			id, company_id, conversation_id,
			milestone_type, number, key, title, status,
			executed_by_agent_id, assignee_user_id, creator_user_id
		) VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'),
			$1, $2, $3, 'product', $5::bigint,
			'agent_lifecycle_' || ($5::bigint)::text,
			'Agent lifecycle fixture', $6, $4, $7, $7
		)`,
		taskID,
		fixture.companyID,
		conversationID,
		agentID,
		number,
		status,
		fixture.userID,
	); err != nil {
		t.Fatalf("execute Agent test statement: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit agent task seed: %v", err)
	}
	if !withRun {
		return
	}
	// runs.runtime_id is NOT NULL: seed the runtime the run executes on.
	runtimeID := uuid.New()
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status)
		VALUES ($1, $2, $3, 'Lifecycle Runtime', 'claude', 'online')`,
		runtimeID, fixture.companyID, "daemon-lifecycle-"+runtimeID.String())
	// runs.id is a bigserial: let the sequence assign it. The run serves a
	// task, so it holds the task lane.
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runs (company_id, task_id, conversation_id, agent_id, runtime_id, run_type)
		VALUES ($1, $2, $3, $4, $5, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))`,
		fixture.companyID, taskID, conversationID, agentID, runtimeID)
}

func assertAgentUpdatedEvent(
	t *testing.T,
	eventLog *agentEventLog,
	companyID uuid.UUID,
	actorID uuid.UUID,
	wantAgent Response,
	wantCount int,
) {
	t.Helper()
	// The payload's CompanyID is the routing fact the realtime bridge reads,
	// so narrowing on it is exactly what delivery does.
	published := agentEventsForCompany(eventLog.updated, companyID)
	if len(published) != wantCount {
		t.Fatalf("agent.updated event count = %d, want %d", len(published), wantCount)
	}
	event := published[wantCount-1]
	got := event.Payload
	if event.Name != "agent.updated" {
		t.Errorf("event name = %q, want agent.updated", event.Name)
	}
	if event.Actor != events.UserActor(actorID.String()) {
		t.Errorf("agent.updated actor = %#v, want the mutating member %s", event.Actor, actorID)
	}
	if !reflect.DeepEqual(got, wantAgent) {
		t.Errorf("agent.updated payload = %+v, want %+v", got, wantAgent)
	}
	if t.Failed() {
		t.Fatalf("agent.updated event payload = %+v", got)
	}
}

// seedLifecycleRuntime adds one online runtime of the provider to the
// fixture company, on a machine of its own, owned by the fixture owner.
func seedLifecycleRuntime(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture agentCompanyFixture,
	provider string,
) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	mustAgentExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id)
		VALUES ($1, $2, $3, $4, $5, 'online', $6)`,
		id, fixture.companyID, "daemon-"+id.String(), provider+" Runtime", provider, fixture.userID)
	return id
}

// The rebind matrix, rule 1: model and thinking level
// are one provider's vocabulary, so a rebind that crosses providers clears
// each of them the request does not supply — a supplied value is the
// client's choice for the new provider and wins — and a rebind within a
// provider keeps both.
// Creation takes the task-lane cap and the thinking level with it: the cap
// lands on the row (default 3 when absent) and the level is fenced to the
// vocabulary of the runtime's provider, at creation and on every later
// change — the provider the agent ends on is the one that judges it.
func TestCreateAgentPersistsCapAndFencesThinkingLevelToTheProvider(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	service, _ := mustAgentService(t, store)
	claudeRuntimeID := seedLifecycleRuntime(t, ctx, pool, fixture, "claude")

	capValue := int32(7)
	capped := validCreateInput()
	capped.ReportsToAgentID = &fixture.rootAgentID
	capped.Provider = "codex"
	capped.RuntimeID = &fixture.runtimeID
	capped.MaxConcurrentRuns = &capValue
	created, err := service.Create(ctx, fixture.principal, capped)
	if err != nil {
		t.Fatalf("Create(cap 7) error = %v", err)
	}
	if created.MaxConcurrentRuns != 7 {
		t.Fatalf("Create(cap 7).MaxConcurrentRuns = %d, want 7", created.MaxConcurrentRuns)
	}
	defaulted := validCreateInput()
	defaulted.ReportsToAgentID = &fixture.rootAgentID
	defaulted.Provider = "codex"
	defaulted.RuntimeID = &fixture.runtimeID
	if created, err = service.Create(ctx, fixture.principal, defaulted); err != nil {
		t.Fatalf("Create(default cap) error = %v", err)
	}
	if created.MaxConcurrentRuns != 3 {
		t.Fatalf(
			"Create(default cap).MaxConcurrentRuns = %d, want the column default 3",
			created.MaxConcurrentRuns,
		)
	}

	// The level is judged by the TEMPLATE's provider (ADR 0014), whichever
	// machine anyone runs it on. Codex levels are catalog tokens, so a
	// malformed one is refused on a codex template; claude takes its fixed
	// set, so "max" belongs to a claude template.
	malformed := "Extra High"
	foreign := validCreateInput()
	foreign.ReportsToAgentID = &fixture.rootAgentID
	foreign.Provider = "codex"
	foreign.RuntimeID = &fixture.runtimeID
	foreign.ThinkingLevel = &malformed
	if _, err := service.Create(ctx, fixture.principal, foreign); !errors.Is(err, ErrThinkingLevelUnsupported) {
		t.Fatalf("Create(codex template, malformed level) error = %v, want ErrThinkingLevelUnsupported", err)
	}
	// "max" is a well-formed Codex token as well as a Claude level, so the
	// same string is judged by the template's provider and accepted here.
	claudeOnly := "max"
	foreign.ThinkingLevel = &claudeOnly
	foreign.Provider = "claude"
	foreign.RuntimeID = &claudeRuntimeID
	onClaude, err := service.Create(ctx, fixture.principal, foreign)
	if err != nil {
		t.Fatalf("Create(claude template, level max) error = %v", err)
	}
	if onClaude.ThinkingLevel == nil || *onClaude.ThinkingLevel != "max" || onClaude.Provider != "claude" {
		t.Fatalf(
			"Create(claude template, level max) = provider %q level %v",
			onClaude.Provider,
			onClaude.ThinkingLevel,
		)
	}

	// An update judges the level by the same immutable provider: a Codex
	// token is refused on a claude template, and no request can move it.
	codexOnly := "minimal"
	if _, err := service.Update(ctx, fixture.principal, onClaude.ID, UpdateInput{
		ExpectedRevision: onClaude.Revision,
		ThinkingLevel:    NullableTextChange{Present: true, Value: &codexOnly},
	}); !errors.Is(err, ErrThinkingLevelUnsupported) {
		t.Fatalf("Update(claude template, level minimal) error = %v, want ErrThinkingLevelUnsupported", err)
	}
	claudeLevel := "high"
	kept, err := service.Update(ctx, fixture.principal, onClaude.ID, UpdateInput{
		ExpectedRevision: onClaude.Revision,
		ThinkingLevel:    NullableTextChange{Present: true, Value: &claudeLevel},
	})
	if err != nil {
		t.Fatalf("Update(claude template, level high) error = %v", err)
	}
	if kept.Provider != "claude" || kept.ThinkingLevel == nil || *kept.ThinkingLevel != claudeLevel {
		t.Fatalf("Update(claude template) = provider %q level %v", kept.Provider, kept.ThinkingLevel)
	}
}

func TestArchiveAgentDropsWaitingScheduleRun(t *testing.T) {
	pool, store := startAgentPostgres(t)
	ctx := context.Background()
	fixture := seedAgentCompany(t, ctx, pool, "owner")
	agentID := seedLifecycleAgent(t, ctx, pool, fixture.companyID, fixture.rootAgentID, "Scheduled Agent")
	mustAgentExec(
		t,
		ctx,
		pool,
		`INSERT INTO agent_runtime_bindings(company_id,agent_id,user_id,runtime_id,provider) VALUES($1,$2,$3,$4,'codex')`,
		fixture.companyID,
		agentID,
		fixture.userID,
		fixture.runtimeID,
	)
	bus := mustAgentTestBus(t)
	schedules, err := schedule.NewScheduleService(schedule.ServiceOptions{
		Store: store,
		Bus:   bus,
		ConversationUpdater: schedule.ConversationUpdaterFunc(
			func(context.Context, *generated.Queries, *[]events.Event, schedule.UpdateConversationInput) error {
				return nil
			},
		),
		Materializer: schedule.MaterializerFunc(
			func(ctx context.Context, q *generated.Queries, pending *[]events.Event, input schedule.MaterializeInput) (schedule.MaterializeResult, error) {
				result, err := conversation.MaterializeScheduleRunInTx(
					ctx,
					q,
					pending,
					conversation.ScheduleRunInput{
						ScheduleID:     input.ScheduleID,
						CompanyID:      input.CompanyID,
						ConversationID: input.ConversationID,
						CreatorUserID:  input.CreatorUserID,
						AgentID:        input.AgentID,
						Provider:       input.Provider,
						Instructions:   input.Instructions,
						OccurrenceAt:   input.OccurrenceAt,
					},
				)
				return schedule.MaterializeResult{
					Disposition: schedule.FiringDispositionMaterialized,
					RunID:       result.RunID,
					MessageID:   result.MessageID,
				}, err
			},
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	expression, zone := "0 9 * * *", "UTC"
	trigger := schedule.Trigger{Type: schedule.TriggerTypeCron, Expression: &expression, TimeZone: &zone}
	preview, err := schedules.Preview(
		ctx,
		fixture.principal,
		schedule.PreviewInput{
			AgentID:      agentID,
			Name:         "Archive me",
			Instructions: "Read updates",
			Trigger:      trigger,
			ConnectorIDs: []uuid.UUID{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := schedules.Create(
		ctx,
		fixture.principal,
		schedule.CreateInput{
			RequestKey:   uuid.Must(uuid.NewV7()),
			AgentID:      agentID,
			Name:         preview.Name,
			Instructions: preview.Instructions,
			Trigger:      trigger,
			Status:       schedule.StatusActive,
			ReviewDigest: preview.ReviewDigest,
			ConnectorIDs: []uuid.UUID{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	fired, err := schedules.RunNow(
		ctx,
		fixture.principal,
		created.Schedule.ID,
		schedule.RunNowInput{RequestKey: uuid.Must(uuid.NewV7())},
	)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := mustAgentService(t, store)
	if _, err := service.Archive(ctx, fixture.principal, agentID, RevisionInput{ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	run, err := store.Queries().GetRun(ctx, *fired.Firing.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("Run remains %s", run.Status)
	}
	current, err := schedules.Get(ctx, fixture.principal, created.Schedule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != schedule.StatusPaused || current.PauseReason == nil ||
		*current.PauseReason != "agent_unavailable" {
		t.Fatalf("Schedule after archive: %+v", current)
	}
}
