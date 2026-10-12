//go:build integration

package execution

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	internaldb "boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

// Every file list and download check reaches attachment_access, a UNION ALL
// view, through a lookup that names the file and the person (ADR 0017, TECH
// "How queries use the views"). The lookup must stay one indexed probe per
// file: no hashed subplan and no scan of the tables behind the view's arms,
// which would build the person's whole readable set (every channel file,
// every Project file) to answer a page of a few. A list may still walk its
// own rows as the planner likes; only the access tables are pinned.
func TestProjectFileListsProbeAttachmentAccessPerRow(t *testing.T) {
	t.Parallel()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := internaldb.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	f := dbtest.SeedFixture(t, ctx, pool)
	company, owner := f.CompanyA, f.UserA

	// A channel of 3000 messages, each with one file, and colleagues in it.
	channel := dbtest.SeedDiscussionChannel(t, ctx, pool, company, owner, nil)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			INSERT INTO conversation_items
				(company_id, conversation_id, seq, type, author_kind, author_id,
				 content, body, request_key, send_fingerprint)
			SELECT $1, $2, n, 'user_message', 'user', $3, 'm', '{"markdown":"m"}'::jsonb, gen_random_uuid(),
				decode(repeat('11', 32), 'hex')
			FROM generate_series(1, 3000) AS n`, company, channel, owner)
	})
	// The Task chat of Project A with 1000 messages, each with one file.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversation_items (company_id, conversation_id, run_id, seq, type, author_kind, author_id, content)
		SELECT $1, $2, $3, 10 + n, 'user_message', 'user', $4, 'm' FROM generate_series(1, 1000) AS n`,
		company, f.ConversationA, f.RunA, owner)
	dbtest.MustExec(t, ctx, pool, `INSERT INTO attachments
		(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
		SELECT $1, $2, gen_random_uuid(), 'bulk-' || n, 'text/plain', 1, 'ready' FROM generate_series(1, 6000) AS n`,
		company, owner)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		SELECT $1, item.id, file.id, 0
		FROM (SELECT id, row_number() OVER (ORDER BY id) AS n FROM conversation_items
		      WHERE company_id = $1 AND type = 'user_message' AND conversation_id IN ($2, $3)) AS item
		JOIN (SELECT id, row_number() OVER (ORDER BY id) AS n FROM attachments
		      WHERE company_id = $1 AND filename LIKE 'bulk-%') AS file ON file.n = item.n`,
		company, channel, f.ConversationA)
	// 40 more Projects of 40 documents each from the files no message holds,
	// Project A among the holders, and colleagues granted on every Project,
	// so a scan of project_files or project_grants would show.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO projects (company_id, key, title)
		SELECT $1, 'bulk_' || n, 'Bulk ' || n FROM generate_series(1, 40) AS n`, company)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
		SELECT $1, project.id, file.id, 'document'
		FROM (SELECT id, row_number() OVER (ORDER BY id) - 1 AS n FROM projects WHERE company_id = $1) AS project
		JOIN (SELECT attachment.id, row_number() OVER (ORDER BY attachment.id) - 1 AS n FROM attachments AS attachment
		      WHERE attachment.company_id = $1 AND attachment.filename LIKE 'bulk-%'
		        AND NOT EXISTS (SELECT 1 FROM message_attachments AS link WHERE link.attachment_id = attachment.id)) AS file
		  ON file.n / 40 = project.n`, company)
	dbtest.MustExec(t, ctx, pool, `
		WITH colleague AS (
			INSERT INTO users (email, display_name)
			SELECT 'files-colleague-' || n || '@example.com', 'Colleague ' || n FROM generate_series(1, 40) AS n
			RETURNING id
		), membership AS (
			INSERT INTO company_members (company_id, user_id, role)
			SELECT $1, colleague.id, 'member' FROM colleague
			RETURNING user_id
		), channel_membership AS (
			INSERT INTO conversation_members (company_id, conversation_id, user_id, source_type)
			SELECT $1, $3, membership.user_id, 'channel' FROM membership
		)
		INSERT INTO project_grants (company_id, project_id, user_id, permission, granted_by_user_id)
		SELECT $1, project.id, membership.user_id, 'project.read', $2
		FROM membership CROSS JOIN projects AS project WHERE project.company_id = $1`, company, owner, channel)
	// Many chats of every kind the readers view walks, so a plan that builds
	// the person's readable chats would show: 40 Task chats per Project, 200
	// channels the colleagues are in, 200 direct chats of the owner.
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			WITH seeded AS MATERIALIZED (
				SELECT project.id AS project_id, gen_random_uuid() AS task_id,
				       gen_random_uuid() AS conversation_id,
				       row_number() OVER (ORDER BY project.id, series.n) AS n
				FROM projects AS project
				CROSS JOIN generate_series(1, 40) AS series(n)
				WHERE project.company_id = $1 AND project.key LIKE 'bulk_%'
			), conversation_rows AS (
				INSERT INTO conversations (id, company_id, kind, task_id, project_id, title)
				SELECT conversation_id, $1, 'task', task_id, project_id, 'Bulk' FROM seeded
			)
			INSERT INTO tasks (id, company_id, conversation_id, project_id, milestone_type,
			                   number, key, title, assignee_user_id, creator_user_id)
			SELECT task_id, $1, conversation_id, project_id, 'product',
			       1000 + n, 'bulk_task_' || n, 'Bulk ' || n, $2, $2
			FROM seeded`, company, owner)
	})
	dbtest.MustExec(t, ctx, pool, `
		WITH channel AS (
			INSERT INTO conversations (company_id, kind, created_by_user_id, title, request_key)
			SELECT $1, 'channel', $2, 'Bulk channel ' || n, gen_random_uuid() FROM generate_series(1, 200) AS n
			RETURNING id
		), settings AS (
			INSERT INTO conversation_channels (company_id, conversation_id, create_fingerprint)
			SELECT $1, channel.id, decode(repeat('00', 32), 'hex') FROM channel
		)
		INSERT INTO conversation_members (company_id, conversation_id, user_id, source_type)
		SELECT $1, channel.id, member.user_id, 'channel'
		FROM channel JOIN company_members AS member ON member.company_id = $1`, company, owner)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO conversations (company_id, kind, agent_id, created_by_user_id, title, archived_at)
		SELECT $1, 'direct', $2, $3, 'Direct ' || n, now() FROM generate_series(1, 200) AS n`, company, f.AgentA, owner)
	// Forty Groups of forty more colleagues, each Group holding grants on
	// every Project, so a scan of company_group_members or
	// project_group_grants would show too (000101). Seeded after the channels:
	// a private channel holds at most 50 people, and the channel seed adds
	// every Company member.
	dbtest.SeedGroupColleagues(t, ctx, pool, company, "files-group-colleague", 40)
	// Artifacts: 300 Outputs of the owner's, each with a file, so the
	// view's Artifact exclusion has rows to look through.
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			WITH run AS (
				INSERT INTO runs (company_id, agent_id, conversation_id, runtime_id, execution_authorized_by_user_id,
				                  status, run_type)
				SELECT $1, $2, $3, $4, $5, 'completed', 'direct_chat' FROM generate_series(1, 300)
				RETURNING id
			), numbered AS (
				SELECT id AS run_id, row_number() OVER (ORDER BY id) AS n, gen_random_uuid() AS asset_id,
				       gen_random_uuid() AS version_id, gen_random_uuid() AS output_id, gen_random_uuid() AS file_id
				FROM run
			), file AS (
				INSERT INTO attachments (id, company_id, uploaded_by_user_id, request_key, filename, content_type,
				                         size_bytes, status)
				SELECT file_id, $1, $5, gen_random_uuid(), 'artifact-' || n, 'text/plain', 1, 'ready' FROM numbered
			), asset AS (
				INSERT INTO assets (id, company_id, title, media_class, created_by_user_id)
				SELECT asset_id, $1, 'Artifact ' || n, 'document', $5 FROM numbered
			), version AS (
				INSERT INTO asset_versions (id, company_id, asset_id, version_number, attachment_id, source_run_id,
				                            filename, content_type, size_bytes, integrity_status)
				SELECT version_id, $1, asset_id, 1, file_id, run_id, 'artifact-' || n, 'text/plain', 1, 'verified'
				FROM numbered
			), output AS (
				INSERT INTO outputs (id, company_id, run_id, title, summary, presentation_fingerprint, owner_user_id, actor_agent_id, conversation_id)
				SELECT output_id, $1, run_id, 'Artifact ' || n, 'Artifact', repeat('b', 64), $5, $2, $3 FROM numbered
			)
			INSERT INTO output_items (company_id, output_id, ordinal, role, label, asset_version_id)
			SELECT $1, output_id, 0, 'primary', 'Artifact', version_id FROM numbered`,
			company, f.AgentA, f.DirectConversationA, f.RuntimeA, owner)
	})
	dbtest.MustExec(t, ctx, pool, `ANALYZE`)

	recorder := &explainRecorder{pool: pool}
	queries := db.New(recorder)
	// These tables grow with a Company's chats, files and grants. The small
	// ones (Companies, members, accounts, Projects) and the empty
	// channel_invocations are scanned whatever the plan, and a list may walk
	// its own messages as it likes.
	scans := regexp.MustCompile(`(?m)Seq Scan on (project_grants|company_group_members|project_group_grants|` +
		`project_files|conversation_members|conversations|` +
		`message_attachments)( |$)`)
	assertProbesPerRow := func(name string) {
		t.Helper()
		plan, err := pool.Query(ctx, `EXPLAIN (COSTS OFF) `+recorder.sql, recorder.args...)
		if err != nil {
			t.Fatalf("%s explain: %v", name, err)
		}
		lines, err := pgx.CollectRows(plan, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(lines, "\n")
		if strings.Contains(text, "hashed SubPlan") || scans.MatchString(text) {
			t.Errorf("%s builds the readable set instead of probing per file:\n%s", name, text)
		}
	}
	firstMessages := func(conversationID uuid.UUID) []uuid.UUID {
		t.Helper()
		rows, err := pool.Query(
			ctx,
			`SELECT id FROM conversation_items WHERE conversation_id = $1 AND type = 'user_message'
			ORDER BY seq LIMIT 3`,
			conversationID,
		)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil || len(ids) != 3 {
			t.Fatalf("first messages = %v, %v", ids, err)
		}
		return ids
	}
	channelMessages, taskMessages := firstMessages(channel), firstMessages(f.ConversationA)

	discussion, err := queries.ListDiscussionAttachments(ctx, db.ListDiscussionAttachmentsParams{
		CompanyID: company, MessageIds: channelMessages, ActorUserID: owner,
	})
	if err != nil || len(discussion) != 3 {
		t.Fatalf("ListDiscussionAttachments = %d, %v; want 3", len(discussion), err)
	}
	assertProbesPerRow("the channel file list")
	frozen, err := queries.ListRunChannelAttachments(ctx, db.ListRunChannelAttachmentsParams{
		CompanyID: company, MessageIds: channelMessages, ActorUserID: owner,
	})
	if err != nil || len(frozen) != 3 {
		t.Fatalf("ListRunChannelAttachments = %d, %v; want 3", len(frozen), err)
	}
	assertProbesPerRow("the channel Run's file freeze")
	bulk, err := queries.ListMessageAttachmentsBulk(ctx, db.ListMessageAttachmentsBulkParams{
		CompanyID: company, MessageIds: taskMessages, ActorUserID: owner,
	})
	if err != nil || len(bulk) != 3 {
		t.Fatalf("ListMessageAttachmentsBulk = %d, %v; want 3", len(bulk), err)
	}
	assertProbesPerRow("the chat page file list")
	if _, err := queries.ListMessagesWithWithheldAttachments(ctx, db.ListMessagesWithWithheldAttachmentsParams{
		CompanyID: company, MessageIds: taskMessages, ActorUserID: owner,
	}); err != nil {
		t.Fatalf("ListMessagesWithWithheldAttachments: %v", err)
	}
	assertProbesPerRow("the withheld-file check of a chat page")
	one, err := queries.ListMessageAttachments(ctx, db.ListMessageAttachmentsParams{
		CompanyID: company, MessageID: taskMessages[0], ActorUserID: owner,
	})
	if err != nil || len(one) != 1 {
		t.Fatalf("ListMessageAttachments = %d, %v; want 1", len(one), err)
	}
	assertProbesPerRow("one message's file list")
	sources, err := queries.ListTaskResultSources(ctx, db.ListTaskResultSourcesParams{
		CompanyID: company, ConversationID: f.ConversationA, ActorUserID: owner, RowLimit: 20,
	})
	if err != nil || len(sources) != 20 {
		t.Fatalf("ListTaskResultSources = %d, %v; want 20", len(sources), err)
	}
	assertProbesPerRow("the Task result source list")
	projectFiles, err := queries.ListProjectFiles(ctx, db.ListProjectFilesParams{
		CompanyID: company, ProjectIds: []uuid.UUID{f.ProjectA}, ActorUserID: owner,
	})
	if err != nil || len(projectFiles) != 40 {
		t.Fatalf("ListProjectFiles = %d, %v; want 40", len(projectFiles), err)
	}
	assertProbesPerRow("one Project's file list")
	documents, err := queries.ListTaskProjectDocuments(ctx, db.ListTaskProjectDocumentsParams{
		CompanyID: company, TaskID: f.TaskA, ActorUserID: owner,
	})
	if err != nil || len(documents) != 40 {
		t.Fatalf("ListTaskProjectDocuments = %d, %v; want 40", len(documents), err)
	}
	assertProbesPerRow("the Task Run's document freeze")
	inputs, err := queries.ListTaskRunInputAttachments(ctx, db.ListTaskRunInputAttachmentsParams{
		TaskID: f.TaskA, CompanyID: company, AuthorizedByUserID: owner,
		TriggerAttachmentIds: []uuid.UUID{}, CarriedQueueItemIds: []uuid.UUID{}, RowLimit: 20,
	})
	if err != nil || len(inputs) != 20 {
		t.Fatalf("ListTaskRunInputAttachments = %d, %v; want 20", len(inputs), err)
	}
	assertProbesPerRow("the Task Run's chat file freeze")
	if _, err := queries.AuthorizeAttachmentDownload(ctx, db.AuthorizeAttachmentDownloadParams{
		CompanyID: company, AttachmentID: discussion[0].Attachment.ID, ActorUserID: owner,
	}); err != nil {
		t.Fatalf("AuthorizeAttachmentDownload: %v", err)
	}
	assertProbesPerRow("a person's download")
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE runs SET execution_authorized_by_user_id = $2 WHERE id = $1`,
		f.RunA,
		owner,
	)
	if _, err := queries.AuthorizeRunAttachmentDownload(ctx, db.AuthorizeRunAttachmentDownloadParams{
		CompanyID: company, AttachmentID: documents[0].ID, RunID: f.RunA, AuthorizerUserID: owner,
		FrozenProjectDocument: true,
	}); err != nil {
		t.Fatalf("AuthorizeRunAttachmentDownload: %v", err)
	}
	assertProbesPerRow("a Run's download")

	// Three queued prompts on the Task chat, each holding a Project A document.
	dbtest.MustExec(t, ctx, pool, `
		WITH document AS (
			SELECT file.attachment_id, row_number() OVER (ORDER BY file.attachment_id) AS n
			FROM project_files AS file WHERE file.company_id = $1 AND file.project_id = $2
			ORDER BY file.attachment_id LIMIT 3
		), item AS (
			INSERT INTO conversation_run_queue_items
				(company_id, conversation_id, initiated_by_user_id, request_key, content, "position")
			SELECT $1, $3, $4, gen_random_uuid(), 'queued ' || n, n FROM document
			RETURNING id, "position"
		)
		INSERT INTO conversation_run_queue_attachments (company_id, queue_item_id, attachment_id, "position")
		SELECT $1, item.id, document.attachment_id, 0 FROM item JOIN document ON document.n = item."position"`,
		company, f.ProjectA, f.ConversationA, owner)
	queued, err := pool.Query(
		ctx,
		`SELECT id FROM conversation_run_queue_items WHERE conversation_id = $1`,
		f.ConversationA,
	)
	if err != nil {
		t.Fatal(err)
	}
	queueItems, err := pgx.CollectRows(queued, pgx.RowTo[uuid.UUID])
	if err != nil {
		t.Fatal(err)
	}
	queueFiles, err := queries.ListRunQueueItemAttachments(ctx, db.ListRunQueueItemAttachmentsParams{
		QueueItemIds: queueItems, ViewerUserID: owner,
	})
	if err != nil || len(queueFiles) != 3 {
		t.Fatalf("ListRunQueueItemAttachments = %d, %v; want 3", len(queueFiles), err)
	}
	assertProbesPerRow("the queued prompts' file list")
	if _, err := queries.ListSocialMedia(ctx, db.ListSocialMediaParams{
		CompanyID: company, ConversationID: channel, ActorUserID: owner, MaxVideoBytes: 1 << 20,
	}); err != nil {
		t.Fatalf("ListSocialMedia: %v", err)
	}
	assertProbesPerRow("a chat's Social media list")
	found, err := queries.SearchCompany(ctx, db.SearchCompanyParams{
		PageSize: 20, Query: "m", TsQuery: "m:*", ActorUserID: owner, CompanyID: company,
		IncludeMessages: true, HasFile: true, ConversationFilter: uuid.NullUUID{UUID: channel, Valid: true},
	})
	if err != nil || len(found) == 0 {
		t.Fatalf("SearchCompany(hasFile) = %d, %v; want hits", len(found), err)
	}
	assertProbesPerRow("the search file filter")
}
