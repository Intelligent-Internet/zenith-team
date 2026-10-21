//go:build integration

package db

import (
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

// 000086's cutover (ADR 0017) stops the work its backfill left without
// access. Only active owners hold grants after it, so a Run in a Task chat
// whose authorizer may no longer run work there is cancelled with
// project_access_revoked, its tokens are deleted and its Task takes the
// cancelled Run's terminal state, while the owner's Runs and every Run
// outside a Task chat keep going. Pending Task-chat Approvals and Questions
// are addressed again to the Project's deciders; other copies close; a
// request with no decider goes to every active owner. Items queued in Task
// chats by people who may no longer post there are removed, their files
// released and their nudges closed.
func TestProjectAccessCutoverCancelsRunsAndReaddressesDecisions(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	toEightyFive := func(m *migrate.Migrate) error { return m.Migrate(85) }
	if err := runMigration(ctx, pool, "migrate to 85", toEightyFive); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)

	memberID, adminID := dbtest.NewUUID(t), dbtest.NewUUID(t)
	for id, role := range map[uuid.UUID]string{memberID: "member", adminID: "admin"} {
		dbtest.MustExec(t, ctx, pool, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $2)`,
			id, role+"@cutover.example.com")
		dbtest.MustExec(
			t,
			ctx,
			pool,
			`INSERT INTO company_members (company_id, user_id, role) VALUES ($1, $2, $3)`,
			f.CompanyA,
			id,
			role,
		)
	}

	// Two Tasks the member is running in Project A: one whose Run still owns
	// its execution generation, one a newer Run has since taken over.
	seedTask := func(key string, number, generation int) (uuid.UUID, uuid.UUID) {
		taskID, conversationID := dbtest.NewUUID(t), dbtest.NewUUID(t)
		dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
			dbtest.MustExecTx(t, ctx, tx, `INSERT INTO conversations (id, company_id, kind, task_id, title)
				VALUES ($1, $2, 'task', $3, $4)`, conversationID, f.CompanyA, taskID, key)
			dbtest.MustExecTx(
				t,
				ctx,
				tx,
				`INSERT INTO tasks
				(id, company_id, conversation_id, project_id, milestone_type, number, key, title,
				 assignee_user_id, executed_by_agent_id, creator_user_id, status, execution_generation)
				VALUES ($1, $2, $3, $4, 'product', $5, $6, $6, $7, $8, $7, 'in_progress', $9)`,
				taskID,
				f.CompanyA,
				conversationID,
				f.ProjectA,
				number,
				key,
				memberID,
				f.AgentA,
				generation,
			)
		})
		return taskID, conversationID
	}
	ownedTask, ownedConversation := seedTask("owned", 2, 3)
	staleTask, staleConversation := seedTask("stale", 3, 5)

	// A Task chat whose Task was deleted before the migration keeps no Project.
	legacyConversation, legacyTask := dbtest.NewUUID(t), dbtest.NewUUID(t)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `INSERT INTO conversations (id, company_id, kind, task_id, title)
			VALUES ($1, $2, 'task', $3, 'Legacy')`, legacyConversation, f.CompanyA, legacyTask)
		dbtest.MustExecTx(t, ctx, tx, `INSERT INTO tasks
			(id, company_id, conversation_id, project_id, milestone_type, number, key, title,
			 assignee_user_id, executed_by_agent_id, creator_user_id)
			VALUES ($1, $2, $3, $4, 'product', 4, 'legacy', 'Legacy', $5, $6, $5)`,
			legacyTask, f.CompanyA, legacyConversation, f.ProjectA, f.UserA, f.AgentA)
	})
	dbtest.MustExec(t, ctx, pool, `UPDATE conversations SET task_id = NULL WHERE id = $1`, legacyConversation)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM tasks WHERE id = $1`, legacyTask)

	seedRun := func(companyID, agentID, runtimeID, conversationID uuid.UUID, taskID, authorizer any,
		runType, status string, generation any,
	) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO runs
			(company_id, agent_id, task_id, conversation_id, runtime_id, run_type, status,
			 fire_at, execution_authorized_by_user_id, task_execution_generation)
			VALUES ($1, $2, $3, $4, $5, $6, $7,
			 CASE WHEN $7 = 'deferred' THEN now() + interval '1 hour' END, $8, $9)
			RETURNING id`,
			companyID, agentID, taskID, conversationID, runtimeID, runType, status, authorizer, generation).
			Scan(&id); err != nil {
			t.Fatalf("seed %s Run: %v", status, err)
		}
		return id
	}
	taskRun := func(conversationID uuid.UUID, taskID, authorizer any, status string, generation any) uuid.UUID {
		return seedRun(
			f.CompanyA,
			f.AgentA,
			f.RuntimeA,
			conversationID,
			taskID,
			authorizer,
			"task",
			status,
			generation,
		)
	}
	ownerQueued := taskRun(f.ConversationA, nil, f.UserA, "queued", nil)
	ownerWaiting := taskRun(f.ConversationA, nil, f.UserA, "waiting_local_directory", nil)
	memberRunning := taskRun(ownedConversation, ownedTask, memberID, "running", 3)
	memberStale := taskRun(staleConversation, staleTask, memberID, "running", 4)
	memberDeferred := taskRun(f.ConversationA, nil, memberID, "deferred", nil)
	adminDispatched := taskRun(f.ConversationA, nil, adminID, "dispatched", nil)
	unauthorized := taskRun(f.ConversationA, nil, nil, "queued", nil)
	memberCompleted := taskRun(f.ConversationA, nil, memberID, "completed", nil)
	legacyQueued := taskRun(legacyConversation, nil, f.UserA, "queued", nil)
	memberDirect := seedRun(f.CompanyA, f.AgentA, f.RuntimeA, f.DirectConversationA, nil, memberID,
		"direct_chat", "queued", nil)
	// Company B's only owner has a disabled account: no grant of theirs counts.
	disabledOwnerQueued := seedRun(f.CompanyB, f.AgentB, f.RuntimeB, f.ConversationB, nil, f.UserB,
		"task", "queued", nil)

	for _, runID := range []uuid.UUID{ownerQueued, memberRunning} {
		dbtest.MustExec(t, ctx, pool, `INSERT INTO run_tokens (run_id, token_hash, expires_at)
			VALUES ($1::uuid, decode(md5($1::uuid::text), 'hex'), now() + interval '1 hour')`, runID)
	}

	// Decisions raised before the migration, addressed under the old rules.
	seedApproval := func(companyID, conversationID, agentID, runID uuid.UUID, taskID, originUserID any,
		status string,
	) uuid.UUID {
		var continuation any
		if status != "pending" {
			continuation = "not_required"
		}
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO approvals
			(company_id, status, rule_key, title, preview_markdown, requested_by_agent_id, origin_run_id,
			 request_key, task_id, conversation_id, origin_user_id, canonical_action, request_sha256,
			 action_sha256, expires_at, continuation_status)
			VALUES ($1, $2, 'company_plan', 'Apply the plan', 'preview', $3, $4, gen_random_uuid(), $5, $6, $7,
			 '{}'::jsonb, decode(repeat('ab', 32), 'hex'), decode(repeat('cd', 32), 'hex'),
			 now() + interval '1 day', $8)
			RETURNING id`, companyID, status, agentID, runID, taskID, conversationID, originUserID, continuation).
			Scan(&id); err != nil {
			t.Fatalf("seed %s Approval: %v", status, err)
		}
		return id
	}
	seedQuestion := func(companyID, conversationID, agentID, runID, taskID uuid.UUID) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO questions
			(company_id, title, asked_by_agent_id, origin_run_id, request_key, task_id, conversation_id,
			 prompts, request_sha256, expires_at)
			VALUES ($1, 'Which color?', $2, $3, gen_random_uuid(), $4, $5, '[]'::jsonb,
			 decode(repeat('ef', 32), 'hex'), now() + interval '1 day')
			RETURNING id`, companyID, agentID, runID, taskID, conversationID).Scan(&id); err != nil {
			t.Fatalf("seed Question: %v", err)
		}
		return id
	}
	seedInbox := func(companyID, recipient, conversationID uuid.UUID, kind, entityType string, entityID uuid.UUID,
		dedupe string, read bool,
	) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO inbox_items
			(company_id, recipient_user_id, conversation_id, kind, entity_type, entity_id, title, body,
			 dedupe_key, read_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'Old title', 'Old body', $7, CASE WHEN $8 THEN now() END)
			RETURNING id`, companyID, recipient, conversationID, kind, entityType, entityID.String(), dedupe, read).
			Scan(&id); err != nil {
			t.Fatalf("seed Inbox item: %v", err)
		}
		return id
	}

	taskApproval := seedApproval(f.CompanyA, f.ConversationA, f.AgentA, f.RunA, f.TaskA, nil, "pending")
	approvalKey := "approval:" + taskApproval.String() + ":requested"
	approvalToAdmin := seedInbox(f.CompanyA, adminID, f.ConversationA, "approval_request", "approval",
		taskApproval, approvalKey, false)
	approvalToMember := seedInbox(f.CompanyA, memberID, f.ConversationA, "approval_request", "approval",
		taskApproval, approvalKey, true)
	decidedApproval := seedApproval(f.CompanyA, f.ConversationA, f.AgentA, f.RunA, f.TaskA, nil, "approved")
	decidedToAdmin := seedInbox(f.CompanyA, adminID, f.ConversationA, "approval_request", "approval",
		decidedApproval, "approval:"+decidedApproval.String()+":requested", false)
	directApproval := seedApproval(f.CompanyA, f.DirectConversationA, f.AgentA, f.RunA, nil, f.UserA, "pending")
	directToOwner := seedInbox(f.CompanyA, f.UserA, f.DirectConversationA, "approval_request", "approval",
		directApproval, "approval:"+directApproval.String()+":requested", false)

	taskQuestion := seedQuestion(f.CompanyA, f.ConversationA, f.AgentA, f.RunA, f.TaskA)
	questionKey := "question:" + taskQuestion.String() + ":asked"
	questionToOwner := seedInbox(f.CompanyA, f.UserA, f.ConversationA, "question_request", "question",
		taskQuestion, questionKey, true)
	questionToMember := seedInbox(f.CompanyA, memberID, f.ConversationA, "question_request", "question",
		taskQuestion, questionKey, false)
	orphanQuestion := seedQuestion(f.CompanyB, f.ConversationB, f.AgentB, f.RunB, f.TaskB)
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET disabled_at = now() WHERE id = $1`, f.UserB)

	// Items queued in Task chats: the member's saved Message holds a file and
	// nudged the owner; the owner's own prompt stays; an item in the chat
	// with no Project goes whoever wrote it.
	seedQueued := func(conversationID, author uuid.UUID, position int) (uuid.UUID, uuid.UUID) {
		var id, messageID uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO conversation_run_queue_items
			(company_id, conversation_id, initiated_by_user_id, request_key, content, position)
			VALUES ($1, $2, $3, gen_random_uuid(), 'queued words', $4) RETURNING id, message_id`,
			f.CompanyA, conversationID, author, position).Scan(&id, &messageID); err != nil {
			t.Fatalf("seed queued item: %v", err)
		}
		return id, messageID
	}
	memberQueued, memberQueuedMessage := seedQueued(ownedConversation, memberID, 1)
	ownerQueuedItem, _ := seedQueued(ownedConversation, f.UserA, 2)
	legacyQueuedItem, _ := seedQueued(legacyConversation, f.UserA, 1)
	var queuedFile uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO attachments
		(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
		VALUES ($1, $2, gen_random_uuid(), 'notes.txt', 'text/plain', 1, 'ready') RETURNING id`,
		f.CompanyA, memberID).Scan(&queuedFile); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, ctx, pool, `INSERT INTO conversation_run_queue_attachments
		(company_id, queue_item_id, attachment_id, position) VALUES ($1, $2, $3, 0)`,
		f.CompanyA, memberQueued, queuedFile)
	memberQueuedNudge := seedInbox(f.CompanyA, f.UserA, ownedConversation, "task_run_requested", "message",
		memberQueuedMessage, "task_run_requested:"+memberQueuedMessage.String(), false)

	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("migrate to head: %v", err)
	}

	for _, test := range []struct {
		name   string
		id     uuid.UUID
		status string
		error  string
	}{
		{"owner's queued Run", ownerQueued, "queued", ""},
		{"owner's Run waiting for its directory", ownerWaiting, "waiting_local_directory", ""},
		{"member's running Run", memberRunning, "cancelled", "project_access_revoked"},
		{"member's Run a newer one replaced", memberStale, "cancelled", "project_access_revoked"},
		{"member's deferred Run", memberDeferred, "cancelled", "project_access_revoked"},
		{"admin's dispatched Run", adminDispatched, "cancelled", "project_access_revoked"},
		{"Run with no authorizer", unauthorized, "cancelled", "project_access_revoked"},
		{"member's completed Run", memberCompleted, "completed", ""},
		{"owner's Run in a chat with no Project", legacyQueued, "cancelled", "project_access_revoked"},
		{"member's direct-chat Run", memberDirect, "queued", ""},
		{"disabled owner's Run", disabledOwnerQueued, "cancelled", "project_access_revoked"},
	} {
		var status, runError string
		var fireAtCleared bool
		if err := pool.QueryRow(ctx, `SELECT status, COALESCE(error, ''), fire_at IS NULL FROM runs WHERE id = $1`,
			test.id).
			Scan(&status, &runError, &fireAtCleared); err != nil {
			t.Fatalf("%s: read Run: %v", test.name, err)
		}
		if status != test.status || runError != test.error {
			t.Errorf("%s = %s/%q, want %s/%q", test.name, status, runError, test.status, test.error)
		}
		if test.status == "cancelled" && !fireAtCleared {
			t.Errorf("%s kept its fire_at", test.name)
		}
	}

	var ownerTokens, memberTokens int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE run_id = $1), count(*) FILTER (WHERE run_id = $2)
		FROM run_tokens`, ownerQueued, memberRunning).Scan(&ownerTokens, &memberTokens); err != nil {
		t.Fatal(err)
	}
	if ownerTokens != 1 || memberTokens != 0 {
		t.Errorf("run tokens owner/member = %d/%d, want 1/0", ownerTokens, memberTokens)
	}

	for _, test := range []struct {
		name      string
		id        uuid.UUID
		status    string
		version   int64
		completed bool
	}{
		{"Task its cancelled Run owned", ownedTask, "cancelled", 2, true},
		{"Task a newer Run owns", staleTask, "in_progress", 1, false},
	} {
		var status string
		var version int64
		var completed bool
		if err := pool.QueryRow(ctx, `SELECT status, version, completed_at IS NOT NULL FROM tasks WHERE id = $1`,
			test.id).
			Scan(&status, &version, &completed); err != nil {
			t.Fatalf("%s: read Task: %v", test.name, err)
		}
		if status != test.status || version != test.version || completed != test.completed {
			t.Errorf("%s = %s v%d completed=%v, want %s v%d completed=%v",
				test.name, status, version, completed, test.status, test.version, test.completed)
		}
	}

	for _, test := range []struct {
		name, state, reason string
		id                  uuid.UUID
		version             int64
	}{
		{"member's queued Message", "removed", "project_access_revoked", memberQueued, 2},
		{"owner's queued prompt", "queued", "", ownerQueuedItem, 1},
		{"owner's item in a chat with no Project", "removed", "project_access_revoked", legacyQueuedItem, 2},
	} {
		var state, reason string
		var version int64
		var removedAt bool
		if err := pool.QueryRow(ctx, `SELECT state, COALESCE(removal_reason, ''), version, removed_at IS NOT NULL
			FROM conversation_run_queue_items WHERE id = $1`, test.id).
			Scan(&state, &reason, &version, &removedAt); err != nil {
			t.Fatalf("%s: read queued item: %v", test.name, err)
		}
		if state != test.state || reason != test.reason || version != test.version ||
			removedAt != (test.state == "removed") {
			t.Errorf("%s = %s/%q v%d removedAt=%v, want %s/%q v%d",
				test.name, state, reason, version, removedAt, test.state, test.reason, test.version)
		}
	}
	var fileReleased bool
	if err := pool.QueryRow(ctx, `SELECT released_at IS NOT NULL FROM conversation_run_queue_attachments
		WHERE queue_item_id = $1`, memberQueued).Scan(&fileReleased); err != nil {
		t.Fatal(err)
	}
	if !fileReleased {
		t.Error("the removed item's file reservation was not released")
	}

	type inboxState struct {
		open, read bool
		version    int64
	}
	stateOf := func(id uuid.UUID) inboxState {
		t.Helper()
		var state inboxState
		if err := pool.QueryRow(ctx, `SELECT archived_at IS NULL, read_at IS NOT NULL, version
			FROM inbox_items WHERE id = $1`, id).Scan(&state.open, &state.read, &state.version); err != nil {
			t.Fatalf("read Inbox item: %v", err)
		}
		return state
	}
	for _, test := range []struct {
		name string
		id   uuid.UUID
		want inboxState
	}{
		{"Task Approval's copy for the admin", approvalToAdmin, inboxState{false, true, 2}},
		{"Task Approval's read copy for the member", approvalToMember, inboxState{false, true, 2}},
		{"decided Approval's copy", decidedToAdmin, inboxState{true, false, 1}},
		{"direct Approval's copy", directToOwner, inboxState{true, false, 1}},
		{"Task Question's copy the owner already had", questionToOwner, inboxState{true, true, 1}},
		{"Task Question's copy for the member", questionToMember, inboxState{false, true, 2}},
		{"nudge about the member's queued Message", memberQueuedNudge, inboxState{false, true, 2}},
	} {
		if got := stateOf(test.id); got != test.want {
			t.Errorf("%s = %+v, want %+v", test.name, got, test.want)
		}
	}

	type addressed struct {
		recipient, conversation, project                uuid.UUID
		kind, entityType, title, body, deepLink, dedupe string
		open                                            bool
	}
	addressedOf := func(entityType string, entityID uuid.UUID) []addressed {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT recipient_user_id, conversation_id, COALESCE(project_id, $3),
				kind, entity_type, title, body, COALESCE(deep_link, ''), dedupe_key,
				archived_at IS NULL AND read_at IS NULL
			FROM inbox_items WHERE entity_type = $1 AND entity_id = $2 AND archived_at IS NULL
			ORDER BY recipient_user_id`, entityType, entityID.String(), uuid.Nil)
		if err != nil {
			t.Fatal(err)
		}
		items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (addressed, error) {
			var item addressed
			err := row.Scan(
				&item.recipient,
				&item.conversation,
				&item.project,
				&item.kind,
				&item.entityType,
				&item.title,
				&item.body,
				&item.deepLink,
				&item.dedupe,
				&item.open,
			)
			return item, err
		})
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	approvalItems := addressedOf("approval", taskApproval)
	wantApproval := addressed{
		recipient: f.UserA, conversation: f.ConversationA, project: f.ProjectA,
		kind: "approval_request", entityType: "approval", title: "Apply the plan",
		body: "An agent needs approval to apply the Company Plan.",
		deepLink: "/app?agentId=" + f.AgentA.String() + "&conversationId=" + f.ConversationA.String() +
			"&conversationKind=task&taskId=" + f.TaskA.String() + "&approvalId=" + taskApproval.String(),
		dedupe: approvalKey, open: true,
	}
	if len(approvalItems) != 1 || approvalItems[0] != wantApproval {
		t.Errorf("Task Approval's open items = %+v, want only %+v", approvalItems, wantApproval)
	}
	if items := addressedOf("question", taskQuestion); len(items) != 1 || items[0].recipient != f.UserA {
		t.Errorf("Task Question's open items = %+v, want only the owner's", items)
	}
	orphanItems := addressedOf("question", orphanQuestion)
	wantOrphan := addressed{
		recipient: f.UserB, conversation: f.ConversationB, project: f.ProjectB,
		kind: "question_request", entityType: "question", title: "Which color?",
		body: "An agent needs your answer before it can continue.",
		deepLink: "/app?agentId=" + f.AgentB.String() + "&conversationId=" + f.ConversationB.String() +
			"&conversationKind=task&taskId=" + f.TaskB.String() + "&questionId=" + orphanQuestion.String(),
		dedupe: "question:" + orphanQuestion.String() + ":asked", open: true,
	}
	if len(orphanItems) != 1 || orphanItems[0] != wantOrphan {
		t.Errorf("Question with no decider = %+v, want only %+v", orphanItems, wantOrphan)
	}
	if items := addressedOf("approval", directApproval); len(items) != 1 || items[0].recipient != f.UserA {
		t.Errorf("direct Approval's open items = %+v, want only the owner's old one", items)
	}
}
