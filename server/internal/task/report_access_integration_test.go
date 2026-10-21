//go:build integration

package task

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// A report batch delivered into a member's direct chat with the root Agent
// carries only the reports whose Project that member reads (ADR 0017). The
// rest stay queued, the proactive dispatcher does not pick a Company for
// reports its automation member cannot read, and a reader later receives
// them.
func TestTaskReportBatchesCarryOnlyReadableProjects(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	// A second Project the owner has no grant on (the fixture granted the
	// owners every Project that existed when it was seeded).
	hidden := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, 'hidden', 'Hidden')`,
		hidden,
		fixture.CompanyA,
	)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_report_events (
			company_id, task_id, task_version, task_key, task_title,
			source_agent_id, recipient_agent_id, completion_source,
			completed_at, result_summary, project_id
		) VALUES
			($1, $2, 1, 'task_a', 'Readable', $3, $3, 'manual', clock_timestamp(), 'Done', $4),
			($1, $2, 2, 'task_a', 'Hidden', $3, $3, 'manual', clock_timestamp(), 'Done', $5)`,
		fixture.CompanyA, fixture.TaskA, fixture.AgentA, fixture.ProjectA, hidden)
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := NewReportService(store, bus)
	if err != nil {
		t.Fatalf("construct Task report service: %v", err)
	}

	preview, err := service.Preview(ctx, principal, fixture.DirectConversationA)
	if err != nil || preview.PendingCount != 1 || len(preview.Events) != 1 ||
		preview.Events[0].TaskTitle != "Readable" {
		t.Fatalf("Preview() = %+v, %v; want only the readable Project's report", preview, err)
	}
	sent, err := service.Send(
		ctx,
		principal,
		fixture.DirectConversationA,
		SendReportInput{RequestKey: dbtest.NewUUID(t)},
	)
	if err != nil || sent.EventCount != 1 {
		t.Fatalf("Send() = %+v, %v; want a batch of the one readable report", sent, err)
	}
	var message string
	if err := pool.QueryRow(ctx, `SELECT content FROM conversation_items WHERE id = $1`, sent.MessageID).
		Scan(&message); err != nil {
		t.Fatal(err)
	}
	if want := "<title>Hidden</title>"; strings.Contains(message, want) {
		t.Fatalf("the batch message names a report of an unreadable Project: %s", message)
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*) FROM task_report_events WHERE project_id = $1 AND batch_id IS NULL`, hidden); got != 1 {
		t.Fatalf("hidden report still queued = %d, want 1", got)
	}
	// The batch's own Run finishes; nothing is left that this member reads.
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, sent.RunID)
	if _, err := service.Send(ctx, principal, fixture.DirectConversationA,
		SendReportInput{RequestKey: dbtest.NewUUID(t)}); !errors.Is(err, ErrNoPendingReports) {
		t.Fatalf("Send(only unreadable reports left) error = %v, want %v", err, ErrNoPendingReports)
	}

	// Proactive mode acts for the same member: the Company is not picked
	// while its only pending report is one they cannot read.
	dbtest.MustExec(t, ctx, pool, `
		UPDATE companies
		SET proactive_mode_enabled=true,
		    proactive_mode_enabled_by_user_id=$2,
		    proactive_mode_conversation_id=$3
		WHERE id=$1`, fixture.CompanyA, fixture.UserA, fixture.DirectConversationA)
	if _, err := store.Queries().GetNextProactiveTaskReportTarget(ctx,
		auth.RolesWith(auth.PermissionCompanyProactiveModeManage)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("proactive target with only unreadable reports: error = %v, want no target", err)
	}
	if delivered, err := service.DispatchProactiveOnce(ctx); err != nil || delivered {
		t.Fatalf("DispatchProactiveOnce(unreadable) = %t, %v; want false, nil", delivered, err)
	}

	// Once the member reads the Project, the queued report reaches them.
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, hidden, fixture.UserA, "project.read")
	delivered, err := service.DispatchProactiveOnce(ctx)
	if err != nil || !delivered {
		t.Fatalf("DispatchProactiveOnce(readable) = %t, %v; want true, nil", delivered, err)
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*) FROM task_report_events WHERE batch_id IS NULL`); got != 0 {
		t.Fatalf("reports still queued = %d, want 0", got)
	}
}

// A report reaches a Run only as a batch in one person's direct chat with
// the root Agent. The Run a sent batch starts works for its sender: the
// sender authorized it, it runs on the sender's own setup of the root Agent,
// and it answers in the sender's own direct chat, never in another member's.
// It carries the reports whose Project the sender reads, and the reports
// that record no Project; the rest wait for a reader. A Proactive Mode batch
// works the same way for the member who switched it on.
func TestASentReportBatchRunsForItsSenderInTheirOwnDirectChat(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	// Olivia owns the Company, runs the root Agent on RuntimeA and has her
	// own direct chat with it. Mia is a member with her own computer, her own
	// setup of the root Agent and her own direct chat with it.
	olivia := fixture.UserA
	mia := auth.UserPrincipal{UserID: dbtest.NewUUID(t), CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	miaRuntime, miaChat, payroll := dbtest.NewUUID(t), dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO users (id, email, display_name) VALUES ($1, 'mia@example.com', 'Mia')`,
		mia.UserID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, 'member')`,
		fixture.CompanyA, mia.UserID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO runtimes (id, company_id, daemon_id, name, provider, status, owner_user_id, last_seen_at)
		VALUES ($1, $2, 'daemon-mia', 'Mia''s Mac', 'codex', 'online', $3, now())`,
		miaRuntime, fixture.CompanyA, mia.UserID)
	dbtest.SeedAgentBinding(t, ctx, pool, fixture.CompanyA, fixture.AgentA, mia.UserID, miaRuntime, "codex")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (id, company_id, kind, agent_id, created_by_user_id, title)
		VALUES ($1, $2, 'direct', $3, $4, 'Mia and the CEO')`,
		miaChat, fixture.CompanyA, fixture.AgentA, mia.UserID)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, mia.UserID, "project.read")
	// Payroll is read by Olivia alone.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, 'payroll', 'Payroll')`,
		payroll, fixture.CompanyA)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, payroll, olivia, "project.read")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_report_events (
			company_id, task_id, task_version, task_key, task_title,
			source_agent_id, recipient_agent_id, completion_source,
			completed_at, result_summary, project_id, created_at
		) VALUES
			($1, $2, 1, 'task_a', 'Work', $3, $3, 'manual',
			 clock_timestamp(), 'Done', $4, now() - interval '3 seconds'),
			($1, $2, 2, 'task_a', 'No project', $3, $3, 'manual',
			 clock_timestamp(), 'Done', NULL, now() - interval '2 seconds'),
			($1, $2, 3, 'task_a', 'Payroll', $3, $3, 'manual',
			 clock_timestamp(), 'Done', $5, now() - interval '1 second')`,
		fixture.CompanyA, fixture.TaskA, fixture.AgentA, fixture.ProjectA, payroll)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	service, err := NewReportService(store, bus)
	if err != nil {
		t.Fatalf("construct Task report service: %v", err)
	}

	// Mia cannot preview or send into Olivia's direct chat: a batch goes only
	// into its sender's own chat, and nothing is written.
	if _, err := service.Preview(ctx, mia, fixture.DirectConversationA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Preview(another member's chat) error = %v, want %v", err, ErrNotFound)
	}
	if _, err := service.Send(ctx, mia, fixture.DirectConversationA,
		SendReportInput{RequestKey: dbtest.NewUUID(t)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Send(another member's chat) error = %v, want %v", err, ErrNotFound)
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT (SELECT count(*) FROM task_report_batches)
		     + (SELECT count(*) FROM runs WHERE agent_id = $1 AND status = 'queued')`,
		fixture.AgentA); got != 0 {
		t.Fatalf("batches and queued root-Agent Runs after a refused send = %d, want 0", got)
	}

	// In her own chat Mia sends the report she reads and the one with no
	// Project; Payroll's stays queued.
	sent, err := service.Send(ctx, mia, miaChat, SendReportInput{RequestKey: dbtest.NewUUID(t)})
	if err != nil || sent.EventCount != 2 {
		t.Fatalf("Send(own chat) = %+v, %v; want a batch of the Work and no-Project reports", sent, err)
	}
	assertReportBatchRunWorksFor(t, ctx, pool, sent.BatchID, mia.UserID, miaChat, miaRuntime, fixture.AgentA)
	var batched []string
	if err := pool.QueryRow(ctx, `
		SELECT array_agg(task_title ORDER BY task_version) FROM task_report_events WHERE batch_id = $1`,
		sent.BatchID).Scan(&batched); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(batched, []string{"Work", "No project"}) {
		t.Fatalf("Mia's batch carries %v, want [Work No project]", batched)
	}
	var message string
	var author uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT content, author_id FROM conversation_items
		WHERE id = $1 AND conversation_id = $2 AND run_id = $3 AND author_kind = 'user'`,
		sent.MessageID, miaChat, sent.RunID).Scan(&message, &author); err != nil {
		t.Fatalf("read the batch message in Mia's chat: %v", err)
	}
	if author != mia.UserID || strings.Contains(message, "<title>Payroll</title>") {
		t.Fatalf("batch message by %s = %s; want Mia's, without Payroll", author, message)
	}

	// Olivia switches Proactive Mode on. Once the root Agent is idle, it
	// sends only into her own direct chat: pointed at Mia's chat, it sends
	// nothing.
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, sent.RunID)
	switchProactiveOn := func(chatID uuid.UUID) {
		t.Helper()
		dbtest.MustExec(t, ctx, pool, `
			UPDATE companies
			SET proactive_mode_enabled=true,
			    proactive_mode_enabled_by_user_id=$2,
			    proactive_mode_conversation_id=$3
			WHERE id=$1`, fixture.CompanyA, olivia, chatID)
	}
	switchProactiveOn(miaChat)
	if delivered, err := service.DispatchProactiveOnce(ctx); err != nil || delivered {
		t.Fatalf("DispatchProactiveOnce(Olivia's switch, Mia's chat) = %t, %v; want false, nil", delivered, err)
	}
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT (SELECT count(*) FROM task_report_batches WHERE delivery_type = 'proactive')
		     + (SELECT count(*) FROM runs WHERE agent_id = $1 AND status = 'queued')`,
		fixture.AgentA); got != 0 {
		t.Fatalf("proactive batches and queued root-Agent Runs with Mia's chat named = %d, want 0", got)
	}

	// Named on her own chat, the Payroll report goes there, in a Run she
	// authorized on her own computer.
	switchProactiveOn(fixture.DirectConversationA)
	delivered, err := service.DispatchProactiveOnce(ctx)
	if err != nil || !delivered {
		t.Fatalf("DispatchProactiveOnce() = %t, %v; want true, nil", delivered, err)
	}
	var proactiveBatch uuid.UUID
	var automationUser uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id, automation_user_id FROM task_report_batches
		WHERE company_id = $1 AND delivery_type = 'proactive'`, fixture.CompanyA).
		Scan(&proactiveBatch, &automationUser); err != nil {
		t.Fatalf("read the proactive batch: %v", err)
	}
	if automationUser != olivia {
		t.Fatalf("proactive batch automation user = %s, want Olivia %s", automationUser, olivia)
	}
	assertReportBatchRunWorksFor(t, ctx, pool, proactiveBatch, olivia,
		fixture.DirectConversationA, fixture.RuntimeA, fixture.AgentA)
	if got := countTaskReportRows(t, ctx, pool, `
		SELECT count(*) FROM task_report_events WHERE batch_id = $1 AND project_id = $2`,
		proactiveBatch, payroll); got != 1 {
		t.Fatalf("Payroll reports in Olivia's proactive batch = %d, want 1", got)
	}
}

// assertReportBatchRunWorksFor checks the Run a report batch started: its
// person authorized it, it answers in that person's own direct chat with the
// root Agent, and it runs on that person's own computer.
func assertReportBatchRunWorksFor(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	batchID, personID, chatID, runtimeID, rootAgentID uuid.UUID,
) {
	t.Helper()
	var authorizer, conversationID, runtime, agent, chatCreator uuid.UUID
	var chatKind string
	if err := pool.QueryRow(ctx, `
		SELECT runs.execution_authorized_by_user_id, runs.conversation_id, runs.runtime_id, runs.agent_id,
		       chat.kind, chat.created_by_user_id
		FROM task_report_batches AS batch
		JOIN runs ON runs.id = batch.run_id
		JOIN conversations AS chat ON chat.id = runs.conversation_id
		WHERE batch.id = $1 AND batch.conversation_id = runs.conversation_id`, batchID).
		Scan(&authorizer, &conversationID, &runtime, &agent, &chatKind, &chatCreator); err != nil {
		t.Fatalf("read the Run of batch %s: %v", batchID, err)
	}
	if authorizer != personID || conversationID != chatID || chatKind != "direct" || chatCreator != personID ||
		runtime != runtimeID || agent != rootAgentID {
		t.Fatalf("batch Run = authorized by %s in %s chat %s of %s on runtime %s for Agent %s; "+
			"want authorized by %s in their own direct chat %s on runtime %s for the root Agent %s",
			authorizer, chatKind, conversationID, chatCreator, runtime, agent,
			personID, chatID, runtimeID, rootAgentID)
	}
}

// A batch that carries reports from two Projects raises task.report_changed
// once per Project, each for that Project's readers (ADR 0017), so the
// readers of either Project refetch their pending-report preview.
func TestTaskReportBatchSignalsEveryProjectItCarries(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate isolated database: %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("construct store: %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	second := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO projects (id, company_id, key, title) VALUES ($1, $2, 'second', 'Second')`,
		second,
		fixture.CompanyA,
	)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, second, fixture.UserA, "project.read")
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_report_events (
			company_id, task_id, task_version, task_key, task_title,
			source_agent_id, recipient_agent_id, completion_source,
			completed_at, result_summary, project_id, created_at
		) VALUES
			($1, $2, 1, 'task_a', 'First', $3, $3, 'manual', clock_timestamp(), 'Done', $4, now() - interval '3 seconds'),
			($1, $2, 2, 'task_a', 'Second', $3, $3, 'manual', clock_timestamp(), 'Done', $5, now() - interval '2 seconds'),
			($1, $2, 3, 'task_a', 'First again', $3, $3, 'manual', clock_timestamp(), 'Done', $4, now() - interval '1 second')`,
		fixture.CompanyA, fixture.TaskA, fixture.AgentA, fixture.ProjectA, second)
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("construct event bus: %v", err)
	}
	var frames []ReportChangedPayload
	if err := bus.Subscribe(EventReportChanged, func(_ context.Context, event events.Event) error {
		payload, err := events.PayloadOf[ReportChangedPayload](event)
		if err != nil {
			return err
		}
		frames = append(frames, payload)
		return nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	service, err := NewReportService(store, bus)
	if err != nil {
		t.Fatalf("construct Task report service: %v", err)
	}
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	sent, err := service.Send(
		ctx,
		principal,
		fixture.DirectConversationA,
		SendReportInput{RequestKey: dbtest.NewUUID(t)},
	)
	if err != nil || sent.EventCount != 3 {
		t.Fatalf("Send() = %+v, %v; want a batch of all three reports", sent, err)
	}
	if len(frames) != 2 {
		t.Fatalf("task.report_changed frames = %d, want one per Project (2): %+v", len(frames), frames)
	}
	revisions := map[uuid.UUID]int64{}
	for _, frame := range frames {
		audience := frame.ProjectAudience()
		if audience == nil || audience.CompanyWide || !slices.Contains(audience.UserIDs, fixture.UserA) {
			t.Fatalf("frame for Project %s has audience %+v; want its readers, the sender among them",
				frame.ProjectID, audience)
		}
		revisions[frame.ProjectID] = frame.TaskRevision
	}
	// Each frame names its Project's latest report in the batch.
	if revisions[fixture.ProjectA] != 3 || revisions[second] != 2 {
		t.Fatalf("frames by Project = %v; want %s at revision 3 and %s at revision 2",
			revisions, fixture.ProjectA, second)
	}
	secondAudience := frames[1].ProjectAudience()
	if frames[0].ProjectID == second {
		secondAudience = frames[0].ProjectAudience()
	}
	if len(secondAudience.UserIDs) != 1 {
		t.Fatalf("second Project frame audience = %v; want only its one reader", secondAudience.UserIDs)
	}
}
