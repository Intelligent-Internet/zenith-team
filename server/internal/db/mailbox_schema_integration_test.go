//go:build integration

package db

import (
	"context"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestMailboxSchemaEnforcesAddressReplayAndMetadataContracts(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	mailboxA := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_mailboxes (id, company_id, local_part, created_by_user_id)
		VALUES ($1, $2, 'sales', $3)`, mailboxA, fixture.CompanyA, fixture.UserA)

	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO company_mailboxes (id, company_id, local_part, created_by_user_id)
		VALUES ($1, $2, 'support', $3)`,
		dbtest.NewUUID(t), fixture.CompanyA, fixture.UserA)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO company_mailboxes (id, company_id, local_part, created_by_user_id)
		VALUES ($1, $2, 'sales', $3)`,
		dbtest.NewUUID(t), fixture.CompanyB, fixture.UserB)

	messageID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO mailbox_messages (
			id, company_id, mailbox_id, provider_email_id, sender, subject,
			recipients, received_at, expires_at, has_attachments
		) VALUES (
			$1, $2, $3, 'provider-email-1', 'customer@example.com', 'Hello',
			ARRAY['sales@mail.example.com'], now(), now() + interval '30 days', true
		)`, messageID, fixture.CompanyA, mailboxA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO mailbox_message_attachments (
			id, message_id, company_id, provider_attachment_id, filename,
			mime_type, size_bytes, disposition
		) VALUES ($1, $2, $3, 'provider-attachment-1', 'brief.pdf',
			'application/pdf', 42, 'attachment')`,
		dbtest.NewUUID(t), messageID, fixture.CompanyA)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO mailbox_received_receipts (
			provider_event_id, provider_email_id, outcome, received_at, expires_at
		) VALUES ('provider-event-1', 'provider-email-1', 'delivered', now(), now() + interval '30 days')`)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO mailbox_received_receipts (
			provider_event_id, provider_email_id, outcome, received_at, expires_at
		) VALUES ('provider-event-1', 'provider-email-2', 'ignored', now(), now() + interval '30 days')`)
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO mailbox_received_receipts (
			provider_event_id, provider_email_id, outcome, received_at, expires_at
		) VALUES ('provider-event-2', 'provider-email-1', 'ignored', now(), now() + interval '30 days')`)

	for _, forbiddenColumn := range []string{"body", "html", "text", "raw", "download_url"} {
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name IN (
				'company_mailboxes', 'mailbox_messages',
				'mailbox_message_attachments', 'mailbox_received_receipts'
			  )
			  AND column_name = $1`, forbiddenColumn).Scan(&count); err != nil {
			t.Fatalf("inspect forbidden mailbox column %q: %v", forbiddenColumn, err)
		}
		if count != 0 {
			t.Errorf("mailbox schema persists forbidden column %q", forbiddenColumn)
		}
	}
}

func TestAgentMailSchemaEnforcesRunReplayDraftStateAndPrivateDeliveries(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)

	mailboxID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_mailboxes (id, company_id, local_part, created_by_user_id)
		VALUES ($1, $2, 'agent-mail', $3)`, mailboxID, fixture.CompanyA, fixture.UserA)
	groupID := dbtest.NewUUID(t)
	requestKey := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO mailbox_draft_groups (
			id, company_id, mailbox_id, conversation_id, source_run_id, request_key,
			agent_id, created_by_user_id, payload_sha256
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		groupID, fixture.CompanyA, mailboxID, fixture.ConversationA,
		fixture.RunA, requestKey, fixture.AgentA, fixture.UserA, make([]byte, 32))
	assertStatementRejected(t, ctx, pool, "23505", `
		INSERT INTO mailbox_draft_groups (
			id, company_id, mailbox_id, conversation_id, source_run_id, request_key,
			agent_id, created_by_user_id, payload_sha256
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		dbtest.NewUUID(t), fixture.CompanyA, mailboxID, fixture.ConversationA,
		fixture.RunA, requestKey, fixture.AgentA, fixture.UserA, make([]byte, 32))

	draftID := dbtest.NewUUID(t)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO mailbox_drafts (
			id, company_id, group_id, ordinal, subject, body_text,
			from_name, from_address
		) VALUES ($1,$2,$3,0,'Hello','Plain text only','Zenith Team','agent-mail@mail.example.com')`,
		draftID, fixture.CompanyA, groupID)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO mailbox_draft_deliveries (
			id, company_id, draft_id, ordinal, to_addresses, cc_addresses
		) VALUES ($1,$2,$3,0,$4,$5)`, dbtest.NewUUID(t), fixture.CompanyA,
		draftID, []string{"lead-a@example.com"}, []string{})
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO mailbox_draft_deliveries (
			id, company_id, draft_id, ordinal, to_addresses, cc_addresses
		) VALUES ($1,$2,$3,1,$4,$5)`, dbtest.NewUUID(t), fixture.CompanyA,
		draftID, []string{"lead-b@example.com"}, []string{"owner@example.com"})

	assertStatementRejected(t, ctx, pool, "23514", `
		UPDATE mailbox_drafts SET status='unknown' WHERE id=$1`, draftID)

	for _, forbiddenColumn := range []string{"html", "bcc", "attachment_id"} {
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema='public'
			  AND table_name IN ('mailbox_drafts','mailbox_draft_deliveries')
			  AND column_name=$1`, forbiddenColumn).Scan(&count); err != nil {
			t.Fatalf("inspect forbidden Agent Mail column %q: %v", forbiddenColumn, err)
		}
		if count != 0 {
			t.Errorf("Agent Mail schema persists forbidden column %q", forbiddenColumn)
		}
	}
}
