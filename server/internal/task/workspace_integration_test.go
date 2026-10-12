//go:build integration

package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func TestTaskReadsReturnCanonicalProjection(t *testing.T) {
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
	fixture := dbtest.SeedFixture(t, ctx, pool)
	prerequisiteID, prerequisiteConversationID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dependentID, dependentConversationID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE users SET avatar_url = 'https://example.test/schema-a.png' WHERE id = $1`,
		fixture.UserA)
	dbtest.MustExec(t, ctx, pool, `
		UPDATE runs SET status = 'failed', failure_reason = 'provider_timeout' WHERE id = $1`,
		fixture.RunA)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		if _, err := tx.Exec(
			ctx, `
			INSERT INTO conversations (id, company_id, kind, task_id, title, project_id) VALUES
				($1, $2, 'task', $3, 'Prerequisite', (SELECT id FROM projects WHERE company_id=$2 AND key='work')),
				($4, $2, 'task', $5, 'Dependent', (SELECT id FROM projects WHERE company_id=$2 AND key='work'))`,
			prerequisiteConversationID, fixture.CompanyA, prerequisiteID,
			dependentConversationID, dependentID,
		); err != nil {
			t.Fatalf("seed Task projection conversations: %v", err)
		}
		if _, err := tx.Exec(
			ctx, `
						INSERT INTO tasks (project_id,
				id, company_id, conversation_id,
				milestone_type, number, key, title,
				executed_by_agent_id, assignee_user_id, creator_user_id
			) VALUES
				((SELECT id FROM projects WHERE company_id=$2 AND key='work'), $3, $2, $1, 'product', 2, 'prerequisite', 'Prerequisite', $6, $7, $7),
				((SELECT id FROM projects WHERE company_id=$2 AND key='work'), $5, $2, $4, 'product', 3, 'dependent', 'Dependent', $6, $7, $7)`,
			prerequisiteConversationID, fixture.CompanyA, prerequisiteID,
			dependentConversationID, dependentID, fixture.AgentA, fixture.UserA,
		); err != nil {
			t.Fatalf("seed Task projection Tasks: %v", err)
		}
		if _, err := tx.Exec(
			ctx, `
			INSERT INTO task_dependencies (project_id,
				company_id, dependent_task_id, prerequisite_task_id, created_by_user_id
			) VALUES
				((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$2), $1, $2, $3, $4),
				((SELECT project_id FROM tasks WHERE company_id=$1 AND id=$5), $1, $5, $2, $4)`,
			fixture.CompanyA, fixture.TaskA, prerequisiteID, fixture.UserA, dependentID,
		); err != nil {
			t.Fatalf("seed Task projection graph: %v", err)
		}
	})

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewTaskService() error = %v", err)
	}
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}
	dependencies, err := service.ListDependencies(ctx, principal, fixture.TaskA, nil)
	if err != nil {
		t.Fatalf("ListDependencies() error = %v", err)
	}
	if len(dependencies.BlockedBy) != 1 || len(dependencies.Blocks) != 1 {
		t.Fatalf("ListDependencies() = %+v", dependencies)
	}
	// A dependency card names both halves of the assignment (ADR 0014): the
	// member who owns the Task and the Agent that does its work.
	blockedBy := dependencies.BlockedBy[0]
	if blockedBy.Task.ID != prerequisiteID || blockedBy.Task.Key != "prerequisite" ||
		blockedBy.Task.Number != 2 ||
		blockedBy.Task.Title != "Prerequisite" || blockedBy.Task.Status != StatusBacklog ||
		blockedBy.Task.Assignee.Kind != ActorUser || blockedBy.Task.Assignee.ID != fixture.UserA ||
		blockedBy.Task.ExecutedBy != (ActorRef{
			Kind: ActorAgent, ID: fixture.AgentA, DisplayName: "Agent A",
		}) || blockedBy.Task.Revision != 1 ||
		blockedBy.Revision != 1 {
		t.Fatalf("ListDependencies() blockedBy = %+v", blockedBy)
	}
	blocks := dependencies.Blocks[0]
	if blocks.Task.ID != dependentID || blocks.Task.Key != "dependent" || blocks.Task.Number != 3 ||
		blocks.Task.Title != "Dependent" || blocks.Task.Status != StatusBacklog ||
		blocks.Task.Assignee.ID != fixture.UserA ||
		blocks.Task.ExecutedBy.ID != fixture.AgentA ||
		blocks.Task.ExecutedBy.DisplayName != "Agent A" ||
		blocks.Task.Revision != 1 ||
		blocks.Revision != 1 {
		t.Fatalf("ListDependencies() blocks = %+v", blocks)
	}

	// The graph's stubs carry the number every surface displays, so a
	// dependency canvas labels a Task the way the board does (F3).
	graph, err := service.ListDependencyGraph(ctx, principal, DependencyGraphOptions{})
	if err != nil {
		t.Fatalf("ListDependencyGraph() error = %v", err)
	}
	numbersByID := map[uuid.UUID]int64{}
	for _, stub := range graph.Tasks {
		numbersByID[stub.ID] = stub.Number
	}
	if len(graph.Dependencies) != 2 || numbersByID[prerequisiteID] != 2 ||
		numbersByID[fixture.TaskA] != 1 || numbersByID[dependentID] != 3 {
		t.Fatalf("ListDependencyGraph() = %+v, want numbers 2 -> 1 -> 3", graph)
	}

	value, err := service.Get(ctx, principal, fixture.TaskA, nil)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if value.Assignee.ID != fixture.UserA || value.Assignee.DisplayName != "Schema A" ||
		value.ExecutedBy.ID != fixture.AgentA || value.ExecutedBy.DisplayName != "Agent A" ||
		value.ExecutedBy.AvatarURL != nil || value.Creator.ID != fixture.UserA ||
		value.Creator.DisplayName != "Schema A" || value.Creator.AvatarURL == nil ||
		*value.Creator.AvatarURL != "https://example.test/schema-a.png" ||
		value.BlockedByCount != 1 || value.DependenciesResolved {
		t.Fatalf("Get() projection = %+v", value)
	}
	// The assignee's own machine runs the Agent, so the board says the Task
	// can start without naming that machine (ADR 0014).
	if !value.Execution.AssigneeReady || value.Execution.UnreadyReason != nil {
		t.Fatalf("Get() execution = %+v, want ready", value.Execution)
	}

	page, err := service.List(ctx, principal, ListOptions{}, nil)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	var listed *Response
	for index := range page.Tasks {
		if page.Tasks[index].ID == fixture.TaskA {
			listed = &page.Tasks[index]
			break
		}
	}
	if listed == nil || listed.BlockedByCount != 1 || listed.DependenciesResolved {
		t.Fatalf("List() canonical Task = %+v", listed)
	}

	workspace, err := service.Workspace(ctx, principal, fixture.TaskA, WorkspaceOptions{MessageLimit: 20}, nil)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}
	if workspace.Conversation.TaskID != fixture.TaskA ||
		workspace.Task.BlockedByCount != 1 ||
		workspace.Task.DependenciesResolved || len(workspace.Dependencies.BlockedBy) != 1 ||
		workspace.Dependencies.BlockedBy[0].Task.ID != prerequisiteID ||
		len(workspace.Dependencies.Blocks) != 1 || workspace.Dependencies.Blocks[0].Task.ID != dependentID {
		t.Fatalf("Workspace() canonical Task = %+v", workspace.Task)
	}
}

func TestWorkspaceReturnsBoundedBulkSnapshotWithoutDirectConversationLeak(t *testing.T) {
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
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	// The member reads the fixture Project (ADR 0017); the leak this test
	// guards is the direct Conversation, not the Project.
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB, "project.read")

	// Run ids come back from runs_id_seq: the bigserial is the identity, so a
	// test cannot mint one.
	seedRun := func(status string) uuid.UUID {
		t.Helper()
		var runID uuid.UUID
		if err := pool.QueryRow(
			ctx, `
			INSERT INTO runs (
				company_id, agent_id, task_id, conversation_id, runtime_id,
				status
			, run_type) VALUES ($1, $2, $3, $4, $5, $6, (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $4 ))
			RETURNING id`,
			fixture.CompanyA, fixture.AgentA, fixture.TaskA,
			fixture.ConversationA, fixture.RuntimeA, status,
		).Scan(&runID); err != nil {
			t.Fatalf("seed %s Run: %v", status, err)
		}
		return runID
	}
	secondRunID := seedRun("completed")
	activeRunID := seedRun("running")
	var directRunID uuid.UUID
	if err := pool.QueryRow(
		ctx, `
		INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, status, run_type)
		VALUES ($1, $2, $3, $4, 'completed', (SELECT CASE kind WHEN 'task' THEN 'task' WHEN 'direct' THEN 'direct_chat' ELSE 'schedule' END FROM conversations WHERE id = $3 ))
		RETURNING id`,
		fixture.CompanyA, fixture.AgentA, fixture.DirectConversationA, fixture.RuntimeA,
	).Scan(&directRunID); err != nil {
		t.Fatalf("seed direct Conversation Run: %v", err)
	}

	secondMessageID := dbtest.NewUUID(t)
	thirdMessageID := dbtest.NewUUID(t)
	directSecretID := dbtest.NewUUID(t)
	// seq 1 is the fixture's user message on RunA. The two answers below take
	// the next two slots of the SAME conversation-wide counter, one per Run.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items (
			id, company_id, conversation_id, run_id, seq, type, author_kind, author_id, content
		) VALUES
			($1, $2, $3, $4, 2, 'agent_text', 'agent', $5, 'first output'),
			($6, $2, $3, $7, 3, 'agent_text', 'agent', $5, 'second output'),
			($8, $2, $9, $10, 1, 'user_message', 'user', $11, 'private direct secret')`,
		secondMessageID, fixture.CompanyA, fixture.ConversationA, fixture.RunA,
		fixture.AgentA, thirdMessageID, secondRunID,
		directSecretID, fixture.DirectConversationA, directRunID, fixture.UserA)

	attachmentID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO attachments (
			id, company_id, uploaded_by_user_id, request_key, filename,
			content_type, size_bytes, status
		) VALUES ($1, $2, $3, $4, 'result.txt', 'text/plain', 6, 'ready')`,
		attachmentID, fixture.CompanyA, fixture.UserA, dbtest.NewUUID(t))
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		VALUES ($1, $2, $3, 0)`, fixture.CompanyA, secondMessageID, attachmentID)

	// Run-detail items for both Runs, interleaved into the one counter after
	// the visible ones. They are what the preview limit has to bound, and what
	// the chat page must never show.
	seq := 4
	for _, runID := range []uuid.UUID{fixture.RunA, secondRunID} {
		for index := 0; index <= WorkspaceTranscriptPreviewLimit; index++ {
			dbtest.MustExec(t, ctx, pool, `
				INSERT INTO conversation_items (
					company_id, conversation_id, run_id, seq, type, author_kind, author_id,
					item_key, content, parts, content_sha256
				) VALUES (
					$1, $2, $3, $4, 'tool_result', 'agent', $5, $6,
					$7::text,
					jsonb_build_array(jsonb_build_object(
						'type', 'tool_result', 'callId', $6::text, 'output', $7::text
					)),
					digest($7::text, 'sha256')
				)`, fixture.CompanyA, fixture.ConversationA, runID, seq, fixture.AgentA,
				fmt.Sprintf("fixture-%d", seq), fmt.Sprintf("row %d", seq))
			seq++
		}
	}
	queueItemID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_run_queue_items (
			id, company_id, conversation_id, message_id, initiated_by_user_id,
			request_key, content, position
		) VALUES ($1, $2, $3, $4, $5, $6, 'queued follow-up', 1)`,
		queueItemID, fixture.CompanyA, fixture.ConversationA, dbtest.NewUUID(t),
		fixture.UserA, dbtest.NewUUID(t))

	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("NewBus() error = %v", err)
	}
	service, err := NewTaskService(store, bus, nil)
	if err != nil {
		t.Fatalf("NewTaskService() error = %v", err)
	}
	result, err := service.Workspace(ctx, auth.UserPrincipal{
		UserID: fixture.UserB, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
	}, fixture.TaskA, WorkspaceOptions{MessageLimit: 2}, nil)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}
	// ThroughSeq is the whole stream's head, not the timeline's: the window is
	// still the newest two messages even though dozens of run-detail items sit
	// above them.
	if result.Task.ID != fixture.TaskA || result.Conversation.ID != fixture.ConversationA ||
		len(result.Messages) != 2 || result.Messages[0].Message.ID != secondMessageID ||
		len(result.Messages[0].Attachments) != 1 || result.Messages[1].Message.ID != thirdMessageID ||
		result.ThroughSeq != int64(seq-1) ||
		len(result.RunTimelines) != 2 || len(result.ActiveRuns) != 1 ||
		result.ActiveRuns[0].ID != activeRunID || len(result.RunQueue.Items) != 1 ||
		result.RunQueue.Items[0].ID != queueItemID || result.Dependencies.BlockedBy == nil ||
		result.Dependencies.Blocks == nil {
		t.Fatalf("Workspace() = %+v", result)
	}
	// UserB reads a Task whose Runs execute on UserA's machine: the Runs and
	// the queue are theirs to read, the machine is not (ADR 0014).
	if result.RunQueue.CurrentRun == nil || result.RunQueue.CurrentRun.RuntimeID != nil ||
		result.ActiveRuns[0].ViewerRuntimeID != nil {
		t.Fatalf("Workspace() runtimes = current %+v, active %v; want neither named",
			result.RunQueue.CurrentRun, result.ActiveRuns[0].ViewerRuntimeID)
	}
	if encoded, err := json.Marshal(result); err != nil || strings.Contains(string(encoded), fixture.RuntimeA.String()) {
		t.Fatalf("Workspace() names another member's runtime: %s, %v", encoded, err)
	}
	// One timeline per Run the window refers to, each bounded to the newest
	// WorkspaceTranscriptPreviewLimit items. Their seqs are conversation-wide,
	// so they are ascending but not contiguous across the two Runs.
	for _, timeline := range result.RunTimelines {
		if len(timeline.Items) != WorkspaceTranscriptPreviewLimit || !timeline.HasMore ||
			timeline.ThroughSeq != timeline.Items[len(timeline.Items)-1].Seq {
			t.Fatalf("bounded run timeline = %+v", timeline)
		}
		for index := 1; index < len(timeline.Items); index++ {
			if timeline.Items[index].Seq <= timeline.Items[index-1].Seq {
				t.Fatalf("run timeline is not seq-ordered: %+v", timeline.Items)
			}
		}
	}
	if result.RunTimelines[0].RunID != fixture.RunA ||
		result.RunTimelines[1].RunID != secondRunID {
		t.Fatalf("run timelines = %+v, want one per Run of the window in window order",
			result.RunTimelines)
	}
	for _, message := range result.Messages {
		if message.Message.ID == directSecretID || message.Message.Content == "private direct secret" {
			t.Fatalf("Workspace leaked direct Conversation Message: %+v", message)
		}
		// A tool result must never surface as a chat line: the type predicate
		// in ListConversationMessages is the whole visibility rule.
		if message.Message.Type != conversation.ItemTypeUserMessage &&
			message.Message.Type != conversation.ItemTypeAgentText {
			t.Fatalf("Workspace chat window carried run detail: %+v", message.Message)
		}
	}
}

func TestWorkspaceIsCompanyScopedAndReturnsExplicitEmptyCollections(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, _ := db.NewStore(pool)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	bus, _ := events.NewBus(events.BusOptions{})
	service, _ := NewTaskService(store, bus, nil)

	result, err := service.Workspace(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, fixture.TaskA, WorkspaceOptions{MessageLimit: 50}, nil)
	if err != nil {
		t.Fatalf("Workspace() error = %v", err)
	}
	if result.RunTimelines == nil || result.ActiveRuns == nil || result.Dependencies.BlockedBy == nil ||
		result.Dependencies.Blocks == nil || result.RunQueue.Items == nil {
		t.Fatalf("Workspace empty collections = %+v", result)
	}
	if _, err := service.Workspace(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, fixture.TaskB, WorkspaceOptions{MessageLimit: 50}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Workspace(foreign Task) error = %v, want ErrNotFound", err)
	}
}

func TestWorkspaceDirectConversationIsCreatorOnlyWithinCompany(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, _ := db.NewStore(pool)
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	directTaskID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
				INSERT INTO tasks (project_id,
			id, company_id, conversation_id,
			milestone_type, number, key, title,
			assignee_user_id, creator_user_id
		) VALUES ((SELECT id FROM projects WHERE company_id=$2 AND key='work'),
			$1, $2, $3, 'product',
			99, 'private_direct_workspace', 'Private workspace', $4, $4
		)`,
		directTaskID, fixture.CompanyA, fixture.DirectConversationA, fixture.UserA)
	bus, _ := events.NewBus(events.BusOptions{})
	service, _ := NewTaskService(store, bus, nil)

	if _, err := service.Workspace(ctx, auth.UserPrincipal{
		UserID: fixture.UserB, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleMember,
	}, directTaskID, WorkspaceOptions{MessageLimit: 50}, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Workspace(other member, direct Conversation) error = %v, want ErrNotFound", err)
	}
	result, err := service.Workspace(ctx, auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}, directTaskID, WorkspaceOptions{MessageLimit: 50}, nil)
	if err != nil || result.Conversation.ID != fixture.DirectConversationA {
		t.Fatalf("Workspace(direct Conversation creator) = %+v, %v", result, err)
	}
}
