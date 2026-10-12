//go:build integration

package company

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestDeleteCompanyHardDeletesGraphAndRevokesEveryMember(t *testing.T) {
	t.Parallel()
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
	bus := companyTestBus(t)
	service, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
	if err != nil {
		t.Fatalf("NewCompanyService() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	// One statement per Exec: a parameterised seed goes over the extended
	// protocol, which carries exactly one command.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO run_tokens (run_id, token_hash, expires_at)
		VALUES ($1, decode('0123456789abcdef', 'hex'), now() + interval '1 hour')`, fixture.RunA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO run_usage (run_id, provider, model, raw_json)
		VALUES ($1, 'codex', 'company-delete-test', '{}'::jsonb)`, fixture.RunA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO run_identity_mappings (legacy_id, run_id)
		VALUES (9223372036854775000, $1)`, fixture.RunA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO activity_events (
			company_id, actor_type, action, entity_type, entity_id, details
		) VALUES (
			$1::uuid, 'user', 'company.delete_test', 'company', $1::text,
			'{"schemaVersion":0,"payload":{}}'::jsonb
		)`, fixture.CompanyA)
	// Delivered reports reference Agents, Messages and Runs while their owning
	// Conversation sits on a different branch of the Company deletion cascade.
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_report_batches (
			company_id, recipient_agent_id, conversation_id, confirmed_by_user_id,
			request_key, message_id, run_id, event_count
		) VALUES
			($1, $2, $3, $4, gen_random_uuid(), $5, $6, 1),
			($7, $8, $9, $10, gen_random_uuid(), $11, $12, 1)`,
		fixture.CompanyA, fixture.AgentA, fixture.ConversationA, fixture.UserA, fixture.MessageA, fixture.RunA,
		fixture.CompanyB, fixture.AgentB, fixture.ConversationB, fixture.UserB, fixture.MessageB, fixture.RunB)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO task_report_events (
			company_id, task_id, task_version, task_key, task_title,
			source_agent_id, recipient_agent_id, completion_source, completed_at, batch_id
		)
		SELECT task.company_id, task.id, task.version, task.key, task.title,
			batch.recipient_agent_id, batch.recipient_agent_id, 'manual', now(), batch.id
		FROM tasks AS task
		JOIN task_report_batches AS batch ON batch.conversation_id = task.conversation_id
		WHERE task.id IN ($1, $2)`, fixture.TaskA, fixture.TaskB)

	authLog := recordTopic(t, bus, TopicAuth)
	memberLog := recordTopic(t, bus, TopicMember)
	companyLog := recordTopic(t, bus, Topic)
	principal := auth.UserPrincipal{
		UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
	}

	if err := service.Delete(ctx, principal, 1); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	var companyCount, memberCount, taskCount, detachedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM companies WHERE id=$1`, fixture.CompanyA).
		Scan(&companyCount); err != nil {
		t.Fatalf("count deleted Company: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM company_members WHERE company_id=$1`, fixture.CompanyA).
		Scan(&memberCount); err != nil {
		t.Fatalf("count deleted memberships: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE company_id=$1`, fixture.CompanyA).
		Scan(&taskCount); err != nil {
		t.Fatalf("count deleted Company Tasks: %v", err)
	}
	if companyCount != 0 || memberCount != 0 || taskCount != 0 {
		t.Fatalf(
			"deleted graph counts = Company %d, memberships %d, Tasks %d; want all zero",
			companyCount,
			memberCount,
			taskCount,
		)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM run_tokens WHERE run_id=$1) +
			(SELECT count(*) FROM run_usage WHERE run_id=$1) +
			(SELECT count(*) FROM run_identity_mappings WHERE run_id=$1) +
			(SELECT count(*) FROM activity_events WHERE company_id=$2)`,
		fixture.RunA, fixture.CompanyA,
	).Scan(&detachedCount); err != nil {
		t.Fatalf("count detached Company records: %v", err)
	}
	if detachedCount != 0 {
		t.Fatalf("detached Company records after deletion = %d, want 0", detachedCount)
	}
	if _, err := store.Queries().GetCompanyByID(ctx, fixture.CompanyB); err != nil {
		t.Fatalf("unrelated Company was affected: %v", err)
	}
	for _, check := range []struct {
		companyID uuid.UUID
		want      int
	}{
		{companyID: fixture.CompanyA, want: 0},
		{companyID: fixture.CompanyB, want: 1},
	} {
		var batches, reports int
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM task_report_batches WHERE company_id=$1),
				(SELECT count(*) FROM task_report_events WHERE company_id=$1)`,
			check.companyID,
		).Scan(&batches, &reports); err != nil {
			t.Fatalf("count Company Task reports: %v", err)
		}
		if batches != check.want || reports != check.want {
			t.Fatalf("Company %s report batches = %d, reports = %d, want %d each",
				check.companyID, batches, reports, check.want)
		}
	}

	revoked := authLog.named(EventAuthCompanyAccessRevoked)
	if len(revoked) != 2 {
		t.Fatalf("auth.company_access_revoked events = %d, want 2", len(revoked))
	}
	gotUsers := make(map[uuid.UUID]bool, len(revoked))
	for _, event := range revoked {
		if event.CompanyID != uuid.Nil || !event.ActivityRecorded {
			t.Fatalf("access revocation envelope = %+v", event)
		}
		receipt := payloadOf[AccessRevokedReceipt](t, event)
		if receipt.CompanyID != fixture.CompanyA || receipt.Reason != AccessReasonCompanyDeleted {
			t.Fatalf("access revocation = %+v", receipt)
		}
		gotUsers[receipt.UserID] = true
	}
	if !gotUsers[fixture.UserA] || !gotUsers[fixture.UserB] {
		t.Fatalf("revoked users = %v", gotUsers)
	}
	if got := len(memberLog.named(EventMemberRemoved)); got != 2 {
		t.Fatalf("member.removed events = %d, want 2", got)
	}
	for _, event := range memberLog.named(EventMemberRemoved) {
		if event.CompanyID != fixture.CompanyA || !event.ActivityRecorded {
			t.Fatalf("member.removed envelope = %+v", event)
		}
	}
	if got := len(companyLog.named(EventCompanyMembershipChanged)); got != 2 {
		t.Fatalf("company.membership_changed events = %d, want 2", got)
	}
	for _, event := range companyLog.named(EventCompanyMembershipChanged) {
		if event.CompanyID != uuid.Nil || !event.ActivityRecorded {
			t.Fatalf("company.membership_changed envelope = %+v", event)
		}
	}

	if err := service.Delete(ctx, principal, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete(replay) error = %v, want ErrNotFound", err)
	}
	if _, err := store.Queries().GetActiveCompanyMembership(ctx, generated.GetActiveCompanyMembershipParams{
		CompanyID: fixture.CompanyA,
		UserID:    fixture.UserB,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted member access error = %v, want pgx.ErrNoRows", err)
	}
}

func TestDeleteCompanyCleansDependentRecords(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		seed   func(*testing.T, context.Context, *pgxpool.Pool, dbtest.Fixture)
		tables []string
	}{
		{
			name:   "custom skills",
			seed:   seedDeletionSkills,
			tables: []string{"skill_imports", "skill_versions", "agent_skills", "run_skills"},
		},
		{
			name: "mail drafts",
			seed: seedDeletionMailDrafts,
			tables: []string{
				"company_mailboxes", "mailbox_draft_groups", "mailbox_drafts", "mailbox_draft_deliveries",
			},
		},
		{
			name: "presentations",
			seed: seedDeletionPresentation,
			tables: []string{
				"project_presentations", "project_presentation_sources", "attachments",
				"wiki_pages", "wiki_revisions", "project_artifacts", "project_artifact_versions",
			},
		},
		{
			name:   "huddles",
			seed:   seedDeletionHuddle,
			tables: []string{"huddles", "huddle_connections", "huddle_invitations"},
		},
		{
			name:   "whiteboard files",
			seed:   seedDeletionWhiteboardFile,
			tables: []string{"boards", "board_files", "board_items", "attachments"},
		},
		{
			name:   "whiteboard chats",
			seed:   seedDeletionWhiteboardChat,
			tables: []string{"boards", "channel_invocations"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pool, _ := dbtest.StartPostgres(t)
			ctx := t.Context()
			if err := db.MigrateUp(ctx, pool); err != nil {
				t.Fatal(err)
			}
			store, err := db.NewStore(pool)
			if err != nil {
				t.Fatal(err)
			}
			bus := companyTestBus(t)
			service, err := NewCompanyService(ServiceOptions{Store: store, Bus: bus})
			if err != nil {
				t.Fatal(err)
			}
			fixture := dbtest.SeedFixture(t, ctx, pool)
			test.seed(t, ctx, pool, fixture)
			test.seed(t, ctx, pool, dbtest.Fixture{
				CompanyA: fixture.CompanyB, ProjectA: fixture.ProjectB, UserA: fixture.UserB,
				AgentA: fixture.AgentB, ConversationA: fixture.ConversationB, RunA: fixture.RunB,
				RuntimeA: fixture.RuntimeB,
			})
			principal := auth.UserPrincipal{
				UserID: fixture.UserA, CompanyID: fixture.CompanyA, Role: auth.CompanyRoleOwner,
			}
			authLog := recordTopic(t, bus, TopicAuth)
			assertCount := func(table string, companyID uuid.UUID, want int) {
				t.Helper()
				var got int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE company_id=$1", companyID).
					Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Fatalf("%s rows for Company %s = %d, want %d", table, companyID, got, want)
				}
			}
			if err := service.Delete(ctx, principal, 2); !errors.Is(err, ErrDeleteConflict) {
				t.Fatalf("Delete(stale revision) error = %v, want ErrDeleteConflict", err)
			}
			for _, table := range test.tables {
				assertCount(table, fixture.CompanyA, 1)
				assertCount(table, fixture.CompanyB, 1)
			}
			if got := len(authLog.named(EventAuthCompanyAccessRevoked)); got != 0 {
				t.Fatalf("revocation events before deletion = %d, want 0", got)
			}
			if err := service.Delete(ctx, principal, 1); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
			for _, table := range test.tables {
				assertCount(table, fixture.CompanyA, 0)
				assertCount(table, fixture.CompanyB, 1)
			}
		})
	}
}

// A Board item places an attachment that the Company cascade also owns.
func seedDeletionWhiteboardFile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f dbtest.Fixture) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool, `
		WITH board AS (
			INSERT INTO boards (company_id, title, created_by) VALUES ($1, 'Plan', $2) RETURNING id
		), file AS (
			INSERT INTO attachments (company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
			VALUES ($1, $2, gen_random_uuid(), 'brief.pdf', 'application/pdf', 10, 'ready') RETURNING id
		), reference AS (
			INSERT INTO board_files (company_id, board_id, attachment_id, created_by)
			SELECT $1, board.id, file.id, $2 FROM board, file RETURNING board_id, attachment_id
		)
		INSERT INTO board_items (company_id, board_id, type, x, y, width, height, properties, created_by, updated_by)
		SELECT $1, board_id, 'file', 0, 0, 280, 88, jsonb_build_object('attachmentId', attachment_id), $2, $2
		FROM reference`, f.CompanyA, f.UserA)
}

// A Board's chat with a tagged Run: the Board, its channel, the Agent's chat
// and the Run all go with the Company.
func seedDeletionWhiteboardChat(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f dbtest.Fixture) {
	t.Helper()
	var board uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO boards (company_id, title, created_by) VALUES ($1, 'Plan', $2) RETURNING id`,
		f.CompanyA, f.UserA).
		Scan(&board); err != nil {
		t.Fatal(err)
	}
	channel := dbtest.SeedBoardChannel(t, ctx, pool, f.CompanyA, board, f.UserA, []uuid.UUID{f.AgentA})
	run := dbtest.SeedTaggedRun(t, ctx, pool, f.CompanyA, channel, f.AgentA, f.RuntimeA, f.UserA)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status = 'completed' WHERE id = $1`, run)
}

func seedDeletionSkills(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f dbtest.Fixture) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool, `
		WITH imported AS (
			INSERT INTO skill_imports (company_id, agent_id, created_by_user_id, source_type, ready, edited)
			VALUES ($1, $2, $3, 'markdown', true, true) RETURNING id
		), version AS (
			INSERT INTO skill_versions (
				company_id, import_id, name, description, source_path,
				content_hash, size_bytes, file_count, manifest
			)
			SELECT $1, id, 'review-work', 'Review work.', 'SKILL.md', repeat('a', 64), 1, 1, '[]'::jsonb
			FROM imported RETURNING id
		), installed AS (
			INSERT INTO agent_skills (company_id, agent_id, version_id, name)
			SELECT $1, $2, id, 'review-work' FROM version
		)
		INSERT INTO run_skills (company_id, run_id, version_id)
		SELECT $1, $4, id FROM version`, f.CompanyA, f.AgentA, f.UserA, f.RunA)
}

func seedDeletionMailDrafts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f dbtest.Fixture) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool, `
		WITH mailbox AS (
			INSERT INTO company_mailboxes (company_id, local_part, created_by_user_id)
			VALUES ($1::uuid, $1::uuid::text, $2) RETURNING id
		), draft_group AS (
			INSERT INTO mailbox_draft_groups (
				company_id, mailbox_id, conversation_id, source_run_id, request_key,
				agent_id, created_by_user_id, payload_sha256
			)
			SELECT $1, id, $3, $4, gen_random_uuid(), $5, $2, decode(repeat('00', 32), 'hex')
			FROM mailbox RETURNING id
		), draft AS (
			INSERT INTO mailbox_drafts (
				company_id, group_id, ordinal, subject, body_text, from_name, from_address
			)
			SELECT $1, id, 0, 'Hello', 'Draft body', 'Agent', 'agent@example.com'
			FROM draft_group RETURNING id
		)
		INSERT INTO mailbox_draft_deliveries (company_id, draft_id, ordinal, to_addresses, cc_addresses)
		SELECT $1, id, 0, ARRAY['recipient@example.com'], '{}'::text[] FROM draft`,
		f.CompanyA, f.UserA, f.ConversationA, f.RunA, f.AgentA)
}

func seedDeletionPresentation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f dbtest.Fixture) {
	t.Helper()
	dbtest.MustExec(t, ctx, pool, `
		WITH page AS (
			INSERT INTO wiki_pages (company_id, project_id, slug, title, created_by_user_id)
			VALUES ($1, $2, 'presentation', 'Presentation', $3) RETURNING id
		), revision AS (
			INSERT INTO wiki_revisions (company_id, page_id, number, title, body, author_user_id)
			SELECT $1, id, 1, 'Presentation', 'Content', $3 FROM page RETURNING id, page_id
		), artifact AS (
			INSERT INTO project_artifacts (company_id, project_id, title, created_by_user_id)
			VALUES ($1, $2, 'Presentation', $3) RETURNING id
		), artifact_version AS (
			INSERT INTO project_artifact_versions (
				company_id, project_id, artifact_id, number, source_type,
				wiki_page_id, wiki_revision_id, created_by_user_id
			)
			SELECT $1, $2, artifact.id, 1, 'wiki', revision.page_id, revision.id, $3
			FROM artifact CROSS JOIN revision RETURNING id, wiki_page_id, wiki_revision_id
		), attachment AS (
			INSERT INTO attachments (
				company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status
			)
			VALUES ($1, $3, gen_random_uuid(), 'presentation.pdf', 'application/pdf', 1, 'ready')
			RETURNING id
		), presentation AS (
			INSERT INTO project_presentations (
				company_id, project_id, page_id, page_version_id, theme, title, content,
				export_markdown, source_sequence, fingerprint, request_key,
				created_by_user_id, export_attachment_id
			)
			SELECT $1, $2, artifact_version.wiki_page_id, artifact_version.id, 'default',
				'Presentation', '{}'::jsonb, 'Content', 1, 'fingerprint', gen_random_uuid(), $3, attachment.id
			FROM artifact_version CROSS JOIN attachment RETURNING id
		)
		INSERT INTO project_presentation_sources (
			company_id, project_id, presentation_id, version_id, wiki_page_id, wiki_revision_id
		)
		SELECT $1, $2, presentation.id, artifact_version.id,
			artifact_version.wiki_page_id, artifact_version.wiki_revision_id
		FROM presentation CROSS JOIN artifact_version`, f.CompanyA, f.ProjectA, f.UserA)
}

func seedDeletionHuddle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f dbtest.Fixture) {
	t.Helper()
	sourceID := dbtest.SeedDiscussionChannel(t, ctx, pool, f.CompanyA, f.UserA, nil)
	inviteeID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		WITH invitee AS (
			INSERT INTO users (id, email, display_name)
			VALUES ($1::uuid, $1::uuid::text || '@example.com', 'Invitee') RETURNING id
		)
		INSERT INTO company_members (company_id, user_id, role)
		SELECT $2, id, 'member' FROM invitee`, inviteeID, f.CompanyA)
	dbtest.WithinSeedTx(t, ctx, pool, func(tx pgx.Tx) {
		dbtest.MustExecTx(t, ctx, tx, `
			WITH chat AS (
				INSERT INTO conversations (company_id, kind, created_by_user_id)
				VALUES ($1, 'huddle', $2) RETURNING id
			), huddle AS (
				INSERT INTO huddles (
					company_id, source_conversation_id, chat_conversation_id,
					started_by_user_id, request_key, deadline_at
				)
				SELECT $1, $3, id, $2, gen_random_uuid(), now() + interval '1 hour'
				FROM chat RETURNING id
			), connection AS (
				INSERT INTO huddle_connections (
					company_id, huddle_id, user_id, request_key, reservation_expires_at
				)
				SELECT $1, id, $2, gen_random_uuid(), now() + interval '30 seconds' FROM huddle
			)
			INSERT INTO huddle_invitations (
				company_id, huddle_id, inviter_user_id, invitee_user_id, request_key, expires_at
			)
			SELECT $1, id, $2, $4, gen_random_uuid(), now() + interval '1 minute' FROM huddle`,
			f.CompanyA, f.UserA, sourceID, inviteeID)
	})
}
