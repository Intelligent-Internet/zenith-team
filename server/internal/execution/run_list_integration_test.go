//go:build integration

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/runfailure"
)

func TestListRunsForCompanyAppliesDocumentedFilters(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	var queuedRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status
		, run_type) VALUES ($1, $2, $3, $4, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.DirectConversationA, fixture.RuntimeA,
	).Scan(&queuedRunID); err != nil {
		t.Fatalf("insert queued run: %v", err)
	}

	service := NewRunService(RunServiceOptions{Queries: generated.New(pool)})
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA}
	base := ListOptions{Limit: 10}
	tests := []struct {
		name   string
		params ListOptions
		want   map[uuid.UUID]bool
	}{
		{
			name:   "status CSV set",
			params: withRunStatuses(base, "completed", "queued"),
			want:   map[uuid.UUID]bool{fixture.RunA: true, queuedRunID: true},
		},
		{
			name: "active",
			params: func() ListOptions {
				params := base
				params.ActiveOnly = true
				return params
			}(),
			want: map[uuid.UUID]bool{queuedRunID: true},
		},
		{
			name: "task",
			params: func() ListOptions {
				params := base
				params.TaskID = &fixture.TaskA
				return params
			}(),
			want: map[uuid.UUID]bool{fixture.RunA: true},
		},
		{
			name: "conversation",
			params: func() ListOptions {
				params := base
				params.ConversationID = &fixture.DirectConversationA
				return params
			}(),
			want: map[uuid.UUID]bool{queuedRunID: true},
		},
		{
			name: "agent",
			params: func() ListOptions {
				params := base
				params.AgentID = &fixture.AgentA
				return params
			}(),
			want: map[uuid.UUID]bool{fixture.RunA: true, queuedRunID: true},
		},
		{
			name: "runtime",
			params: func() ListOptions {
				params := base
				params.RuntimeID = &fixture.RuntimeA
				return params
			}(),
			want: map[uuid.UUID]bool{fixture.RunA: true, queuedRunID: true},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			page, err := service.List(ctx, principal, test.params)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			rows := page.Items
			if len(rows) != len(test.want) {
				t.Fatalf("len(rows) = %d, want %d", len(rows), len(test.want))
			}
			for _, row := range rows {
				if !test.want[row.ID] {
					t.Fatalf("run %d was not expected", row.ID)
				}
			}
		})
	}
}

func TestRunReadsFenceCreatorPrivateDirectConversations(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	secondDirectConversationID := uuid.Must(uuid.NewV7())
	var firstDirectRunID, secondDirectRunID uuid.UUID
	dbtest.MustExec(
		t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`,
		fixture.CompanyA, fixture.UserB,
	)
	// A reader of the Task chat's Project sees its Runs (ADR 0017).
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB, "project.read")
	dbtest.MustExec(
		t, ctx, pool, `
		INSERT INTO conversations (
			id, company_id, kind, agent_id, created_by_user_id, title
		) VALUES ($1, $2, 'direct', $3, $4, 'Direct B in Company A')`,
		secondDirectConversationID, fixture.CompanyA, fixture.AgentA, fixture.UserB,
	)
	// Each direct chat's creator authorizes its Runs, as the enqueue does.
	rows, err := pool.Query(
		ctx, `
		INSERT INTO runs (
			company_id, agent_id, conversation_id, runtime_id, status
		, run_type, execution_authorized_by_user_id) VALUES
			($1, $2, $3, $4, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ), $6),
			($1, $2, $5, $4, 'queued', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $5 ), $7)
		RETURNING id`,
		fixture.CompanyA,
		fixture.AgentA,
		fixture.DirectConversationA,
		fixture.RuntimeA,
		secondDirectConversationID,
		fixture.UserA,
		fixture.UserB,
	)
	if err != nil {
		t.Fatalf("insert direct runs: %v", err)
	}
	directRunIDs := make([]uuid.UUID, 0, 2)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan direct run id: %v", err)
		}
		directRunIDs = append(directRunIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read direct run ids: %v", err)
	}
	if len(directRunIDs) != 2 {
		t.Fatalf("inserted %d direct runs, want 2", len(directRunIDs))
	}
	firstDirectRunID, secondDirectRunID = directRunIDs[0], directRunIDs[1]

	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("events.NewBus() error = %v", err)
	}
	service := NewRunService(RunServiceOptions{
		Queries:    store.Queries(),
		Transactor: store,
		Bus:        bus,
	})
	tests := []struct {
		name         string
		userID       uuid.UUID
		visibleRun   uuid.UUID
		invisibleRun uuid.UUID
	}{
		{
			name:         "first creator sees only their direct run",
			userID:       fixture.UserA,
			visibleRun:   firstDirectRunID,
			invisibleRun: secondDirectRunID,
		},
		{
			name:         "second creator sees only their direct run",
			userID:       fixture.UserB,
			visibleRun:   secondDirectRunID,
			invisibleRun: firstDirectRunID,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			principal := auth.UserPrincipal{UserID: test.userID, CompanyID: fixture.CompanyA}
			page, err := service.List(ctx, principal, ListOptions{Limit: 100})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			rows := page.Items
			visible := make(map[uuid.UUID]bool, len(rows))
			for _, row := range rows {
				visible[row.ID] = true
			}
			if !visible[fixture.RunA] || !visible[test.visibleRun] || visible[test.invisibleRun] {
				t.Fatalf("visible runs = %v, want task and own direct only", visible)
			}

			if _, err := service.Get(ctx, principal, test.visibleRun); err != nil {
				t.Fatalf("Get(own) error = %v", err)
			}
			if _, err := service.Get(ctx, principal, test.invisibleRun); !errors.Is(err, ErrRunNotFound) {
				t.Fatalf("Get(foreign direct) error = %v, want ErrRunNotFound", err)
			}
			if _, err := service.Cancel(
				ctx,
				principal,
				test.invisibleRun,
				"user_cancelled",
			); !errors.Is(
				err,
				ErrRunNotFound,
			) {
				t.Fatalf("Cancel(foreign direct) error = %v, want ErrRunNotFound", err)
			}
			cancelled, err := service.Cancel(ctx, principal, test.visibleRun, "user_cancelled")
			if err != nil {
				t.Fatalf("Cancel(own) error = %v", err)
			}
			if cancelled.Status != "cancelled" {
				t.Fatalf("Cancel(own) status = %q, want cancelled", cancelled.Status)
			}
		})
	}
}

// Every reader of a Task's Project reads its Runs, but stopping one acts on
// the computer it runs on (ADR 0014): its authorizer may, and so may a tasks.assign holder.
// A colleague who can only read it is refused and the Run keeps going.
func TestRunCancelIsTheAuthorizersOrATasksAssignHolders(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	colleague := dbtest.NewUUID(t)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO users (id, email, display_name) VALUES ($1, 'colleague@example.com', 'Colleague')`,
		colleague,
	)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member'), ($1, $3, 'member')`, fixture.CompanyA, fixture.UserB, colleague)
	// Both members work on the Task's Project (ADR 0017), so what tells them
	// apart is only whose computer the Run is on.
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, colleague)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, fixture.RunA)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("events.NewBus() error = %v", err)
	}
	service := NewRunService(RunServiceOptions{Queries: store.Queries(), Transactor: store, Bus: bus})
	// One live Run of the Task at a time, authorized by the member B.
	liveRun := func() uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO runs (
				company_id, agent_id, task_id, conversation_id, runtime_id,
				execution_authorized_by_user_id, status, task_execution_generation, run_type
			) SELECT $1, $2, $3, $4, $5, $6, 'queued', execution_generation, 'task'
			FROM tasks WHERE id = $3
			RETURNING id`,
			fixture.CompanyA, fixture.AgentA, fixture.TaskA, fixture.ConversationA,
			fixture.RuntimeA, fixture.UserB,
		).Scan(&id); err != nil {
			t.Fatalf("insert live Task Run: %v", err)
		}
		return id
	}
	member := func(id uuid.UUID) auth.UserPrincipal {
		return auth.UserPrincipal{UserID: id, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember}
	}

	running := liveRun()
	if _, err := service.Get(ctx, member(colleague), running); err != nil {
		t.Fatalf("Get(colleague) error = %v, want the Task Run readable", err)
	}
	if _, err := service.Cancel(ctx, member(colleague), running, "user_cancelled"); !errors.Is(
		err, ErrRunCancelPermissionRequired,
	) {
		t.Fatalf("Cancel(colleague) error = %v, want ErrRunCancelPermissionRequired", err)
	}
	if current, err := service.Get(ctx, member(fixture.UserB), running); err != nil || current.Status != "queued" {
		t.Fatalf("refused stop left the Run = %+v, %v; want it still queued", current, err)
	}
	stopped, err := service.Cancel(ctx, member(fixture.UserB), running, "user_cancelled")
	if err != nil || stopped.Status != "cancelled" {
		t.Fatalf("Cancel(authorizer) = %+v, %v; want cancelled", stopped, err)
	}

	owner := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner}
	stopped, err = service.Cancel(ctx, owner, liveRun(), "user_cancelled")
	if err != nil || stopped.Status != "cancelled" {
		t.Fatalf("Cancel(tasks.assign holder) = %+v, %v; want cancelled", stopped, err)
	}
}

func TestRunProjectionPaginatesWithoutDuplicates(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	runIDs := make([]uuid.UUID, 0, 3)
	created := time.Date(2026, time.August, 19, 4, 0, 0, 0, time.UTC)
	for index := range 3 {
		var runID uuid.UUID
		if err := pool.QueryRow(
			ctx, `
			INSERT INTO runs (
				company_id, agent_id, conversation_id, runtime_id,
				status, created_at, updated_at
			, run_type) VALUES ($1, $2, $3, $4, 'completed', $5, $5, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
			RETURNING id`,
			fixture.CompanyA, fixture.AgentA, fixture.DirectConversationA,
			fixture.RuntimeA, created.Add(time.Duration(index)*time.Minute),
		).Scan(&runID); err != nil {
			t.Fatalf("insert paged run: %v", err)
		}
		runIDs = append(runIDs, runID)
	}

	service := NewRunService(RunServiceOptions{Queries: generated.New(pool)})
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA}
	options := ListOptions{Limit: 2, ConversationID: &fixture.DirectConversationA}
	first, err := service.List(ctx, principal, options)
	if err != nil {
		t.Fatalf("List(first) error = %v", err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != runIDs[2] ||
		first.Items[1].ID != runIDs[1] || first.Next == nil {
		t.Fatalf("first page = %+v", first)
	}

	options.Before = first.Next
	second, err := service.List(ctx, principal, options)
	if err != nil {
		t.Fatalf("List(second) error = %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != runIDs[0] || second.Next != nil {
		t.Fatalf("second page = %+v", second)
	}
}

func withRunStatuses(
	params ListOptions,
	statuses ...string,
) ListOptions {
	params.Statuses = statuses
	return params
}

// A Task's Runs are every member's to read, but the machine one ran on is
// its registrant's alone (ADR 0014): the registrant reads its runtimeId,
// another member reads null, and the Company-wide lifecycle frame names no
// machine at all. The in-process snapshot keeps the id for the wakeup.
func TestRunReadsNameTheRuntimeOnlyToItsRegistrant(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB, "project.read")
	queries := generated.New(pool)
	service := NewRunService(RunServiceOptions{Queries: queries})
	for _, test := range []struct {
		name   string
		viewer uuid.UUID
		want   *uuid.UUID
	}{
		{name: "registrant", viewer: fixture.UserA, want: &fixture.RuntimeA},
		{name: "another member", viewer: fixture.UserB},
	} {
		t.Run(test.name, func(t *testing.T) {
			principal := auth.UserPrincipal{UserID: test.viewer, CompanyID: fixture.CompanyA}
			page, err := service.List(ctx, principal, ListOptions{Limit: 100})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			got, err := service.Get(ctx, principal, fixture.RunA)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			for _, view := range append(page.Items, got) {
				if !equalRuntimeIDs(view.ViewerRuntimeID, test.want) {
					t.Fatalf(
						"run %s runtimeId = %v, want %v",
						view.ID,
						view.ViewerRuntimeID,
						test.want,
					)
				}
				encoded, err := json.Marshal(view)
				if err != nil {
					t.Fatalf("encode run: %v", err)
				}
				if test.want == nil && strings.Contains(string(encoded), fixture.RuntimeA.String()) {
					t.Fatalf("another member's run names the registrant's runtime: %s", encoded)
				}
			}
			// The runtime filter is "what is my machine working on": another
			// member's runtime matches nothing, so it ties no Run to it.
			onRuntime, err := service.List(
				ctx,
				principal,
				ListOptions{Limit: 100, RuntimeID: &fixture.RuntimeA},
			)
			if err != nil {
				t.Fatalf("List(runtime_id) error = %v", err)
			}
			if (len(onRuntime.Items) > 0) != (test.want != nil) {
				t.Fatalf(
					"List(runtime_id) = %d runs, want runs only for the registrant",
					len(onRuntime.Items),
				)
			}
		})
	}
	run, err := queries.GetRun(ctx, fixture.RunA)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	frame, err := LoadRunSnapshotInTx(ctx, queries, run)
	if err != nil {
		t.Fatalf("LoadRunSnapshotInTx() error = %v", err)
	}
	if frame.ViewerRuntimeID != nil || frame.RuntimeID != fixture.RuntimeA {
		t.Fatalf("frame runtime = wire %v, in-process %s; want none on the wire, %s in process",
			frame.ViewerRuntimeID, frame.RuntimeID, fixture.RuntimeA)
	}
}

func equalRuntimeIDs(got, want *uuid.UUID) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

func TestRunProjectionIncludesJoinedRefsRetryAndAttribution(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	insertRun := func(statement string, args ...any) uuid.UUID {
		t.Helper()
		var runID uuid.UUID
		if err := pool.QueryRow(ctx, statement, args...).Scan(&runID); err != nil {
			t.Fatalf("insert projection fixture run: %v", err)
		}
		return runID
	}
	agentTriggerID := uuid.Must(uuid.NewV7())
	dispatchedAt := time.Date(2026, time.August, 19, 3, 4, 5, 0, time.UTC)
	updatedAt := dispatchedAt.Add(time.Minute)
	fireAt := updatedAt.Add(5 * time.Second)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs
		SET status = 'failed', priority = 7, attempt = 1,
		    error = 'provider secret', failure_reason = $3,
		    transcript_through_seq = 4,
		    updated_at = $2
		WHERE id = $1`, fixture.RunA, updatedAt, string(runfailure.ReasonAgentProviderNetwork))
	childID := insertRun(`
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, attempt, parent_run_id, fire_at
		, run_type) VALUES ($1, $2, $3, $4, $5, 'deferred', 2, $6, $7, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA, fixture.RunA, fireAt)
	waitingID := insertRun(`
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, wait_reason
		, run_type) VALUES ($1, $2, $3, $4, $5, 'waiting_local_directory', 'local_directory_busy', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA)
	// An agent-delegated turn: the Run's first 'user_message' item is
	// authored by an agent. The projection finds it the same way it finds a
	// user-authored one — the seek does not look at who wrote it. The Run is
	// inserted first and the item carries its id, exactly as production
	// writes them.
	agentTriggeredRunID := insertRun(`
		INSERT INTO runs (
			company_id, agent_id, task_id, conversation_id, runtime_id,
			status, updated_at
		, run_type) VALUES ($1, $2, $3, $4, $5, 'completed', now(), (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.TaskA,
		fixture.ConversationA, fixture.RuntimeA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items (
			id, company_id, conversation_id, run_id, seq, type, author_kind,
			author_id, content
		) VALUES ($1, $2, $3, $4, 2, 'user_message', 'agent', $5, 'Agent-triggered work')`,
		agentTriggerID, fixture.CompanyA, fixture.ConversationA,
		agentTriggeredRunID, fixture.AgentA)

	service := NewRunService(RunServiceOptions{Queries: generated.New(pool)})
	principal := auth.UserPrincipal{UserID: fixture.UserA, CompanyID: fixture.CompanyA}
	parent, err := service.Get(ctx, principal, fixture.RunA)
	if err != nil {
		t.Fatalf("Get(parent) error = %v", err)
	}
	if parent.AgentID != fixture.AgentA || parent.RuntimeID != fixture.RuntimeA {
		t.Fatalf("joined ids = agent %s runtime %s", parent.AgentID, parent.RuntimeID)
	}
	if parent.Agent != (NamedRef{ID: fixture.AgentA, Name: "Agent A"}) {
		t.Fatalf("in-process agent ref = %+v", parent.Agent)
	}
	// maxAttempts is derived from the failure reason: agent_error.provider_network
	// carries the wider three-attempt schedule.
	if parent.Attempt != 1 || parent.MaxAttempts != 3 ||
		parent.TranscriptThroughSeq != 4 ||
		parent.TriggerMessageID == nil || *parent.TriggerMessageID != fixture.MessageA ||
		!parent.RetryQueued || parent.Error == nil || *parent.Error != "provider secret" ||
		parent.FailureReason == nil ||
		*parent.FailureReason != string(runfailure.ReasonAgentProviderNetwork) {
		t.Fatalf("parent projection = %+v", parent)
	}
	if !parent.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("projection clock = updated %v", parent.UpdatedAt)
	}

	child, err := service.Get(ctx, principal, childID)
	if err != nil {
		t.Fatalf("Get(child) error = %v", err)
	}
	if child.ParentRunID == nil || *child.ParentRunID != fixture.RunA || child.RetryQueued ||
		child.TriggerMessageID != nil ||
		child.FireAt == nil || !child.FireAt.Equal(fireAt) {
		t.Fatalf("child projection = %+v", child)
	}
	waiting, err := service.Get(ctx, principal, waitingID)
	if err != nil {
		t.Fatalf("Get(waiting) error = %v", err)
	}
	if waiting.WaitReason == nil || *waiting.WaitReason != "local_directory_busy" {
		t.Fatalf("waiting projection = %+v", waiting)
	}

	agentTriggered, err := service.Get(ctx, principal, agentTriggeredRunID)
	if err != nil {
		t.Fatalf("Get(agent-triggered) error = %v", err)
	}
	if agentTriggered.TriggerMessageID == nil ||
		*agentTriggered.TriggerMessageID != agentTriggerID {
		t.Fatalf("agent-authored trigger item = %v, want %s",
			agentTriggered.TriggerMessageID, agentTriggerID)
	}
}

func TestRunReadsRejectStaleMembershipForTaskAndDirectHistory(t *testing.T) {
	t.Parallel()
	h := newRunHarness(t)
	ctx := t.Context()
	directID := h.seedDirectConversation(t)
	directRunID := h.seedQueuedRun(t, directID)
	principal := auth.UserPrincipal{
		CompanyID: h.fixture.CompanyA,
		UserID:    h.fixture.UserA,
		Role:      auth.CompanyRoleOwner,
	}
	for _, runID := range []uuid.UUID{h.fixture.RunA, directRunID} {
		if _, err := h.svc.Get(ctx, principal, runID); err != nil {
			t.Fatalf("active member Get = %v", err)
		}
	}
	dbtest.MustExec(t,
		ctx,
		h.pool,
		`UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		h.fixture.CompanyA,
		h.fixture.UserA)
	page, err := h.svc.List(ctx, principal, ListOptions{AgentID: &h.fixture.AgentA, Limit: 1})
	if err != nil || len(page.Items) != 0 || page.Next != nil {
		t.Fatalf("revoked member page = %+v, %v", page, err)
	}
	for _, runID := range []uuid.UUID{h.fixture.RunA, directRunID} {
		if _, err := h.svc.Get(ctx, principal, runID); !errors.Is(err, ErrRunNotFound) {
			t.Fatalf("revoked member Get(%s) = %v, want not found", runID, err)
		}
	}
}
